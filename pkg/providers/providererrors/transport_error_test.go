package providererrors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"golang.org/x/net/http2"
)

func TestFromTransportError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind Kind
	}{
		{name: "canceled", err: context.Canceled, kind: KindCanceled},
		{name: "deadline", err: context.DeadlineExceeded, kind: KindTimeout},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, kind: KindNetwork},
		{
			name: "network timeout",
			err:  &net.DNSError{IsTimeout: true, Err: "timeout", Name: "api.example.com"},
			kind: KindTimeout,
		},
		{
			name: "network failure",
			err:  &net.DNSError{Err: "temporary resolver failure", Name: "api.example.com"},
			kind: KindNetwork,
		},
		{
			name: "peer HTTP/2 internal stream failure",
			err: fmt.Errorf("read response stream: %w", http2.StreamError{
				StreamID: 21,
				Code:     http2.ErrCodeInternal,
				Cause:    errors.New("received from peer"),
			}),
			kind: KindNetwork,
		},
		{
			name: "peer refused HTTP/2 stream",
			err: fmt.Errorf("read response stream: %w", http2.StreamError{
				StreamID: 23,
				Code:     http2.ErrCodeRefusedStream,
			}),
			kind: KindNetwork,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := FromTransportError(test.err)
			if !ok || got.Kind != test.kind {
				t.Fatalf("FromTransportError() = (%#v, %v), want kind %q", got, ok, test.kind)
			}
			if !errors.Is(got, test.err) {
				t.Fatal("normalized error did not retain transport cause")
			}
		})
	}
}

func TestFromTransportErrorRejectsUnknown(t *testing.T) {
	if got, ok := FromTransportError(errors.New("provider-specific failure")); ok || got != nil {
		t.Fatalf("FromTransportError() = (%#v, %v), want no normalization", got, ok)
	}
	protocolFailure := http2.StreamError{StreamID: 29, Code: http2.ErrCodeProtocol}
	if got, ok := FromTransportError(protocolFailure); ok || got != nil {
		t.Fatalf("protocol stream error = (%#v, %v), want adapter classification", got, ok)
	}
}
