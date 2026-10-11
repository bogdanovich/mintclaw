//go:build !linux

package supervision

import (
	"context"
	"os"
)

type (
	Launcher struct{}
	Worker   struct{}
)

func OpenLauncher(context.Context) (*Launcher, error)      { return nil, errUnavailable }
func (*Launcher) Prepare(context.Context) (*Worker, error) { return nil, errUnavailable }
func (*Launcher) Close(context.Context) error              { return errUnavailable }
func (*Worker) Endpoints(context.Context) (*os.File, *os.File, error) {
	return nil, nil, errUnavailable
}
func (*Worker) Start(context.Context) error  { return errUnavailable }
func (*Worker) Retire(context.Context) error { return errUnavailable }
func (*Worker) Done() <-chan struct{}        { return nil }
func BootstrapBrowser(context.Context) error { return errUnavailable }
