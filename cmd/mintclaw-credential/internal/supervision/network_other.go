//go:build !linux

package supervision

import (
	"context"
	"os"
)

type Network struct{}

func OpenNetwork(context.Context) (*Network, error)                 { return nil, errUnavailable }
func (*Network) Borrow(context.Context) (*os.File, *os.File, error) { return nil, nil, errUnavailable }
func (*Network) Close() error                                       { return nil }
