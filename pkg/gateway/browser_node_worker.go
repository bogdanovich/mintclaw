package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/protocol"
	nodews "github.com/bogdanovich/mintclaw/pkg/nodes/ws"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

type browserArtifactTransferPrepare struct {
	SessionID             string `json:"session_id"`
	RoutedSessionID       string `json:"routed_session_id"`
	ActionInvocationID    string `json:"action_invocation_id"`
	ArtifactRef           string `json:"artifact_ref"`
	PreparedActionHash    string `json:"prepared_action_hash"`
	BrowserPolicyRevision string `json:"browser_policy_revision"`
	AgentID               string `json:"agent_id"`
	ActorID               string `json:"actor_id"`
	Filename              string `json:"filename"`
	ContentType           string `json:"content_type"`
	ExpiresAt             int64  `json:"expires_at"`
}

type gatewayBrowserWorkerFactory struct {
	config *config.Config
	local  browser.WorkerFactory
	node   *nodeBrowserWorkerFactory
}

func newGatewayBrowserWorkerFactory(
	cfg *config.Config,
	runtime *nodeAdmissionRuntime,
) (browser.WorkerFactory, error) {
	if cfg == nil {
		return nil, browser.ErrDenied
	}
	factory := &gatewayBrowserWorkerFactory{config: cfg}
	for _, target := range cfg.Tools.Browser.Targets {
		if !target.Enabled {
			continue
		}
		switch target.EffectivePlacement() {
		case config.BrowserPlacementGateway:
			local, err := browser.NewPlaywrightWorkerFactory(cfg)
			if err != nil {
				return nil, err
			}
			factory.local = local
		case config.BrowserPlacementNode:
			if factory.node != nil {
				continue
			}
			source, err := newNodeInvocationSource(cfg, runtime)
			if err != nil {
				return nil, err
			}
			policyRevision, err := cfg.Tools.Browser.PolicyRevision()
			if err != nil {
				return nil, err
			}
			factory.node = &nodeBrowserWorkerFactory{
				config: cfg, source: source, policyRevision: policyRevision,
				workspaceID: browserNodeStableID("workspace", cfg.WorkspacePath()),
			}
		}
	}
	if factory.local == nil && factory.node == nil {
		return nil, browser.ErrDenied
	}
	return factory, nil
}

func (factory *gatewayBrowserWorkerFactory) Open(
	ctx context.Context,
	request browser.WorkerOpenRequest,
) (browser.WorkerOpenResult, error) {
	if factory == nil || factory.config == nil {
		return browser.WorkerOpenResult{}, browser.ErrWorkerUnavailable
	}
	target, ok := factory.config.Tools.Browser.Targets[request.Target]
	if !ok || !target.Enabled {
		return browser.WorkerOpenResult{}, browser.ErrDenied
	}
	if target.EffectivePlacement() == config.BrowserPlacementNode {
		if factory.node == nil {
			return browser.WorkerOpenResult{}, browser.ErrWorkerUnavailable
		}
		return factory.node.Open(ctx, request)
	}
	if factory.local == nil {
		return browser.WorkerOpenResult{}, browser.ErrWorkerUnavailable
	}
	return factory.local.Open(ctx, request)
}

func (factory *gatewayBrowserWorkerFactory) PassiveTargetDiagnostics(
	_ context.Context,
	targetName string,
	profileNames []string,
) (browser.TargetDiagnostics, error) {
	if factory == nil || factory.config == nil || len(profileNames) == 0 {
		return browser.TargetDiagnostics{}, browser.ErrWorkerUnavailable
	}
	target, ok := factory.config.Tools.Browser.Targets[targetName]
	if !ok || !target.Enabled {
		return browser.TargetDiagnostics{}, browser.ErrDenied
	}
	if target.EffectivePlacement() != config.BrowserPlacementNode {
		if factory.local == nil {
			return browser.TargetDiagnostics{}, browser.ErrWorkerUnavailable
		}
		driver := unavailableNodeBrowserReadiness("driver_unavailable", "contact_operator")
		if local, available := factory.local.(interface {
			PassiveReadiness() browser.DriverReadiness
		}); available {
			driver = local.PassiveReadiness()
		}
		profiles := make(map[string]browser.DriverReadiness, len(profileNames))
		for _, profileName := range profileNames {
			profiles[profileName] = driver
		}
		dragAvailable := true
		for _, profileName := range profileNames {
			profile, enabled := target.Profiles[profileName]
			if !enabled || !profile.Enabled || profile.DryRun {
				dragAvailable = false
				break
			}
		}
		actions := []browser.ActionKind(nil)
		if driver.Status != browser.ReadinessUnavailable {
			actions = []browser.ActionKind{
				browser.ActionNavigate, browser.ActionClick, browser.ActionFill,
				browser.ActionSelect, browser.ActionCheck, browser.ActionUncheck, browser.ActionHover,
			}
			if dragAvailable {
				actions = append(actions, browser.ActionDrag)
			}
			actions = append(actions, browser.ActionPress, browser.ActionScroll, browser.ActionDialog)
		}
		return browser.TargetDiagnostics{
			Actions: actions, Profiles: profiles, Contexts: driver.Status != browser.ReadinessUnavailable,
		}, nil
	}
	profiles := make(map[string]browser.DriverReadiness, len(profileNames))
	unavailable := func(readiness browser.DriverReadiness) (browser.TargetDiagnostics, error) {
		for _, profileName := range profileNames {
			profiles[profileName] = readiness
		}
		return browser.TargetDiagnostics{Profiles: profiles}, nil
	}
	if factory.node == nil || factory.node.source == nil {
		return unavailable(unavailableNodeBrowserReadiness("node_unavailable", "connect_node"))
	}
	executionTarget, ok := factory.config.Execution.Targets[target.NodeTarget]
	if !ok || executionTarget.Type != "node" {
		return unavailable(unavailableNodeBrowserReadiness("target_unavailable", "configure_target"))
	}
	record, found, err := factory.node.source.Lookup(executionTarget.Node)
	if err != nil || !found || !record.Connected || record.Registration == nil ||
		record.Snapshot.State != nodes.StateConnected {
		return unavailable(unavailableNodeBrowserReadiness("node_unavailable", "connect_node"))
	}
	var intersection map[string]struct{}
	allProfilesReady := true
	for _, profileName := range profileNames {
		localProfile, enabled := target.Profiles[profileName]
		if !enabled || !localProfile.Enabled {
			profiles[profileName] = unavailableNodeBrowserReadiness("profile_unavailable", "configure_profile")
			allProfilesReady = false
			continue
		}
		var remoteProfile nodes.BrowserProfileDescriptor
		profileReady := true
		for _, command := range []string{
			nodes.BrowserCommandSessionOpen, nodes.BrowserCommandSessionStatus,
			nodes.BrowserCommandObserve, nodes.BrowserCommandAct, nodes.BrowserCommandContexts,
			nodes.BrowserCommandSessionClose,
		} {
			descriptor, approved := browserApprovedDescriptor(record.Snapshot, record.Registration, command)
			if !approved {
				profiles[profileName] = unavailableNodeBrowserReadiness(
					"command_unapproved", "approve_browser_commands",
				)
				profileReady = false
				break
			}
			candidate, available := browserDescriptorProfile(descriptor, profileName)
			if !available || !browserProfileIntersects(
				localProfile, factory.config.Tools.Browser.Limits, candidate,
			) || (remoteProfile.Revision != "" && !browserProfilesEqual(remoteProfile, candidate)) {
				profiles[profileName] = unavailableNodeBrowserReadiness(
					"profile_policy_mismatch", "reconcile_profile",
				)
				profileReady = false
				break
			}
			remoteProfile = candidate
		}
		if !profileReady {
			allProfilesReady = false
			continue
		}
		profiles[profileName] = browser.DriverReadiness{
			Status: browser.ReadinessReady, Driver: browser.ReadinessReady,
			Browser: browser.ReadinessReady, Proxy: browser.ReadinessReady,
			Compatibility: browser.CompatibilityCompatible,
		}
		current := make(map[string]struct{}, len(remoteProfile.Actions))
		for _, action := range remoteProfile.Actions {
			if action == "drag" && localProfile.DryRun {
				continue
			}
			if action == "check" || action == "click" || action == "dialog" || action == "drag" ||
				action == "file_chooser" || action == "fill" || action == "hover" ||
				action == "navigate" ||
				action == "press" ||
				action == "scroll" ||
				action == "select" || action == "uncheck" {
				current[action] = struct{}{}
			}
		}
		if intersection == nil {
			intersection = current
		} else {
			for action := range intersection {
				if _, shared := current[action]; !shared {
					delete(intersection, action)
				}
			}
		}
	}
	actions := make([]browser.ActionKind, 0, len(intersection))
	if allProfilesReady {
		for _, action := range []browser.ActionKind{
			browser.ActionNavigate, browser.ActionClick, browser.ActionFill, browser.ActionCheck,
			browser.ActionUncheck, browser.ActionHover, browser.ActionDrag, browser.ActionFileChooser,
			browser.ActionPress, browser.ActionScroll,
			browser.ActionSelect, browser.ActionDialog,
		} {
			if _, available := intersection[string(action)]; available {
				actions = append(actions, action)
			}
		}
	}
	return browser.TargetDiagnostics{Actions: actions, Profiles: profiles, Contexts: allProfilesReady}, nil
}

func (factory *gatewayBrowserWorkerFactory) PassiveTargetReadiness(
	ctx context.Context,
	targetName string,
	profileName string,
) browser.DriverReadiness {
	diagnostics, err := factory.PassiveTargetDiagnostics(ctx, targetName, []string{profileName})
	if err != nil {
		return unavailableNodeBrowserReadiness("node_unavailable", "connect_node")
	}
	return diagnostics.Profiles[profileName]
}

func unavailableNodeBrowserReadiness(code, action string) browser.DriverReadiness {
	return browser.DriverReadiness{
		Status: browser.ReadinessUnavailable, Driver: browser.ReadinessUnavailable,
		Browser: browser.ReadinessUnavailable, Proxy: browser.ReadinessUnavailable,
		Compatibility: browser.CompatibilityUnchecked, Code: code, Action: action,
	}
}

type nodeBrowserWorkerFactory struct {
	config         *config.Config
	source         *nodeInvocationSource
	policyRevision string
	workspaceID    string
}

func (factory *nodeBrowserWorkerFactory) Open(
	ctx context.Context,
	request browser.WorkerOpenRequest,
) (browser.WorkerOpenResult, error) {
	if factory == nil || factory.config == nil || factory.source == nil || request.Owner.Validate() != nil {
		return browser.WorkerOpenResult{}, browser.ErrWorkerUnavailable
	}
	target, ok := factory.config.Tools.Browser.Targets[request.Target]
	if !ok || !target.Enabled || target.EffectivePlacement() != config.BrowserPlacementNode {
		return browser.WorkerOpenResult{}, browser.ErrDenied
	}
	profile, ok := target.Profiles[request.Profile]
	if !ok || !profile.Enabled || profile.DryRun == profile.AllowApprovedActions {
		return browser.WorkerOpenResult{}, browser.ErrDenied
	}
	worker := &nodeBrowserWorker{
		factory: factory, owner: request.Owner, browserTarget: request.Target,
		nodeTarget: target.NodeTarget, sessionID: request.SessionID,
		profile: request.Profile, limits: request.Limits,
		elements: make(map[string]browser.DriverElement),
	}
	descriptor, remoteProfile, err := worker.resolveAuthority(nodes.BrowserCommandSessionOpen)
	if err != nil || !browserProfileIntersects(profile, request.Limits, remoteProfile) {
		return browser.WorkerOpenResult{}, browser.ErrDenied
	}
	worker.profileRevision = remoteProfile.Revision
	worker.actions = slices.Clone(remoteProfile.Actions)
	worker.profileDescriptor = remoteProfile
	worker.profileDescriptor.Actions = slices.Clone(remoteProfile.Actions)
	worker.catalogRevision = worker.catalogHash
	input := nodes.BrowserSessionOpenInput{
		SessionID: request.SessionID, Profile: request.Profile,
		ProfileRevision: remoteProfile.Revision, BrowserPolicyRevision: factory.policyRevision,
		DryRun: request.DryRun, Limits: browserNodeLimits(request.Limits),
	}
	var result nodes.BrowserSessionResult
	if err = worker.invoke(ctx, descriptor, "open", input, &result); err != nil {
		return browser.WorkerOpenResult{Owner: worker}, err
	}
	if result.SessionID != request.SessionID || result.State != "ready" ||
		result.TabID == "" || result.Controller != "agent" ||
		!result.Features.Observe || !result.Features.Navigate || !result.Features.Contexts {
		return browser.WorkerOpenResult{Owner: worker}, browser.ErrDriverIncompatible
	}
	worker.tabID = result.TabID
	return browser.WorkerOpenResult{Owner: worker}, nil
}

type nodeBrowserWorker struct {
	factory           *nodeBrowserWorkerFactory
	owner             browser.Owner
	browserTarget     string
	nodeTarget        string
	sessionID         string
	profile           string
	profileRevision   string
	profileDescriptor nodes.BrowserProfileDescriptor
	limits            config.BrowserLimitsConfig
	nodeID            nodes.ID
	executor          string
	policyRevision    string
	catalogHash       string
	catalogRevision   string
	actions           []string
	tabID             string

	mu                      sync.Mutex
	snapshotGeneration      uint64
	cachedObservation       *browser.DriverObservation
	elements                map[string]browser.DriverElement
	currentOrigin           string
	statusSequence          uint64
	observeRecoverySequence uint64
	contextSequence         uint64
	contextCatalogDigest    string
	closed                  bool
}

func (worker *nodeBrowserWorker) Status(ctx context.Context) (browser.WorkerStatus, error) {
	worker.mu.Lock()
	if worker.closed {
		worker.mu.Unlock()
		return browser.WorkerLost, nil
	}
	worker.statusSequence++
	requestKey := fmt.Sprintf("status_%d", worker.statusSequence)
	worker.mu.Unlock()
	descriptor, _, err := worker.resolveAuthority(nodes.BrowserCommandSessionStatus)
	if err != nil {
		return browser.WorkerLost, err
	}
	var result nodes.BrowserSessionResult
	err = worker.invoke(ctx, descriptor, requestKey, nodes.BrowserSessionStatusInput{
		SessionID: worker.sessionID, ProfileRevision: worker.profileRevision,
	}, &result)
	if err != nil {
		return browser.WorkerLost, err
	}
	if result.State != "ready" {
		return browser.WorkerLost, nil
	}
	return browser.WorkerReady, nil
}

func (worker *nodeBrowserWorker) Close(ctx context.Context) error {
	worker.mu.Lock()
	if worker.closed {
		worker.mu.Unlock()
		return nil
	}
	worker.mu.Unlock()
	descriptor, _, err := worker.resolveAuthority(nodes.BrowserCommandSessionClose)
	if err != nil {
		return err
	}
	var result nodes.BrowserSessionResult
	if err = worker.invoke(ctx, descriptor, "close", nodes.BrowserSessionStatusInput{
		SessionID: worker.sessionID, ProfileRevision: worker.profileRevision,
	}, &result); err != nil {
		return err
	}
	if result.State != "closed" {
		return browser.ErrWorkerUnavailable
	}
	worker.mu.Lock()
	worker.closed = true
	worker.cachedObservation = nil
	worker.elements = make(map[string]browser.DriverElement)
	worker.currentOrigin = ""
	worker.mu.Unlock()
	return nil
}

func (worker *nodeBrowserWorker) Observe(ctx context.Context) (browser.DriverObservation, error) {
	worker.mu.Lock()
	if worker.closed {
		worker.mu.Unlock()
		return browser.DriverObservation{}, browser.ErrWorkerUnavailable
	}
	if worker.cachedObservation != nil {
		result := *worker.cachedObservation
		worker.cachedObservation = nil
		worker.rememberElementsLocked(result)
		worker.mu.Unlock()
		return result, nil
	}
	nextGeneration := worker.snapshotGeneration + 1
	worker.mu.Unlock()
	descriptor, _, err := worker.resolveAuthority(nodes.BrowserCommandObserve)
	if err != nil {
		return browser.DriverObservation{}, err
	}
	input := nodes.BrowserObserveInput{
		SessionID: worker.sessionID, TabID: worker.tabID,
		SnapshotGeneration: nextGeneration, Screenshot: false,
	}
	requestKey := fmt.Sprintf("observe_%d", nextGeneration)
	for attempts := 0; attempts < 10; attempts++ {
		var result nodes.BrowserObservationResult
		err = worker.invoke(ctx, descriptor, requestKey, input, &result)
		if err != nil {
			return browser.DriverObservation{}, err
		}
		if !result.ProtectedResult {
			return worker.acceptObservation(result, nextGeneration)
		}
		// The previous observe completed remotely, but its live response was
		// lost and only a page-data-free receipt survived. A read may safely
		// be repeated, but it needs a fresh invocation identity so the ledger
		// cannot return the same receipt forever.
		worker.mu.Lock()
		if worker.closed || worker.snapshotGeneration+1 != nextGeneration {
			worker.mu.Unlock()
			return browser.DriverObservation{}, browser.ErrStale
		}
		worker.snapshotGeneration = nextGeneration
		worker.cachedObservation = nil
		worker.elements = make(map[string]browser.DriverElement)
		worker.currentOrigin = ""
		worker.observeRecoverySequence++
		recovery := worker.observeRecoverySequence
		worker.mu.Unlock()
		nextGeneration++
		input.SnapshotGeneration = nextGeneration
		requestKey = fmt.Sprintf("observe_%d_recovery_%d", nextGeneration, recovery)
	}
	return browser.DriverObservation{}, browser.ErrWorkerUnavailable
}

func (worker *nodeBrowserWorker) Resolve(
	_ context.Context,
	target string,
) (browser.DriverElement, string, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	element, ok := worker.elements[target]
	if !ok {
		return browser.DriverElement{}, "", browser.ErrStale
	}
	return element, worker.currentOrigin, nil
}

func (*nodeBrowserWorker) Execute(context.Context, browser.DriverAction) error {
	return browser.ErrDriverIncompatible
}

func (worker *nodeBrowserWorker) SupportsPreparedAction(kind browser.ActionKind) bool {
	switch kind {
	case browser.ActionNavigate:
		return slices.Contains(worker.actions, "navigate")
	case browser.ActionClick:
		return slices.Contains(worker.actions, "click")
	case browser.ActionFill:
		return slices.Contains(worker.actions, "fill")
	case browser.ActionSelect:
		return slices.Contains(worker.actions, "select")
	case browser.ActionPress:
		return slices.Contains(worker.actions, "press")
	case browser.ActionScroll:
		return slices.Contains(worker.actions, "scroll")
	case browser.ActionDialog:
		return slices.Contains(worker.actions, "dialog")
	case browser.ActionCheck:
		return slices.Contains(worker.actions, "check")
	case browser.ActionUncheck:
		return slices.Contains(worker.actions, "uncheck")
	case browser.ActionHover:
		return slices.Contains(worker.actions, "hover")
	case browser.ActionDrag:
		return slices.Contains(worker.actions, "drag")
	case browser.ActionFileChooser:
		return slices.Contains(worker.actions, "file_chooser")
	default:
		return false
	}
}

func (worker *nodeBrowserWorker) ExecutePrepared(
	ctx context.Context,
	request browser.WorkerPreparedAction,
) error {
	var action nodes.BrowserAction
	var effect string
	switch {
	case request.Prepared.Action.Kind == browser.ActionNavigate &&
		request.DriverAction.Kind == browser.DriverNavigate && slices.Contains(worker.actions, "navigate"):
		action = nodes.BrowserAction{Kind: "navigate", URL: request.DriverAction.URL}
		effect = "navigation"
	case request.Prepared.Action.Kind == browser.ActionScroll &&
		request.DriverAction.Kind == browser.DriverScroll && slices.Contains(worker.actions, "scroll"):
		action = nodes.BrowserAction{
			Kind: "scroll", Direction: request.DriverAction.Direction, Amount: request.DriverAction.Amount,
		}
		effect = "read"
	case request.Prepared.Action.Kind == browser.ActionClick &&
		request.DriverAction.Kind == browser.DriverClick && slices.Contains(worker.actions, "click"):
		action = nodes.BrowserAction{Kind: "click", Ref: request.DriverAction.Target}
		effect = string(request.Prepared.Effect)
	case request.Prepared.Action.Kind == browser.ActionSelect &&
		request.DriverAction.Kind == browser.DriverSelect && slices.Contains(worker.actions, "select"):
		action = nodes.BrowserAction{
			Kind: "select", Ref: request.DriverAction.Target,
		}
		effect = "local_edit"
	case request.Prepared.Action.Kind == browser.ActionFill &&
		request.DriverAction.Kind == browser.DriverFill && slices.Contains(worker.actions, "fill"):
		action = nodes.BrowserAction{Kind: "fill", Ref: request.DriverAction.Target}
		effect = "local_edit"
	case request.Prepared.Action.Kind == browser.ActionPress &&
		request.DriverAction.Kind == browser.DriverPress && slices.Contains(worker.actions, "press"):
		action = nodes.BrowserAction{
			Kind: "press", Target: request.Prepared.Action.Target, Key: request.DriverAction.Key,
		}
		effect = "unknown"
	case request.Prepared.Action.Kind == browser.ActionDialog &&
		request.DriverAction.Kind == browser.DriverDialog && slices.Contains(worker.actions, "dialog"):
		action = nodes.BrowserAction{
			Kind: "dialog", DialogID: request.Prepared.Action.DialogID,
			Decision:       request.Prepared.Action.Decision,
			PromptProvided: request.DriverAction.PromptProvided,
		}
		effect = string(request.Prepared.Effect)
	case request.Prepared.Action.Kind == browser.ActionCheck &&
		request.DriverAction.Kind == browser.DriverCheck && slices.Contains(worker.actions, "check"):
		action = nodes.BrowserAction{Kind: "check", Ref: request.DriverAction.Target}
		effect = "local_edit"
	case request.Prepared.Action.Kind == browser.ActionUncheck &&
		request.DriverAction.Kind == browser.DriverUncheck && slices.Contains(worker.actions, "uncheck"):
		action = nodes.BrowserAction{Kind: "uncheck", Ref: request.DriverAction.Target}
		effect = "local_edit"
	case request.Prepared.Action.Kind == browser.ActionHover &&
		request.DriverAction.Kind == browser.DriverHover && slices.Contains(worker.actions, "hover"):
		action = nodes.BrowserAction{Kind: "hover", Ref: request.DriverAction.Target}
		effect = "read"
	case request.Prepared.Action.Kind == browser.ActionDrag &&
		request.DriverAction.Kind == browser.DriverDrag && slices.Contains(worker.actions, "drag"):
		action = nodes.BrowserAction{
			Kind: "drag", SourceRef: request.DriverAction.Target,
			DestinationRef: request.DriverAction.DestinationTarget,
		}
		effect = "unknown"
	case request.Prepared.Action.Kind == browser.ActionFileChooser &&
		request.DriverAction.Kind == browser.DriverUpload && slices.Contains(worker.actions, "file_chooser"):
		action = nodes.BrowserAction{
			Kind: "file_chooser", Ref: request.DriverAction.Target,
			ArtifactRef: request.Prepared.Action.ArtifactRef,
		}
		effect = "local_edit"
	default:
		return browser.ErrDenied
	}
	worker.mu.Lock()
	generation := worker.snapshotGeneration
	worker.mu.Unlock()
	descriptor, _, err := worker.resolveAuthority(nodes.BrowserCommandAct)
	if err != nil {
		return err
	}
	input := nodes.BrowserActInput{
		SessionID: worker.sessionID, TabID: worker.tabID,
		SnapshotGeneration: generation, ActionInvocationID: request.InvocationID,
		Action: action, Effect: effect, CurrentOrigin: request.Prepared.CurrentOrigin,
		PreparedActionHash:    request.Prepared.ActionHash,
		BrowserPolicyRevision: worker.factory.policyRevision,
		ProfileRevision:       worker.profileRevision,
	}
	if action.Kind == "click" || action.Kind == "fill" || action.Kind == "select" || action.Kind == "check" ||
		action.Kind == "uncheck" || action.Kind == "hover" || action.Kind == "drag" ||
		action.Kind == "file_chooser" {
		input.ExpectedRole = request.Prepared.ElementRole
		input.ExpectedName = request.Prepared.ElementName
	}
	if action.Kind == "drag" {
		input.DestinationExpectedRole = request.Prepared.DestinationElementRole
		input.DestinationExpectedName = request.Prepared.DestinationElementName
	}
	if action.Kind == "file_chooser" {
		input.ArtifactSHA256 = request.Prepared.ArtifactSHA256
		input.ArtifactBytes = request.Prepared.ArtifactBytes
		input.ArtifactFilename = request.Prepared.ArtifactFilename
		input.ArtifactContentType = request.Prepared.ArtifactContentType
		if err = worker.stageBrowserArtifact(ctx, request); err != nil {
			return err
		}
	}
	var ephemeralInput json.RawMessage
	if action.Kind == "fill" || action.Kind == "select" {
		input.InputDigest = nodes.BrowserInputDigest(request.DriverAction.Value)
		input.InputBytes = len(request.DriverAction.Value)
		ephemeralInput, err = json.Marshal(struct {
			Value string `json:"value"`
		}{Value: request.DriverAction.Value})
		if err != nil {
			return browser.ErrDenied
		}
	}
	if action.Kind == "dialog" {
		input.DialogType = request.Prepared.DialogType
		input.DialogMessageDigest = request.Prepared.DialogMessageDigest
		input.DialogMessageBytes = request.Prepared.DialogMessageBytes
		if action.PromptProvided {
			input.InputDigest = nodes.BrowserInputDigest(request.DriverAction.Value)
			input.InputBytes = len(request.DriverAction.Value)
			ephemeralInput, err = json.Marshal(struct {
				Value string `json:"value"`
			}{Value: request.DriverAction.Value})
			if err != nil {
				return browser.ErrDenied
			}
		}
	}
	if action.Kind == "click" || action.Kind == "drag" || action.Kind == "press" ||
		(action.Kind == "dialog" && action.Decision == "accept") {
		input.ApprovalDigest, err = nodes.BrowserApprovalDigest(input)
		if err != nil {
			return browser.ErrDenied
		}
	}
	var result nodes.BrowserActResult
	err = worker.invokeWithEphemeral(
		ctx, descriptor, "act_"+request.InvocationID, input, ephemeralInput, &result,
	)
	if err != nil {
		return err
	}
	if result.ActionInvocationID != request.InvocationID || result.State != "succeeded" {
		return browser.ErrWorkerUnavailable
	}
	var observation browser.DriverObservation
	if result.Observation == nil {
		// A recovered companion invocation intentionally contains only a
		// terminal receipt: fresh page observations are never durable. Advance
		// the proven action generation, then obtain new live authority without
		// replaying the accepted action.
		worker.mu.Lock()
		if worker.closed || worker.snapshotGeneration != generation {
			worker.mu.Unlock()
			return browser.ErrStale
		}
		worker.snapshotGeneration = generation + 1
		worker.cachedObservation = nil
		worker.elements = make(map[string]browser.DriverElement)
		worker.currentOrigin = ""
		worker.mu.Unlock()
		observation, err = worker.Observe(ctx)
	} else {
		observation, err = worker.acceptObservation(*result.Observation, generation+1)
	}
	if err != nil {
		return err
	}
	worker.mu.Lock()
	worker.cachedObservation = &observation
	worker.mu.Unlock()
	return nil
}

func (worker *nodeBrowserWorker) stageBrowserArtifact(
	ctx context.Context,
	request browser.WorkerPreparedAction,
) error {
	if worker == nil || worker.factory == nil || worker.factory.source == nil ||
		request.DriverAction.Value == "" || !filepath.IsAbs(request.DriverAction.Value) ||
		request.Prepared.ArtifactBytes < 1 || request.Prepared.ArtifactBytes > int64(worker.limits.UploadBytes) {
		return browser.ErrDenied
	}
	digestBytes, err := hex.DecodeString(request.Prepared.ArtifactSHA256)
	if err != nil || len(digestBytes) != sha256.Size {
		return browser.ErrDenied
	}
	var digest [sha256.Size]byte
	copy(digest[:], digestBytes)
	file, err := os.Open(request.DriverAction.Value)
	if err != nil {
		return browser.ErrDenied
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != request.Prepared.ArtifactBytes {
		return browser.ErrDenied
	}
	sessions, err := worker.factory.source.runtime.transferSessionsSnapshot(
		worker.factory.source.registryPath,
		worker.factory.source.generation,
	)
	if err != nil {
		return browser.ErrWorkerUnavailable
	}
	transferID := browserNodeStableID("browser_artifact", worker.sessionID, request.InvocationID)
	binding := nodews.TransferBinding{
		TransferID: transferID, Direction: protocol.TransferUpload,
		PolicyRevision: worker.profileRevision, TotalSize: uint64(request.Prepared.ArtifactBytes), SHA256: digest,
	}
	stream, err := sessions.OpenTransfer(ctx, worker.nodeID, binding)
	if err != nil {
		return browser.ErrWorkerUnavailable
	}
	defer func() { _ = stream.Close() }()
	frame := protocol.TransferFrame{
		Type: protocol.TransferFramePrepare, Direction: binding.Direction,
		TransferID: binding.TransferID, PolicyRevision: binding.PolicyRevision,
		TotalSize: binding.TotalSize, SHA256: binding.SHA256,
	}
	principal := worker.principal()
	frame.Payload, err = json.Marshal(browserArtifactTransferPrepare{
		SessionID: worker.sessionID, RoutedSessionID: principal.SessionID,
		ActionInvocationID: request.InvocationID, ArtifactRef: request.Prepared.Action.ArtifactRef,
		PreparedActionHash: request.Prepared.ActionHash, BrowserPolicyRevision: request.Prepared.PolicyRevision,
		AgentID: principal.AgentID, ActorID: principal.ActorID,
		Filename: request.Prepared.ArtifactFilename, ContentType: request.Prepared.ArtifactContentType,
		ExpiresAt: time.Unix(0, request.Prepared.ExpiresAt).Unix(),
	})
	if err != nil || stream.Send(ctx, frame) != nil {
		return browser.ErrWorkerUnavailable
	}
	response, err := stream.Receive(ctx)
	if err != nil {
		return browser.ErrWorkerUnavailable
	}
	if response.Type == protocol.TransferFrameCommitted {
		return nil
	}
	if response.Type != protocol.TransferFrameAccept {
		return browser.ErrDenied
	}
	hasher := sha256.New()
	buffer := make([]byte, protocol.MaxTransferChunkBytes)
	var sequence uint64
	for {
		count, readErr := file.Read(buffer)
		if count > 0 {
			sequence++
			_, _ = hasher.Write(buffer[:count])
			chunk := frame
			chunk.Type, chunk.Sequence = protocol.TransferFrameChunk, sequence
			chunk.Payload = append([]byte(nil), buffer[:count]...)
			if stream.Send(ctx, chunk) != nil {
				return browser.ErrWorkerUnavailable
			}
			ack, receiveErr := stream.Receive(ctx)
			if receiveErr != nil || ack.Type != protocol.TransferFrameAck || ack.Sequence != sequence {
				return browser.ErrWorkerUnavailable
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return browser.ErrDenied
			}
			break
		}
	}
	if !bytes.Equal(hasher.Sum(nil), digest[:]) {
		return browser.ErrDenied
	}
	frame.Type, frame.Sequence, frame.Payload = protocol.TransferFrameCommit, 0, nil
	if stream.Send(ctx, frame) != nil {
		return browser.ErrWorkerUnavailable
	}
	response, err = stream.Receive(ctx)
	if err != nil || response.Type != protocol.TransferFrameCommitted {
		return browser.ErrWorkerUnavailable
	}
	return nil
}

func (worker *nodeBrowserWorker) CatalogRevision() string {
	return worker.catalogRevision
}

func (worker *nodeBrowserWorker) acceptObservation(
	result nodes.BrowserObservationResult,
	expectedGeneration uint64,
) (browser.DriverObservation, error) {
	if result.SessionID != worker.sessionID || result.TabID != worker.tabID ||
		result.SnapshotGeneration != expectedGeneration {
		return browser.DriverObservation{}, browser.ErrDriverIncompatible
	}
	elements := make([]browser.DriverElement, len(result.Elements))
	for index, element := range result.Elements {
		elements[index] = browser.DriverElement{
			Target: element.Ref, Role: element.Role, Name: element.Name,
		}
	}
	observation := browser.DriverObservation{
		URL: result.URL, Origin: result.Origin, Title: result.Title,
		Snapshot: result.Snapshot, Elements: elements, Truncated: result.Truncated,
	}
	if result.PendingDialog != nil {
		observation.PendingDialog = &browser.DialogObservation{
			Type: result.PendingDialog.Type, Message: result.PendingDialog.Message,
		}
	}
	worker.mu.Lock()
	worker.snapshotGeneration = result.SnapshotGeneration
	worker.currentOrigin = observation.Origin
	worker.rememberElementsLocked(observation)
	worker.mu.Unlock()
	return observation, nil
}

func (worker *nodeBrowserWorker) rememberElementsLocked(observation browser.DriverObservation) {
	worker.elements = make(map[string]browser.DriverElement, len(observation.Elements))
	for _, element := range observation.Elements {
		worker.elements[element.Target] = element
	}
}

func (worker *nodeBrowserWorker) resolveAuthority(
	command string,
) (nodes.CommandDescriptor, nodes.BrowserProfileDescriptor, error) {
	executionTarget, ok := worker.factory.config.Execution.Targets[worker.nodeTarget]
	if !ok || executionTarget.Type != "node" {
		return nodes.CommandDescriptor{}, nodes.BrowserProfileDescriptor{}, browser.ErrDenied
	}
	record, found, err := worker.factory.source.Lookup(executionTarget.Node)
	if err != nil || !found || !record.Connected || record.Registration == nil ||
		record.Snapshot.State != nodes.StateConnected || record.Snapshot.Executor == "" ||
		record.Snapshot.PolicyRevision == "" {
		return nodes.CommandDescriptor{}, nodes.BrowserProfileDescriptor{}, browser.ErrWorkerUnavailable
	}
	descriptor, ok := browserApprovedDescriptor(record.Snapshot, record.Registration, command)
	if !ok {
		return nodes.CommandDescriptor{}, nodes.BrowserProfileDescriptor{}, browser.ErrDenied
	}
	profile, ok := browserDescriptorProfile(descriptor, worker.profile)
	if !ok {
		return nodes.CommandDescriptor{}, nodes.BrowserProfileDescriptor{}, browser.ErrDenied
	}
	if worker.catalogHash == "" {
		worker.nodeID = record.Snapshot.ID
		worker.executor = record.Snapshot.Executor
		worker.policyRevision = record.Snapshot.PolicyRevision
		worker.catalogHash = record.Snapshot.CatalogHash
	} else if worker.nodeID != record.Snapshot.ID ||
		worker.executor != record.Snapshot.Executor ||
		worker.policyRevision != record.Snapshot.PolicyRevision ||
		worker.catalogHash != record.Snapshot.CatalogHash {
		return nodes.CommandDescriptor{}, nodes.BrowserProfileDescriptor{}, browser.ErrDenied
	}
	if worker.profileDescriptor.Alias != "" &&
		!browserProfilesEqual(worker.profileDescriptor, profile) {
		return nodes.CommandDescriptor{}, nodes.BrowserProfileDescriptor{}, browser.ErrDenied
	}
	return descriptor, profile, nil
}

func (worker *nodeBrowserWorker) invoke(
	ctx context.Context,
	descriptor nodes.CommandDescriptor,
	requestKey string,
	input any,
	output any,
) error {
	return worker.invokeWithEphemeral(ctx, descriptor, requestKey, input, nil, output)
}

func (worker *nodeBrowserWorker) invokeWithEphemeral(
	ctx context.Context,
	descriptor nodes.CommandDescriptor,
	requestKey string,
	input any,
	ephemeralInput json.RawMessage,
	output any,
) error {
	executionTarget := worker.factory.config.Execution.Targets[worker.nodeTarget]
	record, found, err := worker.factory.source.Lookup(executionTarget.Node)
	if err != nil || !found || record.Registration == nil {
		return browser.ErrWorkerUnavailable
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return browser.ErrInvalid
	}
	principal := worker.principal()
	invocationID := browserNodeStableID(
		"browser", worker.sessionID, descriptor.Name, requestKey,
	)
	toolCallID := browserNodeStableID("call", invocationID)
	request := nodes.InvocationRequest{
		InvocationID: invocationID, IdempotencyKey: browserNodeStableID("idem", invocationID),
		NodeID: record.Snapshot.ID, CatalogHash: record.Snapshot.CatalogHash,
		Command: descriptor.Name, Input: inputJSON,
		AgentID: principal.AgentID, SessionID: principal.SessionID, ActorID: principal.ActorID,
		TimeoutSeconds:   min(worker.limits.ActionSeconds, nodes.MaxBrowserActionSeconds),
		OutputLimitBytes: min(worker.limits.ToolResultBytes, nodes.MaxBrowserToolResultBytes),
	}
	plan, err := nodes.PrepareExecutionPlan(
		request, descriptor, record.Snapshot.Executor, record.Snapshot.PolicyRevision,
		time.Now(), nodes.MaxExecutionPlanTTL,
	)
	if err != nil {
		return browser.ErrDenied
	}
	gatewayRecord, _, err := worker.factory.source.PrepareInvocation(
		executionTarget.Node, worker.nodeTarget, toolCallID, principal, plan, descriptor, true,
		func(current tools.NodeDiscoveryRecord) error {
			return worker.validateAuthority(current, descriptor)
		},
	)
	if err != nil {
		return browser.ErrDenied
	}
	if !browserRetainedInvocationMatches(gatewayRecord, plan, descriptor) {
		return browser.ErrDenied
	}
	owner := nodes.GatewayInvocationOwner{
		Target: worker.nodeTarget, AgentID: principal.AgentID,
		SessionID: principal.SessionID, ActorID: principal.ActorID,
		ToolCallID: toolCallID, WorkspaceID: principal.WorkspaceID,
		ExecutionID: principal.ExecutionID,
	}
	var raw json.RawMessage
	if gatewayRecord.State == nodes.GatewayInvocationPrepared {
		var dispatched bool
		dispatch := func(
			dispatchCtx context.Context,
			dispatchOwner nodes.GatewayInvocationOwner,
			invocationID string,
			expectedPlanHash string,
			input json.RawMessage,
		) (json.RawMessage, bool, error) {
			if len(input) != 0 {
				return worker.factory.source.DispatchInvocationEphemeral(
					dispatchCtx, dispatchOwner, invocationID, expectedPlanHash, input,
				)
			}
			return worker.factory.source.DispatchInvocation(
				dispatchCtx, dispatchOwner, invocationID, expectedPlanHash,
			)
		}
		raw, dispatched, err = dispatch(ctx, owner, gatewayRecord.Plan.InvocationID,
			gatewayRecord.ExpectedPlanHash, ephemeralInput)
		if err == nil {
			return json.Unmarshal(raw, output)
		}
		if browserInvocationDispatchDenied(err) {
			return browser.ErrDenied
		}
		if !dispatched {
			raw, dispatched, err = dispatch(ctx, owner, gatewayRecord.Plan.InvocationID,
				gatewayRecord.ExpectedPlanHash, ephemeralInput)
			if err == nil {
				return json.Unmarshal(raw, output)
			}
			if browserInvocationDispatchDenied(err) {
				return browser.ErrDenied
			}
			if !dispatched {
				return browser.ErrWorkerUnavailable
			}
		}
	}
	if gatewayRecord.State != nodes.GatewayInvocationDispatched &&
		gatewayRecord.State != nodes.GatewayInvocationPrepared {
		return browser.ErrWorkerUnavailable
	}
	return worker.reconcileInvocation(ctx, gatewayRecord, principal, len(ephemeralInput) != 0, output)
}

func browserRetainedInvocationMatches(
	record nodes.GatewayInvocationRecord,
	plan nodes.ExecutionPlan,
	descriptor nodes.CommandDescriptor,
) bool {
	descriptorHash, err := descriptor.Hash()
	if err != nil {
		return false
	}
	return record.Plan.InvocationID == plan.InvocationID &&
		record.Plan.IdempotencyKey == plan.IdempotencyKey &&
		record.Plan.NodeID == plan.NodeID && record.Plan.CatalogHash == plan.CatalogHash &&
		record.Plan.Command == plan.Command && record.Plan.DescriptorHash == descriptorHash &&
		record.Plan.AgentID == plan.AgentID && record.Plan.SessionID == plan.SessionID &&
		record.Plan.ActorID == plan.ActorID && record.Plan.Executor == plan.Executor &&
		record.Plan.PolicyRevision == plan.PolicyRevision &&
		record.Plan.Input != nil && string(record.Plan.Input) == string(plan.Input) &&
		record.Plan.TimeoutSeconds == plan.TimeoutSeconds &&
		record.Plan.OutputLimitBytes == plan.OutputLimitBytes
}

func (worker *nodeBrowserWorker) reconcileInvocation(
	ctx context.Context,
	record nodes.GatewayInvocationRecord,
	principal nodes.GatewayInvocationPrincipal,
	ephemeral bool,
	output any,
) error {
	redispatched := false
	for attempt := 0; attempt < 10; attempt++ {
		remote, err := worker.factory.source.QueryInvocation(
			ctx, principal, worker.nodeTarget, record.Plan.NodeID, record.Plan.InvocationID,
		)
		if err == nil {
			switch remote.State {
			case nodes.InvocationSucceeded:
				return json.Unmarshal(remote.Result, output)
			case nodes.InvocationFailed, nodes.InvocationCanceled:
				if remote.Failure != nil && remote.Failure.Code == nodes.InvocationDispatchCommandDenied {
					return browser.ErrDenied
				}
				if remote.Failure != nil && remote.Failure.Code == "SESSION_LOST" {
					return browser.ErrWorkerLost
				}
				if remote.Failure != nil && remote.Failure.Code == "STALE_BROWSER_STATE" {
					if record.Plan.Command == nodes.BrowserCommandContexts {
						return errors.Join(browser.ErrStale, browser.ErrContextAuthorityStale)
					}
					return browser.ErrStale
				}
				return browser.ErrWorkerUnavailable
			case nodes.InvocationUnknown:
				return browser.ErrWorkerUnavailable
			}
		} else if code, classified := nodes.InvocationQueryErrorCode(err); classified &&
			code == nodes.InvocationQueryNotFound && !redispatched && !ephemeral {
			raw, dispatched, dispatchErr := worker.factory.source.RedispatchInvocation(
				ctx, principal, worker.nodeTarget, record.Plan.NodeID, record.Plan.InvocationID,
			)
			redispatched = true
			if dispatchErr == nil {
				return json.Unmarshal(raw, output)
			}
			if !dispatched {
				return browser.ErrWorkerUnavailable
			}
		} else if classified && code != nodes.InvocationQueryNodeUnavailable &&
			code != nodes.InvocationQueryTransportUnavailable {
			return browser.ErrWorkerUnavailable
		}
		delay := min(100*time.Millisecond*time.Duration(1<<min(attempt, 3)), time.Second)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return browser.ErrWorkerUnavailable
		case <-timer.C:
		}
	}
	return browser.ErrWorkerUnavailable
}

func browserInvocationDispatchDenied(err error) bool {
	code, classified := nodes.InvocationDispatchErrorCode(err)
	return classified && code == nodes.InvocationDispatchCommandDenied
}

func (worker *nodeBrowserWorker) validateAuthority(
	current tools.NodeDiscoveryRecord,
	expected nodes.CommandDescriptor,
) error {
	if !current.Connected || current.Registration == nil ||
		current.Snapshot.ID != worker.nodeID ||
		current.Snapshot.Executor != worker.executor ||
		current.Snapshot.PolicyRevision != worker.policyRevision ||
		current.Snapshot.CatalogHash != worker.catalogHash {
		return browser.ErrDenied
	}
	descriptor, ok := browserApprovedDescriptor(
		current.Snapshot, current.Registration, expected.Name,
	)
	if !ok {
		return browser.ErrDenied
	}
	expectedHash, expectedErr := expected.Hash()
	currentHash, currentErr := descriptor.Hash()
	if expectedErr != nil || currentErr != nil || expectedHash != currentHash {
		return browser.ErrDenied
	}
	profile, ok := browserDescriptorProfile(descriptor, worker.profile)
	if !ok || worker.profileDescriptor.Alias != "" &&
		!browserProfilesEqual(worker.profileDescriptor, profile) {
		return browser.ErrDenied
	}
	return nil
}

func (worker *nodeBrowserWorker) principal() nodes.GatewayInvocationPrincipal {
	return nodes.GatewayInvocationPrincipal{
		AgentID: worker.owner.AgentID, SessionID: worker.owner.SessionKey,
		ActorID: worker.owner.ActorID, WorkspaceID: worker.factory.workspaceID,
		ExecutionID: worker.owner.ExecutionID,
	}
}

func browserApprovedDescriptor(
	snapshot nodes.Snapshot,
	registration *nodes.Registration,
	command string,
) (nodes.CommandDescriptor, bool) {
	if registration == nil || registration.ApprovedAt <= 0 ||
		registration.ApprovedCatalogHash == "" ||
		registration.ApprovedCatalogHash != snapshot.CatalogHash ||
		!slices.Contains(registration.AllowedCommands, command) {
		return nodes.CommandDescriptor{}, false
	}
	for _, descriptor := range snapshot.Catalog.Commands {
		if descriptor.Name == command && nodes.IsBrowserCommand(command) && descriptor.ModelContract == nil {
			return descriptor, true
		}
	}
	return nodes.CommandDescriptor{}, false
}

func browserDescriptorProfile(
	descriptor nodes.CommandDescriptor,
	alias string,
) (nodes.BrowserProfileDescriptor, bool) {
	for _, profile := range descriptor.BrowserProfiles {
		if profile.Alias == alias {
			return profile, true
		}
	}
	return nodes.BrowserProfileDescriptor{}, false
}

func browserProfileIntersects(
	local config.BrowserProfileConfig,
	limits config.BrowserLimitsConfig,
	remote nodes.BrowserProfileDescriptor,
) bool {
	requested := browserNodeLimits(limits)
	return remote.DryRun == local.DryRun &&
		remote.AllowApprovedActions == local.AllowApprovedActions &&
		remote.NetworkMode == local.EffectiveNetworkMode() &&
		slices.Contains(remote.Actions, "navigate") &&
		requested.Sessions <= remote.Limits.Sessions && requested.Tabs <= remote.Limits.Tabs &&
		requested.SessionSeconds <= remote.Limits.SessionSeconds &&
		requested.IdleSeconds <= remote.Limits.IdleSeconds &&
		requested.PreparedSeconds <= remote.Limits.PreparedSeconds &&
		requested.ActionSeconds <= remote.Limits.ActionSeconds &&
		requested.SnapshotBytes <= remote.Limits.SnapshotBytes &&
		requested.ScreenshotBytes <= remote.Limits.ScreenshotBytes &&
		requested.UploadBytes <= remote.Limits.UploadBytes &&
		requested.DownloadBytes <= remote.Limits.DownloadBytes &&
		requested.SnapshotRefs <= remote.Limits.SnapshotRefs &&
		requested.TextInputBytes <= remote.Limits.TextInputBytes &&
		requested.ToolResultBytes <= remote.Limits.ToolResultBytes &&
		requested.RetentionSecs <= remote.Limits.RetentionSecs
}

func browserProfilesEqual(left, right nodes.BrowserProfileDescriptor) bool {
	return left.Alias == right.Alias && left.Revision == right.Revision &&
		left.Driver == right.Driver && left.Mode == right.Mode &&
		left.NetworkMode == right.NetworkMode && left.DryRun == right.DryRun &&
		left.AllowApprovedActions == right.AllowApprovedActions &&
		left.Headed == right.Headed && slices.Equal(left.Actions, right.Actions) &&
		left.Limits == right.Limits
}

func browserNodeLimits(limits config.BrowserLimitsConfig) nodes.BrowserLimits {
	effective := limits.Effective()
	return nodes.BrowserLimits{
		Sessions: effective.Sessions, Tabs: effective.Tabs,
		SessionSeconds: effective.SessionSeconds, IdleSeconds: effective.IdleSeconds,
		PreparedSeconds: effective.PreparedSeconds, ActionSeconds: effective.ActionSeconds,
		SnapshotBytes: effective.SnapshotBytes, ScreenshotBytes: effective.ScreenshotBytes,
		UploadBytes: effective.UploadBytes, DownloadBytes: effective.DownloadBytes,
		SnapshotRefs: effective.SnapshotRefs, TextInputBytes: effective.TextInputBytes,
		ToolResultBytes: effective.ToolResultBytes, RetentionSecs: effective.RetentionSecs,
	}
}

func browserNodeStableID(prefix string, values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = fmt.Fprintf(hash, "%d:", len(value))
		_, _ = hash.Write([]byte(value))
	}
	return prefix + "_" + hex.EncodeToString(hash.Sum(nil))
}
