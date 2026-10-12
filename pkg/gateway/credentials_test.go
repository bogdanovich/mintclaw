//go:build linux || darwin

package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/credentials/approval"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

type credentialTestRoutes struct {
	handler      http.Handler
	unregistered int
}

func (routes *credentialTestRoutes) RegisterHTTPHandler(path string, handler http.Handler) error {
	if path != credentialPagePath || routes.handler != nil {
		return errCredentialsUnavailable
	}
	routes.handler = handler
	return nil
}

func (routes *credentialTestRoutes) UnregisterHTTPHandler(path string) {
	if path == credentialPagePath {
		routes.handler = nil
		routes.unregistered++
	}
}

func credentialGatewayFixture(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	state := t.TempDir()
	if os.Chmod(state, 0o700) != nil {
		t.Fatal("private state fixture unavailable")
	}
	cfg.Credentials = config.CredentialConfig{
		Enabled: true, OwnerID: "owner", Origin: "https://approval.example.org", StateDirectory: state,
		TrustLoopbackTLSProxy: true,
		Accounts: []credentialbroker.AccountRule{
			{
				Alias:   "facebook",
				VaultID: "vault",
				ItemID:  "item",
				Origins: []string{"https://facebook.com"},
				Principals: []credentialbroker.Principal{
					{OwnerID: "owner", ActorID: "actor", AgentID: "agent", HostID: "host"},
				},
			},
		},
	}
	return cfg
}

func TestGatewayOwnsMountedApprovalAndPrivateEnrollment(t *testing.T) {
	cfg := credentialGatewayFixture(t)
	routes := &credentialTestRoutes{}
	runtime, err := setupCredentialRuntime(cfg, "operator-fixture-token", routes)
	if err != nil || runtime == nil || routes.handler == nil {
		t.Fatal("gateway approval runtime unavailable")
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	server := httptest.NewServer(routes.handler)
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+credentialPagePath, nil)
	if err != nil {
		t.Fatal("phone fixture request unavailable")
	}
	request.Host = "approval.example.org"
	request.Header.Set("X-Forwarded-Proto", "https")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal("mounted phone page unavailable")
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "app.js") {
		t.Fatal("mounted approval page did not load")
	}
	for _, token := range []string{"", "wrong", "operator-fixture-token"} {
		request, err = http.NewRequest(http.MethodPost, server.URL+credentialEnrollmentPath, strings.NewReader("{}"))
		if err != nil {
			t.Fatal("private setup request unavailable")
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err = server.Client().Do(request)
		if err != nil {
			t.Fatal("private setup response unavailable")
		}
		invite, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal("private setup response invalid")
		}
		if token == "operator-fixture-token" {
			if response.StatusCode != http.StatusOK ||
				!strings.HasPrefix(string(invite), cfg.Credentials.Origin+credentialPagePath+"#enroll=") {
				t.Fatal("authenticated private setup did not receive an expiring link")
			}
		} else if response.StatusCode != http.StatusUnauthorized || strings.Contains(string(invite), "#enroll=") {
			t.Fatal("unauthenticated private setup received enrollment authority")
		}
		clear(invite)
	}
	firstClose := runtime.Close(context.Background())
	secondClose := runtime.Close(context.Background())
	if firstClose != nil || secondClose != nil || routes.unregistered != 1 {
		t.Fatal("gateway approval lifecycle did not close once")
	}
	restarted, setup, err := approval.New(approval.Config{
		OwnerID: cfg.Credentials.OwnerID, Origin: cfg.Credentials.Origin,
		StateDirectory: cfg.Credentials.StateDirectory, Accounts: cfg.Credentials.Accounts,
	})
	if err != nil || setup == nil {
		t.Fatal("gateway closure did not release the owner state store")
	}
	_ = restarted.Close()
}

func TestGatewayApprovalStartupDeniesInvalidSettings(t *testing.T) {
	cfg := credentialGatewayFixture(t)
	for _, kind := range []string{"no-auth", "missing-routes", "unsafe-origin", "workspace-store"} {
		t.Run(kind, func(t *testing.T) {
			local := *cfg
			token := "operator-fixture-token"
			var routes credentialRoutes = &credentialTestRoutes{}
			switch kind {
			case "no-auth":
				token = ""
			case "missing-routes":
				routes = nil
			case "unsafe-origin":
				local.Credentials.Origin = "http://approval.example.org"
			case "workspace-store":
				local.Credentials.StateDirectory = local.WorkspacePath()
			}
			if runtime, err := setupCredentialRuntime(&local, token, routes); err == nil || runtime != nil {
				t.Fatal("unsafe approval startup accepted")
			}
		})
	}
}
