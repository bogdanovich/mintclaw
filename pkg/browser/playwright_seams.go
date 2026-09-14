package browser

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

// playwrightRuntimeProvider owns local runtime provisioning independently of
// the control protocol used to drive the resulting browser. The interface and
// its handle are private so runtime paths, proxy endpoints, and native
// identity cannot enter model-facing or durable payloads.
type playwrightRuntimeProvider interface {
	Provision(context.Context, WorkerOpenRequest) (playwrightRuntime, error)
}

// playwrightControlDriver maps the stable MintClaw worker contract onto one
// already provisioned runtime. Phase 1 supplies only the existing MCP
// implementation; later drivers must pass the same contract.
type playwrightControlDriver interface {
	Open(context.Context, WorkerOpenRequest, playwrightRuntime) (playwrightDriverOpenResult, error)
}

// playwrightDriverOpenResult makes partial-startup ownership explicit. A
// driver either transfers an owner that can retry cleanup or proves that it
// never attached a native process to the runtime.
type playwrightDriverOpenResult struct {
	Owner            Worker
	RuntimeUntouched bool
}

type playwrightRuntime interface {
	ProfileConfig() config.BrowserProfileConfig
	NetworkProxy() *browserNetworkProxy
	EphemeralRuntime() *ephemeralRuntimeLease
	Release(driverStopped bool) error
	playwrightRuntime()
}

type localPlaywrightRuntimeProvider struct {
	factory *PlaywrightWorkerFactory
}

type localPlaywrightRuntime struct {
	profile          config.BrowserProfileConfig
	networkProxy     *browserNetworkProxy
	ephemeralRuntime *ephemeralRuntimeLease

	mu     sync.Mutex
	closed bool
}

func clonePlaywrightProfileConfig(profile config.BrowserProfileConfig) config.BrowserProfileConfig {
	profile.AllowedAgents = append([]string(nil), profile.AllowedAgents...)
	profile.AllowedActors = append([]string(nil), profile.AllowedActors...)
	profile.AllowedOrigins = append([]string(nil), profile.AllowedOrigins...)
	profile.Policy = browserpolicy.ClonePolicy(profile.Policy)
	profile.Attached.AllowedOrigins = append(
		[]string(nil),
		profile.Attached.AllowedOrigins...,
	)
	return profile
}

func (*localPlaywrightRuntime) playwrightRuntime() {}

func (runtime *localPlaywrightRuntime) ProfileConfig() config.BrowserProfileConfig {
	return runtime.profile
}

func (runtime *localPlaywrightRuntime) NetworkProxy() *browserNetworkProxy {
	return runtime.networkProxy
}

func (runtime *localPlaywrightRuntime) EphemeralRuntime() *ephemeralRuntimeLease {
	return runtime.ephemeralRuntime
}

// MarshalJSON fails closed even if a future caller accidentally places the
// private runtime handle in an interface accepted by an encoder.
func (*localPlaywrightRuntime) MarshalJSON() ([]byte, error) {
	return nil, errors.New("browser runtime handles are not serializable")
}

// Release first retires network access. Ephemeral identity is removed only
// after the driver process and proxy are both proven stopped; otherwise the
// handle remains retryable and cleanup-required.
func (runtime *localPlaywrightRuntime) Release(driverStopped bool) error {
	if runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed {
		return nil
	}
	proxyErr := error(nil)
	if runtime.networkProxy != nil {
		proxyErr = runtime.networkProxy.Close()
	}
	if !driverStopped {
		return errors.Join(ErrWorkerUnavailable, ErrCleanupRequired, proxyErr)
	}
	if runtime.ephemeralRuntime != nil {
		if proxyErr != nil {
			return errors.Join(ErrWorkerUnavailable, ErrCleanupRequired, proxyErr)
		}
		if err := runtime.ephemeralRuntime.Close(); err != nil {
			return errors.Join(ErrWorkerUnavailable, ErrCleanupRequired, err)
		}
	}
	if proxyErr != nil {
		return errors.Join(ErrWorkerUnavailable, proxyErr)
	}
	runtime.closed = true
	return nil
}

func (provider *localPlaywrightRuntimeProvider) Provision(
	_ context.Context,
	request WorkerOpenRequest,
) (playwrightRuntime, error) {
	if provider == nil || provider.factory == nil {
		return nil, ErrWorkerUnavailable
	}
	factory := provider.factory
	profile := factory.profileConfig
	var runtimeConfig config.BrowserProfileRuntimeConfig
	var err error
	switch profile.Mode {
	case config.BrowserProfileEphemeral:
		runtimeConfig, err = normalizeEphemeralProfileRuntime(profile.Runtime)
	case config.BrowserProfileManaged:
		runtimeConfig, err = normalizeManagedProfileRuntime(profile.Runtime)
	case config.BrowserProfileAttachedUser:
		if profile.Runtime != (config.BrowserProfileRuntimeConfig{}) {
			err = ErrDenied
		}
	default:
		err = ErrDenied
	}
	if err != nil || runtimeConfig != profile.Runtime {
		factory.readiness.Store(playwrightReadinessUnavailable)
		return nil, ErrWorkerUnavailable
	}
	runtimeHandle := &localPlaywrightRuntime{profile: profile}
	if profile.Mode != config.BrowserProfileAttachedUser {
		runtimeHandle.networkProxy, err = startBrowserNetworkProxy(
			profile,
			factory.proxyLookupIP,
			factory.proxyDial,
		)
		if err != nil {
			factory.readiness.Store(playwrightReadinessProxyUnavailable)
			return nil, ErrWorkerUnavailable
		}
	}
	if profile.Mode == config.BrowserProfileEphemeral {
		runtimeHandle.ephemeralRuntime, err = createEphemeralRuntimeLease(
			runtimeConfig,
			request.SessionID,
		)
		if err != nil {
			factory.readiness.Store(playwrightReadinessUnavailable)
			return nil, errors.Join(ErrWorkerUnavailable, runtimeHandle.Release(true))
		}
	}
	return runtimeHandle, nil
}

type playwrightMCPControlDriver struct {
	factory *PlaywrightWorkerFactory
}

func (driver *playwrightMCPControlDriver) Open(
	ctx context.Context,
	request WorkerOpenRequest,
	runtimeHandle playwrightRuntime,
) (playwrightDriverOpenResult, error) {
	if driver == nil || driver.factory == nil || runtimeHandle == nil {
		return playwrightDriverOpenResult{}, ErrWorkerUnavailable
	}
	factory := driver.factory
	if factory.clientFactory == nil {
		factory.readiness.Store(playwrightReadinessUnavailable)
		return playwrightDriverOpenResult{RuntimeUntouched: true}, ErrWorkerUnavailable
	}
	client := factory.clientFactory()
	if client == nil {
		factory.readiness.Store(playwrightReadinessUnavailable)
		return playwrightDriverOpenResult{RuntimeUntouched: true}, ErrWorkerUnavailable
	}
	profile := runtimeHandle.ProfileConfig()
	networkProxy := runtimeHandle.NetworkProxy()
	ephemeralRuntime := runtimeHandle.EphemeralRuntime()
	worker := &playwrightWorker{
		client: client, runtime: runtimeHandle, networkProxy: networkProxy,
		limits: request.Limits.Effective(), downloadReady: factory.downloadReady,
		ephemeralRuntime: ephemeralRuntime, contextSessionID: request.SessionID,
		attached: profile.Mode == config.BrowserProfileAttachedUser,
	}
	server := config.MCPServerConfig{}
	var err error
	if worker.attached {
		server, err = playwrightServerWithAttachedPolicy(factory.serverConfig)
	} else if networkProxy == nil {
		err = ErrWorkerUnavailable
	} else {
		server, err = playwrightServerWithNetworkPolicy(
			factory.serverConfig,
			profile,
			networkProxy.URL(),
		)
	}
	if err != nil {
		factory.readiness.Store(playwrightReadinessUnavailable)
		return failedPlaywrightDriverOpen(worker, ErrWorkerUnavailable)
	}
	if ephemeralRuntime != nil {
		if server.Env == nil {
			server.Env = make(map[string]string)
		}
		server.Env["TMPDIR"] = ephemeralRuntime.Path()
		server.Env["PWTEST_SOCKETS_DIR"] = ephemeralRuntime.Path()
	}
	server, outputRoot := playwrightOutputRoot(server)
	outputDir := ""
	if ephemeralRuntime != nil {
		// Ephemeral driver scratch belongs to the session lease regardless of a
		// shared template's output-root preference. Startup recovery can then
		// remove every browser-generated runtime file after a process crash.
		outputDir, err = os.MkdirTemp(ephemeralRuntime.Path(), "output-")
	} else if outputRoot == "" {
		outputDir, err = os.MkdirTemp("", "mintclaw-browser-"+request.SessionID+"-")
	} else if !filepath.IsAbs(outputRoot) || validatePrivateBrowserOutputRoot(outputRoot) != nil {
		err = ErrWorkerUnavailable
	} else {
		outputDir, err = os.MkdirTemp(outputRoot, request.SessionID+"-")
	}
	if err != nil {
		factory.readiness.Store(playwrightReadinessUnavailable)
		return failedPlaywrightDriverOpen(worker, ErrWorkerUnavailable)
	}
	// Deny the driver's native disk-download path on every platform. The
	// downloadReady bit gates only MintClaw's bounded Chromium capture path;
	// hiding that action must never leave an unbounded click side effect.
	server, err = configurePlaywrightDownloadBoundary(server, outputDir)
	if err != nil {
		factory.readiness.Store(playwrightReadinessUnavailable)
		worker.outputDir = outputDir
		return failedPlaywrightDriverOpen(worker, ErrWorkerUnavailable)
	}
	server.Args = append(server.Args, "--output-dir", outputDir)
	lifetimeCtx, cancelLifetime := context.WithCancel(context.WithoutCancel(ctx))
	worker.cancelLifetime = cancelLifetime
	worker.outputDir = outputDir
	worker.contextSecret = make([]byte, 32)
	if _, err = rand.Read(worker.contextSecret); err != nil {
		factory.readiness.Store(playwrightReadinessUnavailable)
		return failedPlaywrightDriverOpen(worker, ErrWorkerUnavailable)
	}
	stopStartupCancellation := context.AfterFunc(ctx, cancelLifetime)
	catalog, err := client.Connect(lifetimeCtx, playwrightPrivateServerName, server)
	startupActive := stopStartupCancellation()
	if err != nil {
		factory.readiness.Store(playwrightReadinessUnavailable)
		return failedPlaywrightDriverOpen(worker, ErrWorkerUnavailable)
	}
	if !startupActive {
		factory.readiness.Store(playwrightReadinessUnavailable)
		return failedPlaywrightDriverOpen(worker, ErrWorkerUnavailable)
	}
	catalogRevision, err := validatePlaywrightCatalog(catalog)
	if err != nil {
		factory.readiness.Store(playwrightReadinessIncompatible)
		return failedPlaywrightDriverOpen(worker, ErrDriverIncompatible)
	}
	worker.catalogRevision = catalogRevision
	if worker.attached {
		if err = worker.validateAttachedSelection(ctx); err != nil {
			if errors.Is(err, ErrWorkerUnavailable) {
				factory.readiness.Store(playwrightReadinessUnavailable)
				return failedPlaywrightDriverOpen(worker, ErrWorkerUnavailable)
			}
			factory.readiness.Store(playwrightReadinessIncompatible)
			return failedPlaywrightDriverOpen(worker, ErrDriverIncompatible)
		}
	}
	if err = worker.initializeDiagnostics(ctx); err != nil {
		if worker.attached && (errors.Is(err, ErrWorkerUnavailable) || errors.Is(err, ErrDriverRejected)) {
			factory.readiness.Store(playwrightReadinessUnavailable)
			return failedPlaywrightDriverOpen(worker, err)
		}
		factory.readiness.Store(playwrightReadinessIncompatible)
		return failedPlaywrightDriverOpen(worker, ErrDriverIncompatible)
	}
	factory.readiness.Store(playwrightReadinessReady)
	return playwrightDriverOpenResult{Owner: worker}, nil
}

func failedPlaywrightDriverOpen(
	worker *playwrightWorker,
	err error,
) (playwrightDriverOpenResult, error) {
	opened, openErr := failedPlaywrightOpen(worker, err)
	return playwrightDriverOpenResult{Owner: opened.Owner}, openErr
}

var _ json.Marshaler = (*localPlaywrightRuntime)(nil)
