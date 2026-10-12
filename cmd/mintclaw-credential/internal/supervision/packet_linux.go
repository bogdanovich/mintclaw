//go:build linux

package supervision

import (
	"context"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/internal/credentialipc"
)

const (
	allocateOperation byte = 1
	startOperation    byte = 2
	retireOperation   byte = 3
)

// Resource controls contain only an operation and a fixed status/reserved byte.
// The original connected process must remain alive and send every packet itself.
// Only the allocation reply is permitted to carry exactly two descriptors.
func readResourcePacket(ctx context.Context, connection *net.UnixConn, peer credentialipc.Peer,
	operation, status byte, descriptorCount int,
) (result []*os.File, resultErr error) {
	if ctx == nil || ctx.Err() != nil || connection == nil || !peer.Alive() ||
		operation < allocateOperation || operation > retireOperation || status > 1 ||
		(descriptorCount != 0 && descriptorCount != 2) ||
		(operation == allocateOperation && status == 1) != (descriptorCount == 2) {
		return nil, errUnavailable
	}
	deadline := time.Now().Add(5 * time.Second)
	if maximum, ok := ctx.Deadline(); ok {
		deadline = maximum
	}
	if connection.SetReadDeadline(deadline) != nil {
		return nil, errUnavailable
	}
	var body [3]byte
	control := make([]byte, unix.CmsgSpace(12)+unix.CmsgSpace(16*4))
	count, controlCount, flags, readErr := credentialipc.ReadPacket(connection, body[:], control)
	rights, valid := resourceAncillary(control[:controlCount], peer, descriptorCount)
	keep := false
	defer func() {
		if !keep {
			for _, fd := range rights {
				_ = unix.Close(fd)
			}
		}
	}()
	if !valid || readErr != nil || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || count != 2 ||
		body[0] != operation || body[1] != status || ctx.Err() != nil || !peer.Alive() {
		return nil, errUnavailable
	}
	for _, fd := range rights {
		result = append(result, os.NewFile(uintptr(fd), "private credential resource endpoint"))
	}
	keep = true
	return result, nil
}

func resourceAncillary(control []byte, peer credentialipc.Peer, descriptorCount int) ([]int, bool) {
	messages, err := unix.ParseSocketControlMessage(control)
	valid := err == nil
	credentials, rightsMessages := 0, 0
	var rights []int
	for _, message := range messages {
		if message.Header.Level != unix.SOL_SOCKET {
			valid = false
			continue
		}
		switch message.Header.Type {
		case unix.SCM_CREDENTIALS:
			credentials++
			value, parseErr := unix.ParseUnixCredentials(&message)
			if parseErr != nil || value.Pid != peer.PID || value.Uid != peer.UID || value.Gid != peer.GID {
				valid = false
			}
		case unix.SCM_RIGHTS:
			rightsMessages++
			values, parseErr := unix.ParseUnixRights(&message)
			rights = append(rights, values...)
			valid = valid && parseErr == nil
		default:
			valid = false
		}
	}
	expectedRightsMessages := 0
	if descriptorCount != 0 {
		expectedRightsMessages = 1
	}
	return rights, valid && credentials == 1 && rightsMessages == expectedRightsMessages &&
		len(rights) == descriptorCount
}

func writeResourcePacket(ctx context.Context, connection *net.UnixConn, peer credentialipc.Peer,
	operation, status byte, files []*os.File,
) error {
	if ctx == nil || ctx.Err() != nil || connection == nil || !peer.Alive() ||
		operation < allocateOperation || operation > retireOperation || status > 1 ||
		len(files) != 0 && len(files) != 2 ||
		(operation == allocateOperation && status == 1) != (len(files) == 2) {
		return errUnavailable
	}
	if connection.SetWriteDeadline(time.Now().Add(time.Second)) != nil {
		return errUnavailable
	}
	var control []byte
	if len(files) != 0 {
		for _, file := range files {
			if file == nil {
				return errUnavailable
			}
		}
		control = unix.UnixRights(int(files[0].Fd()), int(files[1].Fd()))
	}
	count, _, err := connection.WriteMsgUnix([]byte{operation, status}, control, nil)
	if err != nil || count != 2 || ctx.Err() != nil || !peer.Alive() {
		return errUnavailable
	}
	return nil
}
