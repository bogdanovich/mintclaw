package browserhost

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	browserworker "github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
)

const (
	companionBrowserTarget    = "companion"
	browserHostCleanupTimeout = 15 * time.Second
)

var (
	ErrBrowserHostDenied   = nodes.ErrBrowserHostDenied
	ErrBrowserHostBusy     = nodes.ErrBrowserHostBusy
	ErrBrowserHostNotFound = nodes.ErrBrowserHostNotFound
	ErrBrowserHostStale    = nodes.ErrBrowserHostStale
	ErrBrowserHostLost     = nodes.ErrBrowserHostLost
	browserHostIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type (
	BrowserHostFeatures        = nodes.BrowserHostFeatures
	BrowserHostSession         = nodes.BrowserSessionResult
	BrowserHostOpenRequest     = nodes.BrowserHostOpenRequest
	BrowserHostStatusRequest   = nodes.BrowserHostStatusRequest
	BrowserHostObserveRequest  = nodes.BrowserHostObserveRequest
	BrowserHostNavigateRequest = nodes.BrowserHostActRequest
	BrowserHostElement         = nodes.BrowserElement
	BrowserHostObservation     = nodes.BrowserObservationResult
	BrowserHostCloseRequest    = nodes.BrowserHostStatusRequest
)

type browserHostFactory interface {
	Open(context.Context, browserworker.WorkerOpenRequest) (browserworker.WorkerOpenResult, error)
}

type browserHostUploadWorker interface {
	browserworker.ActionWorker
	UploadAfterNavigationCheck(context.Context, string, browserworker.DriverAction) error
}

type browserHostSession struct {
	mu                    sync.Mutex
	sessionID             string
	profile               companion.BrowserProfilePolicy
	browserPolicyRevision string
	agentID               string
	actorID               string
	routedSessionID       string
	state                 string
	safeFailure           string
	limits                nodes.BrowserLimits
	worker                browserworker.ActionWorker
	navigationWorker      browserworker.NavigationCheckedActionWorker
	fillWorker            browserworker.ProtectedFillWorker
	contextWorker         browserworker.ContextWorker
	contextCatalog        *browserworker.ContextCatalog
	cleanupOwner          browserworker.Worker
	tabID                 string
	snapshotGeneration    uint64
	actionInvocations     map[string]string
	elementBindingKey     []byte
	elementRefs           map[string]browserworker.DriverElement
	observationDigest     []byte
	expiresAt             time.Time
	idleExpiresAt         time.Time
}

// BrowserHost owns companion-local browser processes and profile leases. It
// deliberately exposes typed lifecycle operations rather than raw MCP/CDP.
// The gateway remains responsible for model-visible authorization, prepared
// actions, approvals, and artifacts.
type BrowserHost struct {
	profiles      map[string]companion.BrowserProfilePolicy
	factories     map[string]browserHostFactory
	now           func() time.Time
	verifyProfile func(companion.BrowserProfilePolicy) error

	mu       sync.Mutex
	sessions map[string]*browserHostSession

	transferMu         sync.Mutex
	transferRoot       string
	activeTransfers    map[string]*browserArtifactTransfer
	completedTransfers map[string]browserStagedArtifact
	outputArtifacts    map[string]browserOutputArtifact
	outputTransfers    map[string]*browserOutputTransfer
	outputExpiryAfter  func(time.Duration) <-chan time.Time

	beforeTransferAdmission func()
	beforeTransferCleanup   func()
}

func NewBrowserHost(profiles map[string]companion.BrowserProfilePolicy) (*BrowserHost, error) {
	factories := make(map[string]browserHostFactory)
	for alias, profile := range profiles {
		if !profile.Enabled {
			continue
		}
		server, err := companionPlaywrightServer(profile)
		if err != nil {
			return nil, fmt.Errorf("configure browser profile %q: %w", alias, err)
		}
		factory, err := browserworker.NewPlaywrightManagedHostFactory(
			browserworker.PlaywrightManagedHostConfig{
				Target: companionBrowserTarget, Profile: alias,
				ProfileConfig: config.BrowserProfileConfig{
					Enabled: true, Mode: config.BrowserProfileManaged,
					NetworkMode: profile.NetworkMode, DryRun: profile.DryRun,
					AllowApprovedActions: profile.AllowApprovedActions,
					AllowedOrigins:       append([]string(nil), profile.AllowedOrigins...),
					SensitiveFields:      append([]string(nil), profile.SensitiveFields...),
				},
				ServerConfig: server,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("configure browser profile %q worker: %w", alias, err)
		}
		factories[alias] = factory
	}
	return newBrowserHost(profiles, factories)
}

func newBrowserHost(
	profiles map[string]companion.BrowserProfilePolicy,
	factories map[string]browserHostFactory,
) (*BrowserHost, error) {
	descriptors, err := companion.BrowserProfileDescriptors(profiles)
	if err != nil {
		return nil, err
	}
	if len(descriptors) == 0 {
		return nil, errors.New("companion browser host requires an enabled profile")
	}
	clonedProfiles := make(map[string]companion.BrowserProfilePolicy, len(profiles))
	for alias, profile := range profiles {
		if !profile.Enabled {
			continue
		}
		if factories[alias] == nil {
			return nil, fmt.Errorf("browser profile %q requires a worker factory", alias)
		}
		clonedProfiles[alias] = cloneBrowserProfilePolicy(profile)
	}
	return &BrowserHost{
		profiles: clonedProfiles, factories: factories, now: time.Now,
		verifyProfile:      companion.VerifyBrowserProfileRuntimeIdentity,
		sessions:           make(map[string]*browserHostSession),
		activeTransfers:    make(map[string]*browserArtifactTransfer),
		completedTransfers: make(map[string]browserStagedArtifact),
		outputArtifacts:    make(map[string]browserOutputArtifact),
		outputTransfers:    make(map[string]*browserOutputTransfer),
		outputExpiryAfter:  time.After,
	}, nil
}

func companionPlaywrightServer(profile companion.BrowserProfilePolicy) (config.MCPServerConfig, error) {
	args := append([]string(nil), profile.DriverArguments...)
	for _, argument := range args {
		if managedDriverArgument(argument) {
			return config.MCPServerConfig{}, errors.New("driver arguments contain a host-managed option")
		}
	}
	args = append(args,
		"--user-data-dir", profile.ProfileDirectory,
		"--output-mode", "stdout",
	)
	if !profile.Headed {
		args = append(args, "--headless")
	}
	driverPath := profile.DriverLauncherDirectory()
	if inherited := os.Getenv("PATH"); inherited != "" {
		driverPath += string(os.PathListSeparator) + inherited
	}
	return config.MCPServerConfig{
		Enabled: false, Command: profile.DriverExecutable, Args: args, Type: "stdio",
		Env:               map[string]string{"PATH": driverPath},
		SessionLossReplay: config.MCPSessionLossReplayNever,
		ExclusiveLockFile: profile.LockFile,
	}, nil
}

func managedDriverArgument(argument string) bool {
	for _, managed := range []string{
		"--allowed-origins", "--blocked-origins", "--caps", "--config",
		"--proxy-server", "--proxy-bypass", "--cdp-endpoint", "--endpoint",
		"--extension", "--user-data-dir", "--storage-state", "--isolated",
		"--output-dir", "--output-mode", "--headless",
	} {
		if argument == managed || strings.HasPrefix(argument, managed+"=") {
			return true
		}
	}
	return false
}

func cloneBrowserProfilePolicy(
	profile companion.BrowserProfilePolicy,
) companion.BrowserProfilePolicy {
	profile.AllowedAgents = append([]string(nil), profile.AllowedAgents...)
	profile.AllowedActors = append([]string(nil), profile.AllowedActors...)
	profile.DriverArguments = append([]string(nil), profile.DriverArguments...)
	profile.AllowedOrigins = append([]string(nil), profile.AllowedOrigins...)
	profile.SensitiveFields = append([]string(nil), profile.SensitiveFields...)
	profile.AllowedActions = append([]string(nil), profile.AllowedActions...)
	return profile
}

func (host *BrowserHost) Open(
	ctx context.Context,
	request BrowserHostOpenRequest,
) (BrowserHostSession, error) {
	if host == nil || !browserHostIdentifier(request.SessionID) ||
		!browserHostIdentifier(request.RoutedSessionID) ||
		!browserHostDigest(request.BrowserPolicyRevision) ||
		request.Limits.Validate() != nil {
		return BrowserHostSession{}, ErrBrowserHostDenied
	}
	profile, ok := host.profiles[request.Profile]
	if !ok || request.DryRun != profile.DryRun ||
		!authorizeBrowserProfile(profile, request.ProfileRevision, request.AgentID, request.ActorID) ||
		!browserLimitsWithin(request.Limits, profile.Limits) {
		return BrowserHostSession{}, ErrBrowserHostDenied
	}
	if host.verifyProfile == nil || host.verifyProfile(profile) != nil {
		return BrowserHostSession{}, ErrBrowserHostDenied
	}

	host.mu.Lock()
	if existing := host.sessions[request.SessionID]; existing != nil {
		host.mu.Unlock()
		existing.mu.Lock()
		authorized := existing.profile.Revision == request.ProfileRevision &&
			existing.agentID == request.AgentID && existing.actorID == request.ActorID &&
			existing.routedSessionID == request.RoutedSessionID &&
			existing.browserPolicyRevision == request.BrowserPolicyRevision
		existing.mu.Unlock()
		if !authorized {
			return BrowserHostSession{}, ErrBrowserHostDenied
		}
		return BrowserHostSession{}, ErrBrowserHostBusy
	}
	for _, session := range host.sessions {
		session.mu.Lock()
		occupied := session.state == "opening" || session.state == "ready" || session.state == "closing" ||
			session.worker != nil || session.cleanupOwner != nil || session.safeFailure == "cleanup_required"
		session.mu.Unlock()
		if occupied {
			host.mu.Unlock()
			return BrowserHostSession{}, ErrBrowserHostBusy
		}
	}
	now := host.now().UTC()
	elementBindingKey := make([]byte, sha256.Size)
	if _, randomErr := rand.Read(elementBindingKey); randomErr != nil {
		host.mu.Unlock()
		return BrowserHostSession{}, ErrBrowserHostDenied
	}
	session := &browserHostSession{
		sessionID: request.SessionID,
		profile:   profile, browserPolicyRevision: request.BrowserPolicyRevision,
		agentID: request.AgentID, actorID: request.ActorID,
		routedSessionID: request.RoutedSessionID, state: "opening",
		limits:            request.Limits,
		tabID:             "tab_primary",
		actionInvocations: make(map[string]string),
		elementBindingKey: elementBindingKey,
		elementRefs:       make(map[string]browserworker.DriverElement),
		expiresAt:         now.Add(time.Duration(request.Limits.SessionSeconds) * time.Second),
		idleExpiresAt:     now.Add(time.Duration(request.Limits.IdleSeconds) * time.Second),
	}
	host.sessions[request.SessionID] = session
	host.mu.Unlock()

	opened, openErr := host.factories[request.Profile].Open(ctx, browserworker.WorkerOpenRequest{
		SessionID: request.SessionID, Target: companionBrowserTarget,
		Profile: request.Profile, DryRun: request.DryRun,
		Limits: browserConfigLimits(request.Limits),
	})
	actionWorker, workerOK := opened.Owner.(browserworker.ActionWorker)
	navigationWorker, navigationOK := opened.Owner.(browserworker.NavigationCheckedActionWorker)
	fillWorker, fillOK := opened.Owner.(browserworker.ProtectedFillWorker)
	fillRequired := slices.Contains(profile.AllowedActions, "fill")
	contextWorker, _ := opened.Owner.(browserworker.ContextWorker)
	if openErr != nil || !workerOK || actionWorker == nil || !navigationOK || navigationWorker == nil ||
		(fillRequired && (!fillOK || fillWorker == nil)) {
		cleanupErr := closeBrowserHostOwner(ctx, opened.Owner)
		session.mu.Lock()
		session.state = "lost"
		if cleanupErr != nil {
			session.safeFailure = "cleanup_required"
			session.cleanupOwner = opened.Owner
		} else if errors.Is(openErr, browserworker.ErrDriverIncompatible) {
			session.safeFailure = "driver_incompatible"
		} else {
			session.safeFailure = "worker_unavailable"
		}
		session.mu.Unlock()
		if openErr != nil {
			return host.sessionView(session), openErr
		}
		return host.sessionView(session), browserworker.ErrWorkerUnavailable
	}
	session.mu.Lock()
	if session.state != "opening" {
		session.mu.Unlock()
		cleanupErr := closeBrowserHostOwner(ctx, actionWorker)
		session.mu.Lock()
		if cleanupErr != nil {
			session.state = "lost"
			session.safeFailure = "cleanup_required"
			session.cleanupOwner = actionWorker
		}
		session.mu.Unlock()
		return host.sessionView(session), ErrBrowserHostLost
	}
	session.worker = actionWorker
	session.navigationWorker = navigationWorker
	session.fillWorker = fillWorker
	session.contextWorker = contextWorker
	session.state = "ready"
	session.mu.Unlock()
	return host.sessionView(session), nil
}

func (host *BrowserHost) BrowserProfiles() []nodes.BrowserProfileDescriptor {
	descriptors, _ := companion.BrowserProfileDescriptors(host.profiles)
	return nodes.CloneBrowserProfileDescriptors(descriptors)
}

func (host *BrowserHost) Status(
	ctx context.Context,
	request BrowserHostStatusRequest,
) (BrowserHostSession, error) {
	session, err := host.authorizedSession(request)
	if err != nil {
		return BrowserHostSession{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != "ready" || session.worker == nil {
		return browserHostSessionView(session), nil
	}
	if host.expireSessionLocked(ctx, session) {
		return browserHostSessionView(session), nil
	}
	status, statusErr := session.worker.Status(ctx)
	if statusErr != nil {
		return BrowserHostSession{}, statusErr
	}
	if status == browserworker.WorkerLost {
		session.state = "lost"
		session.safeFailure = "worker_unavailable"
	}
	return browserHostSessionView(session), nil
}

func (host *BrowserHost) Observe(
	ctx context.Context,
	request BrowserHostObserveRequest,
) (BrowserHostObservation, error) {
	session, err := host.authorizedSession(BrowserHostStatusRequest{
		SessionID: request.SessionID, ProfileRevision: host.sessionProfileRevision(request.SessionID),
		RoutedSessionID: request.RoutedSessionID,
		AgentID:         request.AgentID, ActorID: request.ActorID,
	})
	if err != nil {
		return BrowserHostObservation{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != "ready" || session.worker == nil {
		return BrowserHostObservation{}, ErrBrowserHostLost
	}
	if host.expireSessionLocked(ctx, session) {
		return BrowserHostObservation{}, ErrBrowserHostLost
	}
	if request.TabID != session.tabID || request.SnapshotGeneration != session.snapshotGeneration+1 {
		return BrowserHostObservation{}, ErrBrowserHostStale
	}
	actionCtx, cancelAction, actionDeadline := host.actionContextLocked(ctx, session)
	observation, navigationIdentity, observeErr := observeBrowserHostNavigation(actionCtx, session)
	actionContextErr := actionCtx.Err()
	cancelAction()
	if observeErr != nil {
		return BrowserHostObservation{}, observeErr
	}
	if actionContextErr != nil {
		return BrowserHostObservation{}, actionContextErr
	}
	if !host.now().UTC().Before(actionDeadline) {
		return BrowserHostObservation{}, context.DeadlineExceeded
	}
	session.snapshotGeneration = request.SnapshotGeneration
	session.idleExpiresAt = host.now().UTC().Add(time.Duration(session.limits.IdleSeconds) * time.Second)
	return browserHostObservation(request.SessionID, session, observation, navigationIdentity), nil
}

func (host *BrowserHost) Navigate(
	ctx context.Context,
	request BrowserHostNavigateRequest,
) (BrowserHostObservation, error) {
	if !browserHostIdentifier(request.ActionInvocationID) ||
		!browserHostDigest(request.PreparedActionHash) ||
		!browserHostDigest(request.BrowserPolicyRevision) || request.Effect != "navigation" ||
		request.Action.Kind != "navigate" || request.Action.URL == "" ||
		request.Action.Ref != "" || request.Action.Target != "" || request.Action.Value != "" ||
		request.Action.Key != "" || request.Action.Direction != "" || request.Action.Amount != 0 ||
		request.CurrentOrigin == "" || len(request.CurrentOrigin) > nodes.MaxBrowserURLBytes ||
		request.ExpectedRole != "" || request.ExpectedName != "" || request.ApprovalDigest != "" {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	return host.executeAction(ctx, request, "navigate", browserworker.DriverAction{
		Kind: browserworker.DriverNavigate, URL: request.Action.URL,
	})
}

func (host *BrowserHost) Click(
	ctx context.Context,
	request BrowserHostNavigateRequest,
) (BrowserHostObservation, error) {
	if !browserHostIdentifier(request.ActionInvocationID) ||
		!browserHostDigest(request.PreparedActionHash) ||
		!browserHostDigest(request.BrowserPolicyRevision) ||
		request.Action.Kind != "click" || !browserHostIdentifier(request.Action.Ref) ||
		request.Action.URL != "" || request.Action.Target != "" || request.Action.Value != "" || request.Action.Key != "" ||
		request.Action.Direction != "" || request.Action.Amount != 0 ||
		request.ExpectedRole == "" || len(request.ExpectedRole) > 128 || len(request.ExpectedName) > 4096 ||
		request.Effect != nodes.BrowserClickEffect(request.ExpectedRole) ||
		request.CurrentOrigin == "" || len(request.CurrentOrigin) > nodes.MaxBrowserURLBytes ||
		!nodes.BrowserApprovalDigestMatches(browserHostActInput(request)) {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	return host.executeAction(ctx, request, "click", browserworker.DriverAction{
		Kind: browserworker.DriverClick,
	})
}

func (host *BrowserHost) Select(
	ctx context.Context,
	request BrowserHostNavigateRequest,
) (BrowserHostObservation, error) {
	if !browserHostIdentifier(request.ActionInvocationID) ||
		!browserHostDigest(request.PreparedActionHash) ||
		!browserHostDigest(request.BrowserPolicyRevision) || request.Effect != "local_edit" ||
		request.Action.Kind != "select" || !browserHostIdentifier(request.Action.Ref) ||
		request.Action.Value == "" || len(request.Action.Value) > nodes.MaxBrowserTextInputBytes ||
		request.Action.URL != "" || request.Action.Target != "" || request.Action.Key != "" ||
		request.Action.Direction != "" || request.Action.Amount != 0 ||
		request.ExpectedRole != "combobox" || len(request.ExpectedName) > 4096 ||
		request.CurrentOrigin == "" || len(request.CurrentOrigin) > nodes.MaxBrowserURLBytes ||
		request.ApprovalDigest != "" {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	return host.executeAction(ctx, request, "select", browserworker.DriverAction{
		Kind: browserworker.DriverSelect, Value: request.Action.Value,
	})
}

func (host *BrowserHost) Check(
	ctx context.Context,
	request BrowserHostNavigateRequest,
) (BrowserHostObservation, error) {
	return host.checkState(ctx, request, "check", browserworker.DriverCheck)
}

func (host *BrowserHost) Uncheck(
	ctx context.Context,
	request BrowserHostNavigateRequest,
) (BrowserHostObservation, error) {
	return host.checkState(ctx, request, "uncheck", browserworker.DriverUncheck)
}

func (host *BrowserHost) checkState(
	ctx context.Context,
	request BrowserHostNavigateRequest,
	action string,
	driverKind browserworker.DriverActionKind,
) (BrowserHostObservation, error) {
	if !browserHostIdentifier(request.ActionInvocationID) ||
		!browserHostDigest(request.PreparedActionHash) ||
		!browserHostDigest(request.BrowserPolicyRevision) || request.Effect != "local_edit" ||
		request.Action.Kind != action || !browserHostIdentifier(request.Action.Ref) ||
		request.Action.URL != "" || request.Action.Target != "" || request.Action.Value != "" ||
		request.Action.Key != "" || request.Action.Direction != "" || request.Action.Amount != 0 ||
		!nodes.BrowserCheckRoleAllowed(action, request.ExpectedRole) || len(request.ExpectedName) > 4096 ||
		request.CurrentOrigin == "" || len(request.CurrentOrigin) > nodes.MaxBrowserURLBytes ||
		request.ApprovalDigest != "" {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	return host.executeAction(ctx, request, action, browserworker.DriverAction{Kind: driverKind})
}

func (host *BrowserHost) Hover(
	ctx context.Context,
	request BrowserHostNavigateRequest,
) (BrowserHostObservation, error) {
	if !browserHostIdentifier(request.ActionInvocationID) ||
		!browserHostDigest(request.PreparedActionHash) ||
		!browserHostDigest(request.BrowserPolicyRevision) || request.Effect != "read" ||
		request.Action.Kind != "hover" || !browserHostIdentifier(request.Action.Ref) ||
		request.Action.URL != "" || request.Action.Target != "" || request.Action.Value != "" ||
		request.Action.Key != "" || request.Action.Direction != "" || request.Action.Amount != 0 ||
		request.ExpectedRole == "" || len(request.ExpectedRole) > 128 || len(request.ExpectedName) > 4096 ||
		request.CurrentOrigin == "" || len(request.CurrentOrigin) > nodes.MaxBrowserURLBytes ||
		request.ApprovalDigest != "" {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	return host.executeAction(ctx, request, "hover", browserworker.DriverAction{Kind: browserworker.DriverHover})
}

func (host *BrowserHost) Drag(
	ctx context.Context,
	request BrowserHostNavigateRequest,
) (BrowserHostObservation, error) {
	if !browserHostIdentifier(request.ActionInvocationID) ||
		!browserHostDigest(request.PreparedActionHash) ||
		!browserHostDigest(request.BrowserPolicyRevision) || request.Effect != "unknown" ||
		request.Action.Kind != "drag" || !browserHostIdentifier(request.Action.SourceRef) ||
		!browserHostIdentifier(request.Action.DestinationRef) ||
		request.Action.SourceRef == request.Action.DestinationRef || request.Action.URL != "" ||
		request.Action.Ref != "" || request.Action.Target != "" || request.Action.Value != "" ||
		request.Action.Key != "" || request.Action.Direction != "" || request.Action.Amount != 0 ||
		request.ExpectedRole == "" || len(request.ExpectedRole) > 128 || len(request.ExpectedName) > 4096 ||
		request.DestinationExpectedRole == "" || len(request.DestinationExpectedRole) > 128 ||
		len(request.DestinationExpectedName) > 4096 ||
		request.CurrentOrigin == "" || len(request.CurrentOrigin) > nodes.MaxBrowserURLBytes ||
		!nodes.BrowserApprovalDigestMatches(browserHostActInput(request)) {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	return host.executeAction(ctx, request, "drag", browserworker.DriverAction{Kind: browserworker.DriverDrag})
}

func (host *BrowserHost) FileChooser(
	ctx context.Context,
	request BrowserHostNavigateRequest,
) (BrowserHostObservation, error) {
	if !browserHostIdentifier(request.ActionInvocationID) || !browserHostDigest(request.PreparedActionHash) ||
		!browserHostDigest(request.BrowserPolicyRevision) || request.Effect != "local_edit" ||
		request.Action.Kind != "file_chooser" || !browserHostIdentifier(request.Action.Ref) ||
		!strings.HasPrefix(request.Action.ArtifactRef, nodes.TransferArtifactRefPrefix) ||
		request.Action.URL != "" || request.Action.Target != "" || request.Action.Value != "" ||
		request.Action.Key != "" || request.Action.Direction != "" || request.Action.Amount != 0 ||
		request.ExpectedRole != "button" || len(request.ExpectedName) > 4096 ||
		!browserHostDigest(request.ArtifactSHA256) || request.ArtifactBytes < 1 ||
		request.ArtifactBytes > nodes.MaxBrowserUploadBytes || request.ArtifactFilename == "" ||
		request.ArtifactFilename != filepath.Base(request.ArtifactFilename) || len(request.ArtifactFilename) > 255 ||
		request.ArtifactContentType == "" || len(request.ArtifactContentType) > 255 ||
		request.CurrentOrigin == "" || len(request.CurrentOrigin) > nodes.MaxBrowserURLBytes ||
		request.ApprovalDigest != "" {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	artifact, ok := host.takeBrowserArtifact(request)
	if !ok {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	defer func() { _ = os.RemoveAll(artifact.directory) }()
	return host.executeAction(ctx, request, "file_chooser", browserworker.DriverAction{
		Kind: browserworker.DriverUpload, Value: artifact.path,
		ArtifactSHA256: request.ArtifactSHA256, ArtifactBytes: request.ArtifactBytes,
		ArtifactFilename: request.ArtifactFilename, ArtifactContentType: request.ArtifactContentType,
	})
}

func (host *BrowserHost) Fill(
	ctx context.Context,
	request BrowserHostNavigateRequest,
) (BrowserHostObservation, error) {
	if !browserHostIdentifier(request.ActionInvocationID) ||
		!browserHostDigest(request.PreparedActionHash) ||
		!browserHostDigest(request.BrowserPolicyRevision) || request.Effect != "local_edit" ||
		request.Action.Kind != "fill" || !browserHostIdentifier(request.Action.Ref) ||
		request.Action.Value == "" || len(request.Action.Value) > nodes.MaxBrowserTextInputBytes ||
		request.Action.URL != "" || request.Action.Target != "" || request.Action.Key != "" ||
		request.Action.Direction != "" || request.Action.Amount != 0 ||
		!nodes.BrowserFillFieldAllowed(request.ExpectedRole, request.ExpectedName) ||
		request.CurrentOrigin == "" || len(request.CurrentOrigin) > nodes.MaxBrowserURLBytes ||
		request.ApprovalDigest != "" {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	return host.executeAction(ctx, request, "fill", browserworker.DriverAction{
		Kind: browserworker.DriverFill, Value: request.Action.Value,
	})
}

func (host *BrowserHost) Press(
	ctx context.Context,
	request BrowserHostNavigateRequest,
) (BrowserHostObservation, error) {
	if !browserHostIdentifier(request.ActionInvocationID) ||
		!browserHostDigest(request.PreparedActionHash) ||
		!browserHostDigest(request.BrowserPolicyRevision) || request.Effect != "unknown" ||
		request.Action.Kind != "press" || request.Action.Target != "document" ||
		!nodes.BrowserPressKeyValid(request.Action.Key) || request.Action.URL != "" || request.Action.Ref != "" ||
		request.Action.Value != "" || request.Action.Direction != "" || request.Action.Amount != 0 ||
		request.ExpectedRole != "" || request.ExpectedName != "" ||
		request.CurrentOrigin == "" || len(request.CurrentOrigin) > nodes.MaxBrowserURLBytes ||
		!nodes.BrowserApprovalDigestMatches(browserHostActInput(request)) {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	return host.executeAction(ctx, request, "press", browserworker.DriverAction{
		Kind: browserworker.DriverPress, Key: request.Action.Key,
	})
}

func (host *BrowserHost) Scroll(
	ctx context.Context,
	request BrowserHostNavigateRequest,
) (BrowserHostObservation, error) {
	if !browserHostIdentifier(request.ActionInvocationID) ||
		!browserHostDigest(request.PreparedActionHash) ||
		!browserHostDigest(request.BrowserPolicyRevision) || request.Effect != "read" ||
		request.Action.Kind != "scroll" ||
		(request.Action.Direction != "up" && request.Action.Direction != "down") ||
		request.Action.Amount < 1 || request.Action.Amount > nodes.MaxBrowserScrollAmount ||
		request.Action.URL != "" || request.Action.Ref != "" || request.Action.Target != "" ||
		request.Action.Value != "" || request.Action.Key != "" ||
		request.CurrentOrigin == "" || len(request.CurrentOrigin) > nodes.MaxBrowserURLBytes ||
		request.ExpectedRole != "" || request.ExpectedName != "" || request.ApprovalDigest != "" {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	return host.executeAction(ctx, request, "scroll", browserworker.DriverAction{
		Kind: browserworker.DriverScroll, Direction: request.Action.Direction, Amount: request.Action.Amount,
	})
}

func (host *BrowserHost) Dialog(
	ctx context.Context,
	request BrowserHostNavigateRequest,
) (BrowserHostObservation, error) {
	if !browserHostIdentifier(request.ActionInvocationID) ||
		!browserHostDigest(request.PreparedActionHash) ||
		!browserHostDigest(request.BrowserPolicyRevision) ||
		request.Action.Kind != "dialog" || !browserHostIdentifier(request.Action.DialogID) ||
		(request.Action.Decision != "accept" && request.Action.Decision != "dismiss") ||
		(request.DialogType != "alert" && request.DialogType != "beforeunload" &&
			request.DialogType != "confirm" && request.DialogType != "prompt") ||
		!browserHostDigest(request.DialogMessageDigest) || request.DialogMessageBytes < 0 ||
		request.DialogMessageBytes > nodes.MaxBrowserDialogMessageBytes ||
		request.Action.URL != "" || request.Action.Ref != "" || request.Action.Target != "" ||
		request.Action.Key != "" || request.Action.Direction != "" || request.Action.Amount != 0 ||
		request.ExpectedRole != "" || request.ExpectedName != "" ||
		request.CurrentOrigin == "" || len(request.CurrentOrigin) > nodes.MaxBrowserURLBytes ||
		(request.Action.Decision == "dismiss" &&
			(request.Effect != "read" || request.Action.PromptProvided || request.Action.Value != "" ||
				request.InputDigest != "" || request.InputBytes != 0 || request.ApprovalDigest != "")) ||
		(request.Action.Decision == "accept" &&
			(request.Effect != "external_commit" ||
				(request.Action.PromptProvided && request.DialogType != "prompt") ||
				(!request.Action.PromptProvided && request.Action.Value != "") ||
				len(request.Action.Value) > nodes.MaxBrowserTextInputBytes ||
				(request.Action.PromptProvided &&
					(request.InputBytes != len(request.Action.Value) ||
						!nodes.BrowserInputDigestMatches(request.InputDigest, request.Action.Value))) ||
				(!request.Action.PromptProvided && (request.InputDigest != "" || request.InputBytes != 0)) ||
				!nodes.BrowserApprovalDigestMatches(browserHostActInput(request)))) {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	return host.executeAction(ctx, request, "dialog", browserworker.DriverAction{
		Kind: browserworker.DriverDialog, Accept: request.Action.Decision == "accept",
		Value: request.Action.Value, PromptProvided: request.Action.PromptProvided,
	})
}

func (host *BrowserHost) Contexts(
	ctx context.Context,
	request nodes.BrowserHostContextRequest,
) (nodes.BrowserContextResult, error) {
	if !browserHostIdentifier(request.RequestID) ||
		(request.Operation != "list" && request.Operation != "open" &&
			request.Operation != "select" && request.Operation != "close") ||
		((request.Operation == "list" || request.Operation == "open") &&
			(request.Authority != nil || request.TabID != "" || request.FrameID != "")) ||
		((request.Operation == "select" || request.Operation == "close") &&
			(request.Authority == nil || !browserHostIdentifier(request.TabID))) ||
		(request.Operation == "close" && request.FrameID != "") ||
		(request.FrameID != "" && !browserHostIdentifier(request.FrameID)) {
		return nodes.BrowserContextResult{}, ErrBrowserHostDenied
	}
	session, err := host.authorizedSession(BrowserHostStatusRequest{
		SessionID: request.SessionID, ProfileRevision: request.ProfileRevision,
		RoutedSessionID: request.RoutedSessionID,
		AgentID:         request.AgentID, ActorID: request.ActorID,
	})
	if err != nil {
		return nodes.BrowserContextResult{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != "ready" || session.worker == nil || session.contextWorker == nil {
		return nodes.BrowserContextResult{}, ErrBrowserHostLost
	}
	if host.expireSessionLocked(ctx, session) {
		return nodes.BrowserContextResult{}, ErrBrowserHostLost
	}
	actionCtx, cancelAction, actionDeadline := host.actionContextLocked(ctx, session)
	defer cancelAction()
	var catalog browserworker.ContextCatalog
	var observation *nodes.BrowserObservationResult
	switch request.Operation {
	case "list":
		catalog, err = session.contextWorker.ContextCatalog(actionCtx)
	case "open":
		catalog, err = session.contextWorker.OpenTab(actionCtx)
	case "select", "close":
		binding, bindingErr := browserContextMutationBinding(request)
		if bindingErr != nil {
			return nodes.BrowserContextResult{}, ErrBrowserHostDenied
		}
		authority, authorityErr := browserworker.ContextMutationAuthorityFromBinding(binding)
		if authorityErr != nil {
			return nodes.BrowserContextResult{}, ErrBrowserHostDenied
		}
		if request.Operation == "select" {
			var observed browserworker.DriverObservation
			observed, catalog, err = session.contextWorker.SelectContext(actionCtx, authority)
			if err == nil {
				var navigationIdentity string
				navigationIdentity, err = stableBrowserHostNavigationIdentity(actionCtx, session)
				if err == nil {
					session.snapshotGeneration++
					result := browserHostObservation(request.SessionID, session, observed, navigationIdentity)
					observation = &result
				}
			}
		} else {
			catalog, err = session.contextWorker.CloseTab(actionCtx, authority)
		}
	}
	if err != nil {
		if errors.Is(err, browserworker.ErrContextAuthorityStale) {
			return nodes.BrowserContextResult{}, ErrBrowserHostStale
		}
		if request.Operation == "list" {
			session.state = "lost"
			session.safeFailure = "worker_unavailable"
			session.elementRefs = make(map[string]browserworker.DriverElement)
			session.observationDigest = nil
		} else {
			host.quarantineActionLocked(session)
		}
		return nodes.BrowserContextResult{}, ErrBrowserHostLost
	}
	if actionCtx.Err() != nil || !host.now().UTC().Before(actionDeadline) || catalog.Validate() != nil {
		if request.Operation == "list" {
			session.state = "lost"
			session.safeFailure = "worker_unavailable"
			session.elementRefs = make(map[string]browserworker.DriverElement)
			session.observationDigest = nil
		} else {
			host.quarantineActionLocked(session)
		}
		return nodes.BrowserContextResult{}, ErrBrowserHostLost
	}
	catalogCopy, cloneErr := browserContextCatalogValue(browserContextCatalogResult(catalog))
	if cloneErr != nil {
		host.quarantineActionLocked(session)
		return nodes.BrowserContextResult{}, ErrBrowserHostLost
	}
	if request.Operation != "select" && session.contextCatalog != nil &&
		!reflect.DeepEqual(*session.contextCatalog, catalogCopy) {
		session.elementRefs = make(map[string]browserworker.DriverElement)
		session.observationDigest = nil
	}
	session.contextCatalog = &catalogCopy
	session.idleExpiresAt = host.now().UTC().Add(time.Duration(session.limits.IdleSeconds) * time.Second)
	return nodes.BrowserContextResult{
		Operation: request.Operation, Catalog: browserContextCatalogResult(catalogCopy), Observation: observation,
	}, nil
}

func (host *BrowserHost) executeAction(
	ctx context.Context,
	request BrowserHostNavigateRequest,
	action string,
	driverAction browserworker.DriverAction,
) (BrowserHostObservation, error) {
	if action != "drag" &&
		(request.Action.SourceRef != "" || request.Action.DestinationRef != "" ||
			request.DestinationExpectedRole != "" || request.DestinationExpectedName != "") {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	if action != "file_chooser" && (request.Action.ArtifactRef != "" || request.ArtifactSHA256 != "" ||
		request.ArtifactBytes != 0 || request.ArtifactFilename != "" || request.ArtifactContentType != "") {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	session, err := host.authorizedSession(BrowserHostStatusRequest{
		SessionID: request.SessionID, ProfileRevision: request.ProfileRevision,
		RoutedSessionID: request.RoutedSessionID,
		AgentID:         request.AgentID, ActorID: request.ActorID,
	})
	if err != nil {
		return BrowserHostObservation{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != "ready" || session.worker == nil {
		return BrowserHostObservation{}, ErrBrowserHostLost
	}
	if host.expireSessionLocked(ctx, session) {
		return BrowserHostObservation{}, ErrBrowserHostLost
	}
	if request.TabID != session.tabID || request.SnapshotGeneration != session.snapshotGeneration ||
		request.BrowserPolicyRevision != session.browserPolicyRevision ||
		!slices.Contains(session.profile.AllowedActions, action) {
		return BrowserHostObservation{}, ErrBrowserHostStale
	}
	if action == "fill" && (len(request.Action.Value) > session.limits.TextInputBytes ||
		!nodes.BrowserFillFieldAllowedWithPolicy(
			request.ExpectedRole,
			request.ExpectedName,
			session.profile.SensitiveFields,
		)) {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	if (action == "click" || action == "drag" || action == "press" ||
		(action == "dialog" && request.Action.Decision == "accept")) &&
		(session.profile.DryRun || !session.profile.AllowApprovedActions ||
			!nodes.BrowserApprovalDigestMatches(browserHostActInput(request))) {
		return BrowserHostObservation{}, ErrBrowserHostDenied
	}
	var boundElement browserworker.DriverElement
	var destinationElement browserworker.DriverElement
	if action == "click" || action == "fill" || action == "select" || action == "check" ||
		action == "uncheck" || action == "hover" || action == "drag" || action == "file_chooser" {
		ref := request.Action.Ref
		if action == "drag" {
			ref = request.Action.SourceRef
		}
		var found bool
		boundElement, found = session.elementRefs[ref]
		if !found || boundElement.Role != request.ExpectedRole || boundElement.Name != request.ExpectedName {
			return BrowserHostObservation{}, ErrBrowserHostStale
		}
		if action == "drag" {
			destinationElement, found = session.elementRefs[request.Action.DestinationRef]
			if !found || destinationElement.Target == boundElement.Target ||
				destinationElement.Role != request.DestinationExpectedRole ||
				destinationElement.Name != request.DestinationExpectedName {
				return BrowserHostObservation{}, ErrBrowserHostStale
			}
		}
	}
	if existingHash, reserved := session.actionInvocations[request.ActionInvocationID]; reserved {
		if existingHash != request.PreparedActionHash {
			return BrowserHostObservation{}, ErrBrowserHostDenied
		}
		return BrowserHostObservation{}, ErrBrowserHostStale
	}
	if ctx.Err() != nil {
		return BrowserHostObservation{}, ctx.Err()
	}
	actionCtx, cancelAction, actionDeadline := host.actionContextLocked(ctx, session)
	current, currentNavigationIdentity, observeErr := observeBrowserHostNavigation(actionCtx, session)
	currentDigest := browserHostObservationDigest(session, current, currentNavigationIdentity)
	if observeErr != nil || actionCtx.Err() != nil || current.Origin != request.CurrentOrigin ||
		len(session.observationDigest) != sha256.Size || !hmac.Equal(currentDigest, session.observationDigest) {
		actionContextErr := actionCtx.Err()
		cancelAction()
		if errors.Is(observeErr, browserworker.ErrStale) {
			return BrowserHostObservation{}, ErrBrowserHostStale
		}
		if observeErr != nil || actionContextErr != nil {
			return BrowserHostObservation{}, ErrBrowserHostLost
		}
		return BrowserHostObservation{}, ErrBrowserHostStale
	}
	if action == "dialog" {
		if current.PendingDialog == nil || current.PendingDialog.Type != request.DialogType ||
			len(current.PendingDialog.Message) != request.DialogMessageBytes ||
			!nodes.BrowserDialogMessageDigestMatches(
				request.DialogMessageDigest,
				current.PendingDialog.Type,
				current.PendingDialog.Message,
			) {
			cancelAction()
			return BrowserHostObservation{}, ErrBrowserHostStale
		}
	}
	if action == "click" || action == "fill" || action == "select" || action == "check" ||
		action == "uncheck" || action == "hover" || action == "drag" || action == "file_chooser" {
		targets, matches, destinationTargets, destinationMatches := 0, 0, 0, 0
		for _, element := range current.Elements {
			if element.Target == boundElement.Target {
				targets++
				if element.Role == request.ExpectedRole && element.Name == request.ExpectedName {
					matches++
				}
			}
			if action == "drag" && element.Target == destinationElement.Target {
				destinationTargets++
				if element.Role == request.DestinationExpectedRole && element.Name == request.DestinationExpectedName {
					destinationMatches++
				}
			}
		}
		if targets != 1 || matches != 1 ||
			(action == "drag" && (destinationTargets != 1 || destinationMatches != 1)) {
			cancelAction()
			return BrowserHostObservation{}, ErrBrowserHostStale
		}
		driverAction.Target = boundElement.Target
		driverAction.Element = boundElement.Name
		if action == "drag" {
			driverAction.DestinationTarget = destinationElement.Target
			driverAction.DestinationElement = destinationElement.Name
		}
	}
	if action == "fill" {
		if session.fillWorker == nil {
			cancelAction()
			return BrowserHostObservation{}, ErrBrowserHostLost
		}
		if authorizeErr := session.fillWorker.AuthorizeFill(
			actionCtx,
			currentNavigationIdentity,
			boundElement.Target,
		); authorizeErr != nil {
			cancelAction()
			switch {
			case errors.Is(authorizeErr, browserworker.ErrDenied):
				return BrowserHostObservation{}, ErrBrowserHostDenied
			case errors.Is(authorizeErr, browserworker.ErrStale):
				return BrowserHostObservation{}, ErrBrowserHostStale
			default:
				return BrowserHostObservation{}, ErrBrowserHostLost
			}
		}
	}
	// Bind the gateway invocation immediately before driver dispatch. Once
	// reserved, it can never execute again even if the outcome is ambiguous.
	session.actionInvocations[request.ActionInvocationID] = request.PreparedActionHash
	var executeErr error
	if action == "file_chooser" {
		uploadWorker, ok := session.worker.(browserHostUploadWorker)
		if !ok {
			executeErr = browserworker.ErrDriverIncompatible
		} else {
			executeErr = uploadWorker.UploadAfterNavigationCheck(
				actionCtx,
				currentNavigationIdentity,
				driverAction,
			)
		}
	} else {
		executeErr = session.navigationWorker.ExecuteAfterNavigationCheck(
			actionCtx,
			currentNavigationIdentity,
			driverAction,
		)
	}
	if executeErr != nil {
		cancelAction()
		if errors.Is(executeErr, browserworker.ErrStale) {
			return BrowserHostObservation{}, ErrBrowserHostStale
		}
		if errors.Is(executeErr, browserworker.ErrDenied) {
			delete(session.actionInvocations, request.ActionInvocationID)
			return BrowserHostObservation{}, ErrBrowserHostDenied
		}
		host.quarantineActionLocked(session)
		return BrowserHostObservation{}, ErrBrowserHostLost
	}
	if actionCtx.Err() != nil || !host.now().UTC().Before(actionDeadline) {
		cancelAction()
		host.quarantineActionLocked(session)
		return BrowserHostObservation{}, ErrBrowserHostLost
	}
	observation, navigationIdentity, observeErr := observeBrowserHostNavigation(actionCtx, session)
	actionContextErr := actionCtx.Err()
	cancelAction()
	if observeErr != nil || actionContextErr != nil || !host.now().UTC().Before(actionDeadline) {
		host.quarantineActionLocked(session)
		return BrowserHostObservation{}, ErrBrowserHostLost
	}
	session.snapshotGeneration++
	session.idleExpiresAt = host.now().UTC().Add(time.Duration(session.limits.IdleSeconds) * time.Second)
	return browserHostObservation(request.SessionID, session, observation, navigationIdentity), nil
}

func observeBrowserHostNavigation(
	ctx context.Context,
	session *browserHostSession,
) (browserworker.DriverObservation, string, error) {
	if session.navigationWorker == nil {
		return browserworker.DriverObservation{}, "", browserworker.ErrDriverIncompatible
	}
	before, err := session.navigationWorker.NavigationIdentity(ctx)
	if err != nil {
		return browserworker.DriverObservation{}, "", err
	}
	var observation browserworker.DriverObservation
	if session.contextCatalog != nil && session.contextCatalog.SelectedFrameID != "" &&
		session.contextWorker != nil {
		authority, authorityErr := browserworker.ContextMutationAuthorityFromBinding(
			browserworker.ContextMutationBinding{
				Catalog: *session.contextCatalog,
				TabID:   session.contextCatalog.SelectedTabID,
				FrameID: session.contextCatalog.SelectedFrameID,
			},
		)
		if authorityErr != nil {
			return browserworker.DriverObservation{}, "", authorityErr
		}
		var live browserworker.ContextCatalog
		observation, live, err = session.contextWorker.SelectContext(ctx, authority)
		if err == nil {
			var canonicalLive browserworker.ContextCatalog
			canonicalLive, err = browserContextCatalogValue(browserContextCatalogResult(live))
			if err == nil && !reflect.DeepEqual(*session.contextCatalog, canonicalLive) {
				err = browserworker.ErrStale
			}
		}
	} else {
		observation, err = session.worker.Observe(ctx)
	}
	if err != nil {
		return browserworker.DriverObservation{}, "", err
	}
	after, err := session.navigationWorker.NavigationIdentity(ctx)
	if err != nil {
		return browserworker.DriverObservation{}, "", err
	}
	if before == "" || before != after {
		return browserworker.DriverObservation{}, "", browserworker.ErrStale
	}
	return observation, after, nil
}

func stableBrowserHostNavigationIdentity(
	ctx context.Context,
	session *browserHostSession,
) (string, error) {
	if session.navigationWorker == nil {
		return "", browserworker.ErrDriverIncompatible
	}
	before, err := session.navigationWorker.NavigationIdentity(ctx)
	if err != nil {
		return "", err
	}
	after, err := session.navigationWorker.NavigationIdentity(ctx)
	if err != nil {
		return "", err
	}
	if before == "" || before != after {
		return "", browserworker.ErrStale
	}
	return after, nil
}

func browserHostActInput(request BrowserHostNavigateRequest) nodes.BrowserActInput {
	action := request.Action
	action.Value = ""
	return nodes.BrowserActInput{
		SessionID: request.SessionID, TabID: request.TabID,
		SnapshotGeneration: request.SnapshotGeneration,
		ActionInvocationID: request.ActionInvocationID, Action: action,
		Effect: request.Effect, CurrentOrigin: request.CurrentOrigin,
		PreparedActionHash:    request.PreparedActionHash,
		BrowserPolicyRevision: request.BrowserPolicyRevision,
		ProfileRevision:       request.ProfileRevision,
		ExpectedRole:          request.ExpectedRole, ExpectedName: request.ExpectedName,
		DestinationExpectedRole: request.DestinationExpectedRole,
		DestinationExpectedName: request.DestinationExpectedName,
		DialogType:              request.DialogType, DialogMessageDigest: request.DialogMessageDigest,
		DialogMessageBytes: request.DialogMessageBytes,
		InputDigest:        request.InputDigest, InputBytes: request.InputBytes,
		ArtifactSHA256: request.ArtifactSHA256, ArtifactBytes: request.ArtifactBytes,
		ArtifactFilename: request.ArtifactFilename, ArtifactContentType: request.ArtifactContentType,
		ApprovalDigest: request.ApprovalDigest,
	}
}

func (host *BrowserHost) actionContextLocked(
	ctx context.Context,
	session *browserHostSession,
) (context.Context, context.CancelFunc, time.Time) {
	now := host.now().UTC()
	deadline := now.Add(time.Duration(session.limits.ActionSeconds) * time.Second)
	if session.expiresAt.Before(deadline) {
		deadline = session.expiresAt
	}
	if session.idleExpiresAt.Before(deadline) {
		deadline = session.idleExpiresAt
	}
	actionCtx, cancelAction := context.WithTimeout(ctx, max(deadline.Sub(now), 0))
	return actionCtx, cancelAction, deadline
}

func (host *BrowserHost) quarantineActionLocked(session *browserHostSession) {
	session.state = "lost"
	session.safeFailure = "outcome_unknown"
	session.elementRefs = make(map[string]browserworker.DriverElement)
	session.observationDigest = nil
}

func (host *BrowserHost) Close(
	ctx context.Context,
	request BrowserHostCloseRequest,
) (BrowserHostSession, error) {
	session, err := host.authorizedSession(request)
	if err != nil {
		return BrowserHostSession{}, err
	}
	defer host.cleanupBrowserArtifactsForSession(request.SessionID)
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state == "closed" {
		return browserHostSessionView(session), nil
	}
	if session.worker == nil {
		if session.cleanupOwner != nil {
			if closeErr := session.cleanupOwner.Close(ctx); closeErr != nil {
				session.state = "lost"
				session.safeFailure = "cleanup_required"
				return browserHostSessionView(session), ErrBrowserHostLost
			}
			session.cleanupOwner = nil
		}
		session.state = "closed"
		session.safeFailure = ""
		session.contextWorker = nil
		session.contextCatalog = nil
		session.elementRefs = make(map[string]browserworker.DriverElement)
		return browserHostSessionView(session), nil
	}
	session.state = "closing"
	if closeErr := session.worker.Close(ctx); closeErr != nil {
		session.state = "lost"
		session.safeFailure = "cleanup_required"
		return browserHostSessionView(session), ErrBrowserHostLost
	}
	session.worker = nil
	session.contextWorker = nil
	session.contextCatalog = nil
	session.state = "closed"
	session.safeFailure = ""
	session.elementRefs = make(map[string]browserworker.DriverElement)
	return browserHostSessionView(session), nil
}

func closeBrowserHostOwner(ctx context.Context, owner browserworker.Worker) error {
	if owner == nil {
		return nil
	}
	cleanupContext, cancelCleanup := context.WithTimeout(
		context.WithoutCancel(ctx),
		browserHostCleanupTimeout,
	)
	defer cancelCleanup()
	return owner.Close(cleanupContext)
}

func (host *BrowserHost) Shutdown(ctx context.Context) error {
	if host == nil {
		return nil
	}
	host.mu.Lock()
	sessions := make(map[string]*browserHostSession, len(host.sessions))
	for id, session := range host.sessions {
		sessions[id] = session
	}
	host.mu.Unlock()
	var shutdownErr error
	for id, session := range sessions {
		session.mu.Lock()
		request := BrowserHostCloseRequest{
			SessionID: id, ProfileRevision: session.profile.Revision,
			RoutedSessionID: session.routedSessionID,
			AgentID:         session.agentID, ActorID: session.actorID,
		}
		session.mu.Unlock()
		if _, err := host.Close(ctx, request); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}
	host.cleanupAllBrowserArtifacts()
	return shutdownErr
}

// expireSessionLocked applies both node-local absolute and idle ceilings. It
// never starts replacement work; an unsuccessful close requires operator
// cleanup and leaves the profile unavailable.
func (host *BrowserHost) expireSessionLocked(
	ctx context.Context,
	session *browserHostSession,
) bool {
	now := host.now().UTC()
	if now.Before(session.expiresAt) && now.Before(session.idleExpiresAt) {
		return false
	}
	session.state = "closing"
	if session.worker != nil {
		if err := session.worker.Close(ctx); err != nil {
			session.state = "lost"
			session.safeFailure = "cleanup_required"
			return true
		}
		session.worker = nil
		session.contextWorker = nil
		session.contextCatalog = nil
	}
	session.state = "closed"
	session.safeFailure = "session_expired"
	session.elementRefs = make(map[string]browserworker.DriverElement)
	host.cleanupBrowserArtifactsForSession(session.sessionID)
	return true
}

func (host *BrowserHost) authorizedSession(
	request BrowserHostStatusRequest,
) (*browserHostSession, error) {
	if host == nil || !browserHostIdentifier(request.SessionID) {
		return nil, ErrBrowserHostDenied
	}
	host.mu.Lock()
	session := host.sessions[request.SessionID]
	host.mu.Unlock()
	if session == nil {
		return nil, ErrBrowserHostNotFound
	}
	session.mu.Lock()
	authorized := request.ProfileRevision == session.profile.Revision &&
		request.RoutedSessionID == session.routedSessionID &&
		request.AgentID == session.agentID && request.ActorID == session.actorID &&
		authorizeBrowserProfile(session.profile, request.ProfileRevision, request.AgentID, request.ActorID)
	session.mu.Unlock()
	if !authorized {
		return nil, ErrBrowserHostDenied
	}
	return session, nil
}

func (host *BrowserHost) sessionProfileRevision(sessionID string) string {
	if host == nil {
		return ""
	}
	host.mu.Lock()
	session := host.sessions[sessionID]
	host.mu.Unlock()
	if session == nil {
		return ""
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.profile.Revision
}

func (host *BrowserHost) sessionView(session *browserHostSession) BrowserHostSession {
	session.mu.Lock()
	defer session.mu.Unlock()
	return browserHostSessionView(session)
}

func browserHostSessionView(session *browserHostSession) BrowserHostSession {
	recovery := ""
	if session.state == "lost" {
		recovery = "close"
		if session.safeFailure == "cleanup_required" {
			recovery = "operator"
		}
	}
	return BrowserHostSession{
		SessionID: session.sessionID,
		State:     session.state, Reason: session.safeFailure, Recovery: recovery,
		TabID: session.tabID, Controller: "agent",
		Features: BrowserHostFeatures{
			Observe:    true,
			Navigate:   slices.Contains(session.profile.AllowedActions, "navigate"),
			Contexts:   session.contextWorker != nil,
			Screenshot: false, Download: false,
		},
		ExpiresAt: session.expiresAt.Unix(), IdleExpiresAt: session.idleExpiresAt.Unix(),
	}
}

func browserHostObservation(
	sessionID string,
	session *browserHostSession,
	observation browserworker.DriverObservation,
	navigationIdentity string,
) BrowserHostObservation {
	session.observationDigest = browserHostObservationDigest(session, observation, navigationIdentity)
	elements := make([]BrowserHostElement, len(observation.Elements))
	snapshot := observation.Snapshot
	session.elementRefs = make(map[string]browserworker.DriverElement, len(observation.Elements))
	for index, element := range observation.Elements {
		ref := browserHostElementRef(session, index, element)
		session.elementRefs[ref] = element
		elements[index] = BrowserHostElement{
			Ref: ref, Role: element.Role, Name: element.Name,
		}
		snapshot = strings.ReplaceAll(snapshot, "[ref="+element.Target+"]", "[ref="+ref+"]")
	}
	return BrowserHostObservation{
		SessionID: sessionID, TabID: session.tabID,
		SnapshotGeneration: session.snapshotGeneration,
		URL:                observation.URL, Origin: observation.Origin, Title: observation.Title,
		Snapshot: snapshot, Elements: elements, PendingDialog: browserHostDialogObservation(observation.PendingDialog),
		Truncated: observation.Truncated,
	}
}

func browserHostDialogObservation(dialog *browserworker.DialogObservation) *nodes.BrowserDialogObservation {
	if dialog == nil {
		return nil
	}
	return &nodes.BrowserDialogObservation{Type: dialog.Type, Message: dialog.Message}
}

func browserHostObservationDigest(
	session *browserHostSession,
	observation browserworker.DriverObservation,
	navigationIdentity string,
) []byte {
	hash := hmac.New(sha256.New, session.elementBindingKey)
	_, _ = fmt.Fprintf(
		hash,
		"mintclaw.browser.host-observation.v2\x00%d:%s%d:%s%d:%s%d:%s%d:%s:%t:%d",
		len(navigationIdentity), navigationIdentity,
		len(observation.URL), observation.URL,
		len(observation.Origin), observation.Origin,
		len(observation.Title), observation.Title,
		len(observation.Snapshot), observation.Snapshot,
		observation.Truncated,
		len(observation.Elements),
	)
	for _, element := range observation.Elements {
		_, _ = fmt.Fprintf(
			hash,
			"%d:%s%d:%s%d:%s",
			len(element.Target), element.Target,
			len(element.Role), element.Role,
			len(element.Name), element.Name,
		)
	}
	if observation.PendingDialog == nil {
		_, _ = hash.Write([]byte("dialog:nil"))
	} else {
		_, _ = fmt.Fprintf(
			hash,
			"dialog:%d:%s%d:%s",
			len(observation.PendingDialog.Type), observation.PendingDialog.Type,
			len(observation.PendingDialog.Message), observation.PendingDialog.Message,
		)
	}
	return hash.Sum(nil)
}

func browserHostElementRef(
	session *browserHostSession,
	index int,
	element browserworker.DriverElement,
) string {
	hash := hmac.New(sha256.New, session.elementBindingKey)
	_, _ = fmt.Fprintf(
		hash,
		"mintclaw.browser.host-ref.v1\x00%d:%d:%d:%s%d:%s%d:%s",
		session.snapshotGeneration,
		index,
		len(element.Target),
		element.Target,
		len(element.Role),
		element.Role,
		len(element.Name),
		element.Name,
	)
	return "href_" + hex.EncodeToString(hash.Sum(nil))
}

func authorizeBrowserProfile(
	profile companion.BrowserProfilePolicy,
	revision, agentID, actorID string,
) bool {
	return profile.Enabled && profile.Revision == revision &&
		slices.Contains(profile.AllowedAgents, agentID) &&
		slices.Contains(profile.AllowedActors, actorID)
}

func browserLimitsWithin(requested, ceiling nodes.BrowserLimits) bool {
	return requested.Sessions == 1 && requested.Sessions <= ceiling.Sessions &&
		requested.Tabs <= ceiling.Tabs && requested.SessionSeconds <= ceiling.SessionSeconds &&
		requested.IdleSeconds <= ceiling.IdleSeconds &&
		requested.PreparedSeconds <= ceiling.PreparedSeconds &&
		requested.ActionSeconds <= ceiling.ActionSeconds &&
		requested.SnapshotBytes <= ceiling.SnapshotBytes &&
		requested.ScreenshotBytes <= ceiling.ScreenshotBytes &&
		requested.UploadBytes <= ceiling.UploadBytes &&
		requested.DownloadBytes <= ceiling.DownloadBytes &&
		requested.SnapshotRefs <= ceiling.SnapshotRefs &&
		requested.TextInputBytes <= ceiling.TextInputBytes &&
		requested.ToolResultBytes <= ceiling.ToolResultBytes &&
		requested.RetentionSecs <= ceiling.RetentionSecs
}

func browserConfigLimits(limits nodes.BrowserLimits) config.BrowserLimitsConfig {
	return config.BrowserLimitsConfig{
		Sessions: limits.Sessions, Tabs: limits.Tabs,
		SessionSeconds: limits.SessionSeconds, IdleSeconds: limits.IdleSeconds,
		PreparedSeconds: limits.PreparedSeconds, ActionSeconds: limits.ActionSeconds,
		SnapshotBytes: limits.SnapshotBytes, ScreenshotBytes: limits.ScreenshotBytes,
		UploadBytes: limits.UploadBytes, DownloadBytes: limits.DownloadBytes,
		SnapshotRefs: limits.SnapshotRefs, TextInputBytes: limits.TextInputBytes,
		ToolResultBytes: limits.ToolResultBytes, RetentionSecs: limits.RetentionSecs,
	}
}

func browserHostIdentifier(value string) bool {
	return browserHostIDPattern.MatchString(value)
}

func browserHostDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
