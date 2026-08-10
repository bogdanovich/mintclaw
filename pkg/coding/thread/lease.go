package thread

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// LeaseSchemaVersion is the current diagnostic owner-record schema.
	LeaseSchemaVersion = 1
	// MaxLeaseOwnerBytes bounds untrusted diagnostic data in thread.lock.
	MaxLeaseOwnerBytes = 4 * 1024

	leaseFileName       = "thread.lock"
	leaseHostnameMaxLen = 255
)

// ErrLeaseBusy classifies a coding thread already owned by another process.
var ErrLeaseBusy = errors.New("coding thread lease busy")

// LeaseOwner is best-effort diagnostic information stored in thread.lock.
// The OS file lock, rather than this record, is the source of exclusivity.
type LeaseOwner struct {
	SchemaVersion int       `json:"schema_version"`
	PID           int       `json:"pid"`
	Hostname      string    `json:"hostname,omitempty"`
	AcquiredAt    time.Time `json:"acquired_at"`
}

// LeaseBusyError reports a live owner when its bounded record was readable.
type LeaseBusyError struct {
	ThreadID string
	Owner    *LeaseOwner
}

func (e *LeaseBusyError) Error() string {
	if e == nil {
		return ErrLeaseBusy.Error()
	}
	prefix := fmt.Sprintf("coding thread %q lease is busy", e.ThreadID)
	if e.Owner == nil {
		return prefix
	}
	if e.Owner.Hostname != "" {
		return fmt.Sprintf("%s (owner pid %d on %s)", prefix, e.Owner.PID, e.Owner.Hostname)
	}
	return fmt.Sprintf("%s (owner pid %d)", prefix, e.Owner.PID)
}

func (e *LeaseBusyError) Unwrap() error {
	return ErrLeaseBusy
}

// Lease is an exclusive, process-scoped writer claim on one coding thread.
type Lease struct {
	threadID string
	owner    LeaseOwner
	file     *os.File
	once     sync.Once
	err      error
}

// ThreadID returns the leased coding thread ID.
func (l *Lease) ThreadID() string {
	if l == nil {
		return ""
	}
	return l.threadID
}

// Owner returns the diagnostic record written by this process.
func (l *Lease) Owner() LeaseOwner {
	if l == nil {
		return LeaseOwner{}
	}
	return l.owner
}

// Release unlocks and closes the lease. It is safe to call more than once.
func (l *Lease) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		l.err = errors.Join(releaseThreadLeaseFile(l.file), l.file.Close())
	})
	return l.err
}

// AcquireLease takes a non-blocking writer lease on an existing coding thread.
func (s *Store) AcquireLease(threadID string) (*Lease, error) {
	owner := LeaseOwner{
		SchemaVersion: LeaseSchemaVersion,
		PID:           os.Getpid(),
		AcquiredAt:    time.Now().UTC(),
	}
	if hostname, err := os.Hostname(); err == nil {
		owner.Hostname = strings.TrimSpace(hostname)
	}
	return s.acquireLease(threadID, owner)
}

func (s *Store) acquireLease(threadID string, owner LeaseOwner) (*Lease, error) {
	if s == nil {
		return nil, fmt.Errorf("coding thread store is nil")
	}
	if err := validateThreadID(threadID); err != nil {
		return nil, err
	}
	if err := owner.validate(); err != nil {
		return nil, err
	}
	file, err := s.openLeaseFile(threadID)
	if err != nil {
		return nil, fmt.Errorf("coding thread lease: open %q: %w", threadID, err)
	}
	if err := tryAcquireThreadLeaseFile(file); err != nil {
		if errors.Is(err, ErrLeaseBusy) {
			observed, _ := readLeaseOwner(file)
			_ = file.Close()
			return nil, &LeaseBusyError{ThreadID: threadID, Owner: observed}
		}
		_ = file.Close()
		return nil, fmt.Errorf("coding thread lease: lock %q: %w", threadID, err)
	}
	if err := writeLeaseOwner(file, owner); err != nil {
		_ = releaseThreadLeaseFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("coding thread lease: record owner for %q: %w", threadID, err)
	}
	return &Lease{threadID: threadID, owner: owner, file: file}, nil
}

func (s *Store) openLeaseFile(threadID string) (*os.File, error) {
	threads, err := openCatalogRoot(filepath.Join(s.root, "threads"))
	if err != nil {
		return nil, err
	}
	threadRoot, err := openCatalogChildDirectory(threads, threadID)
	if err != nil {
		return nil, errors.Join(err, threads.Close())
	}
	file, openErr := openThreadLeaseFile(threadRoot)
	closeErr := errors.Join(threadRoot.Close(), threads.Close())
	if openErr != nil || closeErr != nil {
		if file != nil {
			_ = file.Close()
		}
		return nil, errors.Join(openErr, closeErr)
	}
	return file, nil
}

func (o LeaseOwner) validate() error {
	if o.SchemaVersion != LeaseSchemaVersion {
		return fmt.Errorf("coding thread lease: unsupported owner schema %d", o.SchemaVersion)
	}
	if o.PID <= 0 {
		return fmt.Errorf("coding thread lease: owner PID must be positive")
	}
	if o.AcquiredAt.IsZero() {
		return fmt.Errorf("coding thread lease: owner acquisition time is required")
	}
	if o.AcquiredAt.Location() != time.UTC {
		return fmt.Errorf("coding thread lease: owner acquisition time must be UTC")
	}
	if o.Hostname != strings.TrimSpace(o.Hostname) || !utf8.ValidString(o.Hostname) ||
		len(o.Hostname) > leaseHostnameMaxLen {
		return fmt.Errorf("coding thread lease: owner hostname is invalid")
	}
	if strings.IndexFunc(o.Hostname, unicode.IsControl) >= 0 {
		return fmt.Errorf("coding thread lease: owner hostname contains control characters")
	}
	return nil
}

func writeLeaseOwner(file *os.File, owner LeaseOwner) error {
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	if len(data)+1 > MaxLeaseOwnerBytes {
		return fmt.Errorf("owner record exceeds %d bytes", MaxLeaseOwnerBytes)
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func readLeaseOwner(file *os.File) (*LeaseOwner, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxLeaseOwnerBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxLeaseOwnerBytes {
		return nil, fmt.Errorf("owner record exceeds %d bytes", MaxLeaseOwnerBytes)
	}
	var owner LeaseOwner
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&owner); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("owner record has trailing JSON content")
	}
	if err := owner.validate(); err != nil {
		return nil, err
	}
	return &owner, nil
}
