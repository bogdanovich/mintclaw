//go:build !linux

package supervision

import (
	"context"
	"os/exec"
)

type (
	Supervisor struct{}
	Forest     struct{}
)

func Open() (*Supervisor, error) { return nil, errUnavailable }

func (*Supervisor) Start(context.Context, *exec.Cmd) (*Forest, error) { return nil, errUnavailable }

func (*Supervisor) Close(context.Context) error { return errUnavailable }

func (*Forest) Retire(context.Context) error { return errUnavailable }
