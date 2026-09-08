package mcp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	file        *os.File
	parent      *exclusiveLeaseParent
	namespace   *exclusiveLeaseNamespace
	reservation string
	mu          sync.Mutex
	closed      bool
}

type exclusiveLeaseNamespace struct {
	mu      sync.Mutex
	closeFn func() error
	closed  bool
}

func (namespace *exclusiveLeaseNamespace) validate() error {
	if namespace == nil {
		return nil
	}
	namespace.mu.Lock()
	defer namespace.mu.Unlock()
	if namespace.closed || namespace.closeFn == nil {
		return errExclusiveLeaseUnsafe
	}
	return nil
}

func (namespace *exclusiveLeaseNamespace) close() error {
	if namespace == nil {
		return nil
	}
	namespace.mu.Lock()
	defer namespace.mu.Unlock()
	if namespace.closed {
		return nil
	}
	if namespace.closeFn == nil {
		return errExclusiveLeaseUnsafe
	}
	namespace.closed = true
	return namespace.closeFn()
}

type exclusiveLeaseParent struct {
	file     *os.File
	path     string
	leaf     string
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

func (parent *exclusiveLeaseParent) validateLeaf(file *os.File) error {
	if err := parent.validate(); err != nil || file == nil || parent.leaf == "" {
		return errExclusiveLeaseUnsafe
	}
	opened, openedErr := file.Stat()
	current, currentErr := os.Lstat(filepath.Join(parent.path, parent.leaf))
	if openedErr != nil || currentErr != nil || !opened.Mode().IsRegular() ||
		!current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(opened, current) {
		return errExclusiveLeaseUnsafe
	}
	return parent.validate()
}

func (parent *exclusiveLeaseParent) close() {
	if parent != nil && parent.file != nil {
		_ = parent.file.Close()
	}
}

var exclusiveLeaseReservations = struct {
	sync.Mutex
	paths map[string]struct{}
}{paths: make(map[string]struct{})}

func reserveExclusiveLeasePath(path string) (string, bool) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", false
	}
	key := exclusiveLeaseReservationKey(path)
	exclusiveLeaseReservations.Lock()
	defer exclusiveLeaseReservations.Unlock()
	if _, found := exclusiveLeaseReservations.paths[key]; found {
		return key, false
	}
	exclusiveLeaseReservations.paths[key] = struct{}{}
	return key, true
}

func releaseExclusiveLeasePath(key string) {
	if key == "" {
		return
	}
	exclusiveLeaseReservations.Lock()
	delete(exclusiveLeaseReservations.paths, key)
	exclusiveLeaseReservations.Unlock()
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
		return lease.lease.close()
	}
	return nil
}

// Validate confirms that the configured namespace still names the parent and
// leaf owned by this lease. Lifecycle callers use it around protected work so
// a rebound path becomes a retryable fail-closed condition.
func (lease *ExclusiveServerLease) Validate() error {
	if lease == nil || lease.lease == nil {
		return errExclusiveLeaseUnsafe
	}
	return lease.lease.validate()
}

func acquireExclusiveServerLease(serverName, path string) (*exclusiveServerLease, error) {
	reservation, reserved := reserveExclusiveLeasePath(path)
	if !reserved {
		if filepath.IsAbs(path) && filepath.Clean(path) == path {
			return nil, &ExclusiveLeaseBusyError{Server: serverName}
		}
		return nil, fmt.Errorf("open MCP server exclusive lease: %w", errExclusiveLeaseUnsafe)
	}
	releaseReservation := true
	defer func() {
		if releaseReservation {
			releaseExclusiveLeasePath(reservation)
		}
	}()
	namespace, err := acquireExclusiveLeaseNamespace(path)
	if err != nil {
		if errors.Is(err, errExclusiveLeaseBusy) {
			return nil, &ExclusiveLeaseBusyError{Server: serverName}
		}
		return nil, fmt.Errorf("reserve MCP server exclusive lease namespace: %w", withoutPath(err))
	}
	releaseNamespace := true
	defer func() {
		if releaseNamespace {
			_ = namespace.close()
		}
	}()
	file, parent, err := openExclusiveLeaseFile(path)
	if err != nil {
		return nil, fmt.Errorf("open MCP server exclusive lease: %w", withoutPath(err))
	}
	if lockErr := tryAcquireExclusiveFileLock(file); lockErr != nil {
		_ = file.Close()
		parent.close()
		if errors.Is(lockErr, errExclusiveLeaseBusy) {
			return nil, &ExclusiveLeaseBusyError{Server: serverName}
		}
		return nil, fmt.Errorf("lock MCP server exclusive lease: %w", lockErr)
	}
	if err = parent.validateLeaf(file); err != nil {
		_ = releaseExclusiveFileLock(file)
		_ = file.Close()
		parent.close()
		return nil, fmt.Errorf("validate MCP server exclusive lease: %w", err)
	}
	if err = namespace.validate(); err != nil {
		_ = releaseExclusiveFileLock(file)
		_ = file.Close()
		parent.close()
		return nil, fmt.Errorf("validate MCP server exclusive lease namespace: %w", err)
	}
	releaseNamespace = false
	releaseReservation = false
	return &exclusiveServerLease{
		file: file, parent: parent, namespace: namespace, reservation: reservation,
	}, nil
}

func withoutPath(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

func (l *exclusiveServerLease) release() {
	_ = l.finish(false)
}

func (l *exclusiveServerLease) close() error {
	return l.finish(true)
}

func (l *exclusiveServerLease) validate() error {
	if l == nil {
		return errExclusiveLeaseUnsafe
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errExclusiveLeaseUnsafe
	}
	return errors.Join(l.namespace.validate(), l.parent.validateLeaf(l.file))
}

func (l *exclusiveServerLease) finish(requireStableNamespace bool) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	if l.file == nil || l.parent == nil {
		return fmt.Errorf("validate MCP server exclusive lease before release: %w", errExclusiveLeaseUnsafe)
	}
	if requireStableNamespace {
		if err := errors.Join(l.namespace.validate(), l.parent.validateLeaf(l.file)); err != nil {
			return fmt.Errorf("validate MCP server exclusive lease before release: %w", err)
		}
	}
	unlockErr := releaseExclusiveFileLock(l.file)
	fileErr := l.file.Close()
	l.parent.close()
	namespaceErr := l.namespace.close()
	releaseExclusiveLeasePath(l.reservation)
	l.closed = true
	return errors.Join(unlockErr, fileErr, namespaceErr)
}
