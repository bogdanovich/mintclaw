package nodes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	gatewayInvocationStoreVersion      = 1
	DefaultGatewayInvocationLimit      = 8192
	DefaultGatewayInvocationStoreBytes = 32 * 1024 * 1024
	DefaultGatewayInvocationRetention  = 7 * 24 * time.Hour
	maxGatewayToolCallIDLength         = 512
	maxGatewayTargetLength             = 64
)

const DefaultGatewayInvocationSQLiteBytes int64 = 4 * 1024 * 1024 * 1024

var (
	ErrGatewayInvocationConflict      = errors.New("gateway node invocation conflicts with durable state")
	ErrGatewayInvocationDispatched    = errors.New("gateway node invocation was already dispatched")
	ErrGatewayInvocationNotDispatched = errors.New("gateway node invocation was not dispatched")
	ErrGatewayInvocationNotFound      = errors.New("gateway node invocation not found")
	ErrGatewayInvocationStoreFull     = errors.New("gateway node invocation store is full")
	gatewayTargetPattern              = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

type GatewayInvocationState string

const (
	GatewayInvocationPrepared   GatewayInvocationState = "prepared"
	GatewayInvocationDispatched GatewayInvocationState = "dispatched"
)

// GatewayInvocationRecord is the gateway-owned authority that links one model
// tool call to one immutable execution plan. ExpectedPlanHash is stored
// separately so a mutated plan cannot validate itself.
type GatewayInvocationRecord struct {
	Target           string                         `json:"target"`
	ToolCallID       string                         `json:"tool_call_id"`
	Plan             ExecutionPlan                  `json:"plan"`
	Descriptor       CommandDescriptor              `json:"descriptor"`
	ExpectedPlanHash string                         `json:"expected_plan_hash"`
	State            GatewayInvocationState         `json:"state"`
	CreatedAt        int64                          `json:"created_at"`
	UpdatedAt        int64                          `json:"updated_at"`
	DispatchedAt     int64                          `json:"dispatched_at,omitempty"`
	WorkspaceID      string                         `json:"workspace_id,omitempty"`
	ExecutionID      string                         `json:"execution_id,omitempty"`
	Cancellation     *GatewayInvocationCancellation `json:"cancellation,omitempty"`
}

type GatewayInvocationCancellation struct {
	RequestedAt int64 `json:"requested_at"`
}

type GatewayInvocationOwner struct {
	Target      string
	AgentID     string
	SessionID   string
	ActorID     string
	ToolCallID  string
	WorkspaceID string
	ExecutionID string
}

type GatewayInvocationPrincipal struct {
	AgentID     string
	SessionID   string
	ActorID     string
	WorkspaceID string
	ExecutionID string
}

type gatewayInvocationDocument struct {
	Version int                                `json:"version"`
	Records map[string]GatewayInvocationRecord `json:"records"`
}

// GatewayInvocationStore persists prepared plan ownership across gateway
// restarts. Runtime gateways use the transactional SQLite backend; the legacy
// JSON backend remains available only for migration and focused compatibility
// tests.
type GatewayInvocationStore struct {
	sqlite *gatewayInvocationSQLiteStore

	path       string
	maxRecords int
	maxBytes   int
	retention  time.Duration
	now        func() time.Time
	writeFile  func(string, []byte, os.FileMode) error
	readFile   func(string, int) (gatewayInvocationDocument, *os.File, error)

	mu      sync.Mutex
	records map[string]GatewayInvocationRecord
	loaded  bool
	// file pins the exact validated file identity so an atomic replacement
	// cannot recycle it into a false unchanged-file cache hit.
	file    *os.File
	missing bool
	closed  bool
}

func GatewayInvocationStorePath(workspace string) string {
	return filepath.Join(workspace, "state", "node_invocations.db")
}

func GatewayInvocationLegacyStorePath(workspace string) string {
	return filepath.Join(workspace, "state", "node_invocations.json")
}

func NewGatewayInvocationSQLiteStore(
	path string,
	maxBytes int64,
) (*GatewayInvocationStore, error) {
	backend, err := newGatewayInvocationSQLiteStore(path, maxBytes, time.Now)
	if err != nil {
		return nil, err
	}
	return &GatewayInvocationStore{sqlite: backend}, nil
}

func NewGatewayInvocationStore(
	path string,
	maxRecords int,
	maxBytes int,
) (*GatewayInvocationStore, error) {
	path = filepath.Clean(path)
	if path == "." || path == string(filepath.Separator) {
		return nil, errors.New("gateway node invocation store path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create gateway node invocation store directory: %w", err)
	}
	store := newGatewayInvocationStore(path, maxRecords, maxBytes, time.Now)
	store.mu.Lock()
	release, err := store.lockAndReloadLocked()
	if err != nil {
		store.mu.Unlock()
		return nil, err
	}
	release()
	store.mu.Unlock()
	return store, nil
}

func newGatewayInvocationStore(
	path string,
	maxRecords int,
	maxBytes int,
	now func() time.Time,
) *GatewayInvocationStore {
	if maxRecords <= 0 {
		maxRecords = DefaultGatewayInvocationLimit
	}
	if maxBytes <= 0 {
		maxBytes = DefaultGatewayInvocationStoreBytes
	}
	if now == nil {
		now = time.Now
	}
	return &GatewayInvocationStore{
		path:       path,
		maxRecords: maxRecords,
		maxBytes:   maxBytes,
		retention:  DefaultGatewayInvocationRetention,
		now:        now,
		writeFile:  fileutil.WriteFileAtomic,
		readFile:   readGatewayInvocationDocument,
		records:    make(map[string]GatewayInvocationRecord),
	}
}

func (store *GatewayInvocationStore) Prepare(
	target string,
	toolCallID string,
	plan ExecutionPlan,
	descriptor CommandDescriptor,
) (GatewayInvocationRecord, bool, error) {
	return store.PrepareOwned(
		GatewayInvocationPrincipal{
			AgentID: plan.AgentID, SessionID: plan.SessionID, ActorID: plan.ActorID,
		},
		target,
		toolCallID,
		plan,
		descriptor,
	)
}

func (store *GatewayInvocationStore) PrepareOwned(
	principal GatewayInvocationPrincipal,
	target string,
	toolCallID string,
	plan ExecutionPlan,
	descriptor CommandDescriptor,
) (GatewayInvocationRecord, bool, error) {
	if store != nil && store.sqlite != nil {
		return store.sqlite.prepareOwned(principal, target, toolCallID, plan, descriptor)
	}
	principal.AgentID = strings.TrimSpace(principal.AgentID)
	principal.SessionID = strings.TrimSpace(principal.SessionID)
	principal.ActorID = strings.TrimSpace(principal.ActorID)
	principal.WorkspaceID = strings.TrimSpace(principal.WorkspaceID)
	principal.ExecutionID = strings.TrimSpace(principal.ExecutionID)
	if principal.AgentID != plan.AgentID ||
		principal.SessionID != plan.SessionID ||
		principal.ActorID != plan.ActorID ||
		(principal.WorkspaceID == "") != (principal.ExecutionID == "") {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationConflict
	}
	now := store.now()
	record := GatewayInvocationRecord{
		Target:           strings.TrimSpace(target),
		ToolCallID:       strings.TrimSpace(toolCallID),
		Plan:             cloneExecutionPlan(plan),
		Descriptor:       cloneCommandDescriptor(descriptor),
		ExpectedPlanHash: plan.PlanHash,
		State:            GatewayInvocationPrepared,
		CreatedAt:        now.UnixNano(),
		UpdatedAt:        now.UnixNano(),
		WorkspaceID:      strings.TrimSpace(principal.WorkspaceID),
		ExecutionID:      strings.TrimSpace(principal.ExecutionID),
	}
	if err := record.validate(); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	if now.Unix() >= plan.ExpiresAt {
		return GatewayInvocationRecord{}, false, fmt.Errorf(
			"%w: execution plan expired before persistence",
			ErrInvalidInvocation,
		)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	release, err := store.lockAndReloadLocked()
	if err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	defer release()
	now = store.now()
	if now.Unix() >= plan.ExpiresAt {
		return GatewayInvocationRecord{}, false, fmt.Errorf(
			"%w: execution plan expired before persistence",
			ErrInvalidInvocation,
		)
	}
	record.CreatedAt = now.UnixNano()
	record.UpdatedAt = record.CreatedAt
	previous := cloneGatewayInvocationRecords(store.records)
	store.pruneLocked(now)
	pruned := len(previous) != len(store.records)
	for _, existing := range store.records {
		if existing.Plan.IdempotencyKey == plan.IdempotencyKey &&
			!sameGatewayInvocationBinding(existing, record) {
			store.records = previous
			return GatewayInvocationRecord{}, false, ErrGatewayInvocationConflict
		}
		if sameGatewayToolCall(
			existing,
			plan.AgentID,
			plan.SessionID,
			plan.ActorID,
			record.ToolCallID,
		) && gatewayInvocationScopeMatches(existing, principal) {
			if sameGatewayInvocationBinding(existing, record) {
				if pruned {
					if err := store.persistMutationLocked(previous); err != nil {
						return GatewayInvocationRecord{}, false, fmt.Errorf(
							"persist pruned node invocations: %w",
							err,
						)
					}
				}
				return cloneGatewayInvocationRecord(existing), false, nil
			}
			store.records = previous
			return GatewayInvocationRecord{}, false, ErrGatewayInvocationConflict
		}
	}
	if existing, found := store.records[plan.InvocationID]; found {
		if sameGatewayInvocationBinding(existing, record) {
			if pruned {
				if err := store.persistMutationLocked(previous); err != nil {
					return GatewayInvocationRecord{}, false, fmt.Errorf(
						"persist pruned node invocations: %w",
						err,
					)
				}
			}
			return cloneGatewayInvocationRecord(existing), false, nil
		}
		store.records = previous
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationConflict
	}
	if len(store.records) >= store.maxRecords {
		store.records = previous
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationStoreFull
	}
	store.records[plan.InvocationID] = record
	if err := store.persistMutationLocked(previous); err != nil {
		return GatewayInvocationRecord{}, false, fmt.Errorf(
			"persist prepared node invocation: %w",
			err,
		)
	}
	return cloneGatewayInvocationRecord(record), true, nil
}

func (store *GatewayInvocationStore) ByToolCall(
	principal GatewayInvocationPrincipal,
	toolCallID string,
) (GatewayInvocationRecord, bool, error) {
	if store != nil && store.sqlite != nil {
		return store.sqlite.byToolCall(principal, toolCallID)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	release, err := store.lockAndReloadLocked()
	if err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	defer release()
	if err := store.pruneAndPersistLocked(store.now()); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	for _, record := range store.records {
		if sameGatewayToolCall(
			record,
			principal.AgentID,
			principal.SessionID,
			principal.ActorID,
			toolCallID,
		) && gatewayInvocationScopeMatches(record, principal) {
			return cloneGatewayInvocationRecord(record), true, nil
		}
	}
	return GatewayInvocationRecord{}, false, nil
}

func (store *GatewayInvocationStore) Lookup(
	principal GatewayInvocationPrincipal,
	invocationID string,
) (GatewayInvocationRecord, bool, error) {
	if store != nil && store.sqlite != nil {
		return store.sqlite.lookup(principal, invocationID)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	release, err := store.lockAndReloadLocked()
	if err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	defer release()
	if err := store.pruneAndPersistLocked(store.now()); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	record, found := store.records[invocationID]
	if !found ||
		record.Plan.AgentID != principal.AgentID ||
		record.Plan.SessionID != principal.SessionID ||
		record.Plan.ActorID != principal.ActorID ||
		!gatewayInvocationWorkspaceMatches(record, principal) {
		return GatewayInvocationRecord{}, false, nil
	}
	return cloneGatewayInvocationRecord(record), true, nil
}

func (store *GatewayInvocationStore) RequestCancellation(
	principal GatewayInvocationPrincipal,
	invocationID string,
) (GatewayInvocationRecord, bool, error) {
	if store != nil && store.sqlite != nil {
		return store.sqlite.requestCancellation(principal, invocationID)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	release, err := store.lockAndReloadLocked()
	if err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	defer release()
	if err := store.pruneAndPersistLocked(store.now()); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	record, found := store.records[invocationID]
	if !found {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationNotFound
	}
	if record.Plan.AgentID != principal.AgentID ||
		record.Plan.SessionID != principal.SessionID ||
		record.Plan.ActorID != principal.ActorID ||
		!gatewayInvocationScopeMatches(record, principal) {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationConflict
	}
	if record.State != GatewayInvocationDispatched {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationNotDispatched
	}
	if record.Cancellation != nil {
		return cloneGatewayInvocationRecord(record), false, nil
	}
	previous := cloneGatewayInvocationRecords(store.records)
	now := store.now().UnixNano()
	if now <= record.UpdatedAt {
		if record.UpdatedAt == math.MaxInt64 {
			return GatewayInvocationRecord{}, false, fmt.Errorf(
				"%w: invocation timestamp exhausted",
				ErrInvalidInvocation,
			)
		}
		now = record.UpdatedAt + 1
	}
	record.Cancellation = &GatewayInvocationCancellation{RequestedAt: now}
	record.UpdatedAt = now
	store.records[invocationID] = record
	if err := store.persistMutationLocked(previous); err != nil {
		return cloneGatewayInvocationRecord(record),
			fileutil.IsCommittedWriteError(err),
			fmt.Errorf("persist node invocation cancellation: %w", err)
	}
	return cloneGatewayInvocationRecord(record), true, nil
}

func (store *GatewayInvocationStore) MarkDispatched(
	owner GatewayInvocationOwner,
	invocationID string,
	expectedPlanHash string,
) (GatewayInvocationRecord, bool, error) {
	if store != nil && store.sqlite != nil {
		return store.sqlite.markDispatched(owner, invocationID, expectedPlanHash)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := owner.validate(); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	release, err := store.lockAndReloadLocked()
	if err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	defer release()
	if err := store.pruneAndPersistLocked(store.now()); err != nil {
		return GatewayInvocationRecord{}, false, err
	}
	record, found := store.records[invocationID]
	if !found {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationNotFound
	}
	if !owner.matches(record) {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationConflict
	}
	if err := record.Plan.ValidateAgainstHash(expectedPlanHash); err != nil ||
		record.ExpectedPlanHash != expectedPlanHash {
		return GatewayInvocationRecord{}, false, ErrGatewayInvocationConflict
	}
	if record.State == GatewayInvocationDispatched {
		return cloneGatewayInvocationRecord(record), false, nil
	}
	previous := cloneGatewayInvocationRecords(store.records)
	now := store.now().UnixNano()
	if now <= record.UpdatedAt {
		if record.UpdatedAt == math.MaxInt64 {
			return GatewayInvocationRecord{}, false, fmt.Errorf(
				"%w: invocation timestamp exhausted",
				ErrInvalidInvocation,
			)
		}
		now = record.UpdatedAt + 1
	}
	record.State = GatewayInvocationDispatched
	record.DispatchedAt = now
	record.UpdatedAt = now
	store.records[invocationID] = record
	if err := store.persistMutationLocked(previous); err != nil {
		return cloneGatewayInvocationRecord(record),
			fileutil.IsCommittedWriteError(err),
			fmt.Errorf("persist dispatched node invocation: %w", err)
	}
	return cloneGatewayInvocationRecord(record), true, nil
}

func (store *GatewayInvocationStore) lockAndReloadLocked() (func(), error) {
	if store.closed {
		return nil, os.ErrClosed
	}
	if store.path == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return nil, fmt.Errorf("create gateway node invocation store directory: %w", err)
	}
	release, err := acquireRegistryFileLock(store.path + ".lock")
	if err != nil {
		return nil, err
	}
	if err := store.loadLocked(); err != nil {
		release()
		return nil, fmt.Errorf("reload gateway node invocation store under lock: %w", err)
	}
	return release, nil
}

func (store *GatewayInvocationStore) loadLocked() error {
	info, err := os.Stat(store.path)
	if errors.Is(err, os.ErrNotExist) {
		if store.loaded && store.missing {
			return nil
		}
		store.records = make(map[string]GatewayInvocationRecord)
		store.loaded = true
		store.replaceCachedFileLocked(nil)
		store.missing = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat gateway node invocation store: %w", err)
	}
	if store.loaded && !store.missing && sameGatewayInvocationPathFile(store.file, info) {
		return nil
	}
	if info.Size() > int64(store.maxBytes) {
		return ErrGatewayInvocationStoreFull
	}
	document, validatedFile, err := store.readFile(store.path, store.maxBytes)
	if err != nil {
		return err
	}
	if validatedFile == nil {
		return errors.New("gateway node invocation store reader did not retain its file")
	}
	keepValidatedFile := false
	defer func() {
		if !keepValidatedFile {
			_ = validatedFile.Close()
		}
	}()
	if document.Version != gatewayInvocationStoreVersion ||
		document.Records == nil ||
		len(document.Records) > store.maxRecords {
		return errors.New("gateway node invocation store has invalid metadata")
	}
	toolCalls := make(map[string]string, len(document.Records))
	idempotency := make(map[string]string, len(document.Records))
	validatedDescriptors := make(map[string][]CommandDescriptor)
	for invocationID, record := range document.Records {
		if invocationID != record.Plan.InvocationID {
			return errors.New("gateway node invocation store has mismatched record identity")
		}
		if err := record.validateWithDescriptorCache(validatedDescriptors); err != nil {
			return fmt.Errorf("validate gateway node invocation %q: %w", invocationID, err)
		}
		toolCallKey := gatewayToolCallKey(
			record.Plan.AgentID,
			record.Plan.SessionID,
			record.Plan.ActorID,
			record.ToolCallID,
		)
		if existing := toolCalls[toolCallKey]; existing != "" {
			return fmt.Errorf(
				"gateway node invocations %q and %q share tool-call ownership",
				existing,
				invocationID,
			)
		}
		toolCalls[toolCallKey] = invocationID
		if existing := idempotency[record.Plan.IdempotencyKey]; existing != "" {
			return fmt.Errorf(
				"gateway node invocations %q and %q share idempotency authority",
				existing,
				invocationID,
			)
		}
		idempotency[record.Plan.IdempotencyKey] = invocationID
	}
	store.records = cloneGatewayInvocationRecords(document.Records)
	store.loaded = true
	store.replaceCachedFileLocked(validatedFile)
	keepValidatedFile = true
	store.missing = false
	if err := store.pruneAndPersistLocked(store.now()); err != nil {
		return fmt.Errorf("prune gateway node invocation store: %w", err)
	}
	return nil
}

func sameGatewayInvocationFileInfo(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func sameGatewayInvocationPathFile(file *os.File, pathInfo os.FileInfo) bool {
	if file == nil {
		return false
	}
	fileInfo, err := file.Stat()
	return err == nil && sameGatewayInvocationFileInfo(fileInfo, pathInfo)
}

func (store *GatewayInvocationStore) replaceCachedFileLocked(file *os.File) {
	previous := store.file
	store.file = file
	if previous != nil && previous != file {
		_ = previous.Close()
	}
}

func (store *GatewayInvocationStore) invalidateCachedFileLocked() {
	store.loaded = false
	store.replaceCachedFileLocked(nil)
}

// Close releases the retained file identity. It is safe to call repeatedly;
// no store operation is admitted after the first close.
func (store *GatewayInvocationStore) Close() error {
	if store == nil {
		return nil
	}
	if store.sqlite != nil {
		return store.sqlite.close()
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	store.loaded = false
	file := store.file
	store.file = nil
	if file == nil {
		return nil
	}
	return file.Close()
}

func readGatewayInvocationDocument(
	path string,
	maxBytes int,
) (document gatewayInvocationDocument, retained *os.File, returnErr error) {
	file, err := openGatewayInvocationFile(path)
	if err != nil {
		return gatewayInvocationDocument{}, nil, fmt.Errorf(
			"open gateway node invocation store: %w",
			err,
		)
	}
	defer func() {
		if returnErr != nil {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return gatewayInvocationDocument{}, nil, fmt.Errorf(
			"stat open gateway node invocation store: %w",
			err,
		)
	}
	if info.Size() > int64(maxBytes) {
		return gatewayInvocationDocument{}, nil, ErrGatewayInvocationStoreFull
	}
	decoder := json.NewDecoder(io.LimitReader(file, int64(maxBytes)+1))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&document); decodeErr != nil {
		return gatewayInvocationDocument{}, nil, fmt.Errorf(
			"decode gateway node invocation store: %w",
			decodeErr,
		)
	}
	if trailingErr := decoder.Decode(new(any)); !errors.Is(trailingErr, io.EOF) {
		return gatewayInvocationDocument{}, nil, errors.New(
			"decode gateway node invocation store: trailing data",
		)
	}
	after, err := file.Stat()
	if err != nil {
		return gatewayInvocationDocument{}, nil, fmt.Errorf(
			"restat open gateway node invocation store: %w",
			err,
		)
	}
	if !sameGatewayInvocationFileInfo(info, after) {
		return gatewayInvocationDocument{}, nil, errors.New(
			"gateway node invocation store changed while reading",
		)
	}
	return document, file, nil
}

func (store *GatewayInvocationStore) saveLocked() error {
	document := gatewayInvocationDocument{
		Version: gatewayInvocationStoreVersion,
		Records: store.records,
	}
	data, err := json.Marshal(document)
	if err != nil {
		return err
	}
	if len(data)+1 > store.maxBytes {
		return ErrGatewayInvocationStoreFull
	}
	if store.path == "" {
		return nil
	}
	data = append(data, '\n')
	if writeErr := store.writeFile(store.path, data, 0o600); writeErr != nil {
		store.invalidateCachedFileLocked()
		return writeErr
	}
	file, err := identifyGatewayInvocationFile(store.path, data)
	if err != nil {
		store.invalidateCachedFileLocked()
		return err
	}
	store.loaded = true
	store.replaceCachedFileLocked(file)
	store.missing = false
	return nil
}

func identifyGatewayInvocationFile(path string, expected []byte) (retained *os.File, returnErr error) {
	file, err := openGatewayInvocationFile(path)
	if err != nil {
		return nil, fmt.Errorf("open persisted gateway node invocation store: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat persisted gateway node invocation store: %w", err)
	}
	if info.Size() != int64(len(expected)) {
		return nil, errors.New("persisted gateway node invocation store changed after write")
	}
	actual, err := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))
	if err != nil {
		return nil, fmt.Errorf("read persisted gateway node invocation store: %w", err)
	}
	if !bytes.Equal(actual, expected) {
		return nil, errors.New("persisted gateway node invocation store changed after write")
	}
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("restat persisted gateway node invocation store: %w", err)
	}
	if !sameGatewayInvocationFileInfo(info, after) {
		return nil, errors.New("persisted gateway node invocation store changed after write")
	}
	return file, nil
}

func (store *GatewayInvocationStore) pruneLocked(now time.Time) {
	retentionBefore := now.Add(-store.retention).UnixNano()
	for invocationID, record := range store.records {
		if record.State == GatewayInvocationPrepared && now.Unix() >= record.Plan.ExpiresAt {
			delete(store.records, invocationID)
			continue
		}
		if record.State == GatewayInvocationDispatched && record.UpdatedAt < retentionBefore {
			delete(store.records, invocationID)
		}
	}
}

func (store *GatewayInvocationStore) pruneAndPersistLocked(now time.Time) error {
	previous := cloneGatewayInvocationRecords(store.records)
	store.pruneLocked(now)
	if len(previous) == len(store.records) {
		return nil
	}
	return store.persistMutationLocked(previous)
}

func (store *GatewayInvocationStore) persistMutationLocked(
	previous map[string]GatewayInvocationRecord,
) error {
	if err := store.ensureCachedFileCanonicalLocked(); err != nil {
		store.records = previous
		return err
	}
	err := store.saveLocked()
	if err != nil && !fileutil.IsCommittedWriteError(err) {
		store.records = previous
	}
	return err
}

func (store *GatewayInvocationStore) ensureCachedFileCanonicalLocked() error {
	if store.path == "" {
		return nil
	}
	info, err := os.Stat(store.path)
	if store.loaded && store.missing && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil && store.loaded && !store.missing &&
		sameGatewayInvocationPathFile(store.file, info) {
		return nil
	}
	store.invalidateCachedFileLocked()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("verify canonical gateway node invocation store: %w", err)
	}
	return fmt.Errorf(
		"%w: gateway node invocation store changed before mutation",
		ErrGatewayInvocationConflict,
	)
}

func (record GatewayInvocationRecord) validate() error {
	return record.validateFields(false)
}

func (record GatewayInvocationRecord) validateWithDescriptorCache(
	cache map[string][]CommandDescriptor,
) error {
	descriptorValidated := false
	for _, descriptor := range cache[record.Plan.DescriptorHash] {
		if reflect.DeepEqual(descriptor, record.Descriptor) {
			descriptorValidated = true
			break
		}
	}
	if err := record.validateFields(descriptorValidated); err != nil {
		return err
	}
	if !descriptorValidated {
		cache[record.Plan.DescriptorHash] = append(
			cache[record.Plan.DescriptorHash],
			cloneCommandDescriptor(record.Descriptor),
		)
	}
	return nil
}

func (record GatewayInvocationRecord) validateFields(descriptorValidated bool) error {
	if len(record.Target) == 0 || len(record.Target) > maxGatewayTargetLength ||
		!gatewayTargetPattern.MatchString(record.Target) {
		return fmt.Errorf("%w: malformed execution target", ErrInvalidInvocation)
	}
	if len(record.ToolCallID) == 0 || len(record.ToolCallID) > maxGatewayToolCallIDLength {
		return fmt.Errorf("%w: malformed tool call identity", ErrInvalidInvocation)
	}
	if err := record.Plan.ValidateAgainstHash(record.ExpectedPlanHash); err != nil {
		return err
	}
	descriptorHash := record.Plan.DescriptorHash
	if !descriptorValidated {
		if err := record.Descriptor.Validate(); err != nil {
			return err
		}
		var err error
		descriptorHash, err = record.Descriptor.Hash()
		if err != nil {
			return err
		}
	}
	if record.Descriptor.Name != record.Plan.Command ||
		record.Descriptor.Risk != record.Plan.Risk ||
		descriptorHash != record.Plan.DescriptorHash {
		return fmt.Errorf("%w: descriptor does not match plan", ErrInvalidInvocation)
	}
	switch record.State {
	case GatewayInvocationPrepared:
		if record.DispatchedAt != 0 || record.Cancellation != nil {
			return fmt.Errorf("%w: prepared invocation has dispatch time", ErrInvalidInvocation)
		}
	case GatewayInvocationDispatched:
		if record.DispatchedAt <= 0 {
			return fmt.Errorf("%w: dispatched invocation lacks dispatch time", ErrInvalidInvocation)
		}
	default:
		return fmt.Errorf("%w: invalid gateway invocation state", ErrInvalidInvocation)
	}
	if record.CreatedAt <= 0 || record.UpdatedAt < record.CreatedAt {
		return fmt.Errorf("%w: invalid gateway invocation timestamps", ErrInvalidInvocation)
	}
	if (record.WorkspaceID == "") != (record.ExecutionID == "") ||
		(record.WorkspaceID != "" &&
			(!validInvocationIdentifier(record.WorkspaceID) ||
				!validInvocationIdentifier(record.ExecutionID))) ||
		(record.Cancellation != nil &&
			(record.Cancellation.RequestedAt < record.DispatchedAt ||
				record.Cancellation.RequestedAt > record.UpdatedAt)) {
		return fmt.Errorf("%w: invalid gateway invocation ownership", ErrInvalidInvocation)
	}
	return nil
}

func gatewayInvocationScopeMatches(
	record GatewayInvocationRecord,
	principal GatewayInvocationPrincipal,
) bool {
	if record.WorkspaceID == "" || record.ExecutionID == "" {
		return strings.TrimSpace(principal.WorkspaceID) == "" &&
			strings.TrimSpace(principal.ExecutionID) == ""
	}
	return record.WorkspaceID == strings.TrimSpace(principal.WorkspaceID) &&
		record.ExecutionID == strings.TrimSpace(principal.ExecutionID)
}

func gatewayInvocationWorkspaceMatches(
	record GatewayInvocationRecord,
	principal GatewayInvocationPrincipal,
) bool {
	return record.WorkspaceID == "" ||
		record.WorkspaceID == strings.TrimSpace(principal.WorkspaceID)
}

func sameGatewayToolCall(
	record GatewayInvocationRecord,
	agentID string,
	sessionID string,
	actorID string,
	toolCallID string,
) bool {
	return record.Plan.AgentID == strings.TrimSpace(agentID) &&
		record.Plan.SessionID == strings.TrimSpace(sessionID) &&
		record.Plan.ActorID == strings.TrimSpace(actorID) &&
		record.ToolCallID == strings.TrimSpace(toolCallID)
}

func gatewayToolCallKey(agentID string, sessionID string, actorID string, toolCallID string) string {
	return strings.TrimSpace(agentID) + "\x00" +
		strings.TrimSpace(sessionID) + "\x00" +
		strings.TrimSpace(actorID) + "\x00" +
		strings.TrimSpace(toolCallID)
}

func sameGatewayInvocationBinding(
	left GatewayInvocationRecord,
	right GatewayInvocationRecord,
) bool {
	return left.Target == right.Target &&
		left.ToolCallID == right.ToolCallID &&
		left.ExpectedPlanHash == right.ExpectedPlanHash &&
		sameCommandDescriptor(left.Descriptor, right.Descriptor) &&
		left.Plan.AgentID == right.Plan.AgentID &&
		left.Plan.SessionID == right.Plan.SessionID &&
		left.Plan.ActorID == right.Plan.ActorID &&
		left.WorkspaceID == right.WorkspaceID &&
		left.ExecutionID == right.ExecutionID
}

func (owner GatewayInvocationOwner) validate() error {
	if !gatewayTargetPattern.MatchString(strings.TrimSpace(owner.Target)) ||
		!validInvocationIdentifier(strings.TrimSpace(owner.AgentID)) ||
		!validInvocationIdentifier(strings.TrimSpace(owner.SessionID)) ||
		!validInvocationIdentifier(strings.TrimSpace(owner.ActorID)) ||
		len(strings.TrimSpace(owner.ToolCallID)) == 0 ||
		len(strings.TrimSpace(owner.ToolCallID)) > maxGatewayToolCallIDLength ||
		(owner.WorkspaceID == "") != (owner.ExecutionID == "") ||
		(owner.WorkspaceID != "" &&
			(!validInvocationIdentifier(strings.TrimSpace(owner.WorkspaceID)) ||
				!validInvocationIdentifier(strings.TrimSpace(owner.ExecutionID)))) {
		return fmt.Errorf("%w: malformed gateway invocation owner", ErrInvalidInvocation)
	}
	return nil
}

func (owner GatewayInvocationOwner) matches(record GatewayInvocationRecord) bool {
	return strings.TrimSpace(owner.Target) == record.Target &&
		strings.TrimSpace(owner.AgentID) == record.Plan.AgentID &&
		strings.TrimSpace(owner.SessionID) == record.Plan.SessionID &&
		strings.TrimSpace(owner.ActorID) == record.Plan.ActorID &&
		strings.TrimSpace(owner.ToolCallID) == record.ToolCallID &&
		strings.TrimSpace(owner.WorkspaceID) == record.WorkspaceID &&
		strings.TrimSpace(owner.ExecutionID) == record.ExecutionID
}

func cloneGatewayInvocationRecords(
	records map[string]GatewayInvocationRecord,
) map[string]GatewayInvocationRecord {
	cloned := make(map[string]GatewayInvocationRecord, len(records))
	for invocationID, record := range records {
		cloned[invocationID] = cloneGatewayInvocationRecord(record)
	}
	return cloned
}

func cloneGatewayInvocationRecord(record GatewayInvocationRecord) GatewayInvocationRecord {
	record.Plan = cloneExecutionPlan(record.Plan)
	record.Descriptor = cloneCommandDescriptor(record.Descriptor)
	if record.Cancellation != nil {
		cancellation := *record.Cancellation
		record.Cancellation = &cancellation
	}
	return record
}

func cloneCommandDescriptor(descriptor CommandDescriptor) CommandDescriptor {
	descriptor.InputSchema = bytes.Clone(descriptor.InputSchema)
	descriptor.OutputSchema = bytes.Clone(descriptor.OutputSchema)
	descriptor.FileProfiles = cloneNodeFileProfileDescriptors(descriptor.FileProfiles)
	descriptor.ServiceProfiles = CloneServiceProfileDescriptors(descriptor.ServiceProfiles)
	descriptor.BrowserProfiles = CloneBrowserProfileDescriptors(descriptor.BrowserProfiles)
	descriptor.UpdateProfiles = CloneUpdateProfileDescriptors(descriptor.UpdateProfiles)
	descriptor.JobProfiles = CloneJobProfileDescriptors(descriptor.JobProfiles)
	if descriptor.ModelContract != nil {
		contract := cloneCommandModelContract(*descriptor.ModelContract)
		descriptor.ModelContract = &contract
	}
	return descriptor
}

func cloneNodeFileProfileDescriptors(
	profiles []FileProfileDescriptor,
) []FileProfileDescriptor {
	cloned := make([]FileProfileDescriptor, len(profiles))
	for index, profile := range profiles {
		cloned[index] = profile
		cloned[index].ReadableRoots = append([]string(nil), profile.ReadableRoots...)
		cloned[index].WritableRoots = append([]string(nil), profile.WritableRoots...)
	}
	return cloned
}

func sameCommandDescriptor(left, right CommandDescriptor) bool {
	leftHash, leftErr := left.Hash()
	rightHash, rightErr := right.Hash()
	return leftErr == nil && rightErr == nil && leftHash == rightHash
}

func cloneExecutionPlan(plan ExecutionPlan) ExecutionPlan {
	plan.Input = bytes.Clone(plan.Input)
	if plan.Update != nil {
		update := *plan.Update
		plan.Update = &update
	}
	return plan
}
