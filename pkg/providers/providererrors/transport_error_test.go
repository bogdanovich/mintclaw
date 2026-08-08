package providererrors

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
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
}
