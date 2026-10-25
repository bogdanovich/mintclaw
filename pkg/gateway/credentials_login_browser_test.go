//go:build linux || darwin

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/internal/credentials/githubread"
	"github.com/bogdanovich/mintclaw/internal/credentials/vault"
	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// Opt-in real Chromium + virtual WebAuthn + production gateway/browser tools.
// This qualifies synthetic flow integration, never a physical phone or account.
func TestCredentialAliasBrowserAndPhoneFixture(t *testing.T) {
	t.Run("login", func(t *testing.T) { testCredentialAliasBrowserAndPhoneFixture(t, "", "logged_in") })
	t.Run("notification-failure", func(t *testing.T) {
		testCredentialBrowserFixtureScenario(t, "", "logged_in", "notification_failure")
	})
}

func TestCredentialDiscoveredBrowserAndPhoneFixture(t *testing.T) {
	for _, outcome := range []string{"logged_in", "handoff_required"} {
		t.Run(outcome, func(t *testing.T) { testCredentialBrowserFixtureScenario(t, "", outcome, "discovery") })
	}
}

func TestCredentialDiscoveredProviderOutputFixture(t *testing.T) {
	for _, kind := range []string{"openai_compat", "codex"} {
		t.Run(kind, func(t *testing.T) { testCredentialBrowserFixtureScenario(t, kind, "logged_in", "discovery") })
	}
}

func TestCredentialDelayedProviderOutputFixture(t *testing.T) {
	for _, kind := range []string{"openai_compat", "codex"} {
		t.Run(kind, func(t *testing.T) { testCredentialBrowserFixtureScenario(t, kind, "logged_in", "discovery_wait") })
	}
}

func TestCredentialAutomaticProviderOutputFixture(t *testing.T) {
	for _, kind := range []string{"openai_compat", "codex"} {
		t.Run(kind, func(t *testing.T) { testCredentialBrowserFixtureScenario(t, kind, "logged_in", "automatic") })
	}
}

func TestCredentialProviderOutputFixture(t *testing.T) {
	testCredentialAliasBrowserAndPhoneFixture(t, "openai_compat", "logged_in")
}

func TestCredentialProviderOutcomeFixture(t *testing.T) {
	for _, outcome := range []string{"handoff_required", "outcome_unknown"} {
		t.Run(outcome, func(t *testing.T) {
			testCredentialAliasBrowserAndPhoneFixture(t, "openai_compat", outcome)
		})
	}
}

func TestCredentialCodexOutputFixture(t *testing.T) {
	for _, outcome := range []string{"logged_in", "handoff_required", "outcome_unknown"} {
		t.Run(outcome, func(t *testing.T) {
			testCredentialAliasBrowserAndPhoneFixture(t, "codex", outcome)
		})
	}
}

func TestCredentialGitHubOutputFixture(t *testing.T) {
	for _, kind := range []string{"openai_compat", "codex"} {
		t.Run(kind, func(t *testing.T) { testCredentialAliasBrowserAndPhoneFixture(t, "github:"+kind, "success") })
	}
}

func TestCredentialRestartOutputFixture(t *testing.T) {
	for _, kind := range []string{"openai_compat", "codex"} {
		t.Run(kind, func(t *testing.T) {
			testCredentialBrowserFixtureScenario(t, kind, "logged_in", "restart")
		})
	}
}

func TestCredentialLifetimeOutputFixture(t *testing.T) {
	for _, mode := range []string{"cancel", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			outcome := "logged_in"
			if mode == "expiry" {
				outcome = "outcome_unknown"
			}
			testCredentialBrowserFixtureScenario(t, "codex", outcome, mode)
		})
	}
}

func testCredentialAliasBrowserAndPhoneFixture(t *testing.T, providerKind, expectedOutcome string) {
	t.Helper()
	testCredentialBrowserFixtureScenario(t, providerKind, expectedOutcome, "")
}

func testCredentialBrowserFixtureScenario(t *testing.T, providerKind, expectedOutcome, mode string) {
	t.Helper()
	restart, canceled, expiry := mode == "restart", mode == "cancel", mode == "expiry"
	automatic := mode == "automatic"
	durableApproval := mode == "discovery_wait"
	discovery := mode == "discovery" || durableApproval || automatic
	failedNotifications := mode == "notification_failure"
	if mode != "" && !restart && !canceled && !expiry && !discovery && !failedNotifications {
		t.Fatal("unknown credential fixture scenario")
	}
	connectorMode := strings.HasPrefix(providerKind, "github:")
	providerKind = strings.TrimPrefix(providerKind, "github:")
	providerMode := providerKind != ""
	if automatic && !providerMode {
		t.Fatal("automatic output qualification requires a provider path")
	}
	if os.Getenv("MINTCLAW_CREDENTIAL_ALIAS_BROWSER_FIXTURE") != "1" {
		t.Skip("synthetic Chromium qualification only")
	}
	timeout := 100 * time.Second
	if durableApproval {
		timeout = 180 * time.Second
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	server := httptest.NewUnstartedServer(nil)
	t.Cleanup(server.Close)
	script, err := filepath.Abs("credential-login-fixture.cjs")
	if err != nil {
		t.Fatal("gateway browser fixture path unavailable")
	}
	cfg := gatewayBrowserConfig(t.TempDir())
	actor := "owner"
	if providerMode {
		actor = "cron"
	}
	runtimeRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil || os.Chmod(runtimeRoot, 0o700) != nil {
		t.Fatal("gateway browser fixture runtime unavailable")
	}
	if os.Mkdir(filepath.Join(runtimeRoot, "ephemeral"), 0o700) != nil {
		t.Fatal("gateway browser fixture namespace unavailable")
	}
	target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
	target.Driver, target.DriverServer, target.DriverExecutable = config.BrowserDriverPlaywrightLibrary, "", "node"
	target.DriverArguments = []string{script, "driver", expectedOutcome}
	if discovery {
		target.DriverArguments[2] = "discovery"
	}
	if canceled || expiry {
		target.DriverArguments = append(target.DriverArguments,
			"--fixture-submission-checkpoint=http://"+server.Listener.Addr().String())
	}
	target.DefaultProfile = "ephemeral"
	target.Profiles = map[string]config.BrowserProfileConfig{
		"ephemeral": {
			Enabled: true, Revision: "fixture-v1", Mode: config.BrowserProfileEphemeral,
			AllowedAgents: []string{"browser"}, AllowedActors: []string{"telegram:" + actor},
			NetworkMode: config.BrowserNetworkAnyHTTP, CapabilityMode: config.BrowserCapabilityFullAccess,
			ApprovalMode: config.BrowserApprovalModelRequested, AllowApprovedActions: true,
			Runtime: config.BrowserProfileRuntimeConfig{
				EphemeralRoot: filepath.Join(
					runtimeRoot,
					"ephemeral",
				),
				LockFile: filepath.Join(runtimeRoot, "browser.lock"),
			},
		},
	}
	cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
	origin := "https://93.184.216.34"
	if discovery {
		origin = "https://example.org"
	}
	cfg.Credentials = config.CredentialConfig{
		Enabled: true, BrowserLoginEnabled: true, HostID: "gateway", OwnerID: "owner",
		Origin: "https://approval.example.org", StateDirectory: t.TempDir(), TrustLoopbackTLSProxy: true,
		Accounts: []credentialbroker.AccountRule{{
			Alias: "fixture-account", VaultID: "fixture-vault", ItemID: "fixture-item", Origins: []string{origin},
			Principals: []credentialbroker.Principal{
				{
					OwnerID: "owner",
					ActorID: browser.OpaqueActorID("telegram:" + actor),
					AgentID: "browser",
					HostID:  "gateway",
				},
			},
			Login: &credentialbroker.LoginFlow{
				NavigationURL: origin + "/login", EntryOrigin: origin, SubmitURL: origin + "/session",
				SuccessURL: origin + "/home", SuccessMarkerID: "authenticated",
				HandoffURL: origin + "/challenge", HandoffMarkerID: "challenge",
			},
		}},
	}
	if discovery {
		account := &cfg.Credentials.Accounts[0]
		account.DiscoverLogins, account.ItemID, account.Origins, account.Login = true, "", nil, nil
	}
	if automatic {
		cfg.Credentials.ApprovalMode = "automatic"
	}
	if connectorMode {
		cfg.Credentials.BrowserLoginEnabled = false
		cfg.Credentials.GitHubReadEnabled = true
		cfg.Credentials.Accounts[0].Origins = []string{credentialbroker.GitHubAPIOrigin}
		cfg.Credentials.Accounts[0].Login = nil
		cfg.Credentials.Accounts[0].GitHub = &credentialbroker.GitHubReadScope{
			Repositories: []string{"fixture-owner/fixture-repo"},
		}
	}
	if os.Chmod(cfg.Credentials.StateDirectory, 0o700) != nil {
		t.Fatal("private approval fixture state unavailable")
	}
	messageBus := bus.NewMessageBus()
	t.Cleanup(messageBus.Close)
	routes := &credentialTestRoutes{}
	credentials, err := setupCredentialRuntime(cfg, "synthetic-operator-token", routes,
		credentialNotificationPublisher(messageBus))
	if err != nil {
		t.Fatal("integrated approval fixture unavailable")
	}
	t.Cleanup(func() { _ = credentials.Close(context.Background()) })
	var retrieved atomic.Int32
	var candidateCount atomic.Int32
	candidateCount.Store(1)
	var expiryInterruptions atomic.Int32
	credentials.consumeLogin = func(consumeCtx context.Context, binding vault.Binding, use func([]byte, []byte) error) error {
		if binding.VaultID != "fixture-vault" || binding.ItemID != "fixture-item" {
			return errors.New("unexpected private fixture binding")
		}
		retrieved.Add(1)
		useErr := use([]byte("gateway-private-account@example.org"), []byte("gateway-private-password-Q68"))
		if expiry && errors.Is(consumeCtx.Err(), context.Canceled) {
			expiryInterruptions.Add(1)
		}
		return useErr
	}
	if discovery {
		credentials.findLogins = func(_ context.Context, vaultID string, query vault.DiscoveryQuery) ([]vault.Candidate, error) {
			if vaultID != "fixture-vault" || query.Website != origin {
				return nil, vault.ErrUnavailable
			}
			candidates := []vault.Candidate{{
				Binding: vault.Binding{VaultID: vaultID, ItemID: "fixture-item"},
				Name:    "Fixture personal Login", Domains: []string{"example.org"},
			}}
			if candidateCount.Load() == 0 {
				return nil, nil
			}
			if candidateCount.Load() == 2 {
				candidates = append(candidates, vault.Candidate{
					Binding: vault.Binding{VaultID: vaultID, ItemID: "fixture-other"},
					Name:    "Fixture work Login", Domains: []string{"example.org"},
				})
			}
			return candidates, nil
		}
		credentials.consumeWebsiteLogin = func(useCtx context.Context, binding vault.Binding, website, step string,
			use func([]byte, []byte) error,
		) error {
			if website != origin || binding.VaultID != "fixture-vault" || binding.ItemID != "fixture-item" {
				return vault.ErrUnavailable
			}
			if step == "totp" {
				if expectedOutcome == "handoff_required" {
					return vault.ErrTOTPUnavailable
				}
				retrieved.Add(1)
				return use(nil, []byte("19482673"))
			}
			if step != "credentials" {
				return vault.ErrUnavailable // Verify must not retrieve any values.
			}
			return credentials.consumeLogin(useCtx, binding, use)
		}
	}
	const apiToken = "gateway-private-api-token-U42"
	var apiCalls atomic.Int32
	inflight := make(chan struct{})
	var inflightResult chan string
	if connectorMode {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/repos/fixture-owner/fixture-repo/pulls" ||
				r.URL.RawQuery != "state=open&per_page=20" || r.Header.Get("Authorization") != "Bearer "+apiToken {
				t.Error("connector fixture changed private HTTP authority")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if apiCalls.Add(1) == 3 {
				close(inflight)
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"number": 7, "state": "open", "title": "Pull request " + apiToken,
				"body": apiToken, "html_url": "https://ignored.invalid/" + apiToken,
			}})
		}))
		t.Cleanup(api.Close)
		credentials.githubReader = githubread.New(credentialGitHubFixtureTransport{api: api})
		credentials.consumeAPIKey = func(_ context.Context, binding vault.Binding, use func([]byte) error) error {
			if binding.VaultID != "fixture-vault" || binding.ItemID != "fixture-item" {
				return errors.New("private connector binding changed")
			}
			retrieved.Add(1)
			key := []byte(apiToken)
			defer clear(key)
			return use(key)
		}
		inflightResult = make(chan string, 1)
	}
	running := &services{Credentials: credentials}
	if err := cfg.ValidateBrowserConfig(); err != nil {
		t.Fatalf("synthetic browser fixture configuration: %v", err)
	}
	if setupBrowserRuntime(ctx, cfg, running) != nil {
		t.Fatal("integrated browser fixture unavailable")
	}
	t.Cleanup(func() { _ = running.Browser.Close(context.Background()) })
	source, err := newGatewayBrowserToolSource(cfg, running)
	if err != nil {
		t.Fatal("integrated browser tool source unavailable")
	}
	options := tools.NewBrowserToolOptions(cfg.Tools.Browser)
	sessions := tools.NewBrowserSessionTool(options, source)
	observe := tools.NewBrowserObserveTool(options, source)
	login := tools.NewBrowserLoginTool(options, source)
	ctx = toolshared.WithToolInboundMetadata(ctx, bus.InboundContext{
		Channel: "telegram", SenderID: actor, ChatID: "fixture-owner-chat",
	})
	ctx = toolshared.WithToolSessionContext(ctx, "browser", "fixture-history", nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, "fixture-route")
	ctx = toolshared.WithToolCallID(ctx, "fixture-call")
	ctx = toolshared.WithToolExecutionIdentity(ctx, cfg.WorkspacePath(), "fixture-execution")
	logPath := filepath.Join(t.TempDir(), "gateway.log")
	if logger.EnableFileLogging(logPath) != nil {
		t.Fatal("gateway output fixture unavailable")
	}
	t.Cleanup(logger.DisableFileLogging)
	var mu sync.Mutex
	var outputs [][]byte
	var sessionIDs []string
	record := func(result *toolshared.ToolResult, target any) error {
		if result == nil || result.IsError {
			if result != nil {
				var view struct {
					Code string `json:"code"`
				}
				if json.Unmarshal([]byte(result.ForLLM), &view) == nil {
					switch view.Code {
					case "driver_incompatible",
						"driver_unavailable",
						"stale_snapshot",
						"policy_denied",
						"invalid_request":
						return errors.New("fixture tool denied: " + view.Code)
					}
				}
			}
			return errors.New("fixture tool denied")
		}
		encoded, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			return errors.New("fixture tool projection failed")
		}
		mu.Lock()
		outputs = append(outputs, encoded)
		mu.Unlock()
		if target != nil && json.Unmarshal([]byte(result.ForLLM), target) != nil {
			return errors.New("fixture tool response invalid")
		}
		return nil
	}
	perform := func() (browser.CredentialLoginResult, error) {
		var session struct {
			ID string `json:"browser_session_id"`
		}
		if record(sessions.Execute(ctx, map[string]any{
			"operation": "open", "target": config.BrowserDefaultTarget, "profile": "ephemeral",
			"interaction_language": "en",
		}), &session) != nil || session.ID == "" {
			return browser.CredentialLoginResult{}, errors.New("fixture open failed")
		}
		mu.Lock()
		sessionIDs = append(sessionIDs, session.ID)
		mu.Unlock()
		var observation browser.Observation
		if err := record(
			observe.Execute(ctx, map[string]any{"browser_session_id": session.ID}),
			&observation,
		); err != nil {
			return browser.CredentialLoginResult{}, err
		}
		args, argsErr := credentialFixtureLoginArguments(observation)
		if argsErr != nil {
			return browser.CredentialLoginResult{}, argsErr
		}
		var result browser.CredentialLoginResult
		if discovery {
			before := retrieved.Load()
			candidateCount.Store(0)
			if err := record(
				login.Execute(ctx, credentialFixtureStepArguments(observation, "discover")),
				&result,
			); err != nil ||
				result.Outcome != "not_found" || retrieved.Load() != before ||
				len(messageBus.OutboundChan()) != 0 {
				return result, errors.New("empty lookup read values or requested approval")
			}
			candidateCount.Store(2)
			ambiguous := make(map[string]any, len(args))
			for key, value := range args {
				if key != "account_alias" {
					ambiguous[key] = value
				}
			}
			ambiguous["operation"] = "credentials"
			if err := record(
				login.Execute(ctx, ambiguous),
				&result,
			); err != nil || result.Outcome != "choice_required" ||
				len(result.Candidates) != 2 || retrieved.Load() != before ||
				len(messageBus.OutboundChan()) != 0 {
				return result, errors.New("ambiguous lookup guessed a record, read values or requested approval")
			}
			candidateCount.Store(1)
			lookup := credentialFixtureStepArguments(observation, "discover")
			if err := record(login.Execute(ctx, lookup), &result); err != nil || result.Outcome != "found" ||
				len(result.Candidates) != 1 || result.CredentialID != "fixture-item" {
				return result, errors.New("website discovery did not return one safe Login reference")
			}
			delete(args, "account_alias")
			args["operation"], args["credential_id"] = "credentials", result.CredentialID
			authorize := credentialFixtureStepArguments(observation, "authorize")
			authorize["credential_id"] = result.CredentialID
			if err := record(login.Execute(ctx, authorize), &result); err != nil ||
				result.Outcome != "authorized" || retrieved.Load() != before {
				return result, errors.New("authorization did not preserve a value-free preflight")
			}
		}
		if err := record(login.Execute(ctx, args), &result); err != nil {
			return result, err
		}
		if discovery {
			for _, step := range []string{"totp", "verify"} {
				if result.Outcome != "submitted" {
					return result, errors.New("generic step did not report its unconfirmed submission")
				}
				if err := record(
					observe.Execute(ctx, map[string]any{"browser_session_id": session.ID}),
					&observation,
				); err != nil {
					return result, err
				}
				args, err = credentialFixtureWebsiteArguments(observation, step)
				if err != nil {
					return result, err
				}
				if err = record(login.Execute(ctx, args), &result); err != nil {
					return result, err
				}
				if result.Outcome == "handoff_required" {
					break
				}
			}
		}
		if result.Outcome == "logged_in" &&
			record(observe.Execute(ctx, map[string]any{"browser_session_id": session.ID}), nil) != nil {
			return result, errors.New("fixture authenticated observation failed")
		}
		return result, nil
	}
	finalize := func() {}
	cancelCurrent := func() {}
	var beforeCleanup func() error
	if expiry {
		beforeCleanup = func() error {
			// Observe cancellation inside the private consumer, before Login's
			// deferred cancel or the provider's scripted close can mask a missing
			// lease watcher. The independent driver timeout cannot satisfy this.
			if retrieved.Load() == 0 || expiryInterruptions.Load() != retrieved.Load() {
				return errors.New("lease expiry did not interrupt the private submission")
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				all, listErr := running.Browser.store.ListSessions(ctx)
				retired := listErr == nil && len(all) == int(retrieved.Load())
				for _, session := range all {
					retired = retired && session.State.Terminal()
				}
				if retired {
					return nil
				}
				if time.Now().After(deadline) {
					return errors.New("lease expiry did not retire the browser before scripted cleanup")
				}
				time.Sleep(25 * time.Millisecond)
			}
		}
	}
	notifications := messageBus.OutboundChan()
	var waiting <-chan struct{}
	if providerMode {
		harness := credentialProviderFixture(
			t,
			ctx,
			cfg,
			running,
			messageBus,
			providerKind,
			connectorMode,
			expectedOutcome,
			canceled,
			durableApproval,
			beforeCleanup,
			func(data []byte) {
				mu.Lock()
				outputs = append(outputs, append([]byte(nil), data...))
				mu.Unlock()
			},
		)
		perform, finalize, cancelCurrent = harness.perform, harness.finalize, harness.cancel
		notifications, waiting = harness.notifications, harness.waiting
	}
	defer finalize()
	results := make(chan browser.CredentialLoginResult, 1)
	var failure atomic.Value
	var submitted atomic.Int32
	var liveAtSubmission atomic.Bool
	var runtimeMu sync.RWMutex
	server.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/_fixture/restart" {
			runtimeMu.RLock()
			defer runtimeMu.RUnlock()
		}
		respond := func(value any) { _ = json.NewEncoder(writer).Encode(value) }
		switch request.URL.Path {
		case "/_fixture/login":
			before := retrieved.Load()
			go func() {
				runtimeMu.RLock()
				defer runtimeMu.RUnlock()
				var performErr error
				if failedNotifications {
					performErr = credentialFixtureRejectNotifications(ctx, credentials, cfg, origin)
				}
				if performErr == nil && !durableApproval && !connectorMode {
					performErr = awaitCredentialFixturePhoneGrant(ctx, credentials, cfg, origin)
				}
				var result browser.CredentialLoginResult
				if performErr == nil {
					result, performErr = perform()
				}
				if performErr != nil {
					failure.Store(performErr.Error())
					result = browser.CredentialLoginResult{Outcome: "fixture_failed"}
				}
				results <- result
			}()
			select {
			case notice := <-notifications:
				if waiting != nil {
					select {
					case <-waiting:
					case <-ctx.Done():
						http.Error(writer, "fixture wait unavailable", http.StatusServiceUnavailable)
						return
					}
				}
				if retrieved.Load() != before {
					http.Error(writer, "fixture unavailable", http.StatusServiceUnavailable)
					return
				}
				parts := strings.Fields(notice.Content)
				link := parts[len(parts)-1]
				if expiry {
					ref, requestErr := credentials.controller.Request(ctx,
						cfg.Credentials.Accounts[0].Principals[0], []string{"fixture-account"}, 10*time.Second)
					if requestErr != nil {
						http.Error(writer, "fixture approval unavailable", http.StatusServiceUnavailable)
						return
					}
					link = cfg.Credentials.Origin + credentialPagePath + "#request=" + ref
				}
				respond(map[string]string{"link": link})
			case <-results:
				http.Error(writer, "fixture unavailable", http.StatusServiceUnavailable)
			case <-ctx.Done():
				http.Error(writer, "fixture unavailable", http.StatusServiceUnavailable)
			}
		case "/_fixture/result":
			select {
			case result := <-results:
				respond(result)
			case <-ctx.Done():
				http.Error(writer, "fixture unavailable", http.StatusServiceUnavailable)
			}
		case "/_fixture/restart":
			if !restart {
				http.NotFound(writer, request)
				return
			}
			runtimeMu.Lock()
			defer runtimeMu.Unlock()
			scope, _, scopeErr := credentials.controller.LoginRequest(ctx,
				cfg.Credentials.Accounts[0].Principals[0], "fixture-account", origin)
			lease, leaseErr := credentials.controller.ActiveLogin(ctx, scope)
			if scopeErr != nil || leaseErr != nil || retrieved.Load() != 1 || credentials.Close(ctx) != nil {
				http.Error(writer, "fixture restart unavailable", http.StatusServiceUnavailable)
				return
			}
			replacement, setupErr := setupCredentialRuntime(cfg, "synthetic-operator-token", routes,
				credentialNotificationPublisher(messageBus))
			if setupErr != nil {
				http.Error(writer, "fixture restart unavailable", http.StatusServiceUnavailable)
				return
			}
			replacement.consumeLogin = credentials.consumeLogin
			credentials, running.Credentials = replacement, replacement
			_, grantErr := replacement.controller.ActiveLogin(ctx, scope)
			respond(map[string]bool{
				"passkey_retained": routes.handler.(*credentialHTTPHandler).enrollment == nil,
				"grant_retained":   grantErr == nil,
				"old_lease_denied": lease.Check(ctx, scope) != nil,
				"no_retrieval":     retrieved.Load() == 1,
			})
		case "/_fixture/reuse":
			// The existing browser limit admits one live session. Close the
			// first, then prove the same signed grant authorizes a new session.
			if !providerMode {
				mu.Lock()
				previous := sessionIDs[len(sessionIDs)-1]
				mu.Unlock()
				if record(sessions.Execute(ctx, map[string]any{
					"operation": "close", "browser_session_id": previous,
				}), nil) != nil {
					http.Error(writer, "fixture unavailable", http.StatusServiceUnavailable)
					return
				}
			}
			if canceled {
				deadline := time.Now().Add(5 * time.Second)
				for {
					all, listErr := running.Browser.store.ListSessions(ctx)
					if listErr == nil && len(all) == 1 && all[0].State.Terminal() {
						break
					}
					if time.Now().After(deadline) {
						http.Error(writer, "canceled browser retained", http.StatusServiceUnavailable)
						return
					}
					time.Sleep(25 * time.Millisecond)
				}
			}
			result, performErr := perform()
			if performErr != nil {
				failure.Store(performErr.Error())
				http.Error(writer, "fixture unavailable", http.StatusServiceUnavailable)
				return
			}
			respond(result)
		case "/_fixture/expired", "/_fixture/revoked":
			if connectorMode {
				select {
				case result := <-inflightResult:
					mu.Lock()
					outputs = append(outputs, []byte(result))
					mu.Unlock()
					scope, _, scopeErr := credentials.controller.GitHubRequest(
						ctx,
						cfg.Credentials.Accounts[0].Principals[0],
						"fixture-account",
						"fixture-owner/fixture-repo",
					)
					_, grantErr := credentials.controller.ActiveLogin(ctx, scope)
					respond(
						map[string]bool{
							"retired": scopeErr == nil && grantErr != nil && apiCalls.Load() == 3 &&
								strings.Contains(result, `"outcome":"unavailable"`),
						},
					)
				case <-ctx.Done():
					http.Error(writer, "fixture unavailable", http.StatusServiceUnavailable)
				case <-time.After(5 * time.Second):
					respond(map[string]bool{"retired": false})
				}
				return
			}
			retired := false
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				all, listErr := running.Browser.store.ListSessions(ctx)
				expectedSessions := int(retrieved.Load())
				if discovery && expectedOutcome == "logged_in" {
					expectedSessions /= 2
				}
				if durableApproval {
					expectedSessions++
				} // Closed before the phone wait; no values consumed.
				retired = listErr == nil && len(all) == expectedSessions
				for _, session := range all {
					retired = retired && session.State.Terminal()
				}
				scope, _, scopeErr := credentials.controller.LoginRequest(ctx,
					cfg.Credentials.Accounts[0].Principals[0], "fixture-account", origin)
				if discovery {
					scope, _, scopeErr = credentials.controller.DiscoveryRequest(ctx,
						cfg.Credentials.Accounts[0].Principals[0], "fixture-account", origin)
				}
				_, grantErr := credentials.controller.ActiveLogin(ctx, scope)
				retired = retired && scopeErr == nil && grantErr != nil
				if expiry {
					retired = retired && liveAtSubmission.Load() && submitted.Load() == retrieved.Load()
				}
				if retired {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			respond(map[string]bool{"retired": retired})
		case "/_fixture/submitted":
			if !canceled && !expiry {
				http.NotFound(writer, request)
				return
			}
			count := submitted.Add(1)
			if expiry {
				scope, _, scopeErr := credentials.controller.LoginRequest(ctx,
					cfg.Credentials.Accounts[0].Principals[0], "fixture-account", origin)
				lease, leaseErr := credentials.controller.ActiveLogin(ctx, scope)
				live := false
				if scopeErr == nil && leaseErr == nil {
					remaining, remainingErr := lease.Remaining(ctx, scope)
					live = remainingErr == nil && remaining > 0
				}
				liveAtSubmission.Store(live)
			}
			respond(map[string]bool{"hold": expiry || count == 1})
			if canceled && count == 1 {
				writer.(http.Flusher).Flush()
				cancelCurrent()
			}
		case "/_fixture/inflight":
			if !connectorMode {
				http.NotFound(writer, request)
				return
			}
			source := &credentialReadSource{
				runtime: credentials,
				reader:  credentials.githubReader,
				agents:  map[string]bool{"browser": true},
			}
			if result := source.ReadCredentialPullRequests(
				ctx,
				"fixture-account",
				"unconfigured/repository",
			); result != `{"outcome":"denied"}` || apiCalls.Load() != 2 ||
				retrieved.Load() != 2 {
				http.Error(writer, "fixture scope unavailable", http.StatusServiceUnavailable)
				return
			}
			go func() {
				inflightResult <- source.ReadCredentialPullRequests(ctx, "fixture-account", "fixture-owner/fixture-repo")
			}()
			select {
			case <-inflight:
				respond(map[string]bool{"started": true})
			case <-ctx.Done():
				http.Error(writer, "fixture unavailable", http.StatusServiceUnavailable)
			}
		default:
			routes.handler.ServeHTTP(writer, request)
		}
	})
	server.Start()
	defer server.Close()
	if automatic {
		// No phone enrollment or approval is performed. Exercise both real
		// provider sequences and the same private browser/output qualification.
		for range 2 {
			result, performErr := perform()
			if performErr != nil || result.Outcome != expectedOutcome {
				t.Fatalf("automatic private login qualification failed: %v", performErr)
			}
		}
	} else {
		invite, err := routes.handler.(*credentialHTTPHandler).enrollment.Issue(ctx)
		if err != nil {
			t.Fatal("private synthetic enrollment unavailable")
		}
		defer clear(invite)
		input, err := json.Marshal(map[string]string{
			"backend": server.URL, "invite": string(invite), "outcome": expectedOutcome,
			"inflight":  map[bool]string{true: "true", false: ""}[connectorMode],
			"restart":   map[bool]string{true: "true", false: ""}[restart],
			"lifetime":  mode,
			"discovery": map[bool]string{true: "true", false: ""}[discovery],
		})
		if err != nil {
			t.Fatal("synthetic phone configuration unavailable")
		}
		defer clear(input)
		command := exec.CommandContext(ctx, "node", script, "phone")
		command.Stdin = bytes.NewReader(input)
		var childOutput bytes.Buffer
		command.Stdout, command.Stderr = &childOutput, &childOutput
		if command.Run() != nil {
			cancel()
			// A fixed stage is enough to locate the failure; never dump child output.
			stage := "unknown"
			for _, candidate := range []string{
				"start", "enroll", "login-request", "approve", "login-result", "reuse", "revoke",
				"restart", "restart-request", "restart-approve", "restart-result",
				"expiry", "expiry-request", "expiry-approve", "expiry-result",
			} {
				if childOutput.String() == "credential gateway fixture failed at "+candidate+"\n" {
					stage = candidate
				}
			}
			if detail, ok := failure.Load().(string); ok {
				stage += ": " + detail
			}
			t.Fatalf("synthetic gateway/browser/phone qualification failed at %s", stage)
		}
	}
	expectedReads := int32(2)
	if discovery && expectedOutcome == "logged_in" {
		expectedReads = 4
	}
	if connectorMode {
		expectedReads = 3
	}
	if retrieved.Load() != expectedReads || len(messageBus.OutboundChan()) != 0 {
		t.Fatal("signed grant reuse retrieved unexpectedly or sent another approval notification")
	}
	finalize()
	logger.DisableFileLogging()
	logs, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal("local logger qualification unavailable")
	}
	mu.Lock()
	outputs = append(outputs, logs)
	mu.Unlock()
	if filepath.WalkDir(cfg.WorkspacePath(), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		outputs = append(outputs, data)
		return nil
	}) != nil {
		t.Fatal("browser durable output qualification unavailable")
	}
	for _, output := range outputs {
		for _, value := range []string{
			"gateway-private-account@example.org", "gateway-private-password-Q68", "gateway-private-cookie-K82",
			"gateway-private-otp-R49",
			"19482673",
			apiToken,
		} {
			if bytes.Contains(output, []byte(value)) {
				t.Fatal("synthetic credential escaped into a serialized tool, browser state or logger sink")
			}
		}
	}
}

func credentialFixtureStepArguments(observation browser.Observation, step string) map[string]any {
	return map[string]any{
		"operation": step, "browser_session_id": observation.SessionID, "tab_id": observation.TabID,
		"snapshot_id": observation.SnapshotID, "snapshot_generation": int(observation.SnapshotGeneration),
	}
}

func credentialFixtureWebsiteArguments(observation browser.Observation, step string) (map[string]any, error) {
	args := credentialFixtureStepArguments(observation, step)
	args["credential_id"] = "fixture-item"
	labels := map[string]string{"otp_ref": "Verification code", "submit_ref": "Verify"}
	if step == "verify" {
		labels = map[string]string{"authenticated_ref": "Log out"}
	}
	for key, label := range labels {
		pattern := regexp.MustCompile(`(?m)- (?:textbox|button) "` + label + `"[^\n]*\[ref=([^\]]+)\]`)
		match := pattern.FindStringSubmatch(observation.Snapshot)
		if len(match) != 2 {
			return nil, errors.New("generic fixture current refs unavailable")
		}
		args[key] = match[1]
	}
	return args, nil
}

func credentialFixtureLoginArguments(observation browser.Observation) (map[string]any, error) {
	args := map[string]any{
		"account_alias": "fixture-account", "browser_session_id": observation.SessionID, "tab_id": observation.TabID,
		"snapshot_id": observation.SnapshotID, "snapshot_generation": int(observation.SnapshotGeneration),
	}
	for key, label := range map[string]string{
		"username_ref": "Account", "password_ref": "Password", "submit_ref": "Sign in",
	} {
		pattern := regexp.MustCompile(`(?m)- (?:textbox|button) "` + label + `"[^\n]*\[ref=([^\]]+)\]`)
		match := pattern.FindStringSubmatch(observation.Snapshot)
		if len(match) != 2 {
			return nil, errors.New("fixture current refs unavailable")
		}
		args[key] = match[1]
	}
	return args, nil
}
