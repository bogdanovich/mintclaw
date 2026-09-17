package worktree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// OwnerRequest identifies the exact worker generation that will mutate an
// allocation. Every field except WorkerGenerationID must match its immutable
// allocation record.
type OwnerRequest struct {
	WorktreeID         string
	TaskID             string
	TaskGenerationID   string
	ThreadID           string
	WorkerGenerationID string
}

func (request OwnerRequest) validate() error {
	parsedThreadID, threadErr := uuid.Parse(request.ThreadID)
	if !validWorktreeID(request.WorktreeID) || !validIdentifier(request.TaskID) ||
		!validIdentifier(request.TaskGenerationID) || !validIdentifier(request.WorkerGenerationID) ||
		threadErr != nil || parsedThreadID.String() != request.ThreadID ||
		request.WorktreeID != IDForThread(request.ThreadID) {
		return fmt.Errorf("coding worktree: invalid owner identity")
	}
	return nil
}

// OwnerRecord is bounded diagnostic evidence written while the authoritative
// process-scoped lock is held.
type OwnerRecord struct {
	SchemaVersion         int       `json:"schema_version"`
	WorktreeID            string    `json:"worktree_id"`
	TaskID                string    `json:"task_id"`
	TaskGenerationID      string    `json:"task_generation_id"`
	ThreadID              string    `json:"thread_id"`
	WorkerGenerationID    string    `json:"worker_generation_id"`
	ExecutionRootIdentity string    `json:"execution_root_identity"`
	PID                   int       `json:"pid"`
	Hostname              string    `json:"hostname,omitempty"`
	AcquiredAt            time.Time `json:"acquired_at"`
}

func (record OwnerRecord) validate() error {
	request := OwnerRequest{
		WorktreeID: record.WorktreeID, TaskID: record.TaskID,
		TaskGenerationID: record.TaskGenerationID, ThreadID: record.ThreadID,
		WorkerGenerationID: record.WorkerGenerationID,
	}
	if record.SchemaVersion != SchemaVersion || request.validate() != nil ||
		len(record.ExecutionRootIdentity) != 64 || !validObjectID(record.ExecutionRootIdentity) ||
		record.PID <= 0 || record.AcquiredAt.IsZero() || record.Hostname != strings.TrimSpace(record.Hostname) ||
		!utf8.ValidString(record.Hostname) || len(record.Hostname) > 255 {
		return fmt.Errorf("coding worktree: invalid owner record")
	}
	return nil
}

// OwnerInspection is a moment-in-time diagnostic view. Busy proves only that
// some process holds the OS lock; callers compare Record to the expected
// binding before trusting the diagnostic identity.
type OwnerInspection struct {
	Busy   bool
	Record *OwnerRecord
}

// Owner is the exclusive mutation authority for one ready allocation.
type Owner struct {
	manager    *Manager
	allocation Allocation
	record     OwnerRecord
	lock       *lockedFile
	operation  sync.Mutex
	mu         sync.Mutex
	once       sync.Once
	released   bool
	err        error
}

// OwnerLifecycle keeps the owner operation gate held across an external
// worker lifecycle. Finish must be called exactly as the worker stops; it
// captures the terminal repository evidence before releasing ownership.
type OwnerLifecycle struct {
	owner      *Owner
	allocation Allocation

	mu        sync.Mutex
	completed bool
	handoff   Handoff
	err       error
}

func (owner *Owner) Allocation() Allocation {
	if owner == nil {
		return Allocation{}
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return cloneAllocation(owner.allocation)
}

func (owner *Owner) Record() OwnerRecord {
	if owner == nil {
		return OwnerRecord{}
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.record
}

func (owner *Owner) Validate(request OwnerRequest) error {
	if owner == nil {
		return fmt.Errorf("coding worktree: owner lease is required")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.released || owner.lock == nil {
		return fmt.Errorf("coding worktree: owner lease was released")
	}
	if err := request.validate(); err != nil {
		return err
	}
	if !owner.record.matches(request) {
		return fmt.Errorf("coding worktree: owner identity mismatch")
	}
	return nil
}

// Revalidate proves that the caller still holds the exact owner lease and
// that its allocation remains ready. It is the parent-side launch gate.
func (owner *Owner) Revalidate(ctx context.Context, request OwnerRequest) (Allocation, error) {
	if owner == nil {
		return Allocation{}, ErrOwnerInactive
	}
	owner.operation.Lock()
	defer owner.operation.Unlock()
	return owner.revalidateLocked(ctx, request)
}

func (owner *Owner) revalidateLocked(ctx context.Context, request OwnerRequest) (Allocation, error) {
	if err := owner.Validate(request); err != nil {
		return Allocation{}, err
	}
	allocation, err := owner.manager.RequireActiveOwner(ctx, request)
	if err != nil {
		return Allocation{}, err
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.released || owner.lock == nil || !owner.record.matches(request) {
		return Allocation{}, ErrOwnerInactive
	}
	owner.allocation = allocation
	return allocation, nil
}

// BeginLifecycle proves the exact owner binding and reserves the operation
// gate until OwnerLifecycle.Finish captures handoff evidence and releases the
// process-scoped lock. Concurrent Release and handoff attempts wait behind
// this lifecycle instead of racing a live worker.
func (owner *Owner) BeginLifecycle(ctx context.Context, request OwnerRequest) (*OwnerLifecycle, error) {
	if owner == nil {
		return nil, ErrOwnerInactive
	}
	owner.operation.Lock()
	allocation, err := owner.revalidateLocked(ctx, request)
	if err != nil {
		owner.operation.Unlock()
		return nil, err
	}
	return &OwnerLifecycle{owner: owner, allocation: allocation}, nil
}

// Allocation returns the immutable allocation admitted for this lifecycle.
func (lifecycle *OwnerLifecycle) Allocation() Allocation {
	if lifecycle == nil {
		return Allocation{}
	}
	return cloneAllocation(lifecycle.allocation)
}

// Finish is idempotent after terminal evidence is captured or the allocation
// is durably quarantined. If neither persistence path succeeds, it retains the
// owner operation gate and may be retried with a fresh context.
func (lifecycle *OwnerLifecycle) Finish(ctx context.Context) (Handoff, error) {
	if lifecycle == nil || lifecycle.owner == nil {
		return Handoff{}, ErrOwnerInactive
	}
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.completed {
		return lifecycle.handoff, lifecycle.err
	}
	handoff, captureErr := lifecycle.owner.captureHandoffLocked(ctx)
	if captureErr != nil {
		quarantineErr := lifecycle.owner.manager.markHandoffFailure(
			ctx,
			lifecycle.owner,
			lifecycle.allocation,
			"terminal handoff persistence failed",
		)
		if quarantineErr != nil {
			lifecycle.err = errors.Join(ErrFinalizationPending, captureErr, quarantineErr)
			return Handoff{}, lifecycle.err
		}
	} else {
		lifecycle.handoff = handoff
	}
	lifecycle.err = errors.Join(captureErr, lifecycle.owner.releaseLocked())
	lifecycle.completed = true
	lifecycle.owner.operation.Unlock()
	return lifecycle.handoff, lifecycle.err
}

// CaptureHandoff persists a passive terminal snapshot before the owner lease
// is released. It never mutates the repository.
func (owner *Owner) CaptureHandoff(ctx context.Context) (Handoff, error) {
	if owner == nil {
		return Handoff{}, ErrOwnerInactive
	}
	owner.operation.Lock()
	defer owner.operation.Unlock()
	return owner.captureHandoffLocked(ctx)
}

func (owner *Owner) captureHandoffLocked(ctx context.Context) (Handoff, error) {
	owner.mu.Lock()
	if owner.released || owner.lock == nil {
		owner.mu.Unlock()
		return Handoff{}, ErrOwnerInactive
	}
	allocation := owner.allocation
	owner.mu.Unlock()
	if allocation.State == StateCleanupPending || allocation.State == StateReleased {
		return Handoff{}, ErrOwnerInactive
	}
	return owner.manager.captureHandoff(ctx, owner, allocation)
}

func (owner *Owner) validateHeldAllocation(allocation Allocation) error {
	if owner == nil {
		return ErrOwnerInactive
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.released || owner.lock == nil || !sameAllocationIdentity(owner.allocation, allocation) {
		return ErrOwnerInactive
	}
	return nil
}

func (owner *Owner) updateAllocation(allocation Allocation) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	owner.allocation = allocation
}

func (owner *Owner) Release() error {
	if owner == nil {
		return nil
	}
	owner.operation.Lock()
	defer owner.operation.Unlock()
	return owner.releaseLocked()
}

func (owner *Owner) releaseLocked() error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	owner.once.Do(func() {
		owner.released = true
		owner.err = owner.lock.Close()
	})
	return owner.err
}

func (record OwnerRecord) matches(request OwnerRequest) bool {
	return record.WorktreeID == request.WorktreeID && record.TaskID == request.TaskID &&
		record.TaskGenerationID == request.TaskGenerationID && record.ThreadID == request.ThreadID &&
		record.WorkerGenerationID == request.WorkerGenerationID
}

// AcquireOwner takes the non-blocking writer claim after revalidating a ready
// linked worktree. It can also claim cleanup_pending state so an interrupted
// normal removal can be reconciled without starting another worker.
func (manager *Manager) AcquireOwner(ctx context.Context, request OwnerRequest) (*Owner, error) {
	if manager == nil {
		return nil, fmt.Errorf("coding worktree: manager is unavailable")
	}
	if ctx == nil {
		return nil, fmt.Errorf("coding worktree: context is required")
	}
	if err := request.validate(); err != nil {
		return nil, err
	}
	var owner *Owner
	err := manager.withCatalog(ctx, func() error {
		if err := manager.validateRoots(); err != nil {
			return err
		}
		allocation, found, err := manager.loadRecord(request.WorktreeID)
		if err != nil {
			return err
		}
		if !found {
			return os.ErrNotExist
		}
		if allocation.TaskID != request.TaskID || allocation.TaskGenerationID != request.TaskGenerationID ||
			allocation.ThreadID != request.ThreadID || allocation.WorktreeParent != manager.worktreeParent {
			return ErrAllocationConflict
		}
		if allocation.State != StateCleanupPending && allocation.State != StateReleased {
			allocation, err = manager.reconcile(ctx, allocation)
			if err != nil || allocation.State != StateReady {
				return errors.Join(ErrAllocationUncertain, err)
			}
		}
		lock, err := acquireLockedFile(ctx, manager.ownerPath(request.WorktreeID), false)
		if errors.Is(err, errFileLockBusy) {
			return ErrOwnerBusy
		}
		if err != nil {
			return fmt.Errorf("coding worktree: acquire owner lock: %w", err)
		}
		record := OwnerRecord{
			SchemaVersion: SchemaVersion, WorktreeID: request.WorktreeID,
			TaskID: request.TaskID, TaskGenerationID: request.TaskGenerationID,
			ThreadID: request.ThreadID, WorkerGenerationID: request.WorkerGenerationID,
			ExecutionRootIdentity: allocation.ExecutionRootIdentity,
			PID:                   os.Getpid(), AcquiredAt: manager.now().UTC(),
		}
		if hostname, hostnameErr := os.Hostname(); hostnameErr == nil {
			record.Hostname = hostname
		}
		if err := writeOwnerRecord(lock.file, record); err != nil {
			return errors.Join(err, lock.Close())
		}
		owner = &Owner{manager: manager, allocation: allocation, record: record, lock: lock}
		return nil
	})
	if err != nil {
		if owner != nil {
			_ = owner.Release()
		}
		return nil, err
	}
	return owner, nil
}

// InspectOwner probes the authoritative lock without acquiring mutation
// authority. An available observation can become busy immediately afterward.
func (manager *Manager) InspectOwner(ctx context.Context, worktreeID string) (OwnerInspection, error) {
	if manager == nil {
		return OwnerInspection{}, fmt.Errorf("coding worktree: manager is unavailable")
	}
	if ctx == nil {
		return OwnerInspection{}, fmt.Errorf("coding worktree: context is required")
	}
	if !validWorktreeID(worktreeID) {
		return OwnerInspection{}, fmt.Errorf("coding worktree: invalid worktree ID")
	}
	if _, err := manager.Load(ctx, worktreeID); err != nil {
		return OwnerInspection{}, err
	}
	lock, err := acquireExistingLockedFile(ctx, manager.ownerPath(worktreeID), false)
	if errors.Is(err, os.ErrNotExist) {
		return OwnerInspection{}, nil
	}
	if err == nil {
		return OwnerInspection{}, lock.Close()
	}
	if !errors.Is(err, errFileLockBusy) {
		return OwnerInspection{}, err
	}
	record, err := readOwnerRecord(manager.ownerPath(worktreeID))
	if err != nil {
		return OwnerInspection{Busy: true}, fmt.Errorf("coding worktree: read busy owner record: %w", err)
	}
	return OwnerInspection{Busy: true, Record: &record}, nil
}

// RequireActiveOwner verifies the durable allocation and the process-scoped
// lock record without acquiring mutation authority. A trusted launcher keeps
// the corresponding Owner object live for the worker's complete lifecycle.
func (manager *Manager) RequireActiveOwner(
	ctx context.Context,
	request OwnerRequest,
) (Allocation, error) {
	if manager == nil || ctx == nil {
		return Allocation{}, ErrOwnerInactive
	}
	if err := request.validate(); err != nil {
		return Allocation{}, err
	}
	var allocation Allocation
	err := manager.withCatalog(ctx, func() error {
		if err := manager.validateRoots(); err != nil {
			return err
		}
		current, found, err := manager.loadRecord(request.WorktreeID)
		if err != nil {
			return err
		}
		if !found {
			return os.ErrNotExist
		}
		if current.TaskID != request.TaskID || current.TaskGenerationID != request.TaskGenerationID ||
			current.ThreadID != request.ThreadID || current.WorktreeParent != manager.worktreeParent {
			return ErrAllocationConflict
		}
		if current.State == StateCleanupPending || current.State == StateReleased {
			return ErrOwnerInactive
		}
		current, err = manager.reconcile(ctx, current)
		if err != nil || current.State != StateReady || current.Execution == nil {
			return errors.Join(ErrAllocationUncertain, err)
		}
		lock, lockErr := acquireExistingLockedFile(ctx, manager.ownerPath(request.WorktreeID), false)
		if lockErr == nil {
			return errors.Join(ErrOwnerInactive, lock.Close())
		}
		if errors.Is(lockErr, os.ErrNotExist) {
			return ErrOwnerInactive
		}
		if !errors.Is(lockErr, errFileLockBusy) {
			return lockErr
		}
		record, recordErr := readOwnerRecord(manager.ownerPath(request.WorktreeID))
		if recordErr != nil || !record.matches(request) ||
			record.ExecutionRootIdentity != current.ExecutionRootIdentity {
			return errors.Join(ErrOwnerInactive, recordErr)
		}
		allocation = current
		return nil
	})
	if err != nil {
		return Allocation{}, err
	}
	return allocation, nil
}

func writeOwnerRecord(file *os.File, record OwnerRecord) error {
	if err := record.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > MaxOwnerRecordBytes {
		return fmt.Errorf("coding worktree: owner record exceeds %d bytes", MaxOwnerRecordBytes)
	}
	if truncateErr := file.Truncate(0); truncateErr != nil {
		return truncateErr
	}
	if _, seekErr := file.Seek(0, io.SeekStart); seekErr != nil {
		return seekErr
	}
	written, err := file.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return err
	}
	return file.Sync()
}

func readOwnerRecord(path string) (OwnerRecord, error) {
	data, err := readBoundedDirectFile(path, "owner record", MaxOwnerRecordBytes)
	if err != nil {
		return OwnerRecord{}, err
	}
	var record OwnerRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return OwnerRecord{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return OwnerRecord{}, fmt.Errorf("coding worktree: owner record has trailing JSON content")
	}
	if err := record.validate(); err != nil {
		return OwnerRecord{}, err
	}
	return record, nil
}
