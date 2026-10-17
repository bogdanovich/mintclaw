//go:build !linux && !darwin

package desktop

import "context"

func watchNativeWindow(context.Context, WindowRequest) (windowBackend, error) {
	return nil, ErrUnsupported
}
