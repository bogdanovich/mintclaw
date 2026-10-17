package computerhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/protocol"
)

const maxOutputBytes = 2 * computer.MaxCaptureBytes

type outputStore struct {
	mu    sync.Mutex
	items map[string]*imageOutput
	bytes uint64
}

type imageOutput struct {
	descriptor nodes.ComputerOutputDescriptor
	data       []byte
	timer      *time.Timer
	stream     *imageStream
}

type imageStream struct {
	ctx        context.Context
	cancel     context.CancelFunc
	connection <-chan struct{}
	ack        chan uint64
	pendingAck uint64
	finished   bool
}

func (store *outputStore) register(
	observation computer.Observation, request nodes.ComputerHostObserveRequest,
) (nodes.ComputerOutputDescriptor, error) {
	if observation.ContentType != "image/png" || len(observation.Screenshot) == 0 ||
		len(observation.Screenshot) > computer.MaxCaptureBytes || nodes.ID(request.InvocationID).Validate() != nil ||
		nodes.ID(request.WorkspaceID).Validate() != nil {
		return nodes.ComputerOutputDescriptor{}, computer.ErrInvalid
	}
	id, err := opaqueID()
	if err != nil {
		return nodes.ComputerOutputDescriptor{}, err
	}
	digest := sha256.Sum256(observation.Screenshot)
	descriptor := nodes.ComputerOutputDescriptor{
		TransferID: id, Binding: observation.Binding, ObservationID: observation.ID,
		InvocationID: request.InvocationID, WorkspaceID: request.WorkspaceID,
		ContentType: observation.ContentType, Size: uint64(len(observation.Screenshot)),
		SHA256: hex.EncodeToString(digest[:]), ExpiresAt: observation.ExpiresAt,
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if !time.Now().Before(descriptor.ExpiresAt) || store.bytes+descriptor.Size > maxOutputBytes ||
		len(store.items) >= 32 {
		return nodes.ComputerOutputDescriptor{}, computer.ErrBusy
	}
	output := &imageOutput{descriptor: descriptor, data: bytes.Clone(observation.Screenshot)}
	store.items[id] = output
	store.bytes += descriptor.Size
	output.timer = time.AfterFunc(time.Until(descriptor.ExpiresAt), func() {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.removeLocked(id)
	})
	return descriptor, nil
}

func (store *outputStore) removeSession(sessionID string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for id, output := range store.items {
		if output.descriptor.Binding.SessionID == sessionID {
			store.removeLocked(id)
		}
	}
}

func (store *outputStore) removeLocked(id string) {
	output := store.items[id]
	if output == nil {
		return
	}
	if output.stream != nil {
		output.stream.cancel()
	}
	if output.timer != nil {
		output.timer.Stop()
	}
	clear(output.data)
	store.bytes -= uint64(len(output.data))
	delete(store.items, id)
}

// No generic file command descriptors are advertised. This host contributes
// only policy-revision routing to the existing authenticated transfer router.
func (*Host) Descriptors() []nodes.CommandDescriptor { return nil }

func (host *Host) TransferPolicyRevisions() []string {
	revisions := make([]string, len(host.metadata))
	for index, profile := range host.metadata {
		revisions[index] = profile.Revision
	}
	return revisions
}

func (host *Host) HandleTransferFrame(
	ctx context.Context, frame protocol.TransferFrame, send func(protocol.TransferFrame) error,
) error {
	if send == nil || frame.Validate() != nil {
		return protocol.ErrInvalidTransferFrame
	}
	if frame.Direction != protocol.TransferDownload || ctx.Err() != nil {
		return send(transferResponse(frame, protocol.TransferFrameDeny, "transfer_denied"))
	}
	if frame.Type == protocol.TransferFramePrepare {
		return host.prepareImage(ctx, frame, send)
	}
	host.outputs.mu.Lock()
	output := host.outputs.items[frame.TransferID]
	if output == nil || !matchesFrame(output.descriptor, frame) || output.stream == nil ||
		output.stream.connection != ctx.Done() || output.stream.ctx.Err() != nil {
		host.outputs.mu.Unlock()
		return send(transferResponse(frame, protocol.TransferFrameFailure, "transfer_unavailable"))
	}
	stream := output.stream
	switch frame.Type {
	case protocol.TransferFrameAck:
		defer host.outputs.mu.Unlock()
		if frame.Sequence != stream.pendingAck || stream.pendingAck == 0 || stream.finished {
			return protocol.ErrInvalidTransferFrame
		}
		stream.pendingAck = 0
		select {
		case stream.ack <- frame.Sequence:
			return nil
		default:
			return protocol.ErrInvalidTransferFrame
		}
	case protocol.TransferFrameCommit:
		host.outputs.mu.Unlock()
		return host.imageStatus(ctx, output, frame, send, true)
	case protocol.TransferFrameCancel:
		host.outputs.removeLocked(frame.TransferID)
		host.outputs.mu.Unlock()
		return send(transferResponse(frame, protocol.TransferFrameFailure, "canceled"))
	case protocol.TransferFrameStatus:
		host.outputs.mu.Unlock()
		return host.imageStatus(ctx, output, frame, send, false)
	default:
		host.outputs.mu.Unlock()
		return protocol.ErrInvalidTransferFrame
	}
}

func (host *Host) imageStatus(
	ctx context.Context, output *imageOutput, frame protocol.TransferFrame,
	send func(protocol.TransferFrame) error, commit bool,
) error {
	entry, err := host.locked(ctx, nodes.ComputerHostStatusRequest{
		Principal: output.descriptor.Binding.Principal, SessionID: output.descriptor.Binding.SessionID,
	})
	if err != nil {
		return send(transferResponse(frame, protocol.TransferFrameFailure, "authority_denied"))
	}
	defer relinquish(entry.gate)
	session, err := host.refresh(ctx, entry)
	if err != nil || session.Binding != output.descriptor.Binding || session.AgentAuthority(time.Now()) != nil {
		return send(transferResponse(frame, protocol.TransferFrameFailure, "authority_denied"))
	}
	host.outputs.mu.Lock()
	stream := output.stream
	if host.outputs.items[frame.TransferID] != output || stream == nil || stream.ctx.Err() != nil ||
		stream.connection != ctx.Done() {
		host.outputs.mu.Unlock()
		return send(transferResponse(frame, protocol.TransferFrameFailure, "transfer_unavailable"))
	}
	state := "streaming"
	if stream.finished {
		state = "received"
	}
	kind := protocol.TransferFrameStatus
	if commit {
		if !stream.finished {
			host.outputs.mu.Unlock()
			return send(transferResponse(frame, protocol.TransferFrameFailure, "transfer_incomplete"))
		}
		host.outputs.removeLocked(frame.TransferID)
		kind, state = protocol.TransferFrameCommitted, "committed"
	}
	host.outputs.mu.Unlock()
	return send(transferResponse(frame, kind, state))
}

func (host *Host) prepareImage(
	ctx context.Context, frame protocol.TransferFrame, send func(protocol.TransferFrame) error,
) error {
	var descriptor nodes.ComputerOutputDescriptor
	decoder := json.NewDecoder(bytes.NewReader(frame.Payload))
	decoder.DisallowUnknownFields()
	if ctx.Done() == nil || decoder.Decode(&descriptor) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		!matchesFrame(descriptor, frame) {
		return send(transferResponse(frame, protocol.TransferFrameDeny, "invalid_prepare"))
	}
	entry, err := host.locked(ctx, nodes.ComputerHostStatusRequest{
		Principal: descriptor.Binding.Principal, SessionID: descriptor.Binding.SessionID,
	})
	if err != nil {
		return send(transferResponse(frame, protocol.TransferFrameDeny, "authority_denied"))
	}
	defer relinquish(entry.gate)
	session, err := host.refresh(ctx, entry)
	if err != nil || session.Binding != descriptor.Binding || session.AgentAuthority(time.Now()) != nil {
		return send(transferResponse(frame, protocol.TransferFrameDeny, "authority_denied"))
	}
	host.outputs.mu.Lock()
	output := host.outputs.items[frame.TransferID]
	if output == nil || !sameImageDescriptor(output.descriptor, descriptor) || output.stream != nil ||
		!time.Now().Before(descriptor.ExpiresAt) {
		host.outputs.mu.Unlock()
		return send(transferResponse(frame, protocol.TransferFrameDeny, "output_denied"))
	}
	lifetime, cancel := context.WithDeadline(ctx, descriptor.ExpiresAt)
	stream := &imageStream{ctx: lifetime, cancel: cancel, connection: ctx.Done(), ack: make(chan uint64, 1)}
	output.stream = stream
	host.outputs.mu.Unlock()
	if err = send(transferResponse(frame, protocol.TransferFrameAccept, "accepted")); err != nil {
		host.outputs.mu.Lock()
		host.outputs.removeLocked(frame.TransferID)
		host.outputs.mu.Unlock()
		return err
	}
	go host.streamImage(entry, output, stream, frame, send)
	return nil
}

func (host *Host) streamImage(
	entry *sessionEntry, output *imageOutput, stream *imageStream,
	frame protocol.TransferFrame, send func(protocol.TransferFrame) error,
) {
	failed := true
	defer func() {
		if failed {
			stream.cancel()
			host.outputs.mu.Lock()
			if host.outputs.items[frame.TransferID] == output {
				host.outputs.removeLocked(frame.TransferID)
			}
			host.outputs.mu.Unlock()
		}
	}()
	var offset uint64
	for sequence := uint64(1); offset < output.descriptor.Size; sequence++ {
		if acquire(stream.ctx, entry.gate) != nil {
			return
		}
		session, err := host.refresh(stream.ctx, entry)
		if err != nil || session.Binding != output.descriptor.Binding || session.AgentAuthority(time.Now()) != nil {
			relinquish(entry.gate)
			return
		}
		host.outputs.mu.Lock()
		if host.outputs.items[frame.TransferID] != output || stream.ctx.Err() != nil {
			host.outputs.mu.Unlock()
			relinquish(entry.gate)
			return
		}
		end := min(offset+protocol.MaxTransferChunkBytes, output.descriptor.Size)
		chunk := frame
		chunk.Type, chunk.Sequence, chunk.Payload = protocol.TransferFrameChunk, sequence, bytes.Clone(
			output.data[offset:end],
		)
		stream.pendingAck = sequence
		host.outputs.mu.Unlock()
		err = send(chunk)
		relinquish(entry.gate)
		if err != nil {
			return
		}
		select {
		case acknowledged := <-stream.ack:
			if acknowledged != sequence {
				return
			}
		case <-stream.ctx.Done():
			return
		}
		offset = end
	}
	if acquire(stream.ctx, entry.gate) != nil {
		return
	}
	defer relinquish(entry.gate)
	session, err := host.refresh(stream.ctx, entry)
	if err != nil || session.Binding != output.descriptor.Binding || session.AgentAuthority(time.Now()) != nil {
		return
	}
	host.outputs.mu.Lock()
	if host.outputs.items[frame.TransferID] != output || stream.ctx.Err() != nil {
		host.outputs.mu.Unlock()
		return
	}
	stream.finished = true
	host.outputs.mu.Unlock()
	if send(transferResponse(frame, protocol.TransferFrameStatus, "received")) == nil {
		failed = false
	}
}

func matchesFrame(descriptor nodes.ComputerOutputDescriptor, frame protocol.TransferFrame) bool {
	return frame.Direction == protocol.TransferDownload && descriptor.TransferID == frame.TransferID &&
		descriptor.Binding.PolicyRevision == frame.PolicyRevision && descriptor.Size == frame.TotalSize &&
		descriptor.SHA256 == hex.EncodeToString(frame.SHA256[:])
}

func sameImageDescriptor(stored, received nodes.ComputerOutputDescriptor) bool {
	// JSON drops Go's monotonic component and location object. Compare the
	// exact expiry instant, not time.Time's in-process representation.
	storedExpiry, receivedExpiry := stored.ExpiresAt, received.ExpiresAt
	stored.ExpiresAt, received.ExpiresAt = time.Time{}, time.Time{}
	return stored == received && storedExpiry.Equal(receivedExpiry)
}

func transferResponse(
	frame protocol.TransferFrame,
	kind protocol.TransferFrameType,
	state string,
) protocol.TransferFrame {
	frame.Type, frame.Sequence = kind, 0
	frame.Payload, _ = json.Marshal(struct {
		State string `json:"state"`
	}{State: state})
	return frame
}
