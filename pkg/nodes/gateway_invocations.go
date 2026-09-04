package nodes

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultGatewayInvocationRetention = 7 * 24 * time.Hour
	maxGatewayToolCallIDLength        = 512
	maxGatewayTargetLength            = 64
)

const DefaultGatewayInvocationStoreBytes int64 = 4 * 1024 * 1024 * 1024

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

// GatewayInvocationStoreReport is a redacted operational view of the
// gateway invocation database. It intentionally contains no authority,
// identities, plans, arguments, or record payloads.
type GatewayInvocationStoreReport struct {
	SchemaVersion    int   `json:"schema_version"`
	Records          int64 `json:"records"`
	Prepared         int64 `json:"prepared"`
	Dispatched       int64 `json:"dispatched"`
	DatabaseBytes    int64 `json:"database_bytes"`
	WALBytes         int64 `json:"wal_bytes"`
	SHMBytes         int64 `json:"shm_bytes"`
	PageBytes        int64 `json:"page_bytes"`
	FreePageBytes    int64 `json:"free_page_bytes"`
	MaximumBytes     int64 `json:"maximum_bytes"`
	OldestUpdatedAt  int64 `json:"oldest_updated_at,omitempty"`
	RetentionSeconds int64 `json:"retention_seconds"`
}

// GatewayInvocationStore persists prepared plan ownership in SQLite.
type GatewayInvocationStore struct {
	backend *gatewayInvocationSQLiteStore
}

func GatewayInvocationStorePath(workspace string) string {
	return filepath.Join(workspace, "state", "node_invocations.db")
}

func NewGatewayInvocationStore(
	path string,
	maxBytes int64,
) (*GatewayInvocationStore, error) {
	backend, err := newGatewayInvocationSQLiteStore(path, maxBytes, time.Now)
	if err != nil {
		return nil, err
	}
	return &GatewayInvocationStore{backend: backend}, nil
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
	return store.backend.prepareOwned(principal, target, toolCallID, plan, descriptor)
}

func (store *GatewayInvocationStore) ByToolCall(
	principal GatewayInvocationPrincipal,
	toolCallID string,
) (GatewayInvocationRecord, bool, error) {
	return store.backend.byToolCall(principal, toolCallID)
}

func (store *GatewayInvocationStore) Lookup(
	principal GatewayInvocationPrincipal,
	invocationID string,
) (GatewayInvocationRecord, bool, error) {
	return store.backend.lookup(principal, invocationID)
}

func (store *GatewayInvocationStore) RequestCancellation(
	principal GatewayInvocationPrincipal,
	invocationID string,
) (GatewayInvocationRecord, bool, error) {
	return store.backend.requestCancellation(principal, invocationID)
}

func (store *GatewayInvocationStore) MarkDispatched(
	owner GatewayInvocationOwner,
	invocationID string,
	expectedPlanHash string,
) (GatewayInvocationRecord, bool, error) {
	return store.backend.markDispatched(owner, invocationID, expectedPlanHash)
}

func (store *GatewayInvocationStore) Close() error {
	if store == nil || store.backend == nil {
		return nil
	}
	return store.backend.close()
}

func (record GatewayInvocationRecord) validate() error {
	return record.validateFields(false)
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
		descriptorHash, err = record.Descriptor.HashForProtocol(record.Plan.ProtocolVersion)
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
