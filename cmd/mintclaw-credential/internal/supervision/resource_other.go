//go:build !linux

package supervision

import (
	"context"
	"os"
)

type ResourceWorker struct{}

func ServeResources(context.Context) error                       { return errUnavailable }
func AllocateResources(context.Context) (*ResourceWorker, error) { return nil, errUnavailable }
func (*ResourceWorker) EndpointFiles(context.Context) (*os.File, *os.File, error) {
	return nil, nil, errUnavailable
}
func (*ResourceWorker) Start(context.Context) error  { return errUnavailable }
func (*ResourceWorker) Retire(context.Context) error { return errUnavailable }
func (*ResourceWorker) Close()                       {}
func (*ResourceWorker) Done() <-chan struct{}        { return nil }
