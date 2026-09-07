//go:build windows

package coding

import (
	"context"
	"os"
	"os/signal"
)

func newExecSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt)
}
