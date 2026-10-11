//go:build !linux

package approval

import "context"

func Enroll(context.Context) error { return ErrUnavailable }
