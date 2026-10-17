//go:build !linux

package desktop

import "context"

func watchNativeWindow(context.Context, WindowRequest) (windowBackend, error) {
	return nil, ErrUnsupported
}
