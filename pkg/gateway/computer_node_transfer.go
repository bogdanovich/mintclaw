package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/protocol"
	nodews "github.com/bogdanovich/mintclaw/pkg/nodes/ws"
)

func (client *nodeComputerClient) receiveImage(
	ctx context.Context, entry *computerNodeSession, output nodes.ComputerOutputDescriptor,
) ([]byte, error) {
	digest, err := hex.DecodeString(output.SHA256)
	if err != nil || len(digest) != sha256.Size || output.Size == 0 || output.Size > computer.MaxCaptureBytes ||
		output.Binding != entry.remote.Binding || output.ContentType != "image/png" {
		return nil, computer.ErrStale
	}
	var sum [sha256.Size]byte
	copy(sum[:], digest)
	hub, err := client.source.runtime.transferSessionsSnapshot(client.source.registryPath, client.source.generation)
	if err != nil {
		return nil, computer.ErrUnavailable
	}
	stream, err := hub.OpenTransfer(ctx, entry.authority.nodeID, nodews.TransferBinding{
		TransferID: output.TransferID, Direction: protocol.TransferDownload,
		PolicyRevision: output.Binding.PolicyRevision, TotalSize: output.Size, SHA256: sum,
	})
	if err != nil {
		return nil, computer.ErrUnavailable
	}
	return receiveComputerImage(ctx, output, stream, func() error {
		_, err := client.authority(entry.authority.alias, entry.remote.Binding.Principal, &entry.authority)
		return err
	})
}

type computerImageTransfer interface {
	Send(context.Context, protocol.TransferFrame) error
	Receive(context.Context) (protocol.TransferFrame, error)
	ReleaseCommitted() error
	Close() error
}

// The authenticated TransferStream owns peer generation, immutable transfer
// binding, sequence and byte backpressure. This receiver additionally verifies
// the image's complete digest and receipt before exposing any bytes.
func receiveComputerImage(
	ctx context.Context, output nodes.ComputerOutputDescriptor, stream computerImageTransfer, authorize func() error,
) ([]byte, error) {
	digest, err := hex.DecodeString(output.SHA256)
	if err != nil || len(digest) != sha256.Size || output.Size == 0 || output.Size > computer.MaxCaptureBytes {
		_ = stream.Close()
		return nil, computer.ErrStale
	}
	var sum [sha256.Size]byte
	copy(sum[:], digest)
	defer func() { _ = stream.Close() }()
	frame := protocol.TransferFrame{
		Type: protocol.TransferFramePrepare, Direction: protocol.TransferDownload, TransferID: output.TransferID,
		PolicyRevision: output.Binding.PolicyRevision, TotalSize: output.Size, SHA256: sum,
	}
	frame.Payload, err = json.Marshal(output)
	if err != nil || stream.Send(ctx, frame) != nil {
		return nil, computer.ErrUnavailable
	}
	buffer := bytes.NewBuffer(make([]byte, 0, int(output.Size)))
	accepted, complete := false, false
	defer func() {
		if !complete {
			clear(buffer.Bytes())
		}
	}()
	for {
		response, err := stream.Receive(ctx)
		if err != nil {
			return nil, computer.ErrUnavailable
		}
		switch response.Type {
		case protocol.TransferFrameAccept:
			if accepted || buffer.Len() != 0 {
				return nil, computer.ErrStale
			}
			accepted = true
		case protocol.TransferFrameChunk:
			if !accepted || len(response.Payload) == 0 ||
				uint64(buffer.Len())+uint64(len(response.Payload)) > output.Size {
				return nil, computer.ErrStale
			}
			_, _ = buffer.Write(response.Payload)
			ack := frame
			ack.Type, ack.Sequence, ack.Payload = protocol.TransferFrameAck, response.Sequence, nil
			if stream.Send(ctx, ack) != nil {
				return nil, computer.ErrUnavailable
			}
		case protocol.TransferFrameStatus:
			var status struct {
				State string `json:"state"`
			}
			if !accepted || json.Unmarshal(response.Payload, &status) != nil || status.State != "received" ||
				uint64(buffer.Len()) != output.Size || sha256.Sum256(buffer.Bytes()) != sum {
				return nil, computer.ErrStale
			}
			commit := frame
			commit.Type, commit.Payload = protocol.TransferFrameCommit, nil
			if stream.Send(ctx, commit) != nil {
				return nil, computer.ErrUnavailable
			}
			committed, commitErr := stream.Receive(ctx)
			if commitErr != nil || committed.Type != protocol.TransferFrameCommitted ||
				stream.ReleaseCommitted() != nil {
				return nil, computer.ErrUnavailable
			}
			if err := authorize(); err != nil {
				return nil, err
			}
			complete = true
			return buffer.Bytes(), nil
		default:
			return nil, computer.ErrUnavailable
		}
	}
}
