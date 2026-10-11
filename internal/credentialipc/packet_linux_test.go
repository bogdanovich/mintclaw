//go:build linux

package credentialipc

import (
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReceivedRightsAreCloseOnExecBeforeRejection(t *testing.T) {
	descriptors, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	connections := make([]*net.UnixConn, 0, 2)
	for _, fd := range descriptors {
		file := os.NewFile(uintptr(fd), "synthetic packet socket")
		connection, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		packet := connection.(*net.UnixConn)
		connections = append(connections, packet)
		t.Cleanup(func() { _ = packet.Close() })
		if packet.SetDeadline(time.Now().Add(time.Second)) != nil {
			t.Fatal("packet fixture deadline failed")
		}
	}
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, _, err := connections[0].WriteMsgUnix([]byte("synthetic"), unix.UnixRights(int(file.Fd())), nil); err != nil {
		t.Fatal(err)
	}
	data, control := make([]byte, 32), make([]byte, unix.CmsgSpace(4))
	_, controlCount, flags, err := ReadPacket(connections[1], data, control)
	if err != nil || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		t.Fatal("packet fixture did not receive ancillary data")
	}
	messages, err := unix.ParseSocketControlMessage(control[:controlCount])
	if err != nil || len(messages) != 1 {
		t.Fatal("packet fixture ancillary parsing failed")
	}
	rights, err := unix.ParseUnixRights(&messages[0])
	if err != nil || len(rights) != 1 {
		t.Fatal("packet fixture rights parsing failed")
	}
	defer func() { _ = unix.Close(rights[0]) }()
	fdFlags, err := unix.FcntlInt(uintptr(rights[0]), unix.F_GETFD, 0)
	if err != nil || fdFlags&unix.FD_CLOEXEC == 0 {
		t.Fatal("received descriptor could leak to a concurrent child")
	}
}
