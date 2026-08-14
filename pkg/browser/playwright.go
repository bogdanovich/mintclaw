package browser

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/config"
	localmcp "github.com/bogdanovich/mintclaw/pkg/mcp"
)

const playwrightPrivateServerName = "browser_driver"

// playwrightDriverResponseBytes is an inbound safety ceiling. It is
// deliberately independent of the configured outbound tool-result limit so a
// large, valid driver snapshot can reach the projection step and be truncated
// to the caller's smaller delivery budget.
const playwrightDriverResponseBytes = config.BrowserMaxSnapshotBytes + config.BrowserToolResultEnvelopeBytes

const opaqueSnapshotReferenceBytes = len("ref_") + 32

const playwrightNavigationIdentityResponseBytes = 4096

const playwrightTargetExpression = `(?:f[1-9][0-9]{0,9})?e[1-9][0-9]{0,9}`

var (
	playwrightTargetPattern     = regexp.MustCompile(`^` + playwrightTargetExpression + `$`)
	playwrightSnapshotRefToken  = regexp.MustCompile(`\[ref=`)
	playwrightSnapshotTargetRef = regexp.MustCompile(`\[ref=(` + playwrightTargetExpression + `)\]`)
	playwrightElementPattern    = regexp.MustCompile(
		`(?m)^\s*-\s+([A-Za-z][A-Za-z0-9_-]*)(?:\s+"([^"]*)")?[^\n]*\[ref=(` +
			playwrightTargetExpression + `)\]`,
	)
	playwrightDialogPattern = regexp.MustCompile(
		`^- \["(alert|beforeunload|confirm|prompt)" dialog with message "(.*)"\]: can be handled by browser_handle_dialog$`,
	)
	playwrightSnapshotLinkPattern    = regexp.MustCompile(`^- \[Snapshot\]\(.+\)$`)
	playwrightNavigationIdentityPart = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,256}$`)
)

const playwrightNavigationIdentityMarker = "MINTCLAW_NAV_V1"

const playwrightNavigationCheckedActionMarker = "MINTCLAW_NAV_ACT_V1"

const playwrightCheckActionMarker = "MINTCLAW_CHECK_V1"

const playwrightNavigationIdentityCode = `async (page) => {
  const trackerKey = Symbol.for("mintclaw.browser.navigation-tracker.v1");
  let state = page[trackerKey];
  if (!state) {
    const cdp = await page.context().newCDPSession(page);
    state = { cdp, mainFrameID: "", loaderID: "", generation: 1 };
    cdp.on("Page.frameNavigated", event => {
      const frame = event && event.frame;
      const frameID = String(frame && frame.id || "");
      const loaderID = String(frame && frame.loaderId || "");
      if (frameID === state.mainFrameID && loaderID && loaderID !== state.loaderID) {
        state.loaderID = loaderID;
        state.generation++;
      }
    });
    cdp.on("Page.navigatedWithinDocument", event => {
      if (String(event && event.frameId || "") === state.mainFrameID) state.generation++;
    });
    await cdp.send("Page.enable");
    const initialTree = await cdp.send("Page.getFrameTree");
    const initialFrame = initialTree.frameTree && initialTree.frameTree.frame;
    state.mainFrameID = String(initialFrame && initialFrame.id || "");
    state.loaderID = String(initialFrame && initialFrame.loaderId || "");
    if (!state.mainFrameID || !state.loaderID) {
      await cdp.detach();
      return "MINTCLAW_NAV_V1|error|missing_identity";
    }
    Object.defineProperty(page, trackerKey, { value: state, configurable: true });
    page.once("close", () => {
      delete page[trackerKey];
      state.cdp.detach().catch(() => {});
    });
  }
  const tree = await state.cdp.send("Page.getFrameTree");
  const frame = tree.frameTree && tree.frameTree.frame;
  const frameID = String(frame && frame.id || "");
  const loaderID = String(frame && frame.loaderId || "");
  if (!frameID || !loaderID) return "MINTCLAW_NAV_V1|error|missing_identity";
  if (frameID !== state.mainFrameID || loaderID !== state.loaderID) {
    state.mainFrameID = frameID;
    state.loaderID = loaderID;
    state.generation++;
  }
  if (!Number.isSafeInteger(state.generation) || state.generation < 1) {
    return "MINTCLAW_NAV_V1|error|invalid_generation";
  }
  return "MINTCLAW_NAV_V1|ok|" + encodeURIComponent(frameID) + "|" +
    encodeURIComponent(loaderID) + "|" + String(state.generation);
}`

var playwrightManagedEnvironmentNames = []string{
	"PLAYWRIGHT_MCP_ALLOWED_ORIGINS",
	"PLAYWRIGHT_MCP_BLOCKED_ORIGINS",
	"PLAYWRIGHT_MCP_CAPS",
	"PLAYWRIGHT_MCP_CONFIG",
	"PLAYWRIGHT_MCP_PROXY_SERVER",
	"PLAYWRIGHT_MCP_PROXY_BYPASS",
	"PLAYWRIGHT_MCP_CDP_ENDPOINT",
	"PLAYWRIGHT_MCP_ENDPOINT",
	"PLAYWRIGHT_MCP_EXTENSION",
}

type DriverActionKind string

const (
	DriverNavigate       DriverActionKind = "navigate"
	DriverClick          DriverActionKind = "click"
	DriverFill           DriverActionKind = "fill"
	DriverSelect         DriverActionKind = "select"
	DriverPress          DriverActionKind = "press"
	DriverScroll         DriverActionKind = "scroll"
	DriverDialog         DriverActionKind = "dialog"
	DriverCheck          DriverActionKind = "check"
	DriverUncheck        DriverActionKind = "uncheck"
	DriverHover          DriverActionKind = "hover"
	DriverDrag           DriverActionKind = "drag"
	DriverUpload         DriverActionKind = "upload"
	DriverDownloadAction DriverActionKind = "download"
)

type DriverAction struct {
	Kind               DriverActionKind
	URL                string
	Target             string
	Element            string
	DestinationTarget  string
	DestinationElement string
	Value              string
	Key                string
	Direction          string
	Amount             int
	Accept             bool
	PromptProvided     bool
	ArtifactSHA256     string
	ArtifactBytes      int64
}

type DriverObservation struct {
	URL           string
	Origin        string
	Title         string
	Snapshot      string
	Elements      []DriverElement
	PendingDialog *DialogObservation
	Truncated     bool
}

type DriverElement struct {
	Target string
	Role   string
	Name   string
}

type playwrightMCPClient interface {
	Connect(context.Context, string, config.MCPServerConfig) ([]*sdkmcp.Tool, error)
	Ping(context.Context) error
	CallTool(context.Context, string, map[string]any) (*sdkmcp.CallToolResult, error)
	Close() error
}

type managerPlaywrightClient struct {
	manager    *localmcp.Manager
	connection *localmcp.ServerConnection
}

func newManagerPlaywrightClient() playwrightMCPClient {
	return &managerPlaywrightClient{manager: localmcp.NewManager()}
}

func (client *managerPlaywrightClient) Connect(
	ctx context.Context,
	server string,
	cfg config.MCPServerConfig,
) ([]*sdkmcp.Tool, error) {
	if err := client.manager.ConnectServer(ctx, server, cfg); err != nil {
		return nil, err
	}
	connection, ok := client.manager.GetServer(server)
	if !ok || connection == nil {
		return nil, errors.New("connected browser driver is unavailable")
	}
	client.connection = connection
	return append([]*sdkmcp.Tool(nil), connection.Tools...), nil
}

func (client *managerPlaywrightClient) Ping(ctx context.Context) error {
	if client.connection == nil || client.connection.Session == nil {
		return errors.New("browser driver session is unavailable")
	}
	return client.connection.Session.Ping(ctx, nil)
}

func (client *managerPlaywrightClient) CallTool(
	ctx context.Context,
	tool string,
	arguments map[string]any,
) (*sdkmcp.CallToolResult, error) {
	if client.connection == nil || client.connection.Session == nil {
		return nil, errors.New("browser driver session is unavailable")
	}
	// Deliberately bypass Manager.CallTool: the generic manager reconnects a
	// lost session for future calls even when replay is disabled. A browser
	// worker must instead become lost without starting a replacement driver.
	return client.connection.Session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: tool, Arguments: arguments,
	})
}

func (client *managerPlaywrightClient) Close() error {
	return client.manager.Close()
}

type PlaywrightWorkerFactory struct {
	target        string
	profileName   string
	profileConfig config.BrowserProfileConfig
	serverConfig  config.MCPServerConfig
	downloadReady bool
	readiness     atomic.Uint32
	clientFactory func() playwrightMCPClient
	lookPath      func(string) (string, error)
	proxyLookupIP browserProxyLookup
	proxyDial     browserProxyDial
}

// PlaywrightManagedHostConfig binds the existing private Playwright adapter to
// an execution host. It is intentionally driver-facing rather than
// model-facing: callers must derive every field from trusted local policy.
type PlaywrightManagedHostConfig struct {
	Target        string
	Profile       string
	ProfileConfig config.BrowserProfileConfig
	ServerConfig  config.MCPServerConfig
}

// PlaywrightHandoffAvailable reports whether the managed driver owns a headed
// local browser window. Handoff does not expose a remote endpoint or admit a
// headless/browser-extension configuration.
func PlaywrightHandoffAvailable(root *config.Config) bool {
	if root == nil || (runtime.GOOS != "linux" && runtime.GOOS != "darwin") ||
		!root.Tools.Browser.Enabled {
		return false
	}
	target, ok := root.Tools.Browser.Targets[config.BrowserDefaultTarget]
	if !ok || !target.Enabled || target.Driver != config.BrowserDriverPlaywrightMCP {
		return false
	}
	server, ok := root.Tools.MCP.Servers[target.DriverServer]
	if !ok {
		return false
	}
	for _, argument := range server.Args {
		if argument == "--headless" || strings.HasPrefix(argument, "--headless=") ||
			argument == "--extension" || strings.HasPrefix(argument, "--extension=") {
			return false
		}
	}
	return true
}

func playwrightOutputRoot(server config.MCPServerConfig) (config.MCPServerConfig, string) {
	filtered := make([]string, 0, len(server.Args))
	root := ""
	for index := 0; index < len(server.Args); index++ {
		argument := server.Args[index]
		if argument == "--output-dir" && index+1 < len(server.Args) {
			root = server.Args[index+1]
			index++
			continue
		}
		if strings.HasPrefix(argument, "--output-dir=") {
			root = strings.TrimPrefix(argument, "--output-dir=")
			continue
		}
		filtered = append(filtered, argument)
	}
	server.Args = filtered
	return server, root
}

func NewPlaywrightWorkerFactory(rootConfig *config.Config) (*PlaywrightWorkerFactory, error) {
	if rootConfig == nil {
		return nil, errors.New("playwright worker factory requires a root config")
	}
	if err := rootConfig.ValidateBrowserConfig(); err != nil {
		return nil, err
	}
	if !rootConfig.Tools.Browser.Enabled {
		return nil, ErrDenied
	}
	target, ok := rootConfig.Tools.Browser.Targets[config.BrowserDefaultTarget]
	if !ok || !target.Enabled || target.Driver != config.BrowserDriverPlaywrightMCP {
		return nil, ErrDenied
	}
	profile, ok := target.Profiles[config.BrowserDefaultProfile]
	if !ok || !profile.Enabled || profile.DryRun == profile.AllowApprovedActions {
		return nil, ErrDenied
	}
	server, ok := rootConfig.Tools.MCP.Servers[target.DriverServer]
	if !ok {
		return nil, ErrDenied
	}
	if err := validatePlaywrightManagedPolicy(server); err != nil {
		return nil, err
	}
	return newPlaywrightManagedHostFactory(PlaywrightManagedHostConfig{
		Target: config.BrowserDefaultTarget, Profile: config.BrowserDefaultProfile,
		ProfileConfig: profile, ServerConfig: server,
	}, PlaywrightDownloadAvailable(rootConfig))
}

// NewPlaywrightManagedHostFactory reuses the B1 Playwright worker on another
// trusted host. Its driver configuration is companion-local and must never be
// projected into a capability catalog or model-visible result.
func NewPlaywrightManagedHostFactory(
	host PlaywrightManagedHostConfig,
) (*PlaywrightWorkerFactory, error) {
	if err := config.ValidateMCPExclusiveLockFile(host.ServerConfig); err != nil {
		return nil, ErrDenied
	}
	return newPlaywrightManagedHostFactory(
		host,
		playwrightServerDownloadAvailable(host.ServerConfig),
	)
}

func newPlaywrightManagedHostFactory(
	host PlaywrightManagedHostConfig,
	downloadReady bool,
) (*PlaywrightWorkerFactory, error) {
	if !validIdentifier(host.Target) || !validIdentifier(host.Profile) ||
		!host.ProfileConfig.Enabled ||
		host.ProfileConfig.Mode != config.BrowserProfileManaged ||
		host.ProfileConfig.DryRun == host.ProfileConfig.AllowApprovedActions {
		return nil, ErrDenied
	}
	networkMode := host.ProfileConfig.EffectiveNetworkMode()
	if networkMode != config.BrowserNetworkExactOrigins &&
		networkMode != config.BrowserNetworkPublicWeb &&
		networkMode != config.BrowserNetworkAnyHTTP {
		return nil, ErrDenied
	}
	if networkMode == config.BrowserNetworkExactOrigins &&
		len(host.ProfileConfig.AllowedOrigins) == 0 {
		return nil, ErrDenied
	}
	if networkMode != config.BrowserNetworkExactOrigins &&
		len(host.ProfileConfig.AllowedOrigins) != 0 {
		return nil, ErrDenied
	}
	if config.EffectiveMCPTransportType(host.ServerConfig) != "stdio" ||
		strings.TrimSpace(host.ServerConfig.Command) == "" ||
		config.EffectiveMCPSessionLossReplay(host.ServerConfig) !=
			config.MCPSessionLossReplayNever ||
		strings.TrimSpace(host.ServerConfig.ExclusiveLockFile) == "" {
		return nil, ErrDenied
	}
	if err := validatePlaywrightManagedPolicy(host.ServerConfig); err != nil {
		return nil, err
	}
	return &PlaywrightWorkerFactory{
		target: host.Target, profileName: host.Profile,
		profileConfig: host.ProfileConfig,
		serverConfig:  cloneMCPServerConfig(host.ServerConfig),
		downloadReady: downloadReady,
		clientFactory: newManagerPlaywrightClient, lookPath: exec.LookPath,
	}, nil
}

const (
	playwrightReadinessUnchecked uint32 = iota
	playwrightReadinessReady
	playwrightReadinessUnavailable
	playwrightReadinessIncompatible
	playwrightReadinessProxyUnavailable
)

// PassiveReadiness reads configuration, executable availability, and the
// compatibility outcome cached by prior real opens. It never starts npx or a
// browser and never probes an active worker.
func (factory *PlaywrightWorkerFactory) PassiveReadiness() DriverReadiness {
	if factory == nil {
		return DriverReadiness{
			Status: ReadinessUnavailable, Driver: ReadinessUnavailable,
			Browser: ReadinessUnavailable, Proxy: ReadinessUnavailable,
			Compatibility: CompatibilityUnchecked,
			Code:          "driver_unavailable", Action: "contact_operator",
		}
	}
	if factory.lookPath == nil {
		return DriverReadiness{
			Status: ReadinessUnavailable, Driver: ReadinessUnavailable,
			Browser: ReadinessConfigured, Proxy: ReadinessConfigured,
			Compatibility: CompatibilityUnchecked,
			Code:          "driver_missing", Action: "install_driver",
		}
	}
	if _, err := factory.lookPath(factory.serverConfig.Command); err != nil {
		return DriverReadiness{
			Status: ReadinessUnavailable, Driver: ReadinessUnavailable,
			Browser: ReadinessConfigured, Proxy: ReadinessConfigured,
			Compatibility: CompatibilityUnchecked,
			Code:          "driver_missing", Action: "install_driver",
		}
	}
	switch factory.readiness.Load() {
	case playwrightReadinessReady:
		return DriverReadiness{
			Status: ReadinessReady, Driver: ReadinessReady, Browser: ReadinessReady,
			Proxy: ReadinessReady, Compatibility: CompatibilityCompatible,
		}
	case playwrightReadinessUnavailable:
		return DriverReadiness{
			Status: ReadinessUnavailable, Driver: ReadinessUnavailable,
			Browser: ReadinessUnavailable, Proxy: ReadinessConfigured,
			Compatibility: CompatibilityUnchecked,
			Code:          "driver_unavailable", Action: "contact_operator",
		}
	case playwrightReadinessIncompatible:
		return DriverReadiness{
			Status: ReadinessDegraded, Driver: ReadinessDegraded,
			Browser: ReadinessUnavailable, Proxy: ReadinessReady,
			Compatibility: CompatibilityIncompatible,
			Code:          "driver_incompatible", Action: "upgrade_driver",
		}
	case playwrightReadinessProxyUnavailable:
		return DriverReadiness{
			Status: ReadinessDegraded, Driver: ReadinessConfigured,
			Browser: ReadinessConfigured, Proxy: ReadinessUnavailable,
			Compatibility: CompatibilityUnchecked,
			Code:          "proxy_unavailable", Action: "contact_operator",
		}
	default:
		return configuredDriverReadiness()
	}
}

func validatePlaywrightManagedPolicy(server config.MCPServerConfig) error {
	for _, argument := range server.Args {
		if argument == "--allowed-origins" || strings.HasPrefix(argument, "--allowed-origins=") ||
			argument == "--blocked-origins" || strings.HasPrefix(argument, "--blocked-origins=") ||
			argument == "--config" || strings.HasPrefix(argument, "--config=") ||
			argument == "--caps" || strings.HasPrefix(argument, "--caps=") ||
			argument == "--proxy-server" || strings.HasPrefix(argument, "--proxy-server=") ||
			argument == "--proxy-bypass" || strings.HasPrefix(argument, "--proxy-bypass=") ||
			argument == "--cdp-endpoint" || strings.HasPrefix(argument, "--cdp-endpoint=") ||
			argument == "--endpoint" || strings.HasPrefix(argument, "--endpoint=") ||
			argument == "--extension" || strings.HasPrefix(argument, "--extension=") {
			return fmt.Errorf(
				"browser driver policy and capabilities must be managed, not %q",
				argument,
			)
		}
	}
	for variable := range server.Env {
		if !localmcp.IsValidEnvironmentName(variable) {
			return fmt.Errorf("browser driver environment name %q is invalid", variable)
		}
		if playwrightManagedEnvironmentName(variable) {
			return fmt.Errorf(
				"browser driver policy and capabilities must be managed, not %s",
				variable,
			)
		}
	}
	return nil
}

func playwrightManagedEnvironmentName(name string) bool {
	for _, managed := range playwrightManagedEnvironmentNames {
		if strings.EqualFold(name, managed) {
			return true
		}
	}
	return false
}

func playwrightServerWithNetworkPolicy(
	server config.MCPServerConfig,
	profile config.BrowserProfileConfig,
	proxyURL string,
) (config.MCPServerConfig, error) {
	server = cloneMCPServerConfig(server)
	if err := validatePlaywrightManagedPolicy(server); err != nil {
		return config.MCPServerConfig{}, err
	}
	if proxyURL == "" {
		return config.MCPServerConfig{}, errors.New("browser driver requires a network-policy proxy")
	}
	origins := make([]string, 0, len(profile.AllowedOrigins))
	for _, rawOrigin := range profile.AllowedOrigins {
		origin, err := config.NormalizeBrowserOrigin(rawOrigin)
		if err != nil {
			return config.MCPServerConfig{}, fmt.Errorf("normalize browser driver origin: %w", err)
		}
		origins = append(origins, origin)
	}
	if profile.EffectiveNetworkMode() == config.BrowserNetworkExactOrigins && len(origins) == 0 {
		return config.MCPServerConfig{}, errors.New("browser driver requires allowed origins")
	}
	if (profile.EffectiveNetworkMode() == config.BrowserNetworkPublicWeb ||
		profile.EffectiveNetworkMode() == config.BrowserNetworkAnyHTTP) && len(origins) != 0 {
		return config.MCPServerConfig{}, errors.New("non-exact browser driver cannot use allowed origins")
	}
	sort.Strings(origins)
	allowedOrigins := strings.Join(origins, ";")
	if server.Env == nil {
		server.Env = make(map[string]string)
	}
	// MCP process environment precedence is parent < env file < explicit Env.
	// Set every policy input explicitly so neither inherited state nor an env
	// file can independently merge a blocklist or config-file policy.
	server.Env["PLAYWRIGHT_MCP_ALLOWED_ORIGINS"] = allowedOrigins
	server.Env["PLAYWRIGHT_MCP_BLOCKED_ORIGINS"] = ""
	server.Env["PLAYWRIGHT_MCP_CAPS"] = "vision"
	server.Env["PLAYWRIGHT_MCP_CONFIG"] = ""
	server.Env["PLAYWRIGHT_MCP_PROXY_SERVER"] = proxyURL
	server.Env["PLAYWRIGHT_MCP_PROXY_BYPASS"] = "<-loopback>"
	server.Env["PLAYWRIGHT_MCP_CDP_ENDPOINT"] = ""
	server.Env["PLAYWRIGHT_MCP_ENDPOINT"] = ""
	server.Env["PLAYWRIGHT_MCP_EXTENSION"] = ""
	server.Args = append(
		server.Args,
		"--caps", "vision",
		"--proxy-server", proxyURL,
		"--proxy-bypass", "<-loopback>",
	)
	if profile.EffectiveNetworkMode() == config.BrowserNetworkExactOrigins {
		server.Args = append(server.Args, "--allowed-origins", allowedOrigins)
	}
	return server, nil
}

func (factory *PlaywrightWorkerFactory) Open(
	ctx context.Context,
	request WorkerOpenRequest,
) (WorkerOpenResult, error) {
	if factory == nil || factory.clientFactory == nil || request.Target != factory.target ||
		request.Profile != factory.profileName || request.DryRun != factory.profileConfig.DryRun ||
		!validIdentifier(request.SessionID) {
		return WorkerOpenResult{}, ErrDenied
	}
	client := factory.clientFactory()
	if client == nil {
		factory.readiness.Store(playwrightReadinessUnavailable)
		return WorkerOpenResult{}, ErrWorkerUnavailable
	}
	networkProxy, err := startBrowserNetworkProxy(
		factory.profileConfig,
		factory.proxyLookupIP,
		factory.proxyDial,
	)
	if err != nil {
		factory.readiness.Store(playwrightReadinessProxyUnavailable)
		return WorkerOpenResult{}, ErrWorkerUnavailable
	}
	server, err := playwrightServerWithNetworkPolicy(
		factory.serverConfig,
		factory.profileConfig,
		networkProxy.URL(),
	)
	if err != nil {
		factory.readiness.Store(playwrightReadinessUnavailable)
		_ = networkProxy.Close()
		return WorkerOpenResult{}, ErrWorkerUnavailable
	}
	server, outputRoot := playwrightOutputRoot(server)
	outputDir := ""
	if outputRoot == "" {
		outputDir, err = os.MkdirTemp("", "mintclaw-browser-"+request.SessionID+"-")
	} else if !filepath.IsAbs(outputRoot) || validatePrivateBrowserOutputRoot(outputRoot) != nil {
		err = ErrWorkerUnavailable
	} else {
		outputDir, err = os.MkdirTemp(outputRoot, request.SessionID+"-")
	}
	if err != nil {
		factory.readiness.Store(playwrightReadinessUnavailable)
		_ = networkProxy.Close()
		return WorkerOpenResult{}, ErrWorkerUnavailable
	}
	// Deny the driver's native disk-download path on every platform. The
	// downloadReady bit gates only MintClaw's bounded Chromium capture path;
	// hiding that action must never leave an unbounded click side effect.
	server, err = configurePlaywrightDownloadBoundary(server, outputDir)
	if err != nil {
		factory.readiness.Store(playwrightReadinessUnavailable)
		_ = networkProxy.Close()
		_ = os.RemoveAll(outputDir)
		return WorkerOpenResult{}, ErrWorkerUnavailable
	}
	server.Args = append(server.Args, "--output-dir", outputDir)
	lifetimeCtx, cancelLifetime := context.WithCancel(context.WithoutCancel(ctx))
	worker := &playwrightWorker{
		client: client, networkProxy: networkProxy,
		limits: request.Limits.Effective(), cancelLifetime: cancelLifetime,
		outputDir: outputDir, contextSessionID: request.SessionID,
		sensitiveFields: append([]string(nil), factory.profileConfig.SensitiveFields...),
	}
	worker.contextSecret = make([]byte, 32)
	if _, err = rand.Read(worker.contextSecret); err != nil {
		factory.readiness.Store(playwrightReadinessUnavailable)
		return failedPlaywrightOpen(worker, ErrWorkerUnavailable)
	}
	stopStartupCancellation := context.AfterFunc(ctx, cancelLifetime)
	catalog, err := client.Connect(
		lifetimeCtx,
		playwrightPrivateServerName,
		server,
	)
	startupActive := stopStartupCancellation()
	if err != nil {
		factory.readiness.Store(playwrightReadinessUnavailable)
		return failedPlaywrightOpen(worker, ErrWorkerUnavailable)
	}
	if !startupActive {
		factory.readiness.Store(playwrightReadinessUnavailable)
		return failedPlaywrightOpen(worker, ErrWorkerUnavailable)
	}
	catalogRevision, err := validatePlaywrightCatalog(catalog)
	if err != nil {
		factory.readiness.Store(playwrightReadinessIncompatible)
		return failedPlaywrightOpen(worker, ErrDriverIncompatible)
	}
	worker.catalogRevision = catalogRevision
	factory.readiness.Store(playwrightReadinessReady)
	return WorkerOpenResult{Owner: worker}, nil
}

func failedPlaywrightOpen(worker *playwrightWorker, err error) (WorkerOpenResult, error) {
	if worker == nil {
		return WorkerOpenResult{}, err
	}
	worker.closing = true
	if worker.cancelLifetime != nil {
		worker.cancelLifetime()
	}
	return WorkerOpenResult{Owner: worker}, err
}

type playwrightWorker struct {
	client          playwrightMCPClient
	networkProxy    *browserNetworkProxy
	limits          config.BrowserLimitsConfig
	catalogRevision string
	cancelLifetime  context.CancelFunc
	outputDir       string
	sensitiveFields []string

	mu              sync.Mutex
	lost            bool
	closing         bool
	closed          bool
	lastObservation DriverObservation
	pendingDialog   *DialogObservation
	humanControl    bool
	navigationID    playwrightNavigationIdentity
	navigationToken string

	contextSessionID string
	contextSecret    []byte
	contextState     playwrightContextState
}

func (worker *playwrightWorker) BeginHumanControl(context.Context) error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closing || worker.closed || worker.lost {
		return ErrWorkerUnavailable
	}
	worker.humanControl = true
	worker.lastObservation = DriverObservation{}
	worker.pendingDialog = nil
	return nil
}

func (worker *playwrightWorker) EndHumanControl(context.Context) error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closing || worker.closed || worker.lost || !worker.humanControl {
		return ErrWorkerUnavailable
	}
	worker.humanControl = false
	worker.lastObservation = DriverObservation{}
	worker.pendingDialog = nil
	return nil
}

func (worker *playwrightWorker) Status(ctx context.Context) (WorkerStatus, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closing || worker.closed || worker.lost {
		return WorkerLost, nil
	}
	if worker.networkProxy != nil && !worker.networkProxy.Available() {
		worker.lost = true
		return WorkerLost, nil
	}
	if err := worker.client.Ping(ctx); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		worker.lost = true
		return WorkerLost, nil
	}
	return WorkerReady, nil
}

func (worker *playwrightWorker) Observe(ctx context.Context) (DriverObservation, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closing || worker.closed || worker.lost || worker.humanControl {
		return DriverObservation{}, ErrWorkerUnavailable
	}
	if worker.pendingDialog != nil {
		return worker.pendingDialogObservationLocked()
	}
	text, err := worker.callAndConsume(
		ctx, "browser_snapshot", map[string]any{"boxes": false}, true,
	)
	if err != nil {
		if errors.Is(err, ErrDriverRejected) && worker.pendingDialog != nil {
			return worker.pendingDialogObservationLocked()
		}
		return DriverObservation{}, err
	}
	observation, err := parsePlaywrightObservation(
		text,
		worker.limits.SnapshotBytes,
		worker.limits.SnapshotRefs,
		worker.limits.ToolResultBytes,
	)
	if err != nil {
		return DriverObservation{}, err
	}
	worker.lastObservation = observation
	return observation, nil
}

func (worker *playwrightWorker) NavigationIdentity(ctx context.Context) (string, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closing || worker.closed || worker.lost || worker.humanControl || worker.pendingDialog != nil {
		return "", ErrWorkerUnavailable
	}
	result, err := worker.client.CallTool(ctx, "browser_run_code_unsafe", map[string]any{
		"code": playwrightNavigationIdentityCode,
	})
	if err != nil || result == nil {
		worker.lost = true
		return "", ErrWorkerUnavailable
	}
	text, err := boundedPlaywrightText(result, playwrightNavigationIdentityResponseBytes)
	if err != nil || result.IsError {
		worker.lost = true
		return "", ErrDriverIncompatible
	}
	identity, err := parsePlaywrightNavigationIdentity(text)
	if err != nil {
		worker.lost = true
		return "", err
	}
	worker.navigationID = identity
	worker.navigationToken = identity.token()
	return worker.navigationToken, nil
}

type playwrightNavigationIdentity struct {
	frameID    string
	loaderID   string
	generation uint64
}

func (identity playwrightNavigationIdentity) token() string {
	digest := sha256.Sum256([]byte(fmt.Sprintf(
		"mintclaw.browser.navigation-identity.v1\x00%s\x00%s\x00%d",
		identity.frameID,
		identity.loaderID,
		identity.generation,
	)))
	return hex.EncodeToString(digest[:])
}

func parsePlaywrightNavigationIdentity(text string) (playwrightNavigationIdentity, error) {
	const resultHeader = "### Result"
	if strings.Count(text, resultHeader) != 1 {
		return playwrightNavigationIdentity{}, ErrDriverIncompatible
	}
	result := text[strings.Index(text, resultHeader)+len(resultHeader):]
	result = strings.TrimLeft(result, "\r\n")
	line := result
	if end := strings.IndexByte(line, '\n'); end >= 0 {
		line = line[:end]
	}
	line = strings.Trim(line, "\r\"' ")
	fields := strings.Split(line, "|")
	if len(fields) != 5 || fields[0] != playwrightNavigationIdentityMarker || fields[1] != "ok" {
		return playwrightNavigationIdentity{}, ErrDriverIncompatible
	}
	frameID, frameErr := url.QueryUnescape(fields[2])
	loaderID, loaderErr := url.QueryUnescape(fields[3])
	generation, generationErr := strconv.ParseUint(fields[4], 10, 64)
	if frameErr != nil || loaderErr != nil || generationErr != nil || generation == 0 ||
		!playwrightNavigationIdentityPart.MatchString(frameID) ||
		!playwrightNavigationIdentityPart.MatchString(loaderID) {
		return playwrightNavigationIdentity{}, ErrDriverIncompatible
	}
	return playwrightNavigationIdentity{frameID: frameID, loaderID: loaderID, generation: generation}, nil
}

func (worker *playwrightWorker) ExecuteAfterNavigationCheck(
	ctx context.Context,
	expectedToken string,
	action DriverAction,
) error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closing || worker.closed || worker.lost || worker.humanControl || worker.pendingDialog != nil {
		return ErrWorkerUnavailable
	}
	if expectedToken == "" || expectedToken != worker.navigationToken {
		return ErrStale
	}
	code, err := playwrightNavigationCheckedActionCode(
		worker.navigationID, action, worker.limits, worker.sensitiveFields,
	)
	if err != nil {
		return err
	}
	text, err := worker.callAndConsume(ctx, "browser_run_code_unsafe", map[string]any{"code": code}, true)
	if err != nil {
		return err
	}
	// Playwright MCP reports a modal state instead of the callback result when
	// the just-issued input opens a dialog. The dialog proves that dispatch
	// crossed the conditional boundary; retain it for the existing dialog
	// state machine and never replay the one-shot action.
	if worker.pendingDialog != nil {
		return nil
	}
	if err = parsePlaywrightNavigationDispatch(text); err != nil &&
		!errors.Is(err, ErrStale) && !errors.Is(err, ErrDenied) {
		worker.lost = true
	}
	return err
}

func (worker *playwrightWorker) AuthorizeFill(
	ctx context.Context,
	expectedToken string,
	target string,
) error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closing || worker.closed || worker.lost || worker.humanControl || worker.pendingDialog != nil {
		return ErrWorkerUnavailable
	}
	if expectedToken == "" || expectedToken != worker.navigationToken ||
		!playwrightTargetPattern.MatchString(target) {
		return ErrStale
	}
	dispatch := playwrightFillDispatch(target, "", false, worker.sensitiveFields)
	code := playwrightNavigationCheckedCode(worker.navigationID, dispatch)
	text, err := worker.callAndConsume(ctx, "browser_run_code_unsafe", map[string]any{"code": code}, true)
	if err != nil {
		return err
	}
	if err = parsePlaywrightNavigationDispatch(text); err != nil &&
		!errors.Is(err, ErrStale) && !errors.Is(err, ErrDenied) {
		worker.lost = true
	}
	return err
}

func playwrightNavigationCheckedActionCode(
	identity playwrightNavigationIdentity,
	action DriverAction,
	limits config.BrowserLimitsConfig,
	sensitiveFields []string,
) (string, error) {
	tool, arguments, err := mapPlaywrightAction(action, limits)
	if err != nil {
		return "", err
	}
	jsonString := func(value string) string {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	var dispatch string
	switch tool {
	case "browser_navigate":
		normalizedURL, ok := arguments["url"].(string)
		if !ok || normalizedURL == "" {
			return "", fmt.Errorf("%w: normalized navigation URL is unavailable", ErrInvalid)
		}
		dispatch = "await page.goto(" + jsonString(normalizedURL) + ");"
	case "browser_click":
		dispatch = "await page.locator(\"aria-ref=\" + " + jsonString(action.Target) +
			").click({ button: \"left\" });"
	case "browser_type":
		dispatch = playwrightFillDispatch(action.Target, action.Value, true, sensitiveFields)
	case "browser_select_option":
		dispatch = "await page.locator(\"aria-ref=\" + " + jsonString(action.Target) +
			").selectOption([" + jsonString(action.Value) + "]);"
	case "browser_press_key":
		dispatch = "await page.keyboard.press(" + jsonString(action.Key) + ");"
	case "browser_mouse_wheel":
		delta := action.Amount * 500
		if action.Direction == "up" {
			delta = -delta
		}
		dispatch = fmt.Sprintf("await page.mouse.wheel(0, %d);", delta)
	default:
		return "", fmt.Errorf("%w: navigation-checked action is unsupported", ErrInvalid)
	}
	return playwrightNavigationCheckedCode(identity, dispatch), nil
}

func playwrightFillDispatch(target, value string, execute bool, sensitiveFields []string) string {
	jsonString := func(value string) string {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	sensitive, sensitiveErr := browserpolicy.NormalizeSensitiveFieldTerms(sensitiveFields)
	sensitive = append(browserpolicy.BuiltInSensitiveFieldTerms(), sensitive...)
	policyJSON, _ := json.Marshal(map[string]any{
		"valid": sensitiveErr == nil, "sensitive": sensitive, "ordinary": browserpolicy.OrdinaryFieldTerms(),
	})
	dispatch := `const fillTarget = page.locator("aria-ref=" + ` + jsonString(target) + `);
  if (await fillTarget.count() !== 1 || !await fillTarget.isVisible()) {
    return "MINTCLAW_NAV_ACT_V1|stale";
  }
	  const fillOutcome = await fillTarget.evaluate((element, args) => {
    const ordinaryTypes = new Set(["", "text", "search", "email", "tel", "url", "number"]);
    const ordinaryAutocomplete = new Set(["", "off", "on", "name", "honorific-prefix",
      "given-name", "additional-name", "family-name", "honorific-suffix", "nickname",
      "email", "organization-title", "organization", "street-address", "address-line1",
      "address-line2", "address-line3", "address-level1", "address-level2", "address-level3",
      "address-level4", "country", "country-name", "postal-code", "tel", "tel-country-code",
      "tel-national", "tel-area-code", "tel-local", "tel-local-prefix", "tel-local-suffix",
      "tel-extension", "url", "photo"]);
    const matchesTerm = (identity, term) => {
      let offset = 0;
      while (term && offset <= identity.length - term.length) {
        const index = identity.indexOf(term, offset);
        if (index < 0) return false;
        const end = index + term.length;
        const left = index === 0 || !/[\p{L}\p{N}]/u.test(identity[index - 1]);
        const right = end === identity.length || !/[\p{L}\p{N}]/u.test(identity[end]);
        if (left && right) return true;
        offset = index + 1;
      }
      return false;
    };
    const classify = () => {
      const tag = String(element.tagName || "").toLowerCase();
      const type = String(element.getAttribute("type") || "").toLowerCase();
      const autocomplete = String(element.getAttribute("autocomplete") || "").toLowerCase();
      const role = String(element.getAttribute("role") || "").toLowerCase().trim();
      const ariaDisabled = String(element.getAttribute("aria-disabled") || "").toLowerCase().trim();
      const ariaReadOnly = String(element.getAttribute("aria-readonly") || "").toLowerCase().trim();
      const labelledBy = String(element.getAttribute("aria-labelledby") || "").trim();
      const identityParts = ["name", "id", "aria-label", "placeholder", "title"].map(name =>
        String(element.getAttribute(name) || "")).concat(Array.from(element.labels || []).map(label =>
        String(label.textContent || "")));
      let labelledByValid = labelledBy.length <= 1024;
      if (labelledBy) {
        const ids = labelledBy.split(/\s+/u).filter(Boolean);
        const wanted = new Set(ids);
        const references = new Map(ids.map(id => [id, []]));
        if (ids.length === 0 || ids.length > 16 || wanted.size !== ids.length) labelledByValid = false;
        if (labelledByValid) {
          for (const candidate of element.ownerDocument.querySelectorAll("[id]")) {
            if (wanted.has(candidate.id)) references.get(candidate.id).push(candidate);
          }
          for (const id of ids) {
            const candidates = references.get(id);
            const text = candidates && candidates.length === 1 ? String(candidates[0].textContent || "") : "";
            if (!candidates || candidates.length !== 1 || !text.trim() || text.length > 1024) {
              labelledByValid = false;
              break;
            }
            identityParts.push(text);
          }
        }
      }
      const identityPartsValid = identityParts.length <= 32 &&
        identityParts.every(part => part.length <= 1024);
      const identity = identityParts.join(" ").toLowerCase().replace(/\s+/gu, " ").trim();
      const style = element.ownerDocument.defaultView.getComputedStyle(element);
      const bounds = element.getBoundingClientRect();
      const visible = element.isConnected && style.visibility !== "hidden" && style.display !== "none" &&
        bounds.width > 0 && bounds.height > 0;
      const inputLike = (tag === "input" && ordinaryTypes.has(type)) ||
        tag === "textarea" || element.isContentEditable;
      const effectivelyDisabled = element.disabled || element.matches(":disabled");
      const compatibleRole = role === "" || role === "textbox" || role === "searchbox";
      const ariaEnabled = ariaDisabled === "" || ariaDisabled === "false";
      const ariaWritable = ariaReadOnly === "" || ariaReadOnly === "false";
      return args.policy.valid && visible && inputLike && !effectivelyDisabled && !element.readOnly &&
        labelledByValid && identityPartsValid && identity.length <= 4096 && compatibleRole && ariaEnabled && ariaWritable &&
        ordinaryAutocomplete.has(autocomplete) && !args.policy.sensitive.some(term => matchesTerm(identity, term)) &&
        args.policy.ordinary.some(term => matchesTerm(identity, term));
    };
    if (!classify()) return "denied";
    if (!args.execute) return "ok";
    element.focus({ preventScroll: true });
    if (!classify()) return "denied";
    const tag = String(element.tagName || "").toLowerCase();
    if (element.isContentEditable) {
      element.textContent = args.value;
    } else {
      const prototype = tag === "input" ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype;
      const valueDescriptor = Object.getOwnPropertyDescriptor(prototype, "value");
      const setter = valueDescriptor && valueDescriptor.set;
      const getter = valueDescriptor && valueDescriptor.get;
      if (typeof setter !== "function" || typeof getter !== "function") return "error|missing_value_setter";
      if (tag === "input" && String(element.getAttribute("type") || "").toLowerCase() === "number") {
        const probe = element.ownerDocument.createElement("input");
        probe.type = "number";
        setter.call(probe, args.value);
        if (getter.call(probe) !== args.value) return "denied";
      }
      const priorValue = getter.call(element);
      setter.call(element, args.value);
      if (getter.call(element) !== args.value) {
        setter.call(element, priorValue);
        return "denied";
      }
    }
    const inputEvent = typeof InputEvent === "function" ?
      new InputEvent("input", { bubbles: true, inputType: "insertText", data: args.value }) :
      new Event("input", { bubbles: true });
    element.dispatchEvent(inputEvent);
    return "ok";
  }, { policy: ` + string(policyJSON) + `, execute: ` + strconv.FormatBool(execute) + `, value: ` + jsonString(value) + ` });
  if (fillOutcome !== "ok") return "MINTCLAW_NAV_ACT_V1|" + fillOutcome;`
	return dispatch
}

func playwrightNavigationCheckedCode(identity playwrightNavigationIdentity, dispatch string) string {
	jsonString := func(value string) string {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	return fmt.Sprintf(`async (page) => {
  const expectedFrameID = %s;
  const expectedLoaderID = %s;
  const expectedGeneration = %d;
  const trackerKey = Symbol.for("mintclaw.browser.navigation-tracker.v1");
  const state = page[trackerKey];
  if (!state) return "MINTCLAW_NAV_ACT_V1|error|missing_tracker";
  const tree = await state.cdp.send("Page.getFrameTree");
  const frame = tree.frameTree && tree.frameTree.frame;
  const frameID = String(frame && frame.id || "");
  const loaderID = String(frame && frame.loaderId || "");
  if (!frameID || !loaderID) return "MINTCLAW_NAV_ACT_V1|error|missing_identity";
  if (frameID !== state.mainFrameID || loaderID !== state.loaderID) {
    state.mainFrameID = frameID;
    state.loaderID = loaderID;
    state.generation++;
  }
  if (frameID !== expectedFrameID || loaderID !== expectedLoaderID ||
      state.generation !== expectedGeneration) return "MINTCLAW_NAV_ACT_V1|stale";
  %s
  return "MINTCLAW_NAV_ACT_V1|ok";
}`,
		jsonString(identity.frameID),
		jsonString(identity.loaderID),
		identity.generation,
		dispatch,
	)
}

func parsePlaywrightNavigationDispatch(text string) error {
	const resultHeader = "### Result"
	if strings.Count(text, resultHeader) != 1 {
		return ErrDriverIncompatible
	}
	result := strings.TrimLeft(text[strings.Index(text, resultHeader)+len(resultHeader):], "\r\n")
	if end := strings.IndexByte(result, '\n'); end >= 0 {
		result = result[:end]
	}
	switch strings.Trim(result, "\r\"' ") {
	case playwrightNavigationCheckedActionMarker + "|ok":
		return nil
	case playwrightNavigationCheckedActionMarker + "|stale":
		return ErrStale
	case playwrightNavigationCheckedActionMarker + "|denied":
		return ErrDenied
	default:
		return ErrDriverIncompatible
	}
}

func (worker *playwrightWorker) CaptureScreenshot(
	ctx context.Context,
	maximumBytes int,
) (DriverScreenshot, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closing || worker.closed || worker.lost || worker.humanControl || maximumBytes <= 0 {
		return DriverScreenshot{}, ErrWorkerUnavailable
	}
	result, err := worker.client.CallTool(ctx, "browser_take_screenshot", map[string]any{
		"type": "png", "fullPage": false, "scale": "css",
	})
	if err != nil || result == nil {
		worker.lost = true
		return DriverScreenshot{}, ErrWorkerUnavailable
	}
	if result.IsError {
		return DriverScreenshot{}, ErrDriverRejected
	}
	if len(result.Content) > 4 {
		return DriverScreenshot{}, ErrDriverIncompatible
	}
	var image []byte
	textBytes := 0
	for _, content := range result.Content {
		switch value := content.(type) {
		case *sdkmcp.TextContent:
			textBytes += len(value.Text)
			if textBytes > playwrightDriverResponseBytes {
				return DriverScreenshot{}, ErrDriverIncompatible
			}
		case *sdkmcp.ImageContent:
			if image != nil || value.MIMEType != "image/png" || len(value.Data) == 0 ||
				len(value.Data) > maximumBytes {
				return DriverScreenshot{}, ErrDriverIncompatible
			}
			image = append([]byte(nil), value.Data...)
		default:
			return DriverScreenshot{}, ErrDriverIncompatible
		}
	}
	if len(image) == 0 {
		return DriverScreenshot{}, ErrDriverIncompatible
	}
	return DriverScreenshot{Data: image, ContentType: "image/png"}, nil
}

func (worker *playwrightWorker) Resolve(ctx context.Context, target string) (DriverElement, string, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closing || worker.closed || worker.lost || worker.humanControl || worker.pendingDialog != nil ||
		!playwrightTargetPattern.MatchString(target) {
		return DriverElement{}, "", ErrWorkerUnavailable
	}
	text, err := worker.callAndConsume(
		ctx, "browser_snapshot", map[string]any{"boxes": false, "target": target}, true,
	)
	if err != nil {
		return DriverElement{}, "", err
	}
	observation, err := parsePlaywrightObservation(
		text,
		worker.limits.SnapshotBytes,
		worker.limits.SnapshotRefs,
		worker.limits.ToolResultBytes,
	)
	if err != nil {
		return DriverElement{}, "", err
	}
	for _, element := range observation.Elements {
		if element.Target == target {
			return element, observation.Origin, nil
		}
	}
	return DriverElement{}, "", ErrStale
}

func (worker *playwrightWorker) CatalogRevision() string {
	return worker.catalogRevision
}

func (worker *playwrightWorker) Execute(ctx context.Context, action DriverAction) error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closing || worker.closed || worker.lost || worker.humanControl {
		return ErrWorkerUnavailable
	}
	if worker.pendingDialog != nil && action.Kind != DriverDialog {
		return ErrDriverRejected
	}
	if worker.pendingDialog == nil && action.Kind == DriverDialog {
		return ErrStale
	}
	tool, arguments, err := mapPlaywrightAction(action, worker.limits)
	if err != nil {
		return err
	}
	text, err := worker.callAndConsume(
		ctx, tool, arguments, playwrightActionIncludesSnapshot(action.Kind),
	)
	if err != nil {
		return err
	}
	if action.Kind != DriverCheck && action.Kind != DriverUncheck {
		return nil
	}
	if err = parsePlaywrightCheckAction(text); err != nil &&
		!errors.Is(err, ErrStale) && !errors.Is(err, ErrDenied) {
		worker.lost = true
	}
	return err
}

func (worker *playwrightWorker) Upload(ctx context.Context, action DriverAction) error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closing || worker.closed || worker.lost || worker.humanControl || worker.pendingDialog != nil ||
		action.Kind != DriverUpload || !playwrightTargetPattern.MatchString(action.Target) ||
		action.Value == "" || !filepath.IsAbs(action.Value) {
		return ErrWorkerUnavailable
	}
	info, err := os.Lstat(action.Value)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > int64(worker.limits.UploadBytes) ||
		info.Size() != action.ArtifactBytes || !validDigest(action.ArtifactSHA256) {
		return ErrDenied
	}
	file, err := os.Open(action.Value)
	if err != nil {
		return ErrDenied
	}
	digest := sha256.New()
	count, copyErr := io.Copy(digest, io.LimitReader(file, action.ArtifactBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || count != action.ArtifactBytes ||
		hex.EncodeToString(digest.Sum(nil)) != action.ArtifactSHA256 {
		return ErrDenied
	}
	text, err := worker.callRawText(ctx, "browser_click", map[string]any{
		"target": action.Target, "element": action.Element, "doubleClick": false, "button": "left",
	})
	if err != nil {
		return err
	}
	if strings.Count(text, "[File chooser]: can be handled by browser_file_upload") != 1 {
		return ErrDriverIncompatible
	}
	_, err = worker.callRawText(ctx, "browser_file_upload", map[string]any{"paths": []string{action.Value}})
	return err
}

func (worker *playwrightWorker) Download(
	ctx context.Context,
	action DriverAction,
	maximumBytes int64,
) (DriverDownload, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closing || worker.closed || worker.lost || worker.humanControl || worker.pendingDialog != nil ||
		action.Kind != DriverDownloadAction || !playwrightTargetPattern.MatchString(action.Target) ||
		maximumBytes < 1 || maximumBytes > int64(worker.limits.DownloadBytes) {
		return DriverDownload{}, ErrWorkerUnavailable
	}
	return worker.captureDownload(ctx, action, maximumBytes)
}

func (worker *playwrightWorker) callRawText(
	ctx context.Context,
	tool string,
	arguments map[string]any,
) (string, error) {
	denialsBefore := uint64(0)
	if worker.networkProxy != nil {
		denialsBefore = worker.networkProxy.Denials()
	}
	result, err := worker.client.CallTool(ctx, tool, arguments)
	if worker.networkProxy != nil && worker.networkProxy.Denials() > denialsBefore {
		return "", ErrDenied
	}
	if err != nil || result == nil {
		worker.lost = true
		return "", ErrWorkerUnavailable
	}
	text, err := boundedPlaywrightText(result, playwrightDriverResponseBytes)
	if err != nil {
		worker.lost = true
		return "", err
	}
	if result.IsError {
		return text, ErrDriverRejected
	}
	return text, nil
}

func (worker *playwrightWorker) pendingDialogObservationLocked() (DriverObservation, error) {
	if worker.pendingDialog == nil || worker.lastObservation.Origin == "" {
		worker.lost = true
		return DriverObservation{}, ErrDriverIncompatible
	}
	observation := worker.lastObservation
	observation.Snapshot = ""
	observation.Elements = nil
	observation.PendingDialog = cloneDialogObservation(worker.pendingDialog)
	return observation, nil
}

func playwrightActionIncludesSnapshot(kind DriverActionKind) bool {
	switch kind {
	case DriverNavigate, DriverClick, DriverFill, DriverSelect:
		return true
	default:
		return false
	}
}

func (worker *playwrightWorker) Close(ctx context.Context) error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closed {
		return nil
	}
	if !worker.closing {
		worker.closing = true
		// browser_close is best effort and must not be replayed. Closing the
		// private manager is the retryable process and exclusive-lease boundary.
		if !worker.lost {
			_, _ = worker.client.CallTool(ctx, "browser_close", map[string]any{})
		}
		if worker.cancelLifetime != nil {
			worker.cancelLifetime()
		}
	}
	clientErr := worker.client.Close()
	proxyErr := worker.networkProxy.Close()
	outputErr := error(nil)
	if worker.outputDir != "" {
		outputErr = os.RemoveAll(worker.outputDir)
	}
	if clientErr != nil || proxyErr != nil || outputErr != nil {
		return ErrWorkerUnavailable
	}
	worker.closed = true
	return nil
}

func (worker *playwrightWorker) callAndConsume(
	ctx context.Context,
	tool string,
	arguments map[string]any,
	allowSnapshotTail bool,
) (string, error) {
	denialsBefore := worker.networkProxy.Denials()
	result, err := worker.client.CallTool(ctx, tool, arguments)
	// A snapshot or the fixed context-catalog probe can overlap browser/profile
	// background traffic. The proxy still enforces every request, while the
	// broker independently validates projected origins. Do not attribute an
	// unrelated denied background request to either read-only observation.
	passiveRead := tool == "browser_snapshot" ||
		(tool == "browser_run_code_unsafe" && arguments["code"] == playwrightContextProbeCode)
	if !passiveRead && worker.networkProxy.Denials() > denialsBefore {
		return "", ErrDenied
	}
	if err != nil || result == nil {
		worker.lost = true
		return "", ErrWorkerUnavailable
	}
	driverErr := error(nil)
	if result.IsError {
		driverErr = ErrDriverRejected
	}
	text, err := boundedPlaywrightText(result, playwrightDriverResponseBytes)
	if err != nil {
		worker.lost = true
		return "", errors.Join(driverErr, err)
	}
	dialog, err := parsePlaywrightPendingDialog(text, allowSnapshotTail)
	if err != nil {
		worker.lost = true
		return "", errors.Join(driverErr, err)
	}
	if result.IsError && tool == "browser_handle_dialog" && worker.pendingDialog != nil && dialog == nil {
		// A rejected handler call without modal metadata does not establish
		// whether the known dialog closed. Do not guess and then issue another
		// potentially blocked MCP call; retire this worker instead.
		worker.lost = true
		return "", errors.Join(driverErr, ErrWorkerUnavailable)
	}
	if dialog != nil && worker.lastObservation.Origin == "" {
		worker.lost = true
		return "", errors.Join(driverErr, ErrDriverIncompatible)
	}
	worker.pendingDialog = dialog
	return text, driverErr
}

func mapPlaywrightAction(
	action DriverAction,
	limits config.BrowserLimitsConfig,
) (string, map[string]any, error) {
	if action.Kind != DriverDrag && (action.DestinationTarget != "" || action.DestinationElement != "") {
		return "", nil, fmt.Errorf("%w: malformed destination action", ErrInvalid)
	}
	switch action.Kind {
	case DriverNavigate:
		normalized, err := normalizeDriverNavigationURL(action.URL)
		if err != nil || action.Target != "" || action.Element != "" || action.Value != "" || action.Accept ||
			action.PromptProvided ||
			action.Key != "" ||
			action.Direction != "" ||
			action.Amount != 0 {
			return "", nil, fmt.Errorf("%w: malformed navigate action", ErrInvalid)
		}
		return "browser_navigate", map[string]any{"url": normalized}, nil
	case DriverClick:
		if !playwrightTargetPattern.MatchString(action.Target) || action.URL != "" || action.Accept ||
			action.PromptProvided ||
			action.Value != "" ||
			action.Key != "" ||
			action.Direction != "" ||
			action.Amount != 0 ||
			len(action.Element) > MaxElementNameBytes {
			return "", nil, fmt.Errorf("%w: malformed click action", ErrInvalid)
		}
		arguments := map[string]any{
			"target": action.Target, "doubleClick": false, "button": "left",
		}
		if action.Element != "" {
			arguments["element"] = action.Element
		}
		return "browser_click", arguments, nil
	case DriverFill:
		if !playwrightTargetPattern.MatchString(action.Target) || action.URL != "" || action.Accept ||
			action.PromptProvided ||
			len(action.Element) > MaxElementNameBytes ||
			len(action.Value) > limits.TextInputBytes ||
			action.Key != "" ||
			action.Direction != "" ||
			action.Amount != 0 {
			return "", nil, fmt.Errorf("%w: malformed fill action", ErrInvalid)
		}
		arguments := map[string]any{
			"target": action.Target, "text": action.Value, "submit": false, "slowly": false,
		}
		if action.Element != "" {
			arguments["element"] = action.Element
		}
		return "browser_type", arguments, nil
	case DriverSelect:
		if !playwrightTargetPattern.MatchString(action.Target) || action.URL != "" || action.Key != "" ||
			action.Accept ||
			action.PromptProvided ||
			action.Direction != "" ||
			action.Amount != 0 ||
			len(action.Element) > MaxElementNameBytes ||
			action.Value == "" ||
			len(action.Value) > limits.TextInputBytes {
			return "", nil, fmt.Errorf("%w: malformed select action", ErrInvalid)
		}
		arguments := map[string]any{"target": action.Target, "values": []string{action.Value}}
		if action.Element != "" {
			arguments["element"] = action.Element
		}
		return "browser_select_option", arguments, nil
	case DriverPress:
		if !validBrowserKey(action.Key) || action.URL != "" || action.Target != "" || action.Accept ||
			action.PromptProvided ||
			action.Element != "" ||
			action.Value != "" ||
			action.Direction != "" ||
			action.Amount != 0 {
			return "", nil, fmt.Errorf("%w: malformed press action", ErrInvalid)
		}
		return "browser_press_key", map[string]any{"key": action.Key}, nil
	case DriverScroll:
		if action.URL != "" || action.Target != "" || action.Element != "" || action.Value != "" || action.Accept ||
			action.PromptProvided ||
			action.Key != "" ||
			(action.Direction != "up" && action.Direction != "down") ||
			action.Amount < 1 ||
			action.Amount > MaxScrollAmount {
			return "", nil, fmt.Errorf("%w: malformed scroll action", ErrInvalid)
		}
		delta := action.Amount * 500
		if action.Direction == "up" {
			delta = -delta
		}
		return "browser_mouse_wheel", map[string]any{"deltaX": 0, "deltaY": delta}, nil
	case DriverDialog:
		if action.URL != "" || action.Target != "" || action.Element != "" || action.Key != "" ||
			action.Direction != "" || action.Amount != 0 || len(action.Value) > limits.TextInputBytes ||
			(!action.Accept && (action.Value != "" || action.PromptProvided)) ||
			(!action.PromptProvided && action.Value != "") {
			return "", nil, fmt.Errorf("%w: malformed dialog action", ErrInvalid)
		}
		arguments := map[string]any{"accept": action.Accept}
		if action.PromptProvided {
			arguments["promptText"] = action.Value
		}
		return "browser_handle_dialog", arguments, nil
	case DriverHover:
		if !validPlaywrightElementAction(action, false) {
			return "", nil, fmt.Errorf("%w: malformed hover action", ErrInvalid)
		}
		arguments := map[string]any{"target": action.Target}
		if action.Element != "" {
			arguments["element"] = action.Element
		}
		return "browser_hover", arguments, nil
	case DriverDrag:
		if !validPlaywrightElementAction(action, true) ||
			!playwrightTargetPattern.MatchString(action.DestinationTarget) ||
			action.DestinationTarget == action.Target || len(action.DestinationElement) > MaxElementNameBytes {
			return "", nil, fmt.Errorf("%w: malformed drag action", ErrInvalid)
		}
		arguments := map[string]any{
			"startRef": action.Target, "endRef": action.DestinationTarget,
			"startElement": action.Element, "endElement": action.DestinationElement,
		}
		return "browser_drag", arguments, nil
	case DriverCheck, DriverUncheck:
		if !validPlaywrightElementAction(action, false) {
			return "", nil, fmt.Errorf("%w: malformed %s action", ErrInvalid, action.Kind)
		}
		return "browser_run_code_unsafe", map[string]any{
			"code": playwrightCheckActionCode(action.Target, action.Kind == DriverCheck),
		}, nil
	default:
		return "", nil, fmt.Errorf("%w: unsupported driver action", ErrInvalid)
	}
}

func validPlaywrightElementAction(action DriverAction, allowDestination bool) bool {
	return playwrightTargetPattern.MatchString(action.Target) && action.URL == "" && action.Value == "" &&
		action.Key == "" && action.Direction == "" && action.Amount == 0 && !action.Accept &&
		!action.PromptProvided && len(action.Element) <= MaxElementNameBytes &&
		(allowDestination || (action.DestinationTarget == "" && action.DestinationElement == ""))
}

func playwrightCheckActionCode(target string, checked bool) string {
	encoded, _ := json.Marshal(target)
	operation := "uncheck"
	if checked {
		operation = "check"
	}
	return `async (page) => {
  const control = page.locator("aria-ref=" + ` + string(encoded) + `);
  if (await control.count() !== 1 || !await control.isVisible()) return "MINTCLAW_CHECK_V1|stale";
  if (!` + strconv.FormatBool(checked) + ` && await control.getAttribute("type") === "radio") {
    return "MINTCLAW_CHECK_V1|denied";
  }
  await control.click({ trial: true });
  if (await control.isChecked() === ` + strconv.FormatBool(checked) + `) return "MINTCLAW_CHECK_V1|no_change";
  await control.` + operation + `();
  if (await control.isChecked() !== ` + strconv.FormatBool(checked) + `) throw new Error("final_state_mismatch");
  return "MINTCLAW_CHECK_V1|completed";
}`
}

func parsePlaywrightCheckAction(text string) error {
	const resultHeader = "### Result"
	if strings.Count(text, resultHeader) != 1 {
		return ErrDriverIncompatible
	}
	result := strings.TrimLeft(text[strings.Index(text, resultHeader)+len(resultHeader):], "\r\n")
	if end := strings.IndexByte(result, '\n'); end >= 0 {
		result = result[:end]
	}
	switch strings.Trim(result, "\r\"' ") {
	case playwrightCheckActionMarker + "|no_change", playwrightCheckActionMarker + "|completed":
		return nil
	case playwrightCheckActionMarker + "|stale":
		return ErrStale
	case playwrightCheckActionMarker + "|denied":
		return ErrDenied
	default:
		return ErrDriverIncompatible
	}
}

func normalizeDriverNavigationURL(raw string) (string, error) {
	if raw == "" || len(raw) > 2048 || strings.TrimSpace(raw) != raw {
		return "", ErrInvalid
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" {
		return "", ErrInvalid
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ErrInvalid
	}
	origin, normalizedOrigin, err := normalizeParsedBrowserHTTPOrigin(parsed)
	if err != nil || origin == "" || normalizedOrigin == nil {
		return "", ErrInvalid
	}
	parsed.Host = normalizedOrigin.Host
	return parsed.String(), nil
}

func boundedPlaywrightText(result *sdkmcp.CallToolResult, maximum int) (string, error) {
	if result == nil || maximum <= 0 {
		return "", ErrDriverIncompatible
	}
	var builder strings.Builder
	for _, content := range result.Content {
		text, ok := content.(*sdkmcp.TextContent)
		if !ok || text == nil {
			return "", ErrDriverIncompatible
		}
		if builder.Len() != 0 {
			builder.WriteByte('\n')
		}
		if builder.Len()+len(text.Text) > maximum {
			return "", ErrDriverIncompatible
		}
		builder.WriteString(text.Text)
	}
	if builder.Len() == 0 {
		return "", ErrDriverIncompatible
	}
	return builder.String(), nil
}

func parsePlaywrightObservation(
	text string,
	maximumSnapshotBytes int,
	maximumSnapshotRefs int,
	maximumToolResultBytes int,
) (DriverObservation, error) {
	pageURL := extractPlaywrightLine(text, "- Page URL: ")
	title := extractPlaywrightLine(text, "- Page Title: ")
	marker := "### Snapshot\n```yaml\n"
	start := strings.Index(text, marker)
	if pageURL == "" || start < 0 {
		return DriverObservation{}, ErrDriverIncompatible
	}
	start += len(marker)
	end := strings.Index(text[start:], "\n```")
	if end < 0 {
		return DriverObservation{}, ErrDriverIncompatible
	}
	snapshot := text[start : start+end]
	referenceTokens := playwrightSnapshotRefToken.FindAllStringIndex(snapshot, -1)
	targetReferences := playwrightSnapshotTargetRef.FindAllStringSubmatch(snapshot, -1)
	if pageURL == initialBlankOrigin {
		if maximumSnapshotBytes <= 0 || maximumSnapshotRefs <= 0 ||
			maximumToolResultBytes < config.BrowserToolResultEnvelopeBytes || title != "" || snapshot != "" ||
			len(referenceTokens) != 0 || len(targetReferences) != 0 {
			return DriverObservation{}, ErrDriverIncompatible
		}
		return DriverObservation{URL: initialBlankOrigin, Origin: initialBlankOrigin}, nil
	}
	if snapshot == "" || len(title) > 1024 || maximumSnapshotBytes <= 0 || maximumSnapshotRefs <= 0 ||
		maximumToolResultBytes < config.BrowserToolResultEnvelopeBytes ||
		len(referenceTokens) != len(targetReferences) {
		return DriverObservation{}, ErrDriverIncompatible
	}
	safeURL, origin, err := sanitizeObservedURL(pageURL)
	if err != nil {
		return DriverObservation{}, ErrDriverIncompatible
	}
	projected, truncated, err := projectPlaywrightSnapshot(
		snapshot,
		maximumSnapshotBytes,
		maximumSnapshotRefs,
		maximumToolResultBytes-config.BrowserToolResultEnvelopeBytes,
	)
	if err != nil {
		return DriverObservation{}, err
	}
	elements := parsePlaywrightElements(projected)
	return DriverObservation{
		URL: safeURL, Origin: origin, Title: title, Snapshot: projected, Elements: elements,
		Truncated: truncated,
	}, nil
}

func projectPlaywrightSnapshot(
	snapshot string,
	maximumBytes int,
	maximumRefs int,
	maximumEncodedBytes int,
) (string, bool, error) {
	if snapshot == "" || maximumBytes <= 0 || maximumRefs <= 0 || maximumEncodedBytes < 0 {
		return "", false, ErrDriverIncompatible
	}
	if visiblePlaywrightSnapshotBytes(snapshot) <= maximumBytes &&
		encodedVisiblePlaywrightSnapshotBytes(snapshot) <= maximumEncodedBytes &&
		len(playwrightSnapshotTargetRef.FindAllStringSubmatch(snapshot, -1)) <= maximumRefs {
		return snapshot, false, nil
	}
	var projected strings.Builder
	retainedRefs := 0
	projectedVisibleBytes := 0
	projectedEncodedBytes := 0
	for _, line := range strings.SplitAfter(snapshot, "\n") {
		lineTargets := playwrightSnapshotTargetRef.FindAllStringSubmatch(line, -1)
		lineVisibleBytes := visiblePlaywrightSnapshotBytes(line)
		lineEncodedBytes := encodedVisiblePlaywrightSnapshotBytes(line)
		if projectedVisibleBytes+lineVisibleBytes > maximumBytes ||
			projectedEncodedBytes+lineEncodedBytes > maximumEncodedBytes ||
			retainedRefs+len(lineTargets) > maximumRefs {
			break
		}
		projected.WriteString(line)
		retainedRefs += len(lineTargets)
		projectedVisibleBytes += lineVisibleBytes
		projectedEncodedBytes += lineEncodedBytes
	}
	result := strings.TrimSuffix(projected.String(), "\n")
	return result, true, nil
}

func visiblePlaywrightSnapshotBytes(snapshot string) int {
	visibleBytes := len(snapshot)
	for _, target := range playwrightSnapshotTargetRef.FindAllStringSubmatch(snapshot, -1) {
		visibleBytes += opaqueSnapshotReferenceBytes - len(target[1])
	}
	return visibleBytes
}

func encodedVisiblePlaywrightSnapshotBytes(snapshot string) int {
	visible := playwrightSnapshotTargetRef.ReplaceAllString(
		snapshot,
		"[ref=ref_00000000000000000000000000000000]",
	)
	return encodedJSONStringBytes(visible)
}

func encodedJSONStringBytes(value string) int {
	encoded, _ := json.Marshal(value)
	return len(encoded) - len(`""`)
}

func parsePlaywrightPendingDialog(
	text string,
	allowSnapshotTail bool,
) (*DialogObservation, error) {
	lines := strings.Split(text, "\n")
	modalHeader := -1
	inFence := false
	for index, line := range lines {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if line == "### Modal state" {
			if modalHeader >= 0 {
				return nil, ErrDriverIncompatible
			}
			modalHeader = index
		}
	}
	if inFence {
		return nil, ErrDriverIncompatible
	}
	if modalHeader < 0 {
		return nil, nil
	}
	if modalHeader+1 >= len(lines) {
		return nil, ErrDriverIncompatible
	}
	match := playwrightDialogPattern.FindStringSubmatch(lines[modalHeader+1])
	if len(match) != 3 || !validDialogType(match[1]) || len(match[2]) > MaxDialogMessageBytes {
		return nil, ErrDriverIncompatible
	}
	tail := trimEmptyPlaywrightLines(lines[modalHeader+2:])
	if len(tail) != 0 {
		if !allowSnapshotTail || !validPlaywrightSnapshotTail(tail) {
			return nil, ErrDriverIncompatible
		}
	}
	return &DialogObservation{Type: match[1], Message: match[2]}, nil
}

func trimEmptyPlaywrightLines(lines []string) []string {
	for len(lines) != 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) != 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func validPlaywrightSnapshotTail(lines []string) bool {
	if len(lines) == 2 && lines[0] == "### Snapshot" &&
		playwrightSnapshotLinkPattern.MatchString(lines[1]) {
		return true
	}
	if len(lines) < 3 || lines[0] != "### Snapshot" || lines[1] != "```yaml" ||
		lines[len(lines)-1] != "```" {
		return false
	}
	for _, line := range lines[2 : len(lines)-1] {
		if line == "```" || strings.HasPrefix(line, "### ") {
			return false
		}
	}
	return true
}

func parsePlaywrightElements(snapshot string) []DriverElement {
	semantics := make(map[string]DriverElement)
	for _, match := range playwrightElementPattern.FindAllStringSubmatch(snapshot, -1) {
		semantics[match[3]] = DriverElement{
			Target: match[3], Role: strings.ToLower(match[1]), Name: match[2],
		}
	}
	seen := make(map[string]struct{})
	refs := playwrightSnapshotTargetRef.FindAllStringSubmatch(snapshot, -1)
	elements := make([]DriverElement, 0, len(refs))
	for _, match := range refs {
		target := match[1]
		if _, exists := seen[target]; exists {
			continue
		}
		seen[target] = struct{}{}
		element, ok := semantics[target]
		if !ok {
			element = DriverElement{Target: target, Role: "unknown"}
		}
		elements = append(elements, element)
	}
	return elements
}

func extractPlaywrightLine(text, prefix string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func sanitizeObservedURL(raw string) (string, string, error) {
	if len(raw) > MaxURLBytes {
		return "", "", ErrInvalid
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return "", "", ErrInvalid
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", ErrInvalid
	}
	origin, normalizedOrigin, err := normalizeParsedBrowserHTTPOrigin(parsed)
	if err != nil || normalizedOrigin == nil {
		return "", "", ErrInvalid
	}
	parsed.Host = normalizedOrigin.Host
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	safeURL := parsed.String()
	if len(safeURL) > MaxURLBytes || len(origin) > MaxURLBytes {
		return "", "", ErrInvalid
	}
	return safeURL, origin, nil
}

func normalizeParsedBrowserHTTPOrigin(parsed *url.URL) (string, *url.URL, error) {
	if parsed == nil {
		return "", nil, ErrInvalid
	}
	// URL.String re-escapes a decoded RFC 6874 IPv6 zone while leaving its
	// case intact. Reconstructing scheme + "://" + Host would produce an
	// invalid bare percent and can silently change zone identity.
	rawOrigin := (&url.URL{Scheme: strings.ToLower(parsed.Scheme), Host: parsed.Host}).String()
	origin, err := config.NormalizeBrowserHTTPOrigin(rawOrigin)
	if err != nil {
		return "", nil, ErrInvalid
	}
	normalized, err := url.Parse(origin)
	if err != nil || normalized.Host == "" {
		return "", nil, ErrInvalid
	}
	return origin, normalized, nil
}

var pinnedPlaywrightToolSchemas = map[string]json.RawMessage{
	"browser_close": json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"additionalProperties":false,
		"properties":{},
		"type":"object"
	}`),
	"browser_navigate": json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"additionalProperties":false,
		"properties":{"url":{"description":"The URL to navigate to","type":"string"}},
		"required":["url"],
		"type":"object"
	}`),
	"browser_snapshot": json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"additionalProperties":false,
		"properties":{
			"boxes":{"description":"Include each element's bounding box as [box=x,y,width,height] in the snapshot. Coordinates are viewport-relative, in CSS pixels (Element.getBoundingClientRect)","type":"boolean"},
			"depth":{"description":"Limit the depth of the snapshot tree","type":"number"},
			"filename":{"description":"Save snapshot to markdown file instead of returning it in the response.","type":"string"},
			"target":{"description":"Exact target element reference from the page snapshot, or a unique element selector","type":"string"}
		},
		"type":"object"
	}`),
	"browser_tabs": json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"additionalProperties":false,
		"properties":{
			"action":{"description":"Operation to perform","enum":["list","new","close","select"],"type":"string"},
			"index":{"description":"Tab index, used for close/select. If omitted for close, current tab is closed.","type":"number"},
			"url":{"description":"URL to navigate to in the new tab, used for new.","type":"string"}
		},
		"required":["action"],
		"type":"object"
	}`),
	"browser_take_screenshot": json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"additionalProperties":false,
		"properties":{
			"element":{"description":"Human-readable element description used to obtain permission to interact with the element","type":"string"},
			"filename":{"description":"File name to save the screenshot to. Defaults to ` + "`page-{timestamp}.{png|jpeg}`" + ` if not specified. Prefer relative file names to stay within the output directory.","type":"string"},
			"fullPage":{"description":"When true, takes a screenshot of the full scrollable page, instead of the currently visible viewport. Cannot be used with element screenshots.","type":"boolean"},
			"scale":{"default":"css","description":"Image resolution scale. \"css\" produces a screenshot sized in CSS pixels (smaller, consistent across devices). \"device\" produces a high-resolution screenshot using device pixels (larger, accounts for the device pixel ratio). Default is css.","enum":["css","device"],"type":"string"},
			"target":{"description":"Exact target element reference from the page snapshot, or a unique element selector","type":"string"},
			"type":{"default":"png","description":"Image format for the screenshot. Default is png.","enum":["png","jpeg"],"type":"string"}
		},
		"required":["type","scale"],
		"type":"object"
	}`),
	"browser_click": json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"additionalProperties":false,
		"properties":{
			"button":{"description":"Button to click, defaults to left","enum":["left","right","middle"],"type":"string"},
			"doubleClick":{"description":"Whether to perform a double click instead of a single click","type":"boolean"},
			"element":{"description":"Human-readable element description used to obtain permission to interact with the element","type":"string"},
			"modifiers":{"description":"Modifier keys to press","items":{"enum":["Alt","Control","ControlOrMeta","Meta","Shift"],"type":"string"},"type":"array"},
			"target":{"description":"Exact target element reference from the page snapshot, or a unique element selector","type":"string"}
		},
		"required":["target"],
		"type":"object"
	}`),
	//nolint:misspell // The external driver schema is pinned verbatim.
	"browser_file_upload": json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"additionalProperties":false,
		"properties":{"paths":{"description":"The absolute paths to the files to upload. Can be single file or multiple files. If omitted, file chooser is cancelled.","items":{"type":"string"},"type":"array"}},
		"type":"object"
	}`),
	"browser_run_code_unsafe": json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"additionalProperties":false,
		"properties":{
			"code":{"description":"A JavaScript function containing Playwright code to execute. It will be invoked with a single argument, page, which you can use for any page interaction. For example: ` + "`async (page) => { await page.getByRole('button', { name: 'Submit' }).click(); return await page.title(); }`" + `","type":"string"},
			"filename":{"description":"Load code from the specified file. If both code and filename are provided, code will be ignored.","type":"string"}
		},
		"type":"object"
	}`),
	"browser_type": json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"additionalProperties":false,
		"properties":{
			"element":{"description":"Human-readable element description used to obtain permission to interact with the element","type":"string"},
			"slowly":{"description":"Whether to type one character at a time. Useful for triggering key handlers in the page. By default entire text is filled in at once.","type":"boolean"},
			"submit":{"description":"Whether to submit entered text (press Enter after)","type":"boolean"},
			"target":{"description":"Exact target element reference from the page snapshot, or a unique element selector","type":"string"},
			"text":{"description":"Text to type into the element","type":"string"}
		},
		"required":["target","text"],
		"type":"object"
	}`),
	"browser_select_option": json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"additionalProperties":false,
		"properties":{
			"element":{"description":"Human-readable element description used to obtain permission to interact with the element","type":"string"},
			"target":{"description":"Exact target element reference from the page snapshot, or a unique element selector","type":"string"},
			"values":{"description":"Array of values to select in the dropdown. This can be a single value or multiple values.","items":{"type":"string"},"type":"array"}
		},
		"required":["target","values"],
		"type":"object"
	}`),
	"browser_press_key": json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"additionalProperties":false,
		"properties":{"key":{"description":"Name of the key to press or a character to generate, such as ` + "`ArrowLeft` or `a`" + `","type":"string"}},
		"required":["key"],
		"type":"object"
	}`),
	"browser_mouse_wheel": json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"additionalProperties":false,
		"properties":{
			"deltaX":{"default":0,"description":"X delta","type":"number"},
			"deltaY":{"default":0,"description":"Y delta","type":"number"}
		},
		"required":["deltaX","deltaY"],
		"type":"object"
	}`),
	"browser_handle_dialog": json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"additionalProperties":false,
		"properties":{
			"accept":{"description":"Whether to accept the dialog.","type":"boolean"},
			"promptText":{"description":"The text of the prompt in case of a prompt dialog.","type":"string"}
		},
		"required":["accept"],
		"type":"object"
	}`),
}

func validatePlaywrightCatalog(tools []*sdkmcp.Tool) (string, error) {
	available := make(map[string]*sdkmcp.Tool, len(tools))
	for _, tool := range tools {
		if tool != nil {
			available[tool.Name] = tool
		}
	}
	names := make([]string, 0, len(pinnedPlaywrightToolSchemas))
	for name, expected := range pinnedPlaywrightToolSchemas {
		tool := available[name]
		actualSchema, actualErr := canonicalPlaywrightSchema(toolSchema(tool))
		expectedSchema, expectedErr := canonicalPlaywrightSchema(expected)
		if tool == nil || actualErr != nil || expectedErr != nil || !bytes.Equal(actualSchema, expectedSchema) {
			return "", ErrDriverIncompatible
		}
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		encoded, err := canonicalPlaywrightSchema(available[name].InputSchema)
		if err != nil {
			return "", ErrDriverIncompatible
		}
		_, _ = hash.Write([]byte(name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(encoded)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func toolSchema(tool *sdkmcp.Tool) any {
	if tool == nil {
		return nil
	}
	return tool.InputSchema
}

func canonicalPlaywrightSchema(schema any) ([]byte, error) {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err = json.Unmarshal(encoded, &normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func cloneMCPServerConfig(source config.MCPServerConfig) config.MCPServerConfig {
	cloned := source
	cloned.Args = append([]string(nil), source.Args...)
	cloned.VisibleTools = append([]string(nil), source.VisibleTools...)
	cloned.Env = cloneStringMap(source.Env)
	cloned.Headers = cloneStringMap(source.Headers)
	return cloned
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
