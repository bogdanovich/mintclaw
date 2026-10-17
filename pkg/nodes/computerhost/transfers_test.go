package computerhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/protocol"
)

func imagePrepare(t *testing.T, descriptor nodes.ComputerOutputDescriptor) protocol.TransferFrame {
	t.Helper()
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := hex.DecodeString(descriptor.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	frame := protocol.TransferFrame{
		Type: protocol.TransferFramePrepare, Direction: protocol.TransferDownload,
		TransferID: descriptor.TransferID, PolicyRevision: descriptor.Binding.PolicyRevision,
		TotalSize: descriptor.Size, Payload: encoded,
	}
	copy(frame.SHA256[:], digest)
	return frame
}

func collectImage(t *testing.T, host *Host, descriptor nodes.ComputerOutputDescriptor) []byte {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	frames := make(chan protocol.TransferFrame, 4)
	send := func(frame protocol.TransferFrame) error { frames <- frame; return nil }
	prepare := imagePrepare(t, descriptor)
	if err := host.HandleTransferFrame(ctx, prepare, send); err != nil {
		t.Fatal(err)
	}
	var data []byte
	for {
		var frame protocol.TransferFrame
		select {
		case frame = <-frames:
		case <-time.After(2 * time.Second):
			t.Fatal("image transfer stalled")
		}
		switch frame.Type {
		case protocol.TransferFrameAccept:
		case protocol.TransferFrameChunk:
			data = append(data, frame.Payload...)
			ack := prepare
			ack.Type, ack.Sequence, ack.Payload = protocol.TransferFrameAck, frame.Sequence, nil
			if err := host.HandleTransferFrame(ctx, ack, send); err != nil {
				t.Fatal(err)
			}
		case protocol.TransferFrameStatus:
			var state struct {
				State string `json:"state"`
			}
			if json.Unmarshal(frame.Payload, &state) != nil || state.State != "received" {
				t.Fatal("invalid image completion")
			}
			commit := prepare
			commit.Type, commit.Payload = protocol.TransferFrameCommit, nil
			if err := host.HandleTransferFrame(ctx, commit, send); err != nil {
				t.Fatal(err)
			}
			if result := <-frames; result.Type != protocol.TransferFrameCommitted {
				t.Fatal("image commit was not acknowledged")
			}
			return data
		default:
			t.Fatal("image transfer refused", frame.Type, string(frame.Payload))
		}
	}
}

func TestNativeHostImageTransferAndInvalidation(t *testing.T) {
	host, _ := nativeHostFixture(t, nil)
	session := openFixture(t, host)
	observed := observeFixture(t, host, session, "pixels")
	data := collectImage(t, host, *observed.Output)
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != observed.Output.SHA256 || uint64(len(data)) != observed.Output.Size ||
		!bytes.Equal(data, fixturePNG(t)) {
		t.Fatal("image transfer did not preserve captured bytes")
	}
	host.outputs.mu.Lock()
	remaining, retained := len(host.outputs.items), host.outputs.bytes
	host.outputs.mu.Unlock()
	if remaining != 0 || retained != 0 || len(host.Descriptors()) != 0 {
		t.Fatal("committed image bytes or generic file authority retained")
	}
	second := observeFixture(t, host, session, "second")
	human := controlFixture(t, host, session, computer.EventHandoff)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var response protocol.TransferFrame
	if err := host.HandleTransferFrame(ctx, imagePrepare(t, *second.Output), func(frame protocol.TransferFrame) error {
		response = frame
		return nil
	}); err != nil || response.Type != protocol.TransferFrameDeny {
		t.Fatal("pre-handoff evidence remained downloadable", err)
	}
	if human.Binding.Generation != session.Binding.Generation+1 {
		t.Fatal("handoff generation did not advance")
	}
}

func TestNativeHostMultiChunkImageTransfer(t *testing.T) {
	host, app := nativeHostFixture(t, nil)
	img := image.NewNRGBA(image.Rect(0, 0, 500, 300))
	random := rand.New(rand.NewPCG(1, 2))
	for offset := 0; offset < len(img.Pix); offset += 4 {
		value := random.Uint32()
		img.Pix[offset], img.Pix[offset+1], img.Pix[offset+2], img.Pix[offset+3] = byte(
			value,
		), byte(
			value>>8,
		), byte(
			value>>16,
		), 255
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	app.capture = func(_ context.Context, target computer.Target) (computer.Capture, error) {
		return computer.Capture{
			Scope: target.Scope, Geometry: computer.Geometry{Width: 500, Height: 300, Scale: 1},
			ContentType: "image/png", Screenshot: bytes.Clone(encoded.Bytes()),
		}, nil
	}
	session := openFixture(t, host)
	result := observeFixture(t, host, session, "large-image")
	if result.Output.Size <= protocol.MaxTransferChunkBytes {
		t.Fatal("fixture did not exercise multiple chunks")
	}
	if !bytes.Equal(collectImage(t, host, *result.Output), encoded.Bytes()) {
		t.Fatal("multi-chunk output changed")
	}
}

func TestNativeHostImageDescriptorCannotAcquireAuthority(t *testing.T) {
	host, _ := nativeHostFixture(t, nil)
	session := openFixture(t, host)
	observed := observeFixture(t, host, session, "pixels")
	for name, mutate := range map[string]func(*nodes.ComputerOutputDescriptor){
		"actor":       func(d *nodes.ComputerOutputDescriptor) { d.Binding.Principal.ActorID = "other" },
		"turn":        func(d *nodes.ComputerOutputDescriptor) { d.Binding.Principal.ExecutionID = "other" },
		"worker":      func(d *nodes.ComputerOutputDescriptor) { d.Binding.WorkerID = "other" },
		"window":      func(d *nodes.ComputerOutputDescriptor) { d.Binding.Target.Scope.WindowGeneration++ },
		"generation":  func(d *nodes.ComputerOutputDescriptor) { d.Binding.Generation++ },
		"observation": func(d *nodes.ComputerOutputDescriptor) { d.ObservationID = "other" },
		"invocation":  func(d *nodes.ComputerOutputDescriptor) { d.InvocationID = "other" },
		"workspace":   func(d *nodes.ComputerOutputDescriptor) { d.WorkspaceID = "other" },
		"size":        func(d *nodes.ComputerOutputDescriptor) { d.Size++ },
		"expiry":      func(d *nodes.ComputerOutputDescriptor) { d.ExpiresAt = d.ExpiresAt.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			forged := *observed.Output
			mutate(&forged)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var response protocol.TransferFrame
			err := host.HandleTransferFrame(ctx, imagePrepare(t, forged), func(frame protocol.TransferFrame) error {
				response = frame
				return nil
			})
			if err != nil || response.Type != protocol.TransferFrameDeny {
				t.Fatal("forged immutable image descriptor accepted", err)
			}
		})
	}
	if len(collectImage(t, host, *observed.Output)) == 0 {
		t.Fatal("invalid attempts destroyed valid original evidence")
	}
}

func TestNativeHostTransferIsConnectionBoundAndStopsAtHandoff(t *testing.T) {
	host, _ := nativeHostFixture(t, nil)
	session := openFixture(t, host)
	observed := observeFixture(t, host, session, "pixels")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	frames := make(chan protocol.TransferFrame, 4)
	prepare := imagePrepare(t, *observed.Output)
	if err := host.HandleTransferFrame(ctx, prepare, func(frame protocol.TransferFrame) error {
		frames <- frame
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if (<-frames).Type != protocol.TransferFrameAccept {
		t.Fatal("prepare refused")
	}
	var chunk protocol.TransferFrame
	select {
	case chunk = <-frames:
	case <-time.After(2 * time.Second):
		t.Fatal("first image chunk missing")
	}
	other, stop := context.WithCancel(t.Context())
	defer stop()
	ack := prepare
	ack.Type, ack.Sequence, ack.Payload = protocol.TransferFrameAck, chunk.Sequence, nil
	var denied protocol.TransferFrame
	if err := host.HandleTransferFrame(other, ack, func(frame protocol.TransferFrame) error {
		denied = frame
		return nil
	}); err != nil || denied.Type != protocol.TransferFrameFailure {
		t.Fatal("another connection adopted an existing transfer", err)
	}
	_ = controlFixture(t, host, session, computer.EventHandoff)
	if err := host.HandleTransferFrame(ctx, ack, func(frame protocol.TransferFrame) error {
		denied = frame
		return nil
	}); err != nil || denied.Type != protocol.TransferFrameFailure {
		t.Fatal("ack after handoff retained transfer authority", err)
	}
	host.outputs.mu.Lock()
	retained := host.outputs.bytes
	host.outputs.mu.Unlock()
	if retained != 0 {
		t.Fatal("handoff retained private image bytes")
	}
}
