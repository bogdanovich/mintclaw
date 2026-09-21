package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	steelapi "github.com/steel-dev/steel-go"

	"github.com/bogdanovich/mintclaw/pkg/config"
	localmcp "github.com/bogdanovich/mintclaw/pkg/mcp"
)

const (
	steelAPIBaseURL           = "https://api.steel.dev"
	steelProviderStateBytes   = 4096
	steelProviderStateSchema  = 1
	steelProviderAPITimeout   = 20 * time.Second
	steelProviderCloseTimeout = 15 * time.Second
)

var steelNativeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type steelProviderSessionRequest struct {
	ProfileID        string
	Timeout          time.Duration
	Inactivity       time.Duration
	PersistProfile   bool
	InteractiveDebug bool
}

type steelProviderSession struct {
	SessionID   string
	ProfileID   string
	Status      string
	CDPEndpoint string
	DebugURL    string
}

type steelProviderProfile struct {
	Status string
}

type steelRuntimeClient interface {
	CreateSession(context.Context, steelProviderSessionRequest) (steelProviderSession, error)
	ReleaseSession(context.Context, string) error
	GetProfile(context.Context, string) (steelProviderProfile, error)
}

type steelSDKRuntimeClient struct {
	client *steelapi.Client
}

func newSteelSDKRuntimeClient(apiKey string) steelRuntimeClient {
	return &steelSDKRuntimeClient{client: steelapi.NewClientWithBaseURL(
		apiKey,
		steelAPIBaseURL,
		steelapi.WithTimeout(steelProviderAPITimeout),
		steelapi.WithMaxRetries(2),
		steelapi.WithHTTPClient(&http.Client{}),
	)}
}

func (client *steelSDKRuntimeClient) CreateSession(
	ctx context.Context,
	request steelProviderSessionRequest,
) (steelProviderSession, error) {
	if client == nil || client.client == nil {
		return steelProviderSession{}, ErrProviderUnavailable
	}
	params := steelapi.SessionCreateParams{
		Headless:          steelapi.Bool(false),
		PersistProfile:    steelapi.Bool(request.PersistProfile),
		Timeout:           steelapi.Int(request.Timeout.Milliseconds()),
		InactivityTimeout: steelapi.Int(request.Inactivity.Milliseconds()),
		DebugConfig: steelapi.F(steelapi.SessionCreateParamsDebugConfig{
			Interactive: steelapi.Bool(request.InteractiveDebug),
		}),
	}
	if request.ProfileID != "" {
		params.ProfileID = steelapi.String(request.ProfileID)
	}
	session, err := client.client.Sessions.Create(ctx, params)
	if err != nil {
		return steelProviderSession{}, classifySteelAPIError(ctx, err)
	}
	return steelProviderSession{
		SessionID: session.ID, ProfileID: session.ProfileID,
		Status: string(session.Status), CDPEndpoint: session.WebsocketURL,
		DebugURL: session.DebugURL,
	}, nil
}

func (client *steelSDKRuntimeClient) ReleaseSession(ctx context.Context, sessionID string) error {
	if client == nil || client.client == nil {
		return ErrProviderUnavailable
	}
	released, err := client.client.Sessions.Release(ctx, sessionID, steelapi.SessionReleaseParams{})
	if err != nil {
		var apiErr *steelapi.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return nil
		}
		return classifySteelAPIError(ctx, err)
	}
	if released == nil || !released.Success {
		return ErrProviderUnavailable
	}
	return nil
}

func (client *steelSDKRuntimeClient) GetProfile(
	ctx context.Context,
	profileID string,
) (steelProviderProfile, error) {
	if client == nil || client.client == nil {
		return steelProviderProfile{}, ErrProviderUnavailable
	}
	profile, err := client.client.Profiles.Get(ctx, profileID, steelapi.ProfileGetParams{})
	if err != nil {
		var apiErr *steelapi.APIError
		if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusNotFound ||
			apiErr.StatusCode == http.StatusConflict) {
			return steelProviderProfile{}, ErrProviderProfileNotReady
		}
		return steelProviderProfile{}, classifySteelAPIError(ctx, err)
	}
	return steelProviderProfile{Status: string(profile.Status)}, nil
}

func classifySteelAPIError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ErrProviderTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrProviderTimeout
	}
	var apiErr *steelapi.APIError
	if !errors.As(err, &apiErr) {
		return ErrProviderUnavailable
	}
	switch apiErr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrProviderAuthentication
	case http.StatusConflict:
		return ErrProviderProfileNotReady
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return ErrProviderTimeout
	case http.StatusTooManyRequests:
		return ErrProviderQuota
	default:
		return ErrProviderUnavailable
	}
}

type steelPlaywrightRuntimeProvider struct {
	factory     *PlaywrightWorkerFactory
	provider    config.BrowserSteelProviderConfig
	client      steelRuntimeClient
	concurrency chan struct{}
}

type steelPlaywrightRuntime struct {
	provider    *steelPlaywrightRuntimeProvider
	client      steelRuntimeClient
	profile     config.BrowserProfileConfig
	stateFile   string
	lease       *localmcp.ExclusiveServerLease
	sessionID   string
	profileID   string
	cdpURL      string
	debugURL    string
	networkMode string
	origins     []string

	mu               sync.Mutex
	sessionReleased  bool
	profilePersisted bool
	closed           bool
}

type steelProviderState struct {
	Version   int    `json:"version"`
	ProfileID string `json:"profile_id"`
}

func newSteelPlaywrightProfileWorkerFactory(
	targetName string,
	profileName string,
	target config.BrowserTargetConfig,
	profile config.BrowserProfileConfig,
) (*PlaywrightWorkerFactory, error) {
	if target.EffectivePlacement() != config.BrowserPlacementCloud ||
		target.EffectiveProvider() != config.BrowserProviderSteel ||
		target.Driver != config.BrowserDriverPlaywrightLibrary ||
		profile.Mode != config.BrowserProfileManaged {
		return nil, ErrDenied
	}
	runtimeConfig, err := normalizeSteelProfileRuntime(profile.Runtime)
	if err != nil {
		return nil, err
	}
	profile = clonePlaywrightProfileConfig(profile)
	profile.Runtime = runtimeConfig
	server := config.MCPServerConfig{
		Type: "stdio", Command: target.DriverExecutable,
		Args:              append([]string(nil), target.DriverArguments...),
		ExclusiveLockFile: runtimeConfig.LockFile,
	}
	if err = validatePlaywrightManagedPolicy(server); err != nil {
		return nil, err
	}
	if profile.PrivilegedExecution.Enabled {
		server.Args = append(server.Args, "--privileged-execution")
	}
	factory := &PlaywrightWorkerFactory{
		target: targetName, profileName: profileName, profileConfig: profile,
		serverConfig:  cloneMCPServerConfig(server),
		downloadReady: playwrightServerDownloadAvailable(server),
		clientFactory: newLibraryPlaywrightClient,
		lookPath:      exec.LookPath,
	}
	provider := &steelPlaywrightRuntimeProvider{
		factory: factory, provider: target.Steel,
		client:      newSteelSDKRuntimeClient(target.Steel.APIKeyRef.String()),
		concurrency: make(chan struct{}, target.Steel.Concurrency),
	}
	factory.runtime = provider
	factory.driver = &playwrightProcessControlDriver{factory: factory}
	return factory, nil
}

func normalizeSteelProfileRuntime(
	runtimeConfig config.BrowserProfileRuntimeConfig,
) (config.BrowserProfileRuntimeConfig, error) {
	if runtimeConfig.ProfileDirectory != "" || runtimeConfig.EphemeralRoot != "" ||
		!runtimeConfig.Headed {
		return config.BrowserProfileRuntimeConfig{}, errors.New("cloud browser runtime identity is invalid")
	}
	stateFile := filepath.Clean(runtimeConfig.ProviderStateFile)
	lockFile := filepath.Clean(runtimeConfig.LockFile)
	if !filepath.IsAbs(stateFile) || !filepath.IsAbs(lockFile) ||
		stateFile == string(filepath.Separator) || lockFile == string(filepath.Separator) ||
		stateFile == lockFile {
		return config.BrowserProfileRuntimeConfig{}, errors.New("cloud browser runtime paths are unsafe")
	}
	normalizedState, err := normalizeSteelPrivateFile(stateFile, "provider state")
	if err != nil {
		return config.BrowserProfileRuntimeConfig{}, err
	}
	normalizedLock, err := normalizeSteelPrivateFile(lockFile, "profile lock")
	if err != nil {
		return config.BrowserProfileRuntimeConfig{}, err
	}
	runtimeConfig.ProviderStateFile = normalizedState
	runtimeConfig.LockFile = normalizedLock
	return runtimeConfig, nil
}

func normalizeSteelPrivateFile(path string, label string) (string, error) {
	parent := filepath.Dir(path)
	configured, err := os.Lstat(parent)
	if err != nil || configured.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("cloud browser %s parent identity is unsafe", label)
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil || filepath.Clean(realParent) != parent {
		return "", fmt.Errorf("cloud browser %s parent identity is unsafe", label)
	}
	parentInfo, err := os.Lstat(realParent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 ||
		validateBrowserRuntimeOwner(parentInfo, true) != nil {
		return "", fmt.Errorf("cloud browser %s parent is not private to the gateway account", label)
	}
	path = filepath.Join(realParent, filepath.Base(path))
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			validateBrowserRuntimeOwner(info, false) != nil {
			return "", fmt.Errorf("cloud browser %s identity is unsafe", label)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect cloud browser %s: %w", label, err)
	}
	return path, nil
}

func (provider *steelPlaywrightRuntimeProvider) Provision(
	ctx context.Context,
	_ WorkerOpenRequest,
) (playwrightRuntime, error) {
	if provider == nil || provider.factory == nil || provider.client == nil ||
		cap(provider.concurrency) == 0 {
		return nil, errors.Join(ErrProviderUnavailable, ErrWorkerUnavailable)
	}
	select {
	case provider.concurrency <- struct{}{}:
	default:
		return nil, ErrCapacity
	}
	releaseSlot := true
	defer func() {
		if releaseSlot {
			provider.releaseSlot()
		}
	}()

	lease, err := localmcp.AcquireExclusiveServerLease(
		playwrightPrivateServerName,
		provider.factory.profileConfig.Runtime.LockFile,
	)
	if err != nil {
		var busy *localmcp.ExclusiveLeaseBusyError
		if errors.As(err, &busy) {
			return nil, ErrBusy
		}
		return nil, errors.Join(ErrProviderUnavailable, ErrWorkerUnavailable)
	}
	releaseLease := true
	defer func() {
		if releaseLease {
			_ = lease.Close()
		}
	}()

	profileID, err := readSteelProviderState(provider.factory.profileConfig.Runtime.ProviderStateFile)
	if err != nil {
		return nil, errors.Join(ErrProviderUnavailable, ErrWorkerUnavailable)
	}
	if profileID != "" {
		remoteProfile, profileErr := provider.client.GetProfile(ctx, profileID)
		if profileErr != nil {
			return nil, errors.Join(profileErr, ErrWorkerUnavailable)
		}
		switch remoteProfile.Status {
		case string(steelapi.ProfileStatusReady):
		case string(steelapi.ProfileStatusUploading):
			return nil, errors.Join(ErrProviderProfileNotReady, ErrWorkerUnavailable)
		default:
			return nil, errors.Join(ErrProviderUnavailable, ErrWorkerUnavailable)
		}
	}
	maximum := min(provider.provider.SessionTimeoutSeconds, provider.provider.MaxBillableSeconds)
	created, err := provider.client.CreateSession(ctx, steelProviderSessionRequest{
		ProfileID: profileID, Timeout: time.Duration(maximum) * time.Second,
		Inactivity:     time.Duration(provider.provider.InactivityTimeoutSeconds) * time.Second,
		PersistProfile: true, InteractiveDebug: true,
	})
	if err != nil {
		return nil, errors.Join(err, ErrWorkerUnavailable)
	}
	created.CDPEndpoint, err = authenticatedSteelCDPEndpoint(
		created.CDPEndpoint,
		created.SessionID,
		provider.provider.APIKeyRef.String(),
	)
	if err != nil || !validSteelProviderSession(created, profileID) {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), steelProviderCloseTimeout)
		cleanupErr := provider.client.ReleaseSession(cleanupCtx, created.SessionID)
		cancel()
		if cleanupErr != nil {
			return nil, errors.Join(ErrDriverIncompatible, ErrWorkerUnavailable, ErrCleanupRequired)
		}
		return nil, errors.Join(ErrDriverIncompatible, ErrWorkerUnavailable)
	}
	if err = writeSteelProviderState(
		provider.factory.profileConfig.Runtime.ProviderStateFile,
		created.ProfileID,
	); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), steelProviderCloseTimeout)
		cleanupErr := provider.client.ReleaseSession(cleanupCtx, created.SessionID)
		cancel()
		if cleanupErr != nil {
			return nil, errors.Join(ErrProviderUnavailable, ErrWorkerUnavailable, ErrCleanupRequired)
		}
		return nil, errors.Join(ErrProviderUnavailable, ErrWorkerUnavailable)
	}
	runtimeHandle := &steelPlaywrightRuntime{
		provider: provider, client: provider.client,
		profile:   provider.factory.profileConfig,
		stateFile: provider.factory.profileConfig.Runtime.ProviderStateFile,
		lease:     lease, sessionID: created.SessionID, profileID: created.ProfileID,
		cdpURL: created.CDPEndpoint, debugURL: created.DebugURL,
		networkMode:      provider.factory.profileConfig.NetworkMode,
		origins:          append([]string(nil), provider.factory.profileConfig.AllowedOrigins...),
		profilePersisted: true,
	}
	releaseLease = false
	releaseSlot = false
	return runtimeHandle, nil
}

func validSteelProviderSession(session steelProviderSession, expectedProfileID string) bool {
	if session.Status != string(steelapi.SessionResponseStatusLive) ||
		!steelNativeIDPattern.MatchString(session.SessionID) ||
		!steelNativeIDPattern.MatchString(session.ProfileID) || session.CDPEndpoint == "" ||
		!validSteelDebugURL(session.DebugURL) {
		return false
	}
	return expectedProfileID == "" || session.ProfileID == expectedProfileID
}

func validSteelDebugURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 4096 {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" &&
		parsed.User == nil && parsed.Fragment == ""
}

func authenticatedSteelCDPEndpoint(raw string, sessionID string, apiKey string) (string, error) {
	if len(raw) == 0 || len(raw) > 4096 || !steelNativeIDPattern.MatchString(sessionID) ||
		strings.TrimSpace(apiKey) == "" {
		return "", errors.New("invalid Steel CDP endpoint")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "wss" || parsed.Host == "" || parsed.User != nil ||
		parsed.Fragment != "" {
		return "", errors.New("invalid Steel CDP endpoint")
	}
	query := parsed.Query()
	if endpointSessionIDs, ok := query["sessionId"]; ok &&
		(len(endpointSessionIDs) != 1 || endpointSessionIDs[0] != sessionID) {
		return "", errors.New("invalid Steel CDP endpoint")
	}
	query.Set("apiKey", apiKey)
	query.Set("sessionId", sessionID)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func readSteelProviderState(path string) (string, error) {
	if err := prepareStorePath(path); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	current, currentErr := os.Lstat(path)
	if err != nil || currentErr != nil || !info.Mode().IsRegular() ||
		current.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, current) ||
		validateBrowserRuntimeOwner(info, false) != nil || info.Size() > steelProviderStateBytes {
		return "", errors.New("cloud browser provider state is unsafe")
	}
	raw, err := io.ReadAll(io.LimitReader(file, steelProviderStateBytes+1))
	if err != nil || len(raw) > steelProviderStateBytes {
		return "", errors.New("cloud browser provider state is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var state steelProviderState
	if decoder.Decode(&state) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		state.Version != steelProviderStateSchema ||
		!steelNativeIDPattern.MatchString(state.ProfileID) {
		return "", errors.New("cloud browser provider state is invalid")
	}
	return state.ProfileID, nil
}

func writeSteelProviderState(path string, profileID string) error {
	if !steelNativeIDPattern.MatchString(profileID) {
		return errors.New("cloud browser provider identity is invalid")
	}
	if err := prepareStorePath(path); err != nil {
		return err
	}
	raw, err := json.Marshal(steelProviderState{Version: steelProviderStateSchema, ProfileID: profileID})
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeSecureStoreFile(path, raw, 0o600)
}

func (provider *steelPlaywrightRuntimeProvider) releaseSlot() {
	if provider == nil || provider.concurrency == nil {
		return
	}
	select {
	case <-provider.concurrency:
	default:
	}
}

func (*steelPlaywrightRuntime) playwrightRuntime() {}

func (runtimeHandle *steelPlaywrightRuntime) ProfileConfig() config.BrowserProfileConfig {
	return runtimeHandle.profile
}

func (*steelPlaywrightRuntime) NetworkProxy() *browserNetworkProxy { return nil }

func (*steelPlaywrightRuntime) EphemeralRuntime() *ephemeralRuntimeLease { return nil }

func (*steelPlaywrightRuntime) Remote() bool { return true }

func (runtimeHandle *steelPlaywrightRuntime) DriverLease() *localmcp.ExclusiveServerLease {
	if runtimeHandle == nil {
		return nil
	}
	return runtimeHandle.lease
}

func (runtimeHandle *steelPlaywrightRuntime) ConfigureDriver(
	server config.MCPServerConfig,
) (config.MCPServerConfig, error) {
	if runtimeHandle == nil || runtimeHandle.lease == nil || runtimeHandle.lease.Validate() != nil ||
		runtimeHandle.cdpURL == "" {
		return config.MCPServerConfig{}, ErrProviderUnavailable
	}
	server = cloneMCPServerConfig(server)
	if server.Env == nil {
		server.Env = make(map[string]string)
	}
	for _, name := range playwrightManagedEnvironmentNames {
		server.Env[name] = ""
	}
	for _, name := range playwrightCloudEnvironmentNames {
		server.Env[name] = ""
	}
	origins := append([]string(nil), runtimeHandle.origins...)
	for index, raw := range origins {
		normalized, err := config.NormalizeBrowserOrigin(raw)
		if err != nil {
			return config.MCPServerConfig{}, ErrDenied
		}
		origins[index] = normalized
	}
	sort.Strings(origins)
	encodedOrigins, err := json.Marshal(origins)
	if err != nil {
		return config.MCPServerConfig{}, ErrProviderUnavailable
	}
	server.Env["MINTCLAW_PLAYWRIGHT_CDP_ENDPOINT"] = runtimeHandle.cdpURL
	server.Env["MINTCLAW_PLAYWRIGHT_NETWORK_MODE"] = runtimeHandle.networkMode
	server.Env["MINTCLAW_PLAYWRIGHT_ALLOWED_ORIGINS"] = string(encodedOrigins)
	server.Env["MINTCLAW_PLAYWRIGHT_REMOTE_RUNTIME"] = "1"
	return server, nil
}

func (*steelPlaywrightRuntime) MarshalJSON() ([]byte, error) {
	return nil, errors.New("browser runtime handles are not serializable")
}

func (runtimeHandle *steelPlaywrightRuntime) Release(driverStopped bool) error {
	if runtimeHandle == nil {
		return nil
	}
	runtimeHandle.mu.Lock()
	defer runtimeHandle.mu.Unlock()
	if runtimeHandle.closed {
		return nil
	}
	if !driverStopped {
		return errors.Join(ErrProviderUnavailable, ErrWorkerUnavailable, ErrCleanupRequired)
	}
	if !runtimeHandle.sessionReleased {
		closeCtx, cancel := context.WithTimeout(context.Background(), steelProviderCloseTimeout)
		err := runtimeHandle.client.ReleaseSession(closeCtx, runtimeHandle.sessionID)
		cancel()
		if err != nil {
			return errors.Join(err, ErrWorkerUnavailable, ErrCleanupRequired)
		}
		runtimeHandle.sessionReleased = true
		runtimeHandle.cdpURL = ""
		runtimeHandle.debugURL = ""
	}
	if !runtimeHandle.profilePersisted {
		if err := writeSteelProviderState(runtimeHandle.stateFile, runtimeHandle.profileID); err != nil {
			return errors.Join(ErrProviderUnavailable, ErrWorkerUnavailable, ErrCleanupRequired)
		}
		runtimeHandle.profilePersisted = true
	}
	if runtimeHandle.lease != nil {
		if err := runtimeHandle.lease.Close(); err != nil {
			return errors.Join(ErrProviderUnavailable, ErrWorkerUnavailable, ErrCleanupRequired)
		}
		runtimeHandle.lease = nil
	}
	runtimeHandle.closed = true
	runtimeHandle.sessionID = ""
	runtimeHandle.profileID = ""
	if runtimeHandle.provider != nil {
		runtimeHandle.provider.releaseSlot()
	}
	return nil
}

var (
	_ json.Marshaler            = (*steelPlaywrightRuntime)(nil)
	_ playwrightRuntimeProvider = (*steelPlaywrightRuntimeProvider)(nil)
	_ playwrightRuntime         = (*steelPlaywrightRuntime)(nil)
)
