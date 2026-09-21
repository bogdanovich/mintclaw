package browser

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

type fakeSteelRuntimeClient struct {
	mu sync.Mutex

	createRequests []steelProviderSessionRequest
	createResults  []steelProviderSession
	createErrs     []error
	releaseCalls   []string
	releaseErrs    []error
	profileCalls   []string
	profileStatus  string
	profileErr     error
}

func (client *fakeSteelRuntimeClient) CreateSession(
	_ context.Context,
	request steelProviderSessionRequest,
) (steelProviderSession, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.createRequests = append(client.createRequests, request)
	index := len(client.createRequests) - 1
	if index < len(client.createErrs) && client.createErrs[index] != nil {
		return steelProviderSession{}, client.createErrs[index]
	}
	if index >= len(client.createResults) {
		return steelProviderSession{}, ErrProviderUnavailable
	}
	return client.createResults[index], nil
}

func (client *fakeSteelRuntimeClient) ReleaseSession(
	_ context.Context,
	sessionID string,
) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.releaseCalls = append(client.releaseCalls, sessionID)
	index := len(client.releaseCalls) - 1
	if index < len(client.releaseErrs) {
		return client.releaseErrs[index]
	}
	return nil
}

func (client *fakeSteelRuntimeClient) GetProfile(
	_ context.Context,
	profileID string,
) (steelProviderProfile, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.profileCalls = append(client.profileCalls, profileID)
	lastReleased := ""
	if len(client.releaseCalls) > 0 {
		lastReleased = client.releaseCalls[len(client.releaseCalls)-1]
	}
	return steelProviderProfile{
		Status: client.profileStatus, SourceSessionID: lastReleased,
		UpdatedAt: time.Unix(int64(len(client.releaseCalls)), 0),
	}, client.profileErr
}

func steelProviderFactoryFixture(
	t *testing.T,
	client steelRuntimeClient,
) (*PlaywrightWorkerFactory, *steelPlaywrightRuntimeProvider, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(root, "state")
	lockRoot := filepath.Join(root, "locks")
	if err = os.Mkdir(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(lockRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(stateRoot, "personal.json")
	target := config.BrowserTargetConfig{
		Enabled: true, Placement: config.BrowserPlacementCloud,
		Provider: config.BrowserProviderSteel, Driver: config.BrowserDriverPlaywrightLibrary,
		DriverExecutable: "node",
		Steel: config.BrowserSteelProviderConfig{
			APIKeyRef: *config.NewSecureString("unit-provider-secret"), Concurrency: 1,
			SessionTimeoutSeconds: 300, InactivityTimeoutSeconds: 60, MaxBillableSeconds: 180,
		},
	}
	profile := config.BrowserProfileConfig{
		Enabled: true, Revision: "cloud-personal-v1", Mode: config.BrowserProfileManaged,
		AllowedAgents: []string{"browser"}, AllowedActors: []string{"telegram:owner"},
		NetworkMode: config.BrowserNetworkAnyHTTP, CapabilityMode: config.BrowserCapabilityFullAccess,
		ApprovalMode: config.BrowserApprovalNone, AllowApprovedActions: true,
		Runtime: config.BrowserProfileRuntimeConfig{
			ProviderStateFile: stateFile,
			LockFile:          filepath.Join(lockRoot, "personal.lock"), Headed: true,
		},
	}
	factory, err := newSteelPlaywrightProfileWorkerFactory("cloud", "personal", target, profile)
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := factory.runtime.(*steelPlaywrightRuntimeProvider)
	if !ok {
		t.Fatalf("runtime provider = %T", factory.runtime)
	}
	provider.client = client
	return factory, provider, stateFile
}

func fakeSteelSession(sessionID string, profileID string) steelProviderSession {
	return steelProviderSession{
		SessionID: sessionID, ProfileID: profileID, Status: "live",
		CDPEndpoint: "wss://connect.invalid/session", DebugURL: "https://viewer.invalid/session",
	}
}

func TestSteelProviderPersistsAndReusesOpaqueProfile(t *testing.T) {
	client := &fakeSteelRuntimeClient{
		createResults: []steelProviderSession{
			fakeSteelSession("session_first", "profile_personal"),
			fakeSteelSession("session_second", "profile_personal"),
		},
		profileStatus: "READY",
	}
	_, provider, stateFile := steelProviderFactoryFixture(t, client)
	request := WorkerOpenRequest{SessionID: "browser_session_1"}

	first, err := provider.Provision(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	remote, ok := first.(*steelPlaywrightRuntime)
	if !ok || !remote.Remote() || remote.DriverLease() == nil {
		t.Fatalf("runtime = %T, remote = %v", first, first.Remote())
	}
	if _, err = json.Marshal(first); err == nil {
		t.Fatal("cloud runtime serialized")
	}
	if raw, stateErr := os.ReadFile(stateFile); stateErr != nil ||
		!strings.Contains(string(raw), "profile_personal") {
		t.Fatalf("provider state was not crash-safe before release: %q, %v", raw, stateErr)
	}
	server, err := first.ConfigureDriver(config.MCPServerConfig{Type: "stdio", Command: "node"})
	if err != nil || server.Env["MINTCLAW_PLAYWRIGHT_CDP_ENDPOINT"] == "" ||
		server.Env["MINTCLAW_PLAYWRIGHT_REMOTE_RUNTIME"] != "1" ||
		server.Env["MINTCLAW_PLAYWRIGHT_ALLOWED_ORIGINS"] != "[]" {
		t.Fatalf("ConfigureDriver() failed: %v", err)
	}
	if err = first.Release(true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "unit-provider-secret") {
		t.Fatal("provider state contains API credential")
	}
	info, err := os.Stat(stateFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("provider state mode = %v, %v", info, err)
	}

	second, err := provider.Provision(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err = second.Release(true); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.createRequests) != 2 || client.createRequests[0].ProfileID != "" ||
		client.createRequests[1].ProfileID != "profile_personal" ||
		client.createRequests[0].Timeout != 180*time.Second ||
		client.createRequests[0].Inactivity != 60*time.Second ||
		len(client.profileCalls) != 3 || client.profileCalls[1] != "profile_personal" ||
		len(client.releaseCalls) != 2 {
		t.Fatalf("unexpected provider lifecycle counts: create=%d profile=%d release=%d",
			len(client.createRequests), len(client.profileCalls), len(client.releaseCalls))
	}
}

func TestWaitForSteelProfileReadyRejectsStaleReadyState(t *testing.T) {
	baseline := steelProviderProfile{
		Status: "READY", SourceSessionID: "session_current", UpdatedAt: time.Unix(1, 0),
	}
	client := &sequencedSteelProfileClient{profiles: []steelProviderProfile{
		baseline,
		{Status: "UPLOADING", SourceSessionID: "session_current", UpdatedAt: time.Unix(1, 0)},
		{Status: "READY", SourceSessionID: "session_current", UpdatedAt: time.Unix(2, 0)},
	}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := waitForSteelProfileReady(
		ctx, client, "profile_personal", "session_current", baseline, time.Millisecond,
	); err != nil {
		t.Fatal(err)
	}
	if client.calls != 3 {
		t.Fatalf("profile readiness calls = %d, want 3", client.calls)
	}
}

type sequencedSteelProfileClient struct {
	profiles []steelProviderProfile
	calls    int
}

func (*sequencedSteelProfileClient) CreateSession(
	context.Context,
	steelProviderSessionRequest,
) (steelProviderSession, error) {
	return steelProviderSession{}, ErrProviderUnavailable
}

func (*sequencedSteelProfileClient) ReleaseSession(context.Context, string) error { return nil }

func (client *sequencedSteelProfileClient) GetProfile(
	_ context.Context,
	_ string,
) (steelProviderProfile, error) {
	index := client.calls
	client.calls++
	if index >= len(client.profiles) {
		index = len(client.profiles) - 1
	}
	return client.profiles[index], nil
}

func TestSteelProviderEnforcesConcurrencyAndRetryableCleanup(t *testing.T) {
	client := &fakeSteelRuntimeClient{
		createResults: []steelProviderSession{
			fakeSteelSession("session_cleanup", "profile_cleanup"),
			fakeSteelSession("session_after_cleanup", "profile_cleanup"),
		},
		releaseErrs:   []error{ErrProviderUnavailable, nil, nil},
		profileStatus: "READY",
	}
	_, provider, _ := steelProviderFactoryFixture(t, client)
	first, err := provider.Provision(t.Context(), WorkerOpenRequest{SessionID: "browser_session_1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Provision(
		t.Context(),
		WorkerOpenRequest{SessionID: "browser_session_2"},
	); !errors.Is(err, ErrCapacity) {
		t.Fatalf("concurrent Provision() error = %v, want capacity", err)
	}
	if err = first.Release(false); !errors.Is(err, ErrCleanupRequired) {
		t.Fatalf("Release(driver active) error = %v", err)
	}
	client.mu.Lock()
	releasesBeforeStop := len(client.releaseCalls)
	client.mu.Unlock()
	if releasesBeforeStop != 0 {
		t.Fatal("provider session released before driver stopped")
	}
	if err = first.Release(true); !errors.Is(err, ErrCleanupRequired) {
		t.Fatalf("first stopped Release() error = %v", err)
	}
	if _, err = provider.Provision(
		t.Context(),
		WorkerOpenRequest{SessionID: "browser_session_3"},
	); !errors.Is(err, ErrCapacity) {
		t.Fatalf("Provision() during cleanup error = %v, want capacity", err)
	}
	if err = first.Release(true); err != nil {
		t.Fatalf("retry Release() error = %v", err)
	}
	second, err := provider.Provision(t.Context(), WorkerOpenRequest{SessionID: "browser_session_4"})
	if err != nil {
		t.Fatal(err)
	}
	if err = second.Release(true); err != nil {
		t.Fatal(err)
	}
}

func TestSteelProviderRejectsUploadingProfileWithoutCreatingSession(t *testing.T) {
	client := &fakeSteelRuntimeClient{profileStatus: "UPLOADING"}
	_, provider, stateFile := steelProviderFactoryFixture(t, client)
	if err := writeSteelProviderState(stateFile, "profile_uploading"); err != nil {
		t.Fatal(err)
	}
	_, err := provider.Provision(t.Context(), WorkerOpenRequest{SessionID: "browser_session_1"})
	if !errors.Is(err, ErrProviderProfileNotReady) || !errors.Is(err, ErrWorkerUnavailable) {
		t.Fatalf("Provision() error = %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.createRequests) != 0 || len(client.profileCalls) != 1 {
		t.Fatalf("provider calls: create=%d profile=%d", len(client.createRequests), len(client.profileCalls))
	}
}

func TestAuthenticatedSteelCDPEndpointReplacesProviderCredential(t *testing.T) {
	endpoint, err := authenticatedSteelCDPEndpoint(
		"wss://connect.invalid/session?apiKey=stale&sessionId=session_safe",
		"session_safe",
		"replacement-secret",
	)
	if err != nil || strings.Count(endpoint, "apiKey=") != 1 ||
		strings.Contains(endpoint, "stale") || !strings.Contains(endpoint, "replacement-secret") {
		t.Fatal("authenticated endpoint was not constructed safely")
	}
	for _, raw := range []string{
		"https://connect.invalid/session",
		"wss://user@connect.invalid/session",
		"wss://connect.invalid/session#fragment",
		"wss://connect.invalid/session?sessionId=session_other",
		"wss://connect.invalid/session?sessionId=session_safe&sessionId=session_other",
	} {
		if _, endpointErr := authenticatedSteelCDPEndpoint(raw, "session_safe", "secret"); endpointErr == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
}

func TestSteelProviderRejectsMismatchedCDPSessionAndReleasesCreatedSession(t *testing.T) {
	created := fakeSteelSession("session_created", "profile_created")
	created.CDPEndpoint = "wss://connect.invalid/session?sessionId=session_other"
	client := &fakeSteelRuntimeClient{createResults: []steelProviderSession{created}}
	_, provider, stateFile := steelProviderFactoryFixture(t, client)

	_, err := provider.Provision(t.Context(), WorkerOpenRequest{SessionID: "browser_session_1"})
	if !errors.Is(err, ErrDriverIncompatible) || !errors.Is(err, ErrWorkerUnavailable) {
		t.Fatalf("Provision() error = %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.releaseCalls) != 1 || client.releaseCalls[0] != "session_created" {
		t.Fatalf("released sessions = %v", client.releaseCalls)
	}
	if _, stateErr := os.Stat(stateFile); !errors.Is(stateErr, os.ErrNotExist) {
		t.Fatalf("provider state exists after rejected endpoint: %v", stateErr)
	}
}

func TestSteelProviderOpenErrorsHaveSafeDurableClassifications(t *testing.T) {
	tests := []struct {
		err       error
		wantError error
		failure   string
	}{
		{ErrProviderAuthentication, ErrProviderAuthentication, "provider_authentication_failed"},
		{ErrProviderProfileNotReady, ErrProviderProfileNotReady, "provider_profile_not_ready"},
		{ErrProviderQuota, ErrProviderQuota, "provider_quota_exhausted"},
		{ErrProviderTimeout, ErrProviderTimeout, "provider_timeout"},
		{ErrProviderUnavailable, ErrProviderUnavailable, "provider_unavailable"},
	}
	for _, test := range tests {
		failure, classified := classifyBrowserOpenError(errors.Join(
			test.err,
			ErrWorkerUnavailable,
			errors.New("private endpoint"),
		))
		if !errors.Is(classified, test.wantError) || failure != test.failure {
			t.Errorf("classifyBrowserOpenError(%v) = (%v, %q)", test.err, classified, failure)
		}
	}
}
