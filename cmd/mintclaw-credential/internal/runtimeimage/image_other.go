//go:build !linux

package runtimeimage

import (
	"context"
	"os"
)

type Image struct{}

func Open(context.Context) (*Image, error)                  { return nil, errUnavailable }
func (*Image) BorrowRoot(context.Context) (*os.File, error) { return nil, errUnavailable }
func (*Image) Close() error                                 { return nil }
