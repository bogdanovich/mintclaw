package mcp

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

var (
	errExclusiveLeaseBusy   = errors.New("exclusive lease busy")
	errExclusiveLeaseUnsafe = errors.New("exclusive lease file is unsafe")
)

// ExclusiveLeaseBusyError classifies a configured MCP server lease that is
// already held by another cooperating process.
type ExclusiveLeaseBusyError struct {
	Server string
}

func (e *ExclusiveLeaseBusyError) Error() string {
	if e != nil && e.Server != "" {
		return fmt.Sprintf("MCP server %s exclusive lease is busy", e.Server)
	}
	return "MCP server exclusive lease is busy"
}

func (e *ExclusiveLeaseBusyError) Unwrap() error {
	return errExclusiveLeaseBusy
}

type exclusiveServerLease struct {
	file *os.File
	once sync.Once
}

type exclusiveLeaseParent struct {
	file     *os.File
	path     string
	identity os.FileInfo
}

func (parent *exclusiveLeaseParent) validate() error {
	if parent == nil || parent.file == nil || parent.identity == nil {
		return errExclusiveLeaseUnsafe
	}
	opened, openedErr := parent.file.Stat()
	current, currentErr := os.Lstat(parent.path)
	if openedErr != nil || currentErr != nil || !current.IsDir() ||
		current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(parent.identity, opened) || !os.SameFile(parent.identity, current) {
		return errExclusiveLeaseUnsafe
	}
	return nil
}

func (parent *exclusiveLeaseParent) close() {
	if parent != nil && parent.file != nil {
		_ = parent.file.Close()
	}
}

// ExclusiveServerLease is a process-scoped owner of the same lock used by a
// private MCP server. It lets trusted lifecycle code reconcile host-owned
// state only while no server process can be using that state.
type ExclusiveServerLease struct {
	lease *exclusiveServerLease
}

// AcquireExclusiveServerLease acquires the configured MCP process lock
// without starting the server. Callers must close the returned lease.
func AcquireExclusiveServerLease(serverName, path string) (*ExclusiveServerLease, error) {
	lease, err := acquireExclusiveServerLease(serverName, path)
	if err != nil {
		return nil, err
	}
	return &ExclusiveServerLease{lease: lease}, nil
}

func (lease *ExclusiveServerLease) Close() error {
	if lease != nil && lease.lease != nil {
		lease.lease.release()
	}
	return nil
}

func acquireExclusiveServerLease(serverName, path string) (*exclusiveServerLease, error) {
	file, err := openExclusiveLeaseFile(path)
	if err != nil {
		return nil, fmt.Errorf("open MCP server exclusive lease: %w", withoutPath(err))
	}
	if err := tryAcquireExclusiveFileLock(file); err != nil {
		_ = file.Close()
		if errors.Is(err, errExclusiveLeaseBusy) {
			return nil, &ExclusiveLeaseBusyError{Server: serverName}
		}
		return nil, fmt.Errorf("lock MCP server exclusive lease: %w", err)
	}
	return &exclusiveServerLease{file: file}, nil
}

func withoutPath(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

func (l *exclusiveServerLease) release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		_ = releaseExclusiveFileLock(l.file)
		_ = l.file.Close()
	})
}
