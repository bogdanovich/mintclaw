package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"

	clipboard "golang.design/x/clipboard"
)

func TestClipboardPNGFormatDoesNotRequestNativeTranscoding(t *testing.T) {
	if clipboardPNG == clipboard.FmtImage {
		t.Fatal("clipboard PNG reader uses FmtImage native transcoding")
	}
	if clipboardPNG.MIME() != "image/png" {
		t.Fatalf("clipboard PNG MIME = %q", clipboardPNG.MIME())
	}
}

func TestClipboardTextWriterSelectsNativeTmuxAndOSC52Backends(t *testing.T) {
	tests := []struct {
		name        string
		environment []string
		nativeErr   error
		tmuxErr     error
		wantNative  int
		wantTmux    int
		wantTerm    int
		wantWrapped bool
	}{
		{name: "local native", wantNative: 1},
		{
			name: "remote OSC52", environment: []string{"SSH_CONNECTION=host"},
			wantTerm: 1,
		},
		{
			name: "remote tmux", environment: []string{"SSH_TTY=/dev/pts/1", "TMUX=/tmp/tmux"},
			wantTmux: 1,
		},
		{
			name: "local fallbacks inside tmux", environment: []string{"TMUX_PANE=%1"},
			nativeErr: errors.New("native unavailable"), tmuxErr: errors.New("forwarding unavailable"),
			wantNative: 1, wantTmux: 1, wantTerm: 1, wantWrapped: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var nativeCalls, tmuxCalls, terminalCalls int
			var wrapped bool
			writer := newClipboardTextWriterWith(
				&bytes.Buffer{},
				test.environment,
				func(context.Context, string) error {
					nativeCalls++
					return test.nativeErr
				},
				func(context.Context, string) error {
					tmuxCalls++
					return test.tmuxErr
				},
				func(_ io.Writer, _ string, tmux bool) error {
					terminalCalls++
					wrapped = tmux
					return nil
				},
			)
			if err := writer(t.Context(), "copy me"); err != nil {
				t.Fatal(err)
			}
			if nativeCalls != test.wantNative || tmuxCalls != test.wantTmux || terminalCalls != test.wantTerm ||
				wrapped != test.wantWrapped {
				t.Fatalf(
					"calls native=%d tmux=%d terminal=%d wrapped=%t",
					nativeCalls,
					tmuxCalls,
					terminalCalls,
					wrapped,
				)
			}
		})
	}
}

func TestClipboardTextWriterRejectsOversizedPayloadBeforeAnyBackend(t *testing.T) {
	calls := 0
	writer := newClipboardTextWriterWith(
		&bytes.Buffer{},
		nil,
		func(context.Context, string) error { calls++; return nil },
		func(context.Context, string) error { calls++; return nil },
		func(io.Writer, string, bool) error { calls++; return nil },
	)
	err := writer(t.Context(), strings.Repeat("x", maxClipboardTextBytes+1))
	if err == nil || !strings.Contains(err.Error(), "exceeds") || calls != 0 {
		t.Fatalf("oversized write err=%v backend calls=%d", err, calls)
	}
}

func TestOSC52ClipboardEncodingContainsOneInertPayload(t *testing.T) {
	const untrusted = "hello\x1b]52;c;forged\a\u202eevil"
	var output bytes.Buffer
	if err := writeOSC52ClipboardText(&output, untrusted, false); err != nil {
		t.Fatal(err)
	}
	sequence := output.String()
	if strings.Count(sequence, "\x1b]52;c;") != 1 || !strings.HasSuffix(sequence, "\a") {
		t.Fatalf("OSC52 sequence = %q", sequence)
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(sequence, "\x1b]52;c;"), "\a")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || string(decoded) != untrusted {
		t.Fatalf("OSC52 payload decoded=%q err=%v", decoded, err)
	}

	output.Reset()
	if err := writeOSC52ClipboardText(&output, "hello", true); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "\x1bPtmux;\x1b\x1b]52;c;aGVsbG8=\a\x1b\\"; got != want {
		t.Fatalf("tmux OSC52 sequence = %q, want %q", got, want)
	}

	output.Reset()
	err = writeOSC52ClipboardText(&output, strings.Repeat("x", maxClipboardTextBytes+1), false)
	if err == nil || output.Len() != 0 {
		t.Fatalf("oversized OSC52 err=%v bytes=%d", err, output.Len())
	}
}

func TestTmuxClipboardReadinessRequiresForwardingAndMsCapability(t *testing.T) {
	for _, test := range []struct {
		name         string
		setClipboard string
		info         string
		wantError    string
	}{
		{name: "ready on", setClipboard: "on\n", info: "Ms: set clipboard"},
		{name: "ready external", setClipboard: "external", info: "Ms: set clipboard"},
		{name: "disabled", setClipboard: "off\n", info: "Ms: set clipboard", wantError: "disabled"},
		{name: "missing capability", setClipboard: "on", info: "Ms: [missing]", wantError: "missing Ms"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := tmuxClipboardReady(test.setClipboard, test.info)
			if test.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("readiness err=%v, want %q", err, test.wantError)
			}
		})
	}
}
