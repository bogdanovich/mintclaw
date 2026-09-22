package companion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/coding/privilege"
)

// CodingPrivilegePolicy is node-local configuration for the one privileged
// executor a machine-yolo-root scope may expose. Every value is operator
// owned; none can be supplied by a gateway request or model tool call.
type CodingPrivilegePolicy struct {
	Backend         string `json:"backend"`
	BrokerSocket    string `json:"broker_socket"`
	BrokerRevision  string `json:"broker_revision"`
	Profile         string `json:"profile"`
	ProfileRevision string `json:"profile_revision"`
	WorkingScope    string `json:"working_scope"`
}

func normalizeCodingPrivilegePolicy(
	policy *CodingPrivilegePolicy,
	baseDir string,
	platform string,
	admitted bool,
) (*CodingPrivilegePolicy, error) {
	if !admitted {
		if policy != nil {
			return nil, errors.New("privileged executor requires machine-yolo-root")
		}
		return nil, nil
	}
	if platform != "linux" {
		return nil, fmt.Errorf(
			"machine-yolo-root is unavailable on %s because process-tree containment is not proven",
			platform,
		)
	}
	if policy == nil {
		return nil, errors.New("machine-yolo-root requires an explicit privileged executor")
	}
	ready := *policy
	ready.Backend = strings.TrimSpace(ready.Backend)
	ready.BrokerRevision = strings.TrimSpace(ready.BrokerRevision)
	ready.Profile = strings.TrimSpace(ready.Profile)
	ready.ProfileRevision = strings.TrimSpace(ready.ProfileRevision)
	ready.WorkingScope = strings.TrimSpace(ready.WorkingScope)
	if ready.Backend != privilege.BackendAuthorityBroker ||
		!validShellBrokerRevision(ready.BrokerRevision) ||
		!validShellBrokerRevision(ready.ProfileRevision) ||
		ready.Profile == "" || ready.WorkingScope == "" {
		return nil, errors.New("machine-yolo-root privileged executor is invalid")
	}
	socket, err := resolveConfigPath(baseDir, ready.BrokerSocket)
	if err != nil || strings.TrimSpace(ready.BrokerSocket) == "" {
		return nil, errors.New("machine-yolo-root requires a valid authority broker socket")
	}
	ready.BrokerSocket = socket
	probe := privilege.Binding{
		Backend: ready.Backend, Endpoint: ready.BrokerSocket,
		BrokerRevision: ready.BrokerRevision, Profile: ready.Profile,
		ProfileRevision: ready.ProfileRevision, WorkingScope: ready.WorkingScope,
		TimeoutSecondsMax: 1, OutputBytesMax: 1,
	}
	if err := probe.Validate(); err != nil {
		return nil, fmt.Errorf("validate machine-yolo-root privileged executor: %w", err)
	}
	return &ready, nil
}

func prepareCodingPrivilegeBinding(
	ctx context.Context,
	policy *CodingPrivilegePolicy,
) (*privilege.Binding, error) {
	if policy == nil {
		return nil, nil
	}
	client, err := NewAuthorityBrokerClient(policy.BrokerSocket)
	if err != nil {
		return nil, fmt.Errorf("initialize coding authority broker: %w", err)
	}
	return prepareCodingPrivilegeBindingWithBroker(ctx, policy, client)
}

func prepareCodingPrivilegeBindingWithBroker(
	ctx context.Context,
	policy *CodingPrivilegePolicy,
	client codingPrivilegeBroker,
) (*privilege.Binding, error) {
	if policy == nil || client == nil {
		return nil, errors.New("coding authority broker policy and client are required")
	}
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("load coding authority broker snapshot: %w", err)
	}
	if snapshot.Revision != policy.BrokerRevision || len(snapshot.Profiles) != 1 {
		return nil, errors.New("coding authority broker revision is stale")
	}
	profile := snapshot.Profiles[0]
	if profile.Alias != policy.Profile || profile.Revision != policy.ProfileRevision ||
		profile.UID != 0 || profile.GID != 0 ||
		!slices.Contains(profile.WorkingScopes, policy.WorkingScope) {
		return nil, errors.New("coding authority broker root profile or working scope is stale")
	}
	binding := privilege.Binding{
		Backend: privilege.BackendAuthorityBroker, Endpoint: policy.BrokerSocket,
		BrokerRevision: snapshot.Revision, Profile: profile.Alias,
		ProfileRevision: profile.Revision, WorkingScope: policy.WorkingScope,
		TimeoutSecondsMax: profile.TimeoutSecondsMax, OutputBytesMax: profile.OutputBytesMax,
	}
	if err := binding.Validate(); err != nil {
		return nil, fmt.Errorf("validate coding privileged binding: %w", err)
	}
	return &binding, nil
}

type codingPrivilegeExecutor struct {
	binding privilege.Binding
	client  codingPrivilegeBroker
}

type codingPrivilegeBroker interface {
	Snapshot(context.Context) (ShellBrokerSnapshot, error)
	Execute(context.Context, ShellBrokerRequest) (ShellBrokerResult, error)
}

// NewCodingPrivilegeExecutor reconstructs a worker-local executor from an
// authenticated private worker binding. macOS and other unsupported systems
// fail closed through NewAuthorityBrokerClient.
func NewCodingPrivilegeExecutor(binding privilege.Binding) (privilege.Executor, error) {
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("privileged coding execution is unavailable on %s", runtime.GOOS)
	}
	client, err := NewAuthorityBrokerClient(binding.Endpoint)
	if err != nil {
		return nil, err
	}
	return &codingPrivilegeExecutor{binding: binding, client: client}, nil
}

func (executor *codingPrivilegeExecutor) Binding() privilege.Binding {
	if executor == nil {
		return privilege.Binding{}
	}
	return executor.binding
}

func (executor *codingPrivilegeExecutor) Execute(
	ctx context.Context,
	request privilege.Request,
) (privilege.Result, error) {
	if executor == nil || executor.client == nil || request.Validate(executor.binding) != nil {
		return privilege.Result{}, errors.New("privileged coding execution is invalid")
	}
	if err := executor.revalidate(ctx); err != nil {
		return privilege.Result{}, err
	}
	planHash, err := codingPrivilegePlanHash(executor.binding, request)
	if err != nil {
		return privilege.Result{}, err
	}
	result, err := executor.client.Execute(ctx, ShellBrokerRequest{
		InvocationID: request.InvocationID, PlanHash: planHash,
		Profile: executor.binding.Profile, ProfileRevision: executor.binding.ProfileRevision,
		Script: request.Script, WorkingScope: executor.binding.WorkingScope,
		Environment: map[string]string{}, TimeoutSeconds: request.TimeoutSeconds,
		OutputBytesMax: executor.binding.OutputBytesMax,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrShellBrokerCancellationConfirmed):
			return privilege.Result{}, privilege.ErrCancellationConfirmed
		case errors.Is(err, ErrShellBrokerOutcomeUnknown):
			return privilege.Result{}, privilege.ErrOutcomeUnknown
		default:
			return privilege.Result{}, err
		}
	}
	projected := privilege.Result{
		ExitCode: result.ExitCode, Stdout: result.Stdout, Stderr: result.Stderr,
		Signal: result.Signal, Truncated: result.Truncated,
		StartedAt: result.StartedAt, CompletedAt: result.CompletedAt,
	}
	if err := projected.Validate(executor.binding); err != nil {
		return privilege.Result{}, fmt.Errorf("%w: %w", privilege.ErrOutcomeUnknown, err)
	}
	return projected, nil
}

func (executor *codingPrivilegeExecutor) revalidate(ctx context.Context) error {
	snapshot, err := executor.client.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("%w: refresh authority broker snapshot: %w", privilege.ErrOutcomeUnknown, err)
	}
	if snapshot.Revision != executor.binding.BrokerRevision || len(snapshot.Profiles) != 1 {
		return errors.New("privileged coding authority is stale")
	}
	profile := snapshot.Profiles[0]
	if profile.Alias != executor.binding.Profile || profile.Revision != executor.binding.ProfileRevision ||
		profile.UID != 0 || profile.GID != 0 ||
		profile.TimeoutSecondsMax != executor.binding.TimeoutSecondsMax ||
		profile.OutputBytesMax != executor.binding.OutputBytesMax ||
		!slices.Contains(profile.WorkingScopes, executor.binding.WorkingScope) {
		return errors.New("privileged coding profile is stale")
	}
	return nil
}

func codingPrivilegePlanHash(binding privilege.Binding, request privilege.Request) (string, error) {
	encoded, err := json.Marshal(struct {
		Domain  string            `json:"domain"`
		Binding privilege.Binding `json:"binding"`
		Request privilege.Request `json:"request"`
	}{Domain: "mintclaw:coding-privileged-exec:v1", Binding: binding, Request: request})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
