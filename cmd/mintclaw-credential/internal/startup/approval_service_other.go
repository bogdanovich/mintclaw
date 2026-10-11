//go:build !linux

package startup

import "context"

func ServeApproval(context.Context) error { return errUnavailable }
