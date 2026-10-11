//go:build linux || darwin

package provisioning

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func readOperatorToken(ctx context.Context) ([]byte, error) {
	file, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, ErrInput
	}
	defer func() { _ = file.Close() }()
	return readTerminalToken(ctx, file)
}

func readTerminalToken(ctx context.Context, file *os.File) (token []byte, resultErr error) {
	fd := int(file.Fd())
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return nil, ErrInput
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, ErrInput
	}
	defer func() {
		if flushTerminal(fd) != nil {
			resultErr = ErrInput
		}
		if term.Restore(fd, state) != nil {
			resultErr = ErrInput
		}
		_, _ = unix.Write(fd, []byte("\n"))
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags); err != nil {
			resultErr = ErrInput
		}
		if resultErr != nil {
			clear(token)
			token = nil
		}
	}()
	if unix.SetNonblock(fd, true) != nil {
		return nil, ErrInput
	}
	if _, err := unix.Write(
		fd,
		[]byte("Use a read-only service account for the dedicated MintClaw vault.\r\nToken: "),
	); err != nil {
		return nil, ErrInput
	}
	token = make([]byte, 0, maxTokenBytes)
	var input [128]byte
	defer clear(input[:])
	for {
		if ctx.Err() != nil {
			return token, ErrInput
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 100); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return token, ErrInput
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return token, ErrInput
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		count, err := unix.Read(fd, input[:])
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil || count == 0 {
			return token, ErrInput
		}
		for _, value := range input[:count] {
			switch value {
			case '\r', '\n':
				return token, nil
			case 0x03, 0x04:
				return token, ErrInput
			case 0x08, 0x7f:
				if len(token) > 0 {
					token[len(token)-1] = 0
					token = token[:len(token)-1]
				}
			default:
				if value < 0x21 || value > 0x7e || len(token) == maxTokenBytes {
					return token, ErrInput
				}
				token = append(token, value)
			}
		}
		clear(input[:])
	}
}
