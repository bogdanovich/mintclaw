//go:build !windows && !linux

package mcp

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"syscall"
	"time"
)

const (
	exclusiveLeasePortBase       = 30000
	exclusiveLeasePortCount      = 15001
	exclusiveLeasePortCandidates = 8
	exclusiveLeaseIdentityDomain = "mintclaw-exclusive-lease-identity-v1\x00"
	exclusiveLeaseWireProtocol   = "mintclaw-exclusive-lease-v1 "
)

// acquireExclusiveLeaseNamespace reserves a loopback TCP endpoint and serves
// the complete configured-path digest on it. Supported non-Linux Unix systems
// do not provide Linux abstract Unix sockets. Collision-aware probing keeps
// distinct paths independent, while the live endpoint gives one path a
// kernel-owned identity that cannot be renamed or unlinked.
func acquireExclusiveLeaseNamespace(path string) (*exclusiveLeaseNamespace, error) {
	key, ports := exclusiveLeaseNamespaceIdentity(path)
	var listener net.Listener
	var done chan struct{}
	closeListener := func() {
		if listener != nil {
			_ = listener.Close()
			<-done
		}
	}
	for _, port := range ports {
		address := fmt.Sprintf("127.0.0.1:%d", port)
		if listener == nil {
			candidate, err := net.Listen("tcp4", address)
			if err == nil {
				listener = candidate
				done = make(chan struct{})
				go serveExclusiveLeaseNamespace(listener, key, done)
				continue
			}
			if !errors.Is(err, syscall.EADDRINUSE) {
				return nil, errExclusiveLeaseUnsafe
			}
		}
		occupiedKey, occupied, err := probeExclusiveLeaseNamespace(address)
		if err != nil {
			closeListener()
			return nil, errExclusiveLeaseUnsafe
		}
		if occupied && occupiedKey == key {
			closeListener()
			return nil, errExclusiveLeaseBusy
		}
	}
	if listener == nil {
		return nil, errExclusiveLeaseBusy
	}
	return &exclusiveLeaseNamespace{closeFn: func() error {
		err := listener.Close()
		<-done
		return err
	}}, nil
}

func exclusiveLeaseNamespaceIdentity(path string) (string, []int) {
	digest := sha256.Sum256([]byte(
		exclusiveLeaseIdentityDomain + strconv.Itoa(os.Geteuid()) + "\x00" + path,
	))
	key := hex.EncodeToString(digest[:])
	ports := make([]int, 0, exclusiveLeasePortCandidates)
	seen := make(map[int]struct{}, exclusiveLeasePortCandidates)
	block := digest
	for round := uint32(0); len(ports) < exclusiveLeasePortCandidates; round++ {
		for offset := 0; offset+2 <= len(block) && len(ports) < exclusiveLeasePortCandidates; offset += 2 {
			port := exclusiveLeasePortBase +
				int(binary.BigEndian.Uint16(block[offset:offset+2]))%exclusiveLeasePortCount
			if _, found := seen[port]; found {
				continue
			}
			seen[port] = struct{}{}
			ports = append(ports, port)
		}
		var next [sha256.Size + 4]byte
		copy(next[:], block[:])
		binary.BigEndian.PutUint32(next[sha256.Size:], round+1)
		block = sha256.Sum256(next[:])
	}
	sort.Ints(ports)
	return key, ports
}

func serveExclusiveLeaseNamespace(listener net.Listener, key string, done chan<- struct{}) {
	defer close(done)
	payload := []byte(exclusiveLeaseWireProtocol + key + "\n")
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		_ = connection.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, _ = connection.Write(payload)
		_ = connection.Close()
	}
}

func probeExclusiveLeaseNamespace(address string) (string, bool, error) {
	connection, err := net.DialTimeout("tcp4", address, 2*time.Second)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return "", false, nil
		}
		return "", false, err
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	payload := make([]byte, len(exclusiveLeaseWireProtocol)+sha256.Size*2+1)
	if _, err = io.ReadFull(connection, payload); err != nil {
		return "", true, err
	}
	if string(payload[:len(exclusiveLeaseWireProtocol)]) != exclusiveLeaseWireProtocol ||
		payload[len(payload)-1] != '\n' {
		return "", true, errExclusiveLeaseUnsafe
	}
	key := string(payload[len(exclusiveLeaseWireProtocol) : len(payload)-1])
	decoded := make([]byte, sha256.Size)
	if _, err = hex.Decode(decoded, []byte(key)); err != nil {
		return "", true, errExclusiveLeaseUnsafe
	}
	return key, true, nil
}
