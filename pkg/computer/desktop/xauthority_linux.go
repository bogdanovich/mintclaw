//go:build linux

package desktop

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

var localDisplay = regexp.MustCompile(`^:([0-9]{1,5})(?:\.([0-9]{1,3}))?$`)

type x11Endpoint struct {
	socket string
	number string
	screen int
}

func x11Display(value string) (x11Endpoint, error) {
	match := localDisplay.FindStringSubmatch(value)
	if match == nil {
		return x11Endpoint{}, computer.ErrInvalid
	}
	number, err := strconv.ParseUint(match[1], 10, 16)
	if err != nil {
		return x11Endpoint{}, computer.ErrInvalid
	}
	var screen uint64
	if match[2] != "" {
		if screen, err = strconv.ParseUint(match[2], 10, 8); err != nil {
			return x11Endpoint{}, computer.ErrInvalid
		}
	}
	display := strconv.FormatUint(number, 10)
	return x11Endpoint{socket: "/tmp/.X11-unix/X" + display, number: display, screen: int(screen)}, nil
}

func x11Cookie(path, display string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", computer.ErrInvalid
	}
	// Open without following a symlink, then attest the opened file. No HOME or
	// unauthenticated fallback is used; authority is never printed or persisted.
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", computer.ErrDenied
	}
	file := os.NewFile(uintptr(fd), "selected Xauthority")
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 ||
		info.Size() > 64*1024 || !ownedByCurrentUser(info) {
		return "", computer.ErrDenied
	}
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return "", computer.ErrDenied
	}
	defer clear(data)
	host, err := os.Hostname()
	if err != nil {
		return "", computer.ErrUnavailable
	}
	return parseX11Cookie(data, host, display)
}

func parseX11Cookie(data []byte, host, display string) (string, error) {
	reader := bytes.NewReader(data)
	field := func() ([]byte, error) {
		var size uint16
		if err := binary.Read(reader, binary.BigEndian, &size); err != nil {
			return nil, err
		}
		if size > 4096 {
			return nil, computer.ErrDenied
		}
		value := make([]byte, int(size))
		_, err := io.ReadFull(reader, value)
		return value, err
	}
	for reader.Len() > 0 {
		var family uint16
		if binary.Read(reader, binary.BigEndian, &family) != nil {
			return "", computer.ErrDenied
		}
		address, err := field()
		if err != nil {
			return "", computer.ErrDenied
		}
		number, err := field()
		if err != nil {
			return "", computer.ErrDenied
		}
		protocol, err := field()
		if err != nil {
			return "", computer.ErrDenied
		}
		cookie, err := field()
		if err != nil {
			return "", computer.ErrDenied
		}
		match := (family == 65535 || family == 256 && string(address) == host) &&
			(len(number) == 0 || string(number) == display) &&
			string(protocol) == "MIT-MAGIC-COOKIE-1" && len(cookie) == 16
		if match {
			encoded := hex.EncodeToString(cookie)
			clear(cookie)
			return encoded, nil
		}
		clear(cookie)
	}
	return "", computer.ErrDenied
}
