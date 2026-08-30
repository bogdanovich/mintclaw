package tui

import (
	"context"
	"errors"
	"fmt"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	clipboard "golang.design/x/clipboard"
)

type clipboardImageReader func(context.Context) ([]byte, error)

var (
	clipboardInitOnce sync.Once
	errClipboardInit  error
)

func readSystemClipboardImage(ctx context.Context) ([]byte, error) {
	clipboardInitOnce.Do(func() {
		errClipboardInit = clipboard.Init()
	})
	if errClipboardInit != nil {
		return nil, fmt.Errorf("initialize system clipboard: %w", errClipboardInit)
	}
	data, err := clipboard.Read(ctx, clipboard.FmtImage)
	if err != nil {
		if errors.Is(err, clipboard.ErrNoData) {
			return nil, errors.New("system clipboard does not contain an image")
		}
		return nil, fmt.Errorf("read system clipboard image: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("system clipboard does not contain an image")
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
