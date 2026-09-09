//go:build linux

package mcp

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// acquireExclusiveLeaseNamespace reserves an abstract Unix socket whose name
// is the complete digest of the configured path. The socket name lives in the
// kernel rather than a writable filesystem directory, so another process
// cannot unlink or rename it while the lease is held.
func acquireExclusiveLeaseNamespace(path string) (*exclusiveLeaseNamespace, error) {
	digest := sha256.Sum256([]byte(path))
	address := fmt.Sprintf("@mintclaw-lease-%d-%x", os.Geteuid(), digest)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return nil, errExclusiveLeaseBusy
		}
		return nil, errExclusiveLeaseUnsafe
	}
	return &exclusiveLeaseNamespace{closeFn: listener.Close}, nil
}
