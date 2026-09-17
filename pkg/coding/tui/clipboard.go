package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	clipboard "golang.design/x/clipboard"
)

type (
	clipboardImageReader    func(context.Context) ([]byte, error)
	clipboardTextWriter     func(context.Context, string) error
	terminalClipboardWriter func(io.Writer, string, bool) error
	tmuxClipboardWriter     func(context.Context, string) error
)

const (
	maxClipboardTextBytes = 100_000
	tmuxClipboardTimeout  = 2 * time.Second
)

var (
	clipboardInitOnce sync.Once
	errClipboardInit  error
	clipboardPNG      = clipboard.Register("image/png")
)

func readSystemClipboardImage(ctx context.Context) ([]byte, error) {
	clipboardInitOnce.Do(func() {
		errClipboardInit = clipboard.Init()
	})
	if errClipboardInit != nil {
		return nil, fmt.Errorf("initialize system clipboard: %w", errClipboardInit)
	}
	// Read the raw PNG representation only. FmtImage may ask native backends to
	// decode DIB/TIFF pixels and transcode them before MintClaw can enforce its
	// byte and dimension limits.
	data, err := clipboard.Read(ctx, clipboardPNG)
	if err != nil {
		if errors.Is(err, clipboard.ErrNoData) {
			return nil, errors.New("system clipboard does not contain a PNG image")
		}
		return nil, fmt.Errorf("read system clipboard PNG image: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("system clipboard does not contain a PNG image")
	}
	return data, nil
}

func clipboardImageCmd(ctx context.Context, reader clipboardImageReader) tea.Cmd {
	return func() tea.Msg {
		if reader == nil {
			return ClipboardImageMsg{Err: errors.New("system clipboard image reader is unavailable")}
		}
		data, err := reader(ctx)
		return ClipboardImageMsg{Data: data, Err: err}
	}
}

func writeSystemClipboardText(ctx context.Context, text string) error {
	clipboardInitOnce.Do(func() {
		errClipboardInit = clipboard.Init()
	})
	if errClipboardInit != nil {
		return fmt.Errorf("initialize system clipboard: %w", errClipboardInit)
	}
	if _, err := clipboard.Write(ctx, clipboard.FmtText, []byte(text)); err != nil {
		return fmt.Errorf("write system clipboard text: %w", err)
	}
	return nil
}

func newClipboardTextWriter(
	output io.Writer,
	environment []string,
) clipboardTextWriter {
	return newClipboardTextWriterWith(
		output,
		environment,
		writeSystemClipboardText,
		writeTmuxClipboardText,
		writeOSC52ClipboardText,
	)
}

func newClipboardTextWriterWith(
	output io.Writer,
	environment []string,
	native clipboardTextWriter,
	tmux tmuxClipboardWriter,
	terminal terminalClipboardWriter,
) clipboardTextWriter {
	remote := environmentHasAny(environment, "SSH_TTY", "SSH_CONNECTION")
	insideTmux := environmentHasAny(environment, "TMUX", "TMUX_PANE")
	return func(ctx context.Context, text string) error {
		if len(text) > maxClipboardTextBytes {
			return fmt.Errorf("clipboard text exceeds %d bytes", maxClipboardTextBytes)
		}
		var failures []error
		if !remote && native != nil {
			if err := native(ctx, text); err == nil {
				return nil
			} else {
				failures = append(failures, fmt.Errorf("native clipboard: %w", err))
			}
		}
		if insideTmux && tmux != nil {
			if err := tmux(ctx, text); err == nil {
				return nil
			} else {
				failures = append(failures, fmt.Errorf("tmux clipboard: %w", err))
			}
		}
		if terminal == nil || output == nil {
			failures = append(failures, errors.New("terminal clipboard is unavailable"))
			return errors.Join(failures...)
		}
		if err := terminal(output, text, insideTmux); err != nil {
			failures = append(failures, fmt.Errorf("terminal clipboard: %w", err))
			return errors.Join(failures...)
		}
		return nil
	}
}

// writeTmuxClipboardText uses tmux's paste buffer and clipboard forwarding,
// avoiding native clipboard access on a remote host and DCS passthrough when
// tmux has a usable Ms terminal capability.
func writeTmuxClipboardText(ctx context.Context, text string) error {
	copyCtx, cancel := context.WithTimeout(ctx, tmuxClipboardTimeout)
	defer cancel()
	setClipboard, err := tmuxCommandOutput(copyCtx, "show-options", "-gv", "set-clipboard")
	if err != nil {
		return err
	}
	info, err := tmuxCommandOutput(copyCtx, "info")
	if err != nil {
		return err
	}
	if err := tmuxClipboardReady(setClipboard, info); err != nil {
		return err
	}
	var stderr bytes.Buffer
	command := exec.CommandContext(copyCtx, "tmux", "load-buffer", "-w", "-")
	command.Stdin = strings.NewReader(text)
	command.Stdout = io.Discard
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return fmt.Errorf("load buffer: %s: %w", detail, err)
		}
		return fmt.Errorf("load buffer: %w", err)
	}
	return nil
}

func tmuxClipboardReady(setClipboard, info string) error {
	if strings.TrimSpace(setClipboard) == "off" {
		return errors.New("clipboard forwarding is disabled")
	}
	if strings.Contains(info, "Ms: [missing]") {
		return errors.New("clipboard forwarding is unavailable: missing Ms capability")
	}
	return nil
}

func tmuxCommandOutput(ctx context.Context, arguments ...string) (string, error) {
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, "tmux", arguments...)
	command.Stderr = &stderr
	output, err := command.Output()
	if err == nil {
		return string(output), nil
	}
	if detail := strings.TrimSpace(stderr.String()); detail != "" {
		return "", fmt.Errorf("tmux %s: %s: %w", strings.Join(arguments, " "), detail, err)
	}
	return "", fmt.Errorf("tmux %s: %w", strings.Join(arguments, " "), err)
}

// writeOSC52ClipboardText base64-encodes untrusted text so it cannot inject a
// second terminal command. Inside tmux, DCS passthrough wraps the sequence.
func writeOSC52ClipboardText(output io.Writer, text string, tmux bool) error {
	if output == nil {
		return errors.New("terminal output is unavailable")
	}
	if len(text) > maxClipboardTextBytes {
		return fmt.Errorf("clipboard text exceeds %d bytes", maxClipboardTextBytes)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	sequence := "\x1b]52;c;" + encoded + "\a"
	if tmux {
		sequence = "\x1bPtmux;\x1b" + sequence + "\x1b\\"
	}
	_, err := io.WriteString(output, sequence)
	return err
}

func environmentHasAny(environment []string, names ...string) bool {
	for _, name := range names {
		if strings.TrimSpace(environmentValue(environment, name)) != "" {
			return true
		}
	}
	return false
}
