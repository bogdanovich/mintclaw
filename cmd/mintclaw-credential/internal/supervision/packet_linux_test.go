//go:build linux

package supervision

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/internal/credentialipc"
)

func resourcePacketPair(t *testing.T) (*net.UnixConn, *net.UnixConn, credentialipc.Peer) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Fatal("synthetic packet pair unavailable")
	}
	connections := make([]*net.UnixConn, 0, 2)
	for _, fd := range fds {
		file := os.NewFile(uintptr(fd), "synthetic packet endpoint")
		connection, convertErr := net.FileConn(file)
		_ = file.Close()
		if convertErr != nil {
			t.Fatal("synthetic packet conversion failed")
		}
		packet, ok := connection.(*net.UnixConn)
		if !ok || prepareResourceConnection(packet) != nil {
			t.Fatal("synthetic packet preparation failed")
		}
		t.Cleanup(func() { _ = packet.Close() })
		connections = append(connections, packet)
	}
	peer, err := credentialipc.ConnectedPeer(connections[0])
	if err != nil {
		t.Fatal("required SO_PEERPIDFD unavailable")
	}
	t.Cleanup(peer.Close)
	return connections[0], connections[1], peer
}

func pipeReferences(t *testing.T, file *os.File) int {
	t.Helper()
	var original unix.Stat_t
	if unix.Fstat(int(file.Fd()), &original) != nil {
		t.Fatal("synthetic pipe identity unavailable")
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal("synthetic descriptor inventory unavailable")
	}
	count := 0
	for _, entry := range entries {
		fd, parseErr := strconv.Atoi(entry.Name())
		var current unix.Stat_t
		if parseErr == nil && unix.Fstat(fd, &current) == nil && current.Dev == original.Dev &&
			current.Ino == original.Ino {
			count++
		}
	}
	return count
}

func TestResourcePacketRightsAndRejection(t *testing.T) {
	for _, test := range []struct {
		name     string
		body     []byte
		rights   int
		expected int
		foreign  bool
		valid    bool
	}{
		{"request", []byte{1, 0}, 0, 0, false, true},
		{"reply-pair", []byte{1, 1}, 2, 2, false, true},
		{"rights-on-request", []byte{1, 0}, 2, 0, false, false},
		{"missing-rights", []byte{1, 1}, 0, 2, false, false},
		{"one-right", []byte{1, 1}, 1, 2, false, false},
		{"extra-right", []byte{1, 1}, 3, 2, false, false},
		{"truncated-rights", []byte{1, 1}, 32, 2, false, false},
		{"oversized-body", []byte{1, 1, 0, 0}, 2, 2, false, false},
		{"short-body", []byte{1}, 2, 2, false, false},
		{"wrong-operation", []byte{2, 1}, 2, 2, false, false},
		{"wrong-reserved", []byte{1, 2}, 2, 2, false, false},
		{"foreign-creator", []byte{1, 1}, 2, 2, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			receiver, sender, peer := resourcePacketPair(t)
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal("synthetic pipe unavailable")
			}
			defer func() { _ = read.Close(); _ = write.Close() }()
			before := pipeReferences(t, read)
			var control []byte
			if test.rights != 0 {
				fds := make([]int, test.rights)
				for index := range fds {
					fds[index] = int(read.Fd())
				}
				control = unix.UnixRights(fds...)
			}
			if _, _, err = sender.WriteMsgUnix(test.body, control, nil); err != nil {
				t.Fatal("synthetic packet send failed")
			}
			if test.foreign {
				peer.UID++
			}
			status := byte(0)
			if test.expected != 0 {
				status = 1
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			files, err := readResourcePacket(ctx, receiver, peer, allocateOperation, status, test.expected)
			if test.valid && (err != nil || len(files) != test.expected) {
				t.Fatal("valid resource packet denied")
			}
			if !test.valid && (files != nil || !errors.Is(err, errUnavailable)) {
				t.Fatal("invalid resource packet admitted")
			}
			for _, file := range files {
				flags, flagErr := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
				if flagErr != nil || flags&unix.FD_CLOEXEC == 0 {
					t.Error("received right lacks atomic CLOEXEC")
				}
				_ = file.Close()
			}
			if pipeReferences(t, read) != before {
				t.Fatal("resource packet leaked installed rights")
			}
		})
	}
}

func TestResourcePacketRevokedIdentity(t *testing.T) {
	receiver, _, peer := resourcePacketPair(t)
	peer.Close()
	if files, err := readResourcePacket(t.Context(), receiver, peer, allocateOperation, 0, 0); files != nil ||
		!errors.Is(err, errUnavailable) {
		t.Fatal("closed process-lifetime handle admitted")
	}
}

func TestResourcePacketRejectsProcessHandoff(t *testing.T) {
	receiver, sender, peer := resourcePacketPair(t)
	file, err := sender.File()
	if err != nil {
		t.Fatal("synthetic handoff descriptor unavailable")
	}
	defer func() { _ = file.Close() }()
	self, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic handoff executable unavailable")
	}
	command := exec.Command(self, "resource-packet-handoff")
	command.ExtraFiles = []*os.File{file}
	command.Env = []string{"PATH=/usr/bin:/bin"}
	if command.Run() != nil {
		t.Fatal("synthetic handoff process failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if files, err := readResourcePacket(ctx, receiver, peer, allocateOperation, 0, 0); files != nil ||
		!errors.Is(err, errUnavailable) {
		t.Fatal("different process used creator's packet authority")
	}
}
