package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/protocol"
)

// This fixture tests the image receiver's content/receipt protocol. The real
// authenticated peer binding and native pixels are covered by the WSS smoke.
type computerImageTestStream struct {
	frames     []protocol.TransferFrame
	sent       []protocol.TransferFrame
	closed     bool
	released   bool
	releaseErr error
	sendErr    error
}

func (stream *computerImageTestStream) Send(ctx context.Context, frame protocol.TransferFrame) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stream.sent = append(stream.sent, frame)
	return stream.sendErr
}

func (stream *computerImageTestStream) Receive(ctx context.Context) (protocol.TransferFrame, error) {
	if err := ctx.Err(); err != nil {
		return protocol.TransferFrame{}, err
	}
	if len(stream.frames) == 0 {
		return protocol.TransferFrame{}, io.EOF
	}
	frame := stream.frames[0]
	stream.frames = stream.frames[1:]
	return frame, nil
}

func (stream *computerImageTestStream) ReleaseCommitted() error {
	stream.released = true
	return stream.releaseErr
}
func (stream *computerImageTestStream) Close() error { stream.closed = true; return nil }

func TestComputerImageReceiverRequiresCompleteDigestReceiptAndCurrentAuthority(t *testing.T) {
	data := []byte("fixture-image-bytes")
	digest := sha256.Sum256(data)
	output := nodes.ComputerOutputDescriptor{
		TransferID:  "transfer",
		Size:        uint64(len(data)),
		SHA256:      hex.EncodeToString(digest[:]),
		ContentType: "image/png",
	}
	frame := func(kind protocol.TransferFrameType, payload []byte) protocol.TransferFrame {
		return protocol.TransferFrame{Type: kind, Payload: payload}
	}
	valid := func() []protocol.TransferFrame {
		return []protocol.TransferFrame{
			frame(protocol.TransferFrameAccept, nil),
			frame(protocol.TransferFrameChunk, data),
			frame(
				protocol.TransferFrameStatus,
				[]byte(`{"state":"received"}`),
			),
			frame(protocol.TransferFrameCommitted, nil),
		}
	}
	tests := []struct {
		name     string
		mutate   func(*computerImageTestStream, *nodes.ComputerOutputDescriptor)
		revoked  bool
		canceled bool
	}{
		{name: "valid"},
		{
			name:   "chunk-before-accept",
			mutate: func(s *computerImageTestStream, _ *nodes.ComputerOutputDescriptor) { s.frames = s.frames[1:] },
		},
		{
			name:   "duplicate-accept",
			mutate: func(s *computerImageTestStream, _ *nodes.ComputerOutputDescriptor) { s.frames[1] = s.frames[0] },
		},
		{
			name:   "empty-chunk",
			mutate: func(s *computerImageTestStream, _ *nodes.ComputerOutputDescriptor) { s.frames[1].Payload = nil },
		},
		{name: "overflow", mutate: func(s *computerImageTestStream, _ *nodes.ComputerOutputDescriptor) {
			s.frames[1].Payload = bytes.Repeat([]byte("x"), len(data)+1)
		}},
		{name: "corrupted-pixels", mutate: func(s *computerImageTestStream, _ *nodes.ComputerOutputDescriptor) {
			s.frames[1].Payload = bytes.Repeat([]byte("x"), len(data))
		}},
		{name: "truncated-pixels", mutate: func(s *computerImageTestStream, _ *nodes.ComputerOutputDescriptor) {
			s.frames[1].Payload = data[:len(data)-1]
		}},
		{name: "wrong-status", mutate: func(s *computerImageTestStream, _ *nodes.ComputerOutputDescriptor) {
			s.frames[2].Payload = []byte(`{"state":"pending"}`)
		}},
		{
			name:   "missing-commit-receipt",
			mutate: func(s *computerImageTestStream, _ *nodes.ComputerOutputDescriptor) { s.frames = s.frames[:3] },
		},
		{name: "wrong-commit-receipt", mutate: func(s *computerImageTestStream, _ *nodes.ComputerOutputDescriptor) {
			s.frames[3].Type = protocol.TransferFrameFailure
		}},
		{
			name:   "generation-lost-at-commit",
			mutate: func(s *computerImageTestStream, _ *nodes.ComputerOutputDescriptor) { s.releaseErr = computer.ErrStale },
		},
		{
			name:   "send-failed",
			mutate: func(s *computerImageTestStream, _ *nodes.ComputerOutputDescriptor) { s.sendErr = io.ErrClosedPipe },
		},
		{
			name:   "invalid-digest",
			mutate: func(_ *computerImageTestStream, o *nodes.ComputerOutputDescriptor) { o.SHA256 = "invalid" },
		},
		{name: "unbounded-size", mutate: func(_ *computerImageTestStream, o *nodes.ComputerOutputDescriptor) {
			o.Size = computer.MaxCaptureBytes + 1
		}},
		{name: "revoked-after-transfer", revoked: true},
		{name: "canceled", canceled: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream, descriptor := &computerImageTestStream{frames: valid()}, output
			if test.mutate != nil {
				test.mutate(stream, &descriptor)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.canceled {
				cancel()
			}
			authorityChecks := 0
			image, err := receiveComputerImage(ctx, descriptor, stream, func() error {
				authorityChecks++
				if test.revoked {
					return computer.ErrStale
				}
				return nil
			})
			if test.name == "valid" {
				if err != nil || !bytes.Equal(image, data) || !stream.released || authorityChecks != 1 {
					t.Fatal("valid image lacked complete committed current-authority proof", err)
				}
				if len(stream.sent) != 3 || stream.sent[0].Type != protocol.TransferFramePrepare ||
					stream.sent[1].Type != protocol.TransferFrameAck || stream.sent[2].Type != protocol.TransferFrameCommit {
					t.Fatal("unexpected receiver protocol")
				}
			} else if err == nil || image != nil {
				t.Fatal("unconfirmed image escaped the receiver")
			}
			if test.revoked && !errors.Is(err, computer.ErrStale) {
				t.Fatal("revocation was ignored", err)
			}
			if !stream.closed {
				t.Fatal("transfer resource leaked")
			}
		})
	}
}
