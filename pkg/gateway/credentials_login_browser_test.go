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
	testCredentialAliasBrowserAndPhoneFixture(t, "", "logged_in")
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

func testCredentialAliasBrowserAndPhoneFixture(t *testing.T, providerKind, expectedOutcome string) {
	t.Helper()
	providerMode := providerKind != ""
	if os.Getenv("MINTCLAW_CREDENTIAL_ALIAS_BROWSER_FIXTURE") != "1" {
		t.Skip("synthetic Chromium qualification only")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
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
	const origin = "https://93.184.216.34"
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
				NavigationURL: origin + "/login", EntryOrigin: origin, FormID: "login", UsernameID: "username",
				PasswordID: "password", UsernameType: "email", SubmitURL: origin + "/session",
				SuccessURL: origin + "/home", SuccessMarkerID: "authenticated",
				HandoffURL: origin + "/challenge", HandoffMarkerID: "challenge",
			},
		}},
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
	credentials.consumeLogin = func(_ context.Context, binding vault.Binding, use func([]byte, []byte) error) error {
		if binding.VaultID != "fixture-vault" || binding.ItemID != "fixture-item" {
			return errors.New("unexpected private fixture binding")
		}
		retrieved.Add(1)
		return use([]byte("gateway-private-account@example.org"), []byte("gateway-private-password-Q68"))
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
		if err := record(login.Execute(ctx, args), &result); err != nil {
			return result, err
		}
		if result.Outcome == "logged_in" &&
			record(observe.Execute(ctx, map[string]any{"browser_session_id": session.ID}), nil) != nil {
			return result, errors.New("fixture authenticated observation failed")
		}
		return result, nil
	}
	finalize := func() {}
	if providerMode {
		perform, finalize = credentialProviderFixture(
			t,
			ctx,
			cfg,
			running,
			messageBus,
			providerKind,
			expectedOutcome,
			func(data []byte) {
				mu.Lock()
				outputs = append(outputs, append([]byte(nil), data...))
				mu.Unlock()
			},
		)
	}
	defer finalize()
	results := make(chan browser.CredentialLoginResult, 1)
	var failure atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		respond := func(value any) { _ = json.NewEncoder(writer).Encode(value) }
		switch request.URL.Path {
		case "/_fixture/login":
			go func() {
				result, performErr := perform()
				if performErr != nil {
					failure.Store(performErr.Error())
					result = browser.CredentialLoginResult{Outcome: "fixture_failed"}
				}
				results <- result
			}()
			select {
			case notice := <-messageBus.OutboundChan():
				if retrieved.Load() != 0 {
					http.Error(writer, "fixture unavailable", http.StatusServiceUnavailable)
					return
				}
				parts := strings.Fields(notice.Content)
				respond(map[string]string{"link": parts[len(parts)-1]})
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
			result, performErr := perform()
			if performErr != nil {
				failure.Store(performErr.Error())
				http.Error(writer, "fixture unavailable", http.StatusServiceUnavailable)
				return
			}
			respond(result)
		case "/_fixture/revoked":
			retired := false
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				all, listErr := running.Browser.store.ListSessions(ctx)
				retired = listErr == nil && len(all) == 2
				for _, session := range all {
					retired = retired && session.State.Terminal()
				}
				scope, _, scopeErr := credentials.controller.LoginRequest(ctx,
					cfg.Credentials.Accounts[0].Principals[0], "fixture-account", origin)
				_, grantErr := credentials.controller.ActiveLogin(ctx, scope)
				retired = retired && scopeErr == nil && grantErr != nil
				if retired {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			respond(map[string]bool{"retired": retired})
		default:
			routes.handler.ServeHTTP(writer, request)
		}
	}))
	defer server.Close()
	invite, err := routes.handler.(*credentialHTTPHandler).enrollment.Issue(ctx)
	if err != nil {
		t.Fatal("private synthetic enrollment unavailable")
	}
	defer clear(invite)
	input, err := json.Marshal(map[string]string{
		"backend": server.URL, "invite": string(invite), "outcome": expectedOutcome,
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
		for _, candidate := range []string{"start", "enroll", "login-request", "approve", "login-result", "reuse", "revoke"} {
			if childOutput.String() == "credential gateway fixture failed at "+candidate+"\n" {
				stage = candidate
			}
		}
		if detail, ok := failure.Load().(string); ok {
			stage += ": " + detail
		}
		t.Fatalf("synthetic gateway/browser/phone qualification failed at %s", stage)
	}
	if retrieved.Load() != 2 || len(messageBus.OutboundChan()) != 0 {
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
		} {
			if bytes.Contains(output, []byte(value)) {
				t.Fatal("synthetic credential escaped into a serialized tool, browser state or logger sink")
			}
		}
	}
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
