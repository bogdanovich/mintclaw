//go:build !windows

package fstools

import "syscall"

func nonBlockingReadFlag() int { return syscall.O_NONBLOCK }
