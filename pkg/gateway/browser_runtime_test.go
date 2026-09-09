package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

type attachedOnlyGatewayDiagnosticsFactory struct{}

func (*attachedOnlyGatewayDiagnosticsFactory) Open(
	context.Context,
	browser.WorkerOpenRequest,
) (browser.WorkerOpenResult, error) {
	return browser.WorkerOpenResult{}, browser.ErrWorkerUnavailable
}

func (*attachedOnlyGatewayDiagnosticsFactory) PassiveTargetDiagnostics(
	_ context.Context,
	_ string,
	profiles []string,
) (browser.TargetDiagnostics, error) {
	result := browser.TargetDiagnostics{
		Actions:  []browser.ActionKind{browser.ActionNavigate, browser.ActionClick},
		Profiles: make(map[string]browser.DriverReadiness, len(profiles)),
	}
	for _, profile := range profiles {
		result.Profiles[profile] = browser.DriverReadiness{
			Status: browser.ReadinessReady, Driver: browser.ReadinessReady,
			Browser: browser.ReadinessReady, Proxy: browser.ReadinessReady,
			Compatibility: browser.CompatibilityCompatible,
		}
	}
	return result, nil
}

func TestBrowserRuntimeDisabledDoesNotOwnState(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	runtime, err := newBrowserRuntime(context.Background(), cfg)
	if err != nil || runtime != nil {
		t.Fatalf("newBrowserRuntime() = %+v, %v; want disabled", runtime, err)
	}
	services := &services{}
	if err = setupBrowserRuntime(context.Background(), cfg, services); err != nil || services.Browser != nil {
		t.Fatalf("setupBrowserRuntime() error = %v, runtime = %+v", err, services.Browser)
	}
}

func TestGatewayAttachedOnlyDiscoveryDoesNotAdvertiseUnsupportedDownload(t *testing.T) {
	cfg := gatewayBrowserConfig(t.TempDir())
	target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
	target.Profiles = map[string]config.BrowserProfileConfig{
		"chrome": {
			Enabled: true, Revision: "chrome-v1", Mode: config.BrowserProfileAttachedUser,
			AllowedAgents: []string{"browser"}, AllowedActors: []string{"telegram:owner"},
			NetworkMode: config.BrowserNetworkAnyHTTP, CapabilityMode: config.BrowserCapabilityFullAccess,
			ApprovalMode: config.BrowserApprovalModelRequested, AllowApprovedActions: true,
			Attached: config.BrowserAttachedConfig{
				Connector:   config.BrowserAttachedPlaywright,
				ConsentMode: config.BrowserAttachedConsentSession, ConsentSeconds: 300,
				ActionOriginMode: config.BrowserAttachedOriginExact,
				AllowedOrigins:   []string{"https://example.com"},
			},
		},
	}
	cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
	broker, err := browser.NewBroker(
		cfg,
		browser.NewMemoryStore(),
		&attachedOnlyGatewayDiagnosticsFactory{},
	)
	if err != nil {
		t.Fatal(err)
	}
	policyRevision, err := cfg.Tools.Browser.PolicyRevision()
	if err != nil {
		t.Fatal(err)
	}
	source := &gatewayBrowserToolSource{
		services: &services{Browser: &browserRuntime{
			broker: broker, policyRevision: policyRevision,
		}},
		policyRevision: policyRevision, downloadAvailable: true,
	}
	diagnostics, err := source.PassiveTargetDiagnostics(
		t.Context(), config.BrowserDefaultTarget, []string{"chrome"},
	)
	if err != nil || diagnostics.Download ||
		slices.Contains(diagnostics.Actions, browser.ActionDownload) {
		t.Fatalf("attached-only discovery = %#v, %v", diagnostics, err)
	}
}

func TestGatewayBrowserWorkerFactoryBuildsOneFactoryPerManagedAlias(t *testing.T) {
	root := t.TempDir()
	cfg := gatewayBrowserConfig(root)
	runtimeRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
	personal := target.Profiles[config.BrowserDefaultProfile]
	personal.Revision = "personal-v1"
	personal.Runtime.ProfileDirectory = filepath.Join(runtimeRoot, "browser-personal")
	personal.Runtime.LockFile = filepath.Join(runtimeRoot, "browser-locks", "personal.lock")
	if err := os.Mkdir(personal.Runtime.ProfileDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	target.Profiles["personal"] = personal
	cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target

	rawFactory, err := newGatewayBrowserWorkerFactory(cfg, nil)
	if err != nil {
		t.Fatalf("newGatewayBrowserWorkerFactory() error = %v", err)
	}
	factory, ok := rawFactory.(*gatewayBrowserWorkerFactory)
	if !ok {
		t.Fatalf("worker factory type = %T", rawFactory)
	}
	managed := factory.local[gatewayBrowserProfileKey(config.BrowserDefaultTarget, config.BrowserDefaultProfile)]
	personalFactory := factory.local[gatewayBrowserProfileKey(config.BrowserDefaultTarget, "personal")]
	if len(factory.local) != 2 || managed == nil || personalFactory == nil || managed == personalFactory {
		t.Fatalf("local factories = %#v", factory.local)
	}
}

func TestBrowserRuntimeOwnsAndReleasesDurableStore(t *testing.T) {
	root := t.TempDir()
	cfg := gatewayBrowserConfig(root)
	runtime, err := newBrowserRuntime(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newBrowserRuntime() error = %v", err)
	}
	if runtime.Broker() == nil {
		t.Fatal("newBrowserRuntime() broker = nil")
	}
	if _, secondErr := newBrowserRuntime(context.Background(), cfg); !errors.Is(secondErr, browser.ErrStoreOwned) {
		t.Fatalf("second newBrowserRuntime() error = %v, want ErrStoreOwned", secondErr)
	}
	services := &services{Browser: runtime}
	if err = closeBrowserRuntime(context.Background(), services); err != nil {
		t.Fatalf("closeBrowserRuntime() error = %v", err)
	}
	if services.Browser != nil {
		t.Fatal("closeBrowserRuntime() retained closed runtime")
	}
	if err = runtime.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	reopened, err := newBrowserRuntime(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newBrowserRuntime() after Close error = %v", err)
	}
	if err = reopened.Close(context.Background()); err != nil {
		t.Fatalf("reopened Close() error = %v", err)
	}
}

func TestBrowserRuntimeRetainsOwnershipUntilWorkerShutdownSucceeds(t *testing.T) {
	root := t.TempDir()
	cfg := gatewayBrowserConfig(root)
	storePath := filepath.Join(root, "state", "browser", browserStateFile)
	store, err := browser.NewFileStore(storePath, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	worker := &gatewayTestBrowserWorker{closeErr: errors.New("still running")}
	broker, err := browser.NewBroker(cfg, store, &gatewayTestBrowserFactory{worker: worker})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	owner := browser.Owner{
		ActorID: browser.OpaqueActorID("actor_1"), AgentID: browser.OpaqueAgentID("browser"),
		SessionKey: "session_1", ExecutionID: "execution_1",
	}
	if _, err = broker.Open(context.Background(), browser.OpenRequest{
		Owner: owner, Target: config.BrowserDefaultTarget, Profile: config.BrowserDefaultProfile,
	}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	runtime := &browserRuntime{broker: broker, store: store}
	services := &services{Browser: runtime}
	if err = closeBrowserRuntime(context.Background(), services); err == nil || services.Browser != runtime {
		t.Fatalf("first closeBrowserRuntime() error = %v, runtime = %+v", err, services.Browser)
	}
	if _, openErr := browser.NewFileStore(storePath, 0, 0); !errors.Is(openErr, browser.ErrStoreOwned) {
		t.Fatalf("store after failed shutdown error = %v, want ErrStoreOwned", openErr)
	}
	disabled := config.DefaultConfig()
	disabled.Agents.Defaults.Workspace = root
	if replaceErr := setupBrowserRuntime(context.Background(), disabled, services); replaceErr == nil ||
		services.Browser != runtime {
		t.Fatalf("disabled replacement error = %v, runtime = %+v", replaceErr, services.Browser)
	}
	otherWorkspace := gatewayBrowserConfig(t.TempDir())
	if replaceErr := setupBrowserRuntime(context.Background(), otherWorkspace, services); replaceErr == nil ||
		services.Browser != runtime {
		t.Fatalf("workspace replacement error = %v, runtime = %+v", replaceErr, services.Browser)
	}
	worker.closeErr = nil
	if err = closeBrowserRuntime(context.Background(), services); err != nil || services.Browser != nil {
		t.Fatalf("retry closeBrowserRuntime() error = %v, runtime = %+v", err, services.Browser)
	}
	if closeCalls := worker.closeCalls.Load(); closeCalls != 2 {
		t.Fatalf("worker close calls = %d, want 2", closeCalls)
	}
	reopened, err := browser.NewFileStore(storePath, 0, 0)
	if err != nil {
		t.Fatalf("reopen store after successful retry error = %v", err)
	}
	reopened.Close()
}

type gatewayManagedRevocationTestCase struct {
	name          string
	mutate        func(*config.Config)
	runtimeWanted bool
}

func gatewayManagedRevocationTestCases(root string) []gatewayManagedRevocationTestCase {
	return []gatewayManagedRevocationTestCase{
		{
			name: "profile disabled",
			mutate: func(cfg *config.Config) {
				cfg.Tools.Browser = config.BrowserToolsConfig{}
			},
		},
		{
			name: "profile revision changed", runtimeWanted: true,
			mutate: func(cfg *config.Config) {
				target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
				profile := target.Profiles[config.BrowserDefaultProfile]
				profile.Revision = "managed-v2"
				target.Profiles[config.BrowserDefaultProfile] = profile
				cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
			},
		},
		{
			name: "actor grant removed", runtimeWanted: true,
			mutate: func(cfg *config.Config) {
				target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
				profile := target.Profiles[config.BrowserDefaultProfile]
				profile.AllowedActors = []string{"actor_2"}
				target.Profiles[config.BrowserDefaultProfile] = profile
				cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
			},
		},
		{
			name: "agent grant removed", runtimeWanted: true,
			mutate: func(cfg *config.Config) {
				cfg.Tools.Browser.Agents = []string{"replacement"}
				target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
				profile := target.Profiles[config.BrowserDefaultProfile]
				profile.AllowedAgents = []string{"replacement"}
				target.Profiles[config.BrowserDefaultProfile] = profile
				cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
			},
		},
		{
			name: "runtime mapping changed", runtimeWanted: true,
			mutate: func(cfg *config.Config) {
				profileDirectory := filepath.Join(root, "browser-profile-v2")
				target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
				profile := target.Profiles[config.BrowserDefaultProfile]
				profile.Runtime.ProfileDirectory = profileDirectory
				profile.Runtime.LockFile = filepath.Join(root, "browser-locks", "managed-v2.lock")
				target.Profiles[config.BrowserDefaultProfile] = profile
				cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
			},
		},
	}
}

func TestGatewayBrowserGenerationReplacementRevokesManagedAuthority(t *testing.T) {
	for _, test := range gatewayManagedRevocationTestCases("") {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			runtimeRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Mkdir(filepath.Join(runtimeRoot, "browser-profile-v2"), 0o700); err != nil {
				t.Fatal(err)
			}
			current := gatewayBrowserConfig(root)
			store, err := browser.NewFileStore(filepath.Join(runtimeRoot, "state", "browser", browserStateFile), 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			worker := &gatewayTestBrowserWorker{}
			broker, err := browser.NewBroker(current, store, &gatewayTestBrowserFactory{worker: worker})
			if err != nil {
				store.Close()
				t.Fatal(err)
			}
			if _, err = broker.Open(t.Context(), browser.OpenRequest{
				Owner: browser.Owner{
					ActorID: browser.OpaqueActorID("actor_1"), AgentID: browser.OpaqueAgentID("browser"),
					SessionKey: "session_1", ExecutionID: "execution_1",
				},
				Target: config.BrowserDefaultTarget, Profile: config.BrowserDefaultProfile,
			}); err != nil {
				store.Close()
				t.Fatal(err)
			}
			services := &services{Browser: &browserRuntime{broker: broker, store: store}}
			replacement := gatewayBrowserConfig(root)
			for _, candidate := range gatewayManagedRevocationTestCases(runtimeRoot) {
				if candidate.name == test.name {
					candidate.mutate(replacement)
					break
				}
			}
			if replacement.Tools.Browser.Enabled {
				if _, factoryErr := newGatewayBrowserWorkerFactory(replacement, nil); factoryErr != nil {
					t.Fatalf("prepare replacement worker factory: %v", factoryErr)
				}
			}

			if err = closeBrowserRuntime(t.Context(), services); err != nil {
				t.Fatalf("close retired generation: %v", err)
			}
			if worker.closeCalls.Load() != 1 || services.Browser != nil {
				t.Fatalf(
					"retired generation close calls = %d, runtime = %+v",
					worker.closeCalls.Load(),
					services.Browser,
				)
			}
			if err = setupBrowserRuntime(t.Context(), replacement, services); err != nil {
				t.Fatalf("publish replacement generation: %v", err)
			}
			if (services.Browser != nil) != test.runtimeWanted {
				t.Fatalf("replacement runtime = %+v, wanted = %v", services.Browser, test.runtimeWanted)
			}
			if err = closeBrowserRuntime(t.Context(), services); err != nil {
				t.Fatalf("close replacement generation: %v", err)
			}
		})
	}
}

func TestGatewayBrowserGenerationCleanupFailureBlocksManagedReplacement(t *testing.T) {
	for _, test := range gatewayManagedRevocationTestCases("") {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			runtimeRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			current := gatewayBrowserConfig(root)
			worker := &gatewayTestBrowserWorker{closeErr: errors.New("cleanup outcome unknown")}
			broker, err := browser.NewBroker(
				current, browser.NewMemoryStore(), &gatewayTestBrowserFactory{worker: worker},
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = broker.Open(t.Context(), browser.OpenRequest{
				Owner: browser.Owner{
					ActorID: browser.OpaqueActorID("actor_1"), AgentID: browser.OpaqueAgentID("browser"),
					SessionKey: "session_1", ExecutionID: "execution_1",
				},
				Target: config.BrowserDefaultTarget, Profile: config.BrowserDefaultProfile,
			}); err != nil {
				t.Fatal(err)
			}
			runtime := &browserRuntime{broker: broker}
			services := &services{Browser: runtime}
			replacement := gatewayBrowserConfig(root)
			for _, candidate := range gatewayManagedRevocationTestCases(runtimeRoot) {
				if candidate.name == test.name {
					candidate.mutate(replacement)
					break
				}
			}

			if err = closeBrowserRuntime(t.Context(), services); err == nil || services.Browser != runtime {
				t.Fatalf("failed retirement error = %v, runtime = %+v", err, services.Browser)
			}
			if err = setupBrowserRuntime(
				t.Context(),
				replacement,
				services,
			); err == nil ||
				services.Browser != runtime {
				t.Fatalf("replacement during quarantine error = %v, runtime = %+v", err, services.Browser)
			}
			worker.closeErr = nil
			if err = closeBrowserRuntime(t.Context(), services); err != nil || services.Browser != nil {
				t.Fatalf("retirement retry error = %v, runtime = %+v", err, services.Browser)
			}
		})
	}
}

func TestServiceShutdownReportsBrowserCleanupFailure(t *testing.T) {
	workerErr := errors.New("browser worker still running")
	worker := &gatewayTestBrowserWorker{closeErr: workerErr}
	cfg := gatewayBrowserConfig(t.TempDir())
	broker, err := browser.NewBroker(
		cfg,
		browser.NewMemoryStore(),
		&gatewayTestBrowserFactory{worker: worker},
	)
	if err != nil {
		t.Fatalf("NewBroker() error = %v", err)
	}
	if _, err = broker.Open(context.Background(), browser.OpenRequest{
		Owner: browser.Owner{
			ActorID: browser.OpaqueActorID("actor_1"), AgentID: browser.OpaqueAgentID("browser"),
			SessionKey: "session_1", ExecutionID: "execution_1",
		},
		Target: config.BrowserDefaultTarget, Profile: config.BrowserDefaultProfile,
	}); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	runtime := &browserRuntime{broker: broker}
	services := &services{Browser: runtime}

	err = stopAndCleanupServices(services, time.Second, false)
	if err == nil {
		t.Fatal("stopAndCleanupServices() error = nil, want browser cleanup failure")
	}
	if services.Browser != runtime {
		t.Fatal("failed browser cleanup released runtime ownership")
	}
	worker.closeErr = nil
	if err = closeBrowserRuntime(context.Background(), services); err != nil {
		t.Fatalf("closeBrowserRuntime() retry error = %v", err)
	}
}

func TestBrowserRuntimeCloseHonorsCallerDeadlineAndRetainsOwnership(t *testing.T) {
	root := t.TempDir()
	cfg := gatewayBrowserConfig(root)
	storePath := filepath.Join(root, "state", "browser", browserStateFile)
	store, err := browser.NewFileStore(storePath, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	worker := &gatewayTestBrowserWorker{waitForContextCalls: 1}
	broker, err := browser.NewBroker(cfg, store, &gatewayTestBrowserFactory{worker: worker})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	owner := browser.Owner{
		ActorID: browser.OpaqueActorID("actor_1"), AgentID: browser.OpaqueAgentID("browser"),
		SessionKey: "session_1", ExecutionID: "execution_1",
	}
	if _, err = broker.Open(context.Background(), browser.OpenRequest{
		Owner: owner, Target: config.BrowserDefaultTarget, Profile: config.BrowserDefaultProfile,
	}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	runtime := &browserRuntime{broker: broker, store: store}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	started := time.Now()
	err = runtime.Close(ctx)
	cancel()
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("deadline Close() error = %v, elapsed = %v", err, time.Since(started))
	}
	if _, openErr := browser.NewFileStore(storePath, 0, 0); !errors.Is(openErr, browser.ErrStoreOwned) {
		t.Fatalf("store after deadline error = %v, want ErrStoreOwned", openErr)
	}
	if err = runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close() retry error = %v", err)
	}
	reopened, err := browser.NewFileStore(storePath, 0, 0)
	if err != nil {
		t.Fatalf("reopen store after retry error = %v", err)
	}
	reopened.Close()
}

func TestBrowserRuntimeCloseDeadlineBoundsActiveToolLeaseWait(t *testing.T) {
	services := &services{Browser: &browserRuntime{}}
	services.browserMu.RLock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	started := time.Now()
	err := closeBrowserRuntime(ctx, services)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("close while tool is active error = %v, elapsed = %v", err, time.Since(started))
	}
	if services.Browser == nil {
		t.Fatal("close while tool is active discarded runtime ownership")
	}
	services.browserMu.RUnlock()
}

func TestBrowserRuntimeCloseDeadlineBoundsSweepWait(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state", "browser", browserStateFile)
	store, err := browser.NewFileStore(storePath, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	_, cancelSweep := context.WithCancel(context.Background())
	runtime := &browserRuntime{store: store, cancel: cancelSweep, done: done}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	started := time.Now()
	err = runtime.Close(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("deadline Close() error = %v, elapsed = %v", err, time.Since(started))
	}
	if _, openErr := browser.NewFileStore(storePath, 0, 0); !errors.Is(openErr, browser.ErrStoreOwned) {
		t.Fatalf("store while sweep is retained error = %v, want ErrStoreOwned", openErr)
	}
	close(done)
	if err = runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close() after sweep exit error = %v", err)
	}
	reopened, err := browser.NewFileStore(storePath, 0, 0)
	if err != nil {
		t.Fatalf("reopen store after sweep exit error = %v", err)
	}
	reopened.Close()
}

func TestChannelStartupRollbackReleasesBrowserStore(t *testing.T) {
	root := t.TempDir()
	cfg := gatewayBrowserConfig(root)
	runtime, err := newBrowserRuntime(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	services := &services{Browser: runtime}
	if err = rollbackBrowserRuntime(services); err != nil || services.Browser != nil {
		t.Fatalf("rollbackBrowserRuntime() error = %v, runtime = %+v", err, services.Browser)
	}
	reopened, err := newBrowserRuntime(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newBrowserRuntime() after startup rollback error = %v", err)
	}
	if err = reopened.Close(context.Background()); err != nil {
		t.Fatalf("reopened Close() error = %v", err)
	}
}

func TestSetupBrowserRuntimeRequiresServicesOwner(t *testing.T) {
	if err := setupBrowserRuntime(context.Background(), config.DefaultConfig(), nil); err == nil {
		t.Fatal("setupBrowserRuntime() error = nil")
	}
}

func TestBrowserSweepIntervalUsesShortestAuthorityLifetime(t *testing.T) {
	if got := browserSweepInterval(config.BrowserLimitsConfig{
		IdleSeconds: 20, SessionSeconds: 60, PreparedSeconds: 5,
	}); got != 5*time.Second {
		t.Fatalf("browserSweepInterval() = %v, want 5s", got)
	}
	if got := browserSweepInterval(config.BrowserLimitsConfig{
		IdleSeconds: 600, SessionSeconds: 3600, PreparedSeconds: 300,
	}); got != 30*time.Second {
		t.Fatalf("browserSweepInterval() = %v, want 30s cap", got)
	}
}

func gatewayBrowserConfig(workspace string) *config.Config {
	runtimeRoot, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		panic(err)
	}
	profileDirectory := filepath.Join(runtimeRoot, "browser-profile")
	lockDirectory := filepath.Join(runtimeRoot, "browser-locks")
	if err = os.MkdirAll(profileDirectory, 0o700); err != nil {
		panic(err)
	}
	if err = os.MkdirAll(lockDirectory, 0o700); err != nil {
		panic(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Tools.MCP.Servers["playwright"] = config.MCPServerConfig{
		Enabled: false, Command: "npx", Type: "stdio",
	}
	cfg.Tools.Browser = config.BrowserToolsConfig{
		Enabled: true,
		Agents:  []string{"browser"},
		Targets: map[string]config.BrowserTargetConfig{
			config.BrowserDefaultTarget: {
				Enabled: true, Driver: config.BrowserDriverPlaywrightMCP, DriverServer: "playwright",
				Profiles: map[string]config.BrowserProfileConfig{
					config.BrowserDefaultProfile: {
						Enabled: true, Revision: "managed-v1", Mode: config.BrowserProfileManaged,
						AllowedAgents:  []string{"browser"},
						AllowedActors:  []string{"actor_1", "telegram:browser-test-actor"},
						DryRun:         true,
						NetworkMode:    config.BrowserNetworkExactOrigins,
						CapabilityMode: config.BrowserCapabilityFullAccess,
						ApprovalMode:   config.BrowserApprovalAlwaysCommit,
						AllowedOrigins: []string{"https://example.com"},
						Runtime: config.BrowserProfileRuntimeConfig{
							ProfileDirectory: profileDirectory,
							LockFile:         filepath.Join(lockDirectory, "managed.lock"),
						},
					},
				},
			},
		},
	}
	return cfg
}

type gatewayTestBrowserWorker struct {
	closeErr            error
	closeCalls          atomic.Int32
	waitForContextCalls int32
}

func (*gatewayTestBrowserWorker) Status(context.Context) (browser.WorkerStatus, error) {
	return browser.WorkerReady, nil
}

func (worker *gatewayTestBrowserWorker) Close(ctx context.Context) error {
	call := worker.closeCalls.Add(1)
	if call <= worker.waitForContextCalls {
		<-ctx.Done()
		return ctx.Err()
	}
	return worker.closeErr
}

type gatewayTestBrowserFactory struct {
	worker browser.Worker
}

func (factory *gatewayTestBrowserFactory) Open(
	context.Context,
	browser.WorkerOpenRequest,
) (browser.WorkerOpenResult, error) {
	return browser.WorkerOpenResult{Owner: factory.worker}, nil
}
