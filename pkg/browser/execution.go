package browser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

type PrepareExecutionRequest struct {
	Owner              Owner
	RequestID          string
	SessionID          string
	TabID              string
	FrameID            string
	ContextCatalogID   string
	ContextGeneration  uint64
	SnapshotID         string
	SnapshotGeneration uint64
	Source             string
	Language           ExecutionLanguage
	DeclaredEffect     Effect
	Confirmation       string
}

type ExecutionPreparation struct {
	Invocation       Invocation
	Approval         ExecutionApprovalBinding
	RequiresApproval bool
}

type ExecutionArtifactSink func(
	context.Context,
	Invocation,
	int,
	DriverScreenshot,
) (RetainedScreenshot, error)

type ExecutionResult struct {
	Status          string               `json:"status"`
	Value           json.RawMessage      `json:"value"`
	Actions         int                  `json:"actions"`
	NetworkRequests int                  `json:"network_requests"`
	Artifacts       []RetainedScreenshot `json:"artifacts,omitempty"`
}

func ExecutionSourceDigest(source string) string {
	digest := sha256.Sum256([]byte("mintclaw.browser.execute.source.v1\x00" + source))
	return hex.EncodeToString(digest[:])
}

func hashExecutionBinding(binding ExecutionBinding) (string, error) {
	encoded, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("mintclaw.browser.execute.binding.v1\x00"), encoded...))
	return hex.EncodeToString(digest[:]), nil
}

func (broker *Broker) PrepareExecution(
	ctx context.Context,
	request PrepareExecutionRequest,
) (ExecutionPreparation, error) {
	if err := request.Owner.Validate(); err != nil {
		return ExecutionPreparation{}, err
	}
	if !validIdentifier(request.RequestID) || !validIdentifier(request.SessionID) ||
		!validIdentifier(request.TabID) || !validIdentifier(request.SnapshotID) ||
		request.SnapshotGeneration == 0 ||
		!validContextBinding(request.FrameID, request.ContextCatalogID, request.ContextGeneration) ||
		request.Source == "" || len(request.Source) > config.BrowserMaxExecuteSourceBytes ||
		!request.Language.Valid() || !request.DeclaredEffect.Valid() || len(request.Confirmation) > 4096 {
		return ExecutionPreparation{}, fmt.Errorf("%w: malformed execution preparation", ErrInvalid)
	}

	broker.mu.Lock()
	defer broker.mu.Unlock()
	session, slot, worker, err := broker.actionSessionLocked(
		ctx, request.Owner, request.SessionID, request.TabID,
	)
	if err != nil {
		return ExecutionPreparation{}, err
	}
	profile, ok := broker.browserProfile(session)
	if !ok || !profile.PrivilegedExecution.Enabled || profile.Revision != session.ProfileRevision {
		return ExecutionPreparation{}, ErrDenied
	}
	target := broker.config.Targets[session.Target]
	if profile.Mode == config.BrowserProfileAttachedUser ||
		(target.EffectivePlacement() == config.BrowserPlacementGateway &&
			target.Driver != config.BrowserDriverPlaywrightLibrary) {
		return ExecutionPreparation{}, ErrDriverIncompatible
	}
	executor, ok := worker.(PrivilegedExecutionWorker)
	if !ok || executor == nil {
		return ExecutionPreparation{}, ErrDriverIncompatible
	}
	if session.SnapshotID != request.SnapshotID ||
		session.SnapshotGeneration != request.SnapshotGeneration ||
		!sessionMatchesContextBinding(
			session, request.FrameID, request.ContextCatalogID, request.ContextGeneration,
		) || session.FrameID != "" || slot.navigationID == "" {
		return ExecutionPreparation{}, ErrStale
	}
	if session.ContextAuthority != nil {
		contextWorker, supported := worker.(ContextWorker)
		if !supported {
			return ExecutionPreparation{}, ErrDriverIncompatible
		}
		if err = broker.ensureContextFreshLocked(ctx, session, contextWorker); err != nil {
			return ExecutionPreparation{}, broker.handleWorkerBoundaryErrorLocked(ctx, session, err)
		}
	}

	limits := profile.PrivilegedExecution.Effective()
	allowedOrigins := append([]string(nil), profile.AllowedOrigins...)
	sort.Strings(allowedOrigins)
	binding := ExecutionBinding{
		Target: session.Target, Profile: session.Profile, ProfileRevision: session.ProfileRevision,
		PolicyRevision: session.PolicyRevision, ControllerGeneration: session.ControllerGeneration,
		TabID: session.TabID, FrameID: session.FrameID, ContextCatalogID: request.ContextCatalogID,
		ContextGeneration: request.ContextGeneration, SnapshotID: session.SnapshotID,
		SnapshotGeneration: session.SnapshotGeneration, CurrentOrigin: session.SnapshotOrigin,
		NetworkMode: profile.NetworkMode, AllowedOrigins: allowedOrigins,
		SourceDigest: ExecutionSourceDigest(request.Source), SourceBytes: len(request.Source),
		Language: request.Language, Effect: request.DeclaredEffect,
		CapabilityMode: profile.CapabilityMode, ApprovalMode: profile.ApprovalMode,
		Confirmation: request.Confirmation, DryRun: session.DryRun, Limits: limits,
	}
	if !broker.originNetworkAllowed(ctx, session, binding.CurrentOrigin) {
		return ExecutionPreparation{}, broker.quarantineNetworkDeniedLocked(ctx, session)
	}
	if err = broker.evaluateRestrictedExecutionPolicyLocked(ctx, session, worker, &binding); err != nil {
		return ExecutionPreparation{}, err
	}
	hash, err := hashExecutionBinding(binding)
	if err != nil {
		return ExecutionPreparation{}, err
	}
	invocationID := derivedIdentifier("execute", request.Owner, request.SessionID, request.RequestID)
	if existing, getErr := broker.store.GetInvocation(ctx, invocationID); getErr == nil {
		if existing.Execution == nil || existing.ActionHash != hash || existing.Effect != request.DeclaredEffect ||
			!reflect.DeepEqual(*existing.Execution, binding) {
			return ExecutionPreparation{}, ErrConflict
		}
		return executionPreparationView(existing), nil
	} else if !errors.Is(getErr, ErrNotFound) {
		return ExecutionPreparation{}, getErr
	}
	now := broker.now().UTC()
	invocation := Invocation{
		ID: invocationID, SessionID: session.ID, Owner: request.Owner, ActionHash: hash,
		Effect: request.DeclaredEffect, State: InvocationPrepared, Revision: 1,
		CreatedAt: now.UnixNano(), UpdatedAt: now.UnixNano(),
		ExpiresAt: now.Add(time.Duration(broker.config.Limits.Effective().PreparedSeconds) * time.Second).UnixNano(),
		Execution: &binding,
	}
	if err = broker.store.CreateInvocation(ctx, invocation); err != nil {
		return ExecutionPreparation{}, err
	}
	return executionPreparationView(invocation), nil
}

func executionPreparationView(invocation Invocation) ExecutionPreparation {
	return ExecutionPreparation{
		Invocation: invocation,
		Approval: ExecutionApprovalBinding{
			InvocationID: invocation.ID, ActionHash: invocation.ActionHash,
			PolicyRevision: invocation.Execution.PolicyRevision, ExpiresAt: invocation.ExpiresAt,
		},
		RequiresApproval: executionRequiresApproval(invocation),
	}
}

func executionRequiresApproval(invocation Invocation) bool {
	if invocation.Execution == nil {
		return true
	}
	if invocation.Execution.ApprovalMode == browserpolicy.ApprovalPolicy {
		return browserpolicy.RestrictedRequiresApproval(
			invocation.Execution.RestrictedDecision,
			invocation.Execution.Confirmation,
		)
	}
	return browserpolicy.RequiresApproval(
		invocation.Execution.ApprovalMode,
		string(invocation.Effect),
		invocation.Execution.Confirmation,
	)
}

func executionApprovalMatches(invocation Invocation, approval *ExecutionApprovalBinding) bool {
	return approval != nil && invocation.Execution != nil && approval.InvocationID == invocation.ID &&
		approval.ActionHash == invocation.ActionHash &&
		approval.PolicyRevision == invocation.Execution.PolicyRevision &&
		approval.ExpiresAt == invocation.ExpiresAt
}

func (broker *Broker) ExecuteExecution(
	ctx context.Context,
	owner Owner,
	invocationID string,
	source string,
	approval *ExecutionApprovalBinding,
	sink ExecutionArtifactSink,
) (Invocation, error) {
	if err := owner.Validate(); err != nil {
		return Invocation{}, err
	}
	if !validIdentifier(invocationID) || source == "" || len(source) > config.BrowserMaxExecuteSourceBytes {
		return Invocation{}, fmt.Errorf("%w: malformed execution dispatch", ErrInvalid)
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	invocation, err := broker.store.GetInvocation(ctx, invocationID)
	if err != nil {
		return Invocation{}, err
	}
	if !invocation.Owner.Equal(owner) || invocation.Execution == nil ||
		invocation.Execution.SourceDigest != ExecutionSourceDigest(source) ||
		invocation.Execution.SourceBytes != len(source) {
		return Invocation{}, ErrNotFound
	}
	if invocation.State.Terminal() {
		return diagnoseRecoveredOutcome(invocation), nil
	}
	if executionRequiresApproval(invocation) && !executionApprovalMatches(invocation, approval) {
		return Invocation{}, ErrApprovalRequired
	}
	binding := *invocation.Execution
	session, slot, worker, err := broker.actionSessionLocked(ctx, owner, invocation.SessionID, binding.TabID)
	if err != nil {
		return Invocation{}, err
	}
	profile, ok := broker.browserProfile(session)
	if !ok || !profile.PrivilegedExecution.Enabled || profile.Revision != binding.ProfileRevision ||
		session.Target != binding.Target || session.Profile != binding.Profile ||
		profile.PrivilegedExecution.Effective() != binding.Limits ||
		profile.NetworkMode != binding.NetworkMode ||
		!reflect.DeepEqual(sortedExecutionOrigins(profile.AllowedOrigins), binding.AllowedOrigins) ||
		session.PolicyRevision != binding.PolicyRevision ||
		session.ControllerGeneration != binding.ControllerGeneration ||
		session.SnapshotID != binding.SnapshotID ||
		session.SnapshotGeneration != binding.SnapshotGeneration ||
		session.SnapshotOrigin != binding.CurrentOrigin ||
		!sessionMatchesContextBinding(
			session, binding.FrameID, binding.ContextCatalogID, binding.ContextGeneration,
		) || slot.navigationID == "" {
		return Invocation{}, ErrStale
	}
	if !broker.originNetworkAllowed(ctx, session, binding.CurrentOrigin) {
		return Invocation{}, broker.quarantineNetworkDeniedLocked(ctx, session)
	}
	if err = broker.revalidateRestrictedExecutionPolicyLocked(ctx, session, binding); err != nil {
		return Invocation{}, err
	}
	if binding.DryRun && (invocation.Effect == EffectExternalCommit || invocation.Effect == EffectUnknown) {
		denied, completeErr := broker.completeInvocationLocked(
			ctx, invocation, InvocationCanceled, nil, "dry_run_denied",
		)
		return denied, errors.Join(ErrDenied, completeErr)
	}
	executor, ok := worker.(PrivilegedExecutionWorker)
	if !ok {
		return Invocation{}, ErrDriverIncompatible
	}
	driverRequest := DriverExecutionRequest{
		InvocationID: invocation.ID, PreparedHash: invocation.ActionHash,
		Source: source, SourceDigest: binding.SourceDigest, Language: binding.Language,
		Effect: invocation.Effect, Confirmation: binding.Confirmation,
		CurrentOrigin: binding.CurrentOrigin, ProfileRevision: binding.ProfileRevision,
		PolicyRevision: binding.PolicyRevision, NetworkMode: binding.NetworkMode,
		AllowedOrigins: append([]string(nil), binding.AllowedOrigins...),
		CapabilityMode: binding.CapabilityMode, PolicyEffect: binding.PolicyEffect,
		RestrictedDecision:       binding.WorkerRestrictedDecision,
		RestrictedPolicyRevision: binding.WorkerRestrictedRevision,
		TabID:                    binding.TabID, FrameID: binding.FrameID,
		ContextCatalogID: binding.ContextCatalogID, ContextGeneration: binding.ContextGeneration,
		SnapshotID: binding.SnapshotID, SnapshotGeneration: binding.SnapshotGeneration,
		Limits: binding.Limits,
	}
	invocation, executeErr := broker.executePreparedLocked(
		ctx,
		owner,
		invocation.ID,
		invocation.ActionHash,
		func(executeCtx context.Context) (json.RawMessage, error) {
			if policyErr := broker.revalidateRestrictedExecutionPolicyLocked(
				executeCtx,
				session,
				binding,
			); policyErr != nil {
				return nil, policyErr
			}
			runtimeCtx, cancel := context.WithTimeout(
				executeCtx,
				time.Duration(binding.Limits.RuntimeSeconds)*time.Second,
			)
			defer cancel()
			result, runErr := executor.ExecutePrivilegedAfterNavigationCheck(
				runtimeCtx,
				slot.navigationID,
				driverRequest,
			)
			if runErr != nil {
				return nil, runErr
			}
			terminal := ExecutionResult{
				Status: "completed", Value: result.Value, Actions: result.Actions,
				NetworkRequests: result.NetworkRequests,
			}
			for index, artifact := range result.Artifacts {
				if sink == nil {
					return nil, ErrDriverIncompatible
				}
				retained, retainErr := sink(runtimeCtx, invocation, index, artifact)
				if retainErr != nil {
					return nil, retainErr
				}
				terminal.Artifacts = append(terminal.Artifacts, retained)
			}
			return json.Marshal(terminal)
		},
	)
	postErr := broker.finalizeActionInvocationLocked(ctx, invocation.SessionID, invocation, "")
	return invocation, errors.Join(executeErr, postErr)
}

func sortedExecutionOrigins(origins []string) []string {
	result := append([]string(nil), origins...)
	sort.Strings(result)
	return result
}

func (broker *Broker) evaluateRestrictedExecutionPolicyLocked(
	ctx context.Context,
	session Session,
	worker ActionWorker,
	binding *ExecutionBinding,
) error {
	if binding == nil || binding.CapabilityMode != browserpolicy.CapabilityRestricted {
		return nil
	}
	profile, ok := broker.browserProfile(session)
	if !ok || profile.Policy == nil || profile.ApprovalMode != browserpolicy.ApprovalPolicy {
		return ErrDenied
	}
	revision, err := browserpolicy.PolicyRevision(*profile.Policy)
	if err != nil {
		return ErrDenied
	}
	binding.PolicyEffect = binding.Effect
	metadata := executionPolicyMetadata(*binding, session.ProfileRevision, revision)
	local, err := browserpolicy.Evaluate(ctx, *profile.Policy, metadata)
	if err != nil || local.Decision == browserpolicy.DecisionDeny {
		return ErrDenied
	}
	binding.LocalRestrictedDecision = local.Decision
	binding.RestrictedDecision = local.Decision
	binding.RestrictedPolicyRevision = revision
	if remote, remotePolicy := worker.(PolicyEvaluationWorker); remotePolicy {
		result, evaluateErr := remote.EvaluatePolicy(ctx, metadata)
		if evaluateErr != nil || !browserpolicy.DecisionValid(result.Result.Decision) ||
			result.Result.Decision == browserpolicy.DecisionDeny || !validDigest(result.PolicyRevision) ||
			result.ProfileRevision != session.ProfileRevision {
			return ErrDenied
		}
		binding.WorkerRestrictedDecision = result.Result.Decision
		binding.WorkerRestrictedRevision = result.PolicyRevision
		binding.RestrictedDecision, err = browserpolicy.CombineDecisions(
			local.Decision,
			result.Result.Decision,
		)
		if err != nil || binding.RestrictedDecision == browserpolicy.DecisionDeny {
			return ErrDenied
		}
	} else if _, remote := worker.(PreparedActionWorker); remote {
		return ErrDriverIncompatible
	}
	return nil
}

func (broker *Broker) revalidateRestrictedExecutionPolicyLocked(
	ctx context.Context,
	session Session,
	binding ExecutionBinding,
) error {
	if binding.CapabilityMode != browserpolicy.CapabilityRestricted {
		return nil
	}
	profile, ok := broker.browserProfile(session)
	if !ok || profile.Policy == nil || binding.PolicyEffect != binding.Effect {
		return ErrDenied
	}
	revision, err := browserpolicy.PolicyRevision(*profile.Policy)
	if err != nil || revision != binding.RestrictedPolicyRevision {
		return ErrDenied
	}
	result, err := browserpolicy.Evaluate(
		ctx,
		*profile.Policy,
		executionPolicyMetadata(binding, session.ProfileRevision, revision),
	)
	if err != nil || result.Decision != binding.LocalRestrictedDecision {
		return ErrDenied
	}
	effective := result.Decision
	if binding.WorkerRestrictedDecision != "" {
		effective, err = browserpolicy.CombineDecisions(effective, binding.WorkerRestrictedDecision)
	}
	if err != nil || effective != binding.RestrictedDecision || effective == browserpolicy.DecisionDeny {
		return ErrDenied
	}
	return nil
}

func executionPolicyMetadata(
	binding ExecutionBinding,
	profileRevision string,
	policyRevision string,
) browserpolicy.ActionMetadata {
	return browserpolicy.ActionMetadata{
		Action: browserpolicy.ActionExecute, Effect: string(binding.PolicyEffect),
		Origin: binding.CurrentOrigin, ProfileRevision: profileRevision,
		PolicyRevision: policyRevision,
	}
}
