package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func saveTestConfig(path string, cfg *config.Config) error {
	_, err := config.NewRepository(path).Save(cfg)
	return err
}

func TestValidateConfigRejectsInvalidMCPContract(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.MCP.Servers["remote"] = config.MCPServerConfig{
		Enabled: true,
		Type:    "http",
		URL:     "https://example.invalid/mcp",
		Command: "unexpected-command",
	}
	errs := validateConfig(cfg)
	if got := strings.Join(errs, "\n"); !strings.Contains(got, "http transport does not support command") {
		t.Fatalf("validateConfig() errors = %q, want MCP transport field error", got)
	}
}

func TestHandlePatchConfig_PreservesTurnProfile(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Agents.Defaults.TurnProfile = config.TurnProfileConfig{
		Enabled:      true,
		History:      config.TurnProfileBlock{Mode: config.TurnProfileModeOff},
		SystemPrompt: config.TurnProfileBlock{Mode: config.TurnProfileModeOff},
		Skills:       config.TurnProfileBlock{Mode: config.TurnProfileModeOff},
		Tools: config.TurnProfileBlock{
			Mode:  config.TurnProfileModeCustom,
			Allow: []string{"web_search", "web_fetch"},
		},
	}
	if saveErr := saveTestConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("saveTestConfig() error = %v", saveErr)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"agents": {
			"defaults": {
				"max_tokens": 1234
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(updated) error = %v", err)
	}
	profile := updated.Agents.Defaults.TurnProfile
	if profile.Tools.Mode != config.TurnProfileModeCustom ||
		strings.Join(profile.Tools.Allow, ",") != "web_search,web_fetch" {
		t.Fatalf("profile tools = %#v, want custom web_search/web_fetch", profile.Tools)
	}
}

func TestHandlePatchConfig_RejectsInvalidTurnProfile(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"agents": {
			"defaults": {
				"turn_profile": {
					"enabled": true,
					"history": { "mode": "custom" }
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf(
			"status = %d, want %d, body=%s",
			rec.Code,
			http.StatusBadRequest,
			rec.Body.String(),
		)
	}
	if !strings.Contains(rec.Body.String(), "history.mode custom is not supported") {
		t.Fatalf("body=%s, want turn profile validation error", rec.Body.String())
	}

	if _, err := config.LoadConfig(configPath); err != nil {
		t.Fatalf("LoadConfig() after rejected patch error = %v", err)
	}
}

func assertGatewayLogLevelApplied(t *testing.T, method, body string, want logger.LogLevel) {
	t.Helper()

	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	initialLevel := logger.GetLevel()
	logger.SetLevel(logger.INFO)
	t.Cleanup(func() {
		logger.SetLevel(initialLevel)
	})

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(method, "/api/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	setConfigIfMatch(t, req, configPath)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"%s /api/config status = %d, want %d, body=%s",
			method,
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}
	if got := logger.GetLevel(); got != want {
		t.Fatalf("logger.GetLevel() = %v, want %v", got, want)
	}
}

func setConfigIfMatch(t *testing.T, req *http.Request, configPath string) {
	t.Helper()
	snapshot, err := config.NewRepository(configPath).ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly() error = %v", err)
	}
	req.Header.Set("If-Match", configRevisionETag(snapshot.Revision))
}

func TestHandleGetConfig_ReturnsRevisionWithoutWritingConfig(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	const secret = "get-config-public-projection-secret"
	if _, err := config.NewRepository(configPath).Update(func(cfg *config.Config) error {
		cfg.Tools.Web.Gemini.APIKey = *config.NewSecureString(secret)
		return nil
	}); err != nil {
		t.Fatalf("seed config secret: %v", err)
	}

	securityPath := filepath.Join(filepath.Dir(configPath), config.SecurityConfigFile)
	publicBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	securityBefore, err := os.ReadFile(securityPath)
	if err != nil {
		t.Fatalf("ReadFile(security) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if etag := rec.Header().Get("ETag"); len(etag) != 66 || etag[0] != '"' || etag[len(etag)-1] != '"' {
		t.Fatalf("ETag = %q, want quoted SHA-256 revision", etag)
	}
	var response struct {
		Version int `json:"version"`
		Session struct {
			Dimensions []string `json:"dimensions"`
		} `json:"session"`
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal(response) error = %v", err)
	}
	if response.Version != config.CurrentVersion {
		t.Fatalf("response version = %d, want %d", response.Version, config.CurrentVersion)
	}
	if len(response.Session.Dimensions) != 1 || response.Session.Dimensions[0] != "chat" {
		t.Fatalf("response session.dimensions = %v, want [chat]", response.Session.Dimensions)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"dm_scope"`)) {
		t.Fatalf("response contains removed session.dm_scope field: %s", rec.Body.Bytes())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(secret)) {
		t.Fatal("GET /api/config exposed a secret value")
	}
	publicAfter, _ := os.ReadFile(configPath)
	securityAfter, _ := os.ReadFile(securityPath)
	if !bytes.Equal(publicBefore, publicAfter) || !bytes.Equal(securityBefore, securityAfter) {
		t.Fatal("GET /api/config modified durable config documents")
	}
}

func TestHandlePatchConfigPersistsCanonicalSessionDimensions(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(
		http.MethodPatch,
		"/api/config",
		bytes.NewBufferString(`{"session":{"dimensions":[]}}`),
	)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if updated.Session.Dimensions == nil || len(updated.Session.Dimensions) != 0 {
		t.Fatalf("Session.Dimensions = %#v, want explicit empty dimensions", updated.Session.Dimensions)
	}
}

func TestHandleUpdateConfigAppliesToolFeedbackSubagentsDefault(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  bool
	}{
		{name: "omitted", value: nil, want: true},
		{name: "explicit_false", value: false, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath, cleanup := setupOAuthTestEnv(t)
			defer cleanup()

			repository := config.NewRepository(configPath)
			snapshot, err := repository.ReadOnly()
			if err != nil {
				t.Fatalf("ReadOnly() error = %v", err)
			}
			body, err := json.Marshal(snapshot.Config)
			if err != nil {
				t.Fatalf("Marshal(config) error = %v", err)
			}
			var payload map[string]any
			if err = json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("Unmarshal(config) error = %v", err)
			}
			toolFeedback := payload["agents"].(map[string]any)["defaults"].(map[string]any)["tool_feedback"].(map[string]any)
			if test.value == nil {
				delete(toolFeedback, "subagents")
			} else {
				toolFeedback["subagents"] = test.value
			}
			body, err = json.Marshal(payload)
			if err != nil {
				t.Fatalf("Marshal(payload) error = %v", err)
			}

			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
			req.Header.Set("If-Match", configRevisionETag(snapshot.Revision))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}

			updated, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if got := updated.Agents.Defaults.ToolFeedback.Subagents; got != test.want {
				t.Fatalf("tool_feedback.subagents = %t, want %t", got, test.want)
			}
		})
	}
}

func TestHandlePatchConfigAppliesToolFeedbackSubagentsDefault(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "deleted", body: `{"agents":{"defaults":{"tool_feedback":{"subagents":null}}}}`, want: true},
		{name: "explicit_false", body: `{"agents":{"defaults":{"tool_feedback":{"subagents":false}}}}`, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath, cleanup := setupOAuthTestEnv(t)
			defer cleanup()

			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(test.body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}

			updated, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if got := updated.Agents.Defaults.ToolFeedback.Subagents; got != test.want {
				t.Fatalf("tool_feedback.subagents = %t, want %t", got, test.want)
			}
		})
	}
}

func TestHandleUpdateConfig_RequiresRevision(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusPreconditionRequired, rec.Body.String())
	}
}

func TestHandleUpdateConfig_RejectsNonCurrentSchemaWithoutWriting(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	repository := config.NewRepository(configPath)
	snapshot, err := repository.ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly() error = %v", err)
	}
	currentBody, err := json.Marshal(snapshot.Config)
	if err != nil {
		t.Fatalf("Marshal(config) error = %v", err)
	}

	tests := []struct {
		name      string
		mutate    func(map[string]any)
		wantError string
	}{
		{
			name: "future_version",
			mutate: func(payload map[string]any) {
				payload["version"] = config.CurrentVersion + 1
			},
			wantError: "unsupported config version",
		},
		{
			name: "removed_field",
			mutate: func(payload map[string]any) {
				payload["bindings"] = []any{}
			},
			wantError: "unknown field(s): bindings",
		},
		{
			name: "removed_session_scope_field",
			mutate: func(payload map[string]any) {
				payload["session"].(map[string]any)["dm_scope"] = "per-channel"
			},
			wantError: "unknown field(s): session.dm_scope",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var payload map[string]any
			if err := json.Unmarshal(currentBody, &payload); err != nil {
				t.Fatalf("Unmarshal(config) error = %v", err)
			}
			test.mutate(payload)
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("Marshal(payload) error = %v", err)
			}
			assertRejectedConfigWritePreservesDocuments(
				t,
				configPath,
				http.MethodPut,
				body,
				test.wantError,
			)
		})
	}

	duplicateVersionBody := append(
		[]byte(fmt.Sprintf(`{"version":%d,`, config.CurrentVersion+1)),
		currentBody[1:]...,
	)
	assertRejectedConfigWritePreservesDocuments(
		t,
		configPath,
		http.MethodPut,
		duplicateVersionBody,
		"duplicate field: version",
	)
	duplicateToolsBody := append(
		[]byte(`{"tools":{"spawn_status":{"enabled":true}},`),
		currentBody[1:]...,
	)
	assertRejectedConfigWritePreservesDocuments(
		t,
		configPath,
		http.MethodPut,
		duplicateToolsBody,
		"duplicate field: tools",
	)
}

func TestHandlePatchConfig_RejectsNonCurrentSchemaWithoutWriting(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	if _, err := config.NewRepository(configPath).Update(func(cfg *config.Config) error {
		cfg.Channels["telegram_alerts"] = &config.Channel{
			Type:     config.ChannelTelegram,
			Settings: config.RawNode(`{}`),
		}
		return nil
	}); err != nil {
		t.Fatalf("add aliased channel fixture: %v", err)
	}

	tests := []struct {
		name      string
		body      string
		wantError string
	}{
		{
			name:      "future_version",
			body:      fmt.Sprintf(`{"version":%d}`, config.CurrentVersion+1),
			wantError: "unsupported config version",
		},
		{
			name:      "removed_field",
			body:      `{"bindings":[]}`,
			wantError: "unknown field(s): bindings",
		},
		{
			name:      "removed_field_null_tombstone",
			body:      `{"bindings":null}`,
			wantError: "unknown field(s): bindings",
		},
		{
			name:      "removed_session_scope_field",
			body:      `{"session":{"dm_scope":"per-channel"}}`,
			wantError: "unknown field(s): session.dm_scope",
		},
		{
			name:      "nested_removed_field_null_tombstone",
			body:      `{"tools":{"spawn_status":null}}`,
			wantError: "unknown field(s): tools.spawn_status",
		},
		{
			name: "unknown_channel_setting",
			body: `{
				"channel_list": {"telegram": {"settings": {"removed_setting": true}}}
			}`,
			wantError: "unknown field(s): channel_list.telegram.settings.removed_setting",
		},
		{
			name: "unknown_channel_setting_null_tombstone",
			body: `{
				"channel_list": {"telegram": {"settings": {"removed_setting": null}}}
			}`,
			wantError: "unknown field(s): channel_list.telegram.settings.removed_setting",
		},
		{
			name: "aliased_channel_unknown_setting",
			body: `{
				"channel_list": {"telegram_alerts": {"settings": {"removed_setting": true}}}
			}`,
			wantError: "unknown field(s): channel_list.telegram_alerts.settings.removed_setting",
		},
		{
			name: "aliased_channel_unknown_setting_null_tombstone",
			body: `{
				"channel_list": {"telegram_alerts": {"settings": {"removed_setting": null}}}
			}`,
			wantError: "unknown field(s): channel_list.telegram_alerts.settings.removed_setting",
		},
		{
			name:      "duplicate_version",
			body:      fmt.Sprintf(`{"version":%d,"version":%d}`, config.CurrentVersion+1, config.CurrentVersion),
			wantError: "duplicate field: version",
		},
		{
			name:      "nested_duplicate_field",
			body:      `{"gateway":{"port":1234,"port":2345}}`,
			wantError: "duplicate field: gateway.port",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertRejectedConfigWritePreservesDocuments(
				t,
				configPath,
				http.MethodPatch,
				[]byte(test.body),
				test.wantError,
			)
		})
	}
}

func assertRejectedConfigWritePreservesDocuments(
	t *testing.T,
	configPath string,
	method string,
	body []byte,
	wantError string,
) {
	t.Helper()

	securityPath := filepath.Join(filepath.Dir(configPath), config.SecurityConfigFile)
	publicBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	securityBefore, err := os.ReadFile(securityPath)
	if err != nil {
		t.Fatalf("ReadFile(security) error = %v", err)
	}
	repository := config.NewRepository(configPath)
	snapshotBefore, err := repository.ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly(before) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(method, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if method == http.MethodPut {
		req.Header.Set("If-Match", configRevisionETag(snapshotBefore.Revision))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), wantError) {
		t.Fatalf("body = %q, want error containing %q", rec.Body.String(), wantError)
	}

	publicAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config after rejection) error = %v", err)
	}
	securityAfter, err := os.ReadFile(securityPath)
	if err != nil {
		t.Fatalf("ReadFile(security after rejection) error = %v", err)
	}
	if !bytes.Equal(publicBefore, publicAfter) || !bytes.Equal(securityBefore, securityAfter) {
		t.Fatal("rejected config write modified durable config documents")
	}
	snapshotAfter, err := repository.ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly(after) error = %v", err)
	}
	if snapshotAfter.Revision != snapshotBefore.Revision {
		t.Fatalf("revision after rejection = %q, want %q", snapshotAfter.Revision, snapshotBefore.Revision)
	}
}

func TestHandleUpdateConfig_RejectsStaleRevisionWithoutLosingNewerWrite(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", getRec.Code, getRec.Body.String())
	}
	staleRevision := getRec.Header().Get("ETag")

	newer, err := config.NewRepository(configPath).Update(func(cfg *config.Config) error {
		cfg.Gateway.Port = 23456
		return nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(getRec.Body.Bytes()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", staleRevision)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("PUT status = %d, want %d, body=%s", rec.Code, http.StatusPreconditionFailed, rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != configRevisionETag(newer.Revision) {
		t.Fatalf("conflict ETag = %q, want %q", got, configRevisionETag(newer.Revision))
	}
	current, err := config.NewRepository(configPath).ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly() error = %v", err)
	}
	if current.Config.Gateway.Port != 23456 {
		t.Fatalf("gateway.port = %d, want newer value 23456", current.Config.Gateway.Port)
	}
}

type blockingRequestBody struct {
	data    []byte
	started chan struct{}
	release chan struct{}
	read    bool
}

func (b *blockingRequestBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.EOF
	}
	b.read = true
	close(b.started)
	<-b.release
	return copy(p, b.data), nil
}

func (b *blockingRequestBody) Close() error { return nil }

func TestHandlePatchConfig_PreservesConcurrentRepositoryUpdate(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	body := &blockingRequestBody{
		data:    []byte(`{"agents":{"defaults":{"max_tokens":4321}}}`),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/config", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(rec, req)
	}()
	<-body.started
	if _, err := config.NewRepository(configPath).Update(func(cfg *config.Config) error {
		cfg.Gateway.Port = 23456
		return nil
	}); err != nil {
		t.Fatalf("concurrent Update() error = %v", err)
	}
	close(body.release)
	<-done
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	current, err := config.NewRepository(configPath).ReadOnly()
	if err != nil {
		t.Fatalf("ReadOnly() error = %v", err)
	}
	if current.Config.Gateway.Port != 23456 {
		t.Fatalf("gateway.port = %d, want concurrent value 23456", current.Config.Gateway.Port)
	}
	if current.Config.Agents.Defaults.MaxTokens != 4321 {
		t.Fatalf("agents.defaults.max_tokens = %d, want patch value 4321", current.Config.Agents.Defaults.MaxTokens)
	}
}

func TestHandleUpdateConfig_PreservesExecAllowRemoteDefaultWhenOmitted(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{
"version": 4,
		"agents": {
			"defaults": {
				"workspace": "~/.mintclaw/workspace"
			}
		},
		"model_list": [
			{
				"model_name": "custom-default",
				"provider": "openai",
				"model": "gpt-4o",
				"api_keys": ["sk-default"]
			}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	setConfigIfMatch(t, req, configPath)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !cfg.Tools.Exec.AllowRemote {
		t.Fatal("tools.exec.allow_remote should remain true when omitted from PUT /api/config")
	}
}

func TestHandleUpdateConfig_PreservesSkillsRegistryIntent(t *testing.T) {
	tests := []struct {
		name              string
		registries        string
		wantRegistryCount int
		wantGitHub        bool
		wantToken         string
	}{
		{
			name:              "omitted normalizes to defaults",
			wantRegistryCount: 2,
			wantGitHub:        true,
			wantToken:         "ghp-current",
		},
		{
			name:              "explicit empty disables defaults",
			registries:        `"registries": {}`,
			wantRegistryCount: 0,
		},
		{
			name: "rotates token without channels",
			registries: `"registries": {
				"github": {
					"enabled": true,
					"base_url": "https://github.com",
					"auth_token": "ghp-new"
				}
			}`,
			wantRegistryCount: 1,
			wantGitHub:        true,
			wantToken:         "ghp-new",
		},
		{
			name: "clears token without channels",
			registries: `"registries": {
				"github": {
					"enabled": true,
					"base_url": "https://github.com",
					"auth_token": ""
				}
			}`,
			wantRegistryCount: 1,
			wantGitHub:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath, cleanup := setupOAuthTestEnv(t)
			defer cleanup()
			repository := config.NewRepository(configPath)
			if _, err := repository.Update(func(cfg *config.Config) error {
				github, ok := cfg.Tools.Skills.Registries.Get("github")
				if !ok {
					return fmt.Errorf("github registry is not configured")
				}
				github.AuthToken = *config.NewSecureString("ghp-current")
				cfg.Tools.Skills.Registries.Set("github", github)
				return nil
			}); err != nil {
				t.Fatalf("seed GitHub token: %v", err)
			}

			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			body := fmt.Sprintf(`{
				"version": 4,
				"agents": {
					"defaults": {
						"workspace": "~/.mintclaw/workspace"
					}
				},
				"model_list": [
					{
						"model_name": "custom-default",
						"provider": "openai",
						"model": "gpt-4o",
						"api_keys": ["sk-default"]
					}
				],
				"tools": {
					"skills": {%s}
				}
			}`, tt.registries)
			req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			setConfigIfMatch(t, req, configPath)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}

			public, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if !strings.Contains(string(public), `"registries"`) {
				t.Fatalf("persisted config omitted canonical registries field:\n%s", public)
			}

			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if got := len(cfg.Tools.Skills.Registries); got != tt.wantRegistryCount {
				t.Fatalf("loaded registry count = %d, want %d", got, tt.wantRegistryCount)
			}
			if cfg.Tools.Skills.Registries == nil {
				t.Fatal("explicit empty registry map reloaded as nil")
			}
			github, hasGitHub := cfg.Tools.Skills.Registries.Get("github")
			if hasGitHub != tt.wantGitHub {
				t.Fatalf("loaded GitHub registry = %t, want %t", hasGitHub, tt.wantGitHub)
			}
			if hasGitHub && github.AuthToken.String() != tt.wantToken {
				t.Fatalf("loaded GitHub token = %q, want %q", github.AuthToken.String(), tt.wantToken)
			}
			security, err := os.ReadFile(filepath.Join(filepath.Dir(configPath), config.SecurityConfigFile))
			if err != nil {
				t.Fatalf("ReadFile(.security.yml) error = %v", err)
			}
			if strings.Contains(string(security), "ghp-current") != (tt.wantToken == "ghp-current") {
				t.Fatalf("old GitHub token persistence did not match replacement intent:\n%s", security)
			}
			if tt.wantToken != "" && !strings.Contains(string(security), tt.wantToken) {
				t.Fatalf("persisted security config omitted GitHub token %q:\n%s", tt.wantToken, security)
			}
		})
	}
}

func TestHandleUpdateConfig_DoesNotInheritDefaultModelFields(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{
		"version": 4,
		"agents": {
			"defaults": {
				"workspace": "~/.mintclaw/workspace"
			}
		},
		"model_list": [
			{
				"model_name": "custom-default",
				"provider": "openai",
				"model": "gpt-4o",
				"api_keys": ["sk-default"]
			}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	setConfigIfMatch(t, req, configPath)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := cfg.ModelList[0].APIBase; got != "" {
		t.Fatalf("model_list[0].api_base = %q, want empty string", got)
	}
}

func TestHandlePatchConfig_RejectsInvalidExecRegexPatterns(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"tools": {
			"exec": {
				"custom_deny_patterns": ["("]
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf(
			"status = %d, want %d, body=%s",
			rec.Code,
			http.StatusBadRequest,
			rec.Body.String(),
		)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("custom_deny_patterns")) {
		t.Fatalf(
			"expected validation error mentioning custom_deny_patterns, body=%s",
			rec.Body.String(),
		)
	}
}

func TestHandlePatchConfig_AllowsInvalidExecRegexPatternsWhenExecDisabled(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"tools": {
			"exec": {
				"enabled": false,
				"custom_deny_patterns": ["("],
				"custom_allow_patterns": ["("]
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestHandlePatchConfig_SavesChannelListSettingsPatch(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": {
			"feishu": {
				"enabled": true,
				"allow_from": ["ou_patch_user"],
				"settings": {
					"app_id": "cli_patch_app",
					"app_secret": "patch-secret",
					"is_lark": true
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	bc := cfg.Channels[config.ChannelFeishu]
	if !bc.Enabled {
		t.Fatal("feishu should be enabled after PATCH")
	}
	if len(bc.AllowFrom) != 1 || bc.AllowFrom[0] != "ou_patch_user" {
		t.Fatalf("feishu allow_from = %#v, want [\"ou_patch_user\"]", bc.AllowFrom)
	}
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	feishuCfg := decoded.(*config.FeishuSettings)
	if got := feishuCfg.AppID; got != "cli_patch_app" {
		t.Fatalf("feishu app_id = %q, want %q", got, "cli_patch_app")
	}
	if got := feishuCfg.AppSecret.String(); got != "patch-secret" {
		t.Fatalf("feishu app_secret = %q, want %q", got, "patch-secret")
	}
	if !feishuCfg.IsLark {
		t.Fatal("feishu is_lark should be true after PATCH")
	}
}

func TestHandlePatchConfig_NormalizesStringChannelArrayFields(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": {
			"mintclaw": {
				"type": "mintclaw",
				"allow_from": " ou_a\u200b，\u2060ou_b\tou_c\u202e，ou_a ",
				"group_trigger": {
					"prefixes": "/，!;\n?，/"
				},
				"placeholder": {
					"enabled": true,
					"text": "Working, please wait"
				},
				"settings": {
					"allow_origins": "https://a.example.com，http://localhost:5173，https://a.example.com"
				}
			},
			"irc": {
				"type": "irc",
				"settings": {
					"channels": "#ops,\n#dev,\n#ops",
					"request_caps": "multi-prefix，echo-message\tbatch，multi-prefix"
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	mintclawChannel := cfg.Channels[config.ChannelMintClaw]
	if len(mintclawChannel.AllowFrom) != 3 ||
		mintclawChannel.AllowFrom[0] != "ou_a" ||
		mintclawChannel.AllowFrom[1] != "ou_b" ||
		mintclawChannel.AllowFrom[2] != "ou_c" {
		t.Fatalf(
			"mintclaw allow_from = %#v, want [\"ou_a\", \"ou_b\", \"ou_c\"]",
			mintclawChannel.AllowFrom,
		)
	}
	if len(mintclawChannel.GroupTrigger.Prefixes) != 3 ||
		mintclawChannel.GroupTrigger.Prefixes[0] != "/" ||
		mintclawChannel.GroupTrigger.Prefixes[1] != "!;" ||
		mintclawChannel.GroupTrigger.Prefixes[2] != "?" {
		t.Fatalf(
			"mintclaw group_trigger.prefixes = %#v, want [\"/\", \"!;\", \"?\"]",
			mintclawChannel.GroupTrigger.Prefixes,
		)
	}
	if len(mintclawChannel.Placeholder.Text) != 1 ||
		mintclawChannel.Placeholder.Text[0] != "Working, please wait" {
		t.Fatalf(
			"mintclaw placeholder.text = %#v, want [\"Working, please wait\"]",
			mintclawChannel.Placeholder.Text,
		)
	}
	assertPersistedStringArray(
		t,
		configPath,
		[]string{"Working, please wait"},
		"channel_list",
		"mintclaw",
		"placeholder",
		"text",
	)

	decoded, err := mintclawChannel.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() mintclaw error = %v", err)
	}
	mintclawCfg := decoded.(*config.MintClawSettings)
	if len(mintclawCfg.AllowOrigins) != 2 ||
		mintclawCfg.AllowOrigins[0] != "https://a.example.com" ||
		mintclawCfg.AllowOrigins[1] != "http://localhost:5173" {
		t.Fatalf(
			"mintclaw allow_origins = %#v, want [\"https://a.example.com\", \"http://localhost:5173\"]",
			mintclawCfg.AllowOrigins,
		)
	}

	ircChannel := cfg.Channels[config.ChannelIRC]
	decoded, err = ircChannel.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() irc error = %v", err)
	}
	ircCfg := decoded.(*config.IRCSettings)
	if len(ircCfg.Channels) != 2 ||
		ircCfg.Channels[0] != "#ops" ||
		ircCfg.Channels[1] != "#dev" {
		t.Fatalf("irc channels = %#v, want [\"#ops\", \"#dev\"]", ircCfg.Channels)
	}
	if len(ircCfg.RequestCaps) != 3 ||
		ircCfg.RequestCaps[0] != "multi-prefix" ||
		ircCfg.RequestCaps[1] != "echo-message" ||
		ircCfg.RequestCaps[2] != "batch" {
		t.Fatalf(
			"irc request_caps = %#v, want [\"multi-prefix\", \"echo-message\", \"batch\"]",
			ircCfg.RequestCaps,
		)
	}
}

func TestHandlePatchConfig_NormalizesStringArraySettingsForNamedChannel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	if _, err := config.NewRepository(configPath).Update(func(cfg *config.Config) error {
		cfg.Channels["telegram_alerts"] = &config.Channel{
			Type:     config.ChannelTelegram,
			Settings: config.RawNode(`{}`),
		}
		return nil
	}); err != nil {
		t.Fatalf("add named channel: %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": {
			"telegram_alerts": {
				"settings": {"allowed_topic_ids": "100, 200"}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(updated) error = %v", err)
	}
	decoded, err := updated.Channels["telegram_alerts"].GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	settings := decoded.(*config.TelegramSettings)
	want := []string{"100", "200"}
	if !reflect.DeepEqual(settings.AllowedTopicIDs, want) {
		t.Fatalf("allowed_topic_ids = %#v, want %#v", settings.AllowedTopicIDs, want)
	}
}

func TestHandlePatchConfig_PreservesCanonicalPlaceholderText(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	want := []string{"Wait", "Wait", " Hold "}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Channels[config.ChannelMintClaw].Placeholder.Text = want
	if err = saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"agents": {"defaults": {"max_tokens": 1234}}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(updated) error = %v", err)
	}
	got := updated.Channels[config.ChannelMintClaw].Placeholder.Text
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("placeholder.text = %#v, want %#v", got, want)
	}
	assertPersistedStringArray(
		t,
		configPath,
		want,
		"channel_list",
		config.ChannelMintClaw,
		"placeholder",
		"text",
	)
}

func TestHandlePatchConfig_PreservesCanonicalWebPrivateHostWhitelist(t *testing.T) {
	want := []string{" 127.0.0.1 ", "127.0.0.1", ""}
	for _, tt := range []struct {
		name  string
		setup bool
		body  string
	}{
		{
			name:  "unrelated patch",
			setup: true,
			body:  `{"agents":{"defaults":{"max_tokens":1234}}}`,
		},
		{
			name: "explicit array",
			body: `{
				"tools": {
					"web": {
						"private_host_whitelist": [" 127.0.0.1 ", "127.0.0.1", ""]
					}
				}
			}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			configPath, cleanup := setupOAuthTestEnv(t)
			defer cleanup()

			if tt.setup {
				cfg, err := config.LoadConfig(configPath)
				if err != nil {
					t.Fatalf("LoadConfig() error = %v", err)
				}
				cfg.Tools.Web.PrivateHostWhitelist = want
				if err = saveTestConfig(configPath, cfg); err != nil {
					t.Fatalf("saveTestConfig() error = %v", err)
				}
			}

			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}

			updated, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig(updated) error = %v", err)
			}
			if !reflect.DeepEqual(updated.Tools.Web.PrivateHostWhitelist, want) {
				t.Fatalf("private_host_whitelist = %#v, want %#v", updated.Tools.Web.PrivateHostWhitelist, want)
			}
			assertPersistedStringArray(t, configPath, want, "tools", "web", "private_host_whitelist")
		})
	}
}

func TestHandleConfigWrite_NormalizesWebPrivateHostWhitelist(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			configPath, cleanup := setupOAuthTestEnv(t)
			defer cleanup()

			repository := config.NewRepository(configPath)
			snapshot, err := repository.ReadOnly()
			if err != nil {
				t.Fatalf("ReadOnly() error = %v", err)
			}
			payload := map[string]any{
				"tools": map[string]any{
					"web": map[string]any{},
				},
			}
			if method == http.MethodPut {
				body, marshalErr := json.Marshal(snapshot.Config)
				if marshalErr != nil {
					t.Fatalf("Marshal(config) error = %v", marshalErr)
				}
				if unmarshalErr := json.Unmarshal(body, &payload); unmarshalErr != nil {
					t.Fatalf("Unmarshal(config) error = %v", unmarshalErr)
				}
			}
			toolsMap := payload["tools"].(map[string]any)
			webMap := toolsMap["web"].(map[string]any)
			webMap["private_host_whitelist"] = "localhost, 10.0.0.0/8, localhost"
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("Marshal(payload) error = %v", err)
			}

			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			req := httptest.NewRequest(method, "/api/config", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if method == http.MethodPut {
				req.Header.Set("If-Match", configRevisionETag(snapshot.Revision))
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}

			updated, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			want := []string{"localhost", "10.0.0.0/8"}
			if len(updated.Tools.Web.PrivateHostWhitelist) != len(want) ||
				updated.Tools.Web.PrivateHostWhitelist[0] != want[0] ||
				updated.Tools.Web.PrivateHostWhitelist[1] != want[1] {
				t.Fatalf(
					"private_host_whitelist = %#v, want %#v",
					updated.Tools.Web.PrivateHostWhitelist,
					want,
				)
			}
			assertPersistedStringArray(t, configPath, want, "tools", "web", "private_host_whitelist")
		})
	}
}

func assertPersistedStringArray(t *testing.T, configPath string, want []string, path ...string) {
	t.Helper()

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var current any
	if err = json.Unmarshal(data, &current); err != nil {
		t.Fatalf("Unmarshal(config) error = %v", err)
	}
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("persisted %s parent = %T, want object", strings.Join(path, "."), current)
		}
		current, ok = object[key]
		if !ok {
			t.Fatalf("persisted %s is missing", strings.Join(path, "."))
		}
	}
	items, ok := current.([]any)
	if !ok {
		t.Fatalf("persisted %s = %T, want array", strings.Join(path, "."), current)
	}
	if len(items) != len(want) {
		t.Fatalf("persisted %s length = %d, want %d", strings.Join(path, "."), len(items), len(want))
	}
	for i, item := range items {
		if item != want[i] {
			t.Fatalf("persisted %s[%d] = %#v, want %q", strings.Join(path, "."), i, item, want[i])
		}
	}
}

func TestHandlePatchConfig_NormalizesSingleNumericAllowFrom(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": {
			"telegram": {
				"type": "telegram",
				"allow_from": 123456
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	telegramChannel := cfg.Channels[config.ChannelTelegram]
	if len(telegramChannel.AllowFrom) != 1 || telegramChannel.AllowFrom[0] != "123456" {
		t.Fatalf("telegram allow_from = %#v, want [\"123456\"]", telegramChannel.AllowFrom)
	}
}

func TestHandleConfigWriteRejectsMixedStringArrays(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			configPath, cleanup := setupOAuthTestEnv(t)
			defer cleanup()

			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("ReadFile(config) error = %v", err)
			}
			payload := map[string]any{
				"channel_list": map[string]any{
					"telegram": map[string]any{"type": "telegram"},
				},
			}
			if method == http.MethodPut {
				cfg, loadErr := config.LoadConfig(configPath)
				if loadErr != nil {
					t.Fatalf("LoadConfig() error = %v", loadErr)
				}
				body, marshalErr := json.Marshal(cfg)
				if marshalErr != nil {
					t.Fatalf("Marshal(config) error = %v", marshalErr)
				}
				if unmarshalErr := json.Unmarshal(body, &payload); unmarshalErr != nil {
					t.Fatalf("Unmarshal(config) error = %v", unmarshalErr)
				}
			}
			channelsMap := payload["channel_list"].(map[string]any)
			telegramMap := channelsMap[config.ChannelTelegram].(map[string]any)
			telegramMap["allow_from"] = []any{"trusted", 123}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("Marshal(payload) error = %v", err)
			}

			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			req := httptest.NewRequest(method, "/api/config", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if method == http.MethodPut {
				setConfigIfMatch(t, req, configPath)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}

			after, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("ReadFile(config) after request error = %v", err)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("rejected request changed persisted config")
			}
		})
	}
}

func TestHandlePatchConfig_RejectsInvalidStringArrayFields(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	telegramChannel := cfg.Channels[config.ChannelTelegram]
	telegramChannel.AllowFrom = []string{"existing-user"}
	if err := saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	tests := []struct {
		name string
		body string
	}{
		{
			name: "object allow_from",
			body: `{
				"channel_list": {
					"telegram": {
						"type": "telegram",
						"allow_from": {"id": "bad"}
					}
				}
			}`,
		},
		{
			name: "boolean allow_from",
			body: `{
				"channel_list": {
					"telegram": {
						"type": "telegram",
						"allow_from": true
					}
				}
			}`,
		},
		{
			name: "object settings array",
			body: `{
				"channel_list": {
					"irc": {
						"type": "irc",
						"settings": {
							"channels": {"name": "#ops"}
						}
					}
				}
			}`,
		},
		{
			name: "boolean placeholder text",
			body: `{
				"channel_list": {
					"telegram": {
						"type": "telegram",
						"placeholder": {"text": true}
					}
				}
			}`,
		},
		{
			name: "numeric placeholder item",
			body: `{
				"channel_list": {
					"telegram": {
						"type": "telegram",
						"placeholder": {"text": ["Wait", 1]}
					}
				}
			}`,
		},
		{
			name: "object private host whitelist",
			body: `{
				"tools": {
					"web": {
						"private_host_whitelist": {"host": "localhost"}
					}
				}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)

			req := httptest.NewRequest(
				http.MethodPatch,
				"/api/config",
				bytes.NewBufferString(tt.body),
			)
			req.Header.Set("Content-Type", "application/json")

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf(
					"PATCH /api/config status = %d, want %d, body=%s",
					rec.Code,
					http.StatusBadRequest,
					rec.Body.String(),
				)
			}

			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			telegramChannel := cfg.Channels[config.ChannelTelegram]
			if len(telegramChannel.AllowFrom) != 1 ||
				telegramChannel.AllowFrom[0] != "existing-user" {
				t.Fatalf(
					"telegram allow_from = %#v, want unchanged [\"existing-user\"]",
					telegramChannel.AllowFrom,
				)
			}
		})
	}
}

func TestHandlePatchConfig_RejectsNegativeStreamingDeliveryValues(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": {
			"mintclaw": {
				"settings": {
					"streaming": {
						"enabled": true,
						"throttle_seconds": -1
					}
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusBadRequest,
			rec.Body.String(),
		)
	}
	if !strings.Contains(rec.Body.String(), "streaming.throttle_seconds") {
		t.Fatalf(
			"response body = %q, want streaming.throttle_seconds validation error",
			rec.Body.String(),
		)
	}
}

func TestHandlePatchConfig_ClearingAllowFromDoesNotLeaveEmptyStringItem(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	feishuChannel := cfg.Channels[config.ChannelFeishu]
	feishuChannel.Enabled = true
	feishuChannel.AllowFrom = []string{"ou_existing_user"}
	decoded, err := feishuChannel.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	feishuCfg := decoded.(*config.FeishuSettings)
	feishuCfg.AppID = "cli_existing_app"
	feishuCfg.AppSecret = *config.NewSecureString("existing-secret")
	if err = saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": {
			"feishu": {
				"enabled": true,
				"allow_from": "",
				"settings": {
					"app_id": "cli_existing_app"
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	feishuChannel = cfg.Channels[config.ChannelFeishu]
	if len(feishuChannel.AllowFrom) != 0 {
		t.Fatalf("feishu allow_from = %#v, want empty slice", feishuChannel.AllowFrom)
	}

	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(configPath) error = %v", err)
	}
	if strings.Contains(string(configData), `"allow_from": [""]`) {
		t.Fatalf(
			"config file should not contain empty-string allow_from item: %s",
			string(configData),
		)
	}
}

func TestHandlePatchConfig_CreatesMissingChannelWithTypeAndSecret(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	delete(cfg.Channels, config.ChannelIRC)
	if err = saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": {
			"irc": {
				"enabled": true,
				"type": "irc",
				"settings": {
					"server": "irc.example.com",
					"password": "irc-patch-password"
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	bc := cfg.Channels[config.ChannelIRC]
	if bc == nil {
		t.Fatal("irc channel should exist after PATCH")
	}
	if got := bc.Type; got != config.ChannelIRC {
		t.Fatalf("irc type = %q, want %q", got, config.ChannelIRC)
	}
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	ircCfg := decoded.(*config.IRCSettings)
	if got := ircCfg.Server; got != "irc.example.com" {
		t.Fatalf("irc server = %q, want %q", got, "irc.example.com")
	}
	if got := ircCfg.Password.String(); got != "irc-patch-password" {
		t.Fatalf("irc password = %q, want %q", got, "irc-patch-password")
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(configPath) error = %v", err)
	}
	if bytes.Contains(configData, []byte("irc-patch-password")) {
		t.Fatalf("config file leaked irc password: %s", string(configData))
	}
}

// setupMintClawEnabledEnv creates a test environment with MintClaw channel enabled and
// its token stored only in .security.yml (not in the JSON payload).
func setupMintClawEnabledEnv(t *testing.T) (string, func()) {
	t.Helper()

	tmp := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldMintClawHome := os.Getenv("MINTCLAW_HOME")

	if err := os.Setenv("HOME", tmp); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	if err := os.Setenv("MINTCLAW_HOME", filepath.Join(tmp, ".mintclaw")); err != nil {
		t.Fatalf("set MINTCLAW_HOME: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "custom-default", Provider: "openai", Model: "gpt-4o",
		APIKeys: config.SimpleSecureStrings("sk-default"),
		Enabled: true,
	}}
	cfg.Agents.Defaults.ModelName = "custom-default"
	bc := cfg.Channels["mintclaw"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	mintclawCfg := decoded.(*config.MintClawSettings)
	bc.Enabled = true
	mintclawCfg.Token = *config.NewSecureString("test-mintclaw-token")

	configPath := filepath.Join(tmp, "config.json")
	if err := saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig error: %v", err)
	}

	cleanup := func() {
		_ = os.Setenv("HOME", oldHome)
		if oldMintClawHome == "" {
			_ = os.Unsetenv("MINTCLAW_HOME")
		} else {
			_ = os.Setenv("MINTCLAW_HOME", oldMintClawHome)
		}
	}
	return configPath, cleanup
}

func TestHandleUpdateConfig_SucceedsWhenMintClawTokenInSecurityOnly(t *testing.T) {
	configPath, cleanup := setupMintClawEnabledEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// PUT request with mintclaw enabled but no token in JSON — token is in .security.yml
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{
		"version": 4,
		"agents": {
			"defaults": {
				"workspace": "~/.mintclaw/workspace",
				"model_name": "custom-default"
			}
		},
		"channel_list": {
			"mintclaw": {
				"enabled": true,
				"type": "mintclaw",
				"settings": {
					"ping_interval": 30,
					"read_timeout": 60,
					"write_timeout": 10,
					"max_connections": 100
				}
			}
		},
		"model_list": [
			{
				"model_name": "custom-default",
				"provider": "openai",
				"model": "gpt-4o",
				"api_keys": ["sk-default"],
				"enabled": true
			}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	setConfigIfMatch(t, req, configPath)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PUT /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}
}

func TestHandlePatchConfig_SucceedsWhenMintClawTokenInSecurityOnly(t *testing.T) {
	configPath, cleanup := setupMintClawEnabledEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// PATCH request changing an unrelated field — mintclaw token still in .security.yml
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"gateway": {
			"log_level": "info"
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}
}

func TestHandleUpdateConfig_AppliesGatewayLogLevel(t *testing.T) {
	assertGatewayLogLevelApplied(t, http.MethodPut, `{
		"version": 4,
		"agents": {
			"defaults": {
				"workspace": "~/.mintclaw/workspace",
				"model_name": "custom-default"
			}
		},
		"gateway": {
			"log_level": "error"
		},
		"model_list": [
			{
				"model_name": "custom-default",
				"provider": "openai",
				"model": "gpt-4o",
				"api_keys": ["sk-default"],
				"enabled": true
			}
		]
	}`, logger.ERROR)
}

func TestHandlePatchConfig_AppliesGatewayLogLevel(t *testing.T) {
	assertGatewayLogLevelApplied(t, http.MethodPatch, `{
		"gateway": {
			"log_level": "debug"
		}
	}`, logger.DEBUG)
}

func TestHandlePatchConfig_PreservesDebugFlagOverride(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	initialLevel := logger.GetLevel()
	logger.SetLevel(logger.INFO)
	t.Cleanup(func() {
		logger.SetLevel(initialLevel)
	})

	h := NewHandler(configPath)
	h.SetDebug(true)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"gateway": {
			"log_level": "error"
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}
	if got := logger.GetLevel(); got != logger.DEBUG {
		t.Fatalf("logger.GetLevel() = %v, want %v", got, logger.DEBUG)
	}
}

func TestHandlePatchConfig_SavesDiscordTokenFromPayload(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": {
			"discord": {
				"enabled": true,
				"settings": {
					"token": "discord-test-token"
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	bc := cfg.Channels[config.ChannelDiscord]
	if !bc.Enabled {
		t.Fatal("discord should be enabled after PATCH")
	}
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	if got := decoded.(*config.DiscordSettings).Token.String(); got != "discord-test-token" {
		t.Fatalf("discord token = %q, want %q", got, "discord-test-token")
	}
}

func TestHandlePatchConfig_DoesNotPersistShadowRegistryAuthTokenField(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"tools": {
			"skills": {
				"registries": {
					"github": {
						"_auth_token": "ghp-shadow-token"
					}
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	githubRegistry, ok := cfg.Tools.Skills.Registries.Get("github")
	if !ok {
		t.Fatal("github registry missing after PATCH")
	}
	if got := githubRegistry.AuthToken.String(); got != "ghp-shadow-token" {
		t.Fatalf("github registry auth token = %q, want %q", got, "ghp-shadow-token")
	}
	if got := githubRegistry.BaseURL; got != "https://github.com" {
		t.Fatalf("github registry base_url = %q, want %q", got, "https://github.com")
	}

	rawConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(configPath) error = %v", err)
	}
	if strings.Contains(string(rawConfig), "_auth_token") {
		t.Fatalf(
			"config.json should not persist _auth_token shadow field, got:\n%s",
			string(rawConfig),
		)
	}
}

func TestHandleConfigWrite_ShadowRegistryTokenOverridesLegacyPlaceholder(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			configPath, cleanup := setupOAuthTestEnv(t)
			defer cleanup()
			if err := os.WriteFile(
				filepath.Join(filepath.Dir(configPath), "replacement.key"),
				[]byte("replacement-token\n"),
				0o600,
			); err != nil {
				t.Fatalf("write replacement credential: %v", err)
			}

			repository := config.NewRepository(configPath)
			if _, err := repository.Update(func(cfg *config.Config) error {
				registry, _ := cfg.Tools.Skills.Registries.Get("github")
				registry.AuthToken = *config.NewSecureString("current-token")
				cfg.Tools.Skills.Registries.Set("github", registry)
				return nil
			}); err != nil {
				t.Fatalf("seed registry credential: %v", err)
			}
			snapshot, err := repository.ReadOnly()
			if err != nil {
				t.Fatalf("ReadOnly() error = %v", err)
			}

			payload := map[string]any{
				"tools": map[string]any{
					"skills": map[string]any{
						"registries": map[string]any{
							"github": map[string]any{},
						},
					},
				},
			}
			if method == http.MethodPut {
				body, marshalErr := json.Marshal(snapshot.Config)
				if marshalErr != nil {
					t.Fatalf("Marshal(config) error = %v", marshalErr)
				}
				if unmarshalErr := json.Unmarshal(body, &payload); unmarshalErr != nil {
					t.Fatalf("Unmarshal(config) error = %v", unmarshalErr)
				}
			}
			toolsMap := payload["tools"].(map[string]any)
			skillsMap := toolsMap["skills"].(map[string]any)
			registriesMap := skillsMap["registries"].(map[string]any)
			registryMap := registriesMap["github"].(map[string]any)
			registryMap["auth_token"] = legacySecretPlaceholder
			registryMap["_auth_token"] = "file://replacement.key"
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("Marshal(payload) error = %v", err)
			}

			handler := NewHandler(configPath)
			mux := http.NewServeMux()
			handler.RegisterRoutes(mux)
			req := httptest.NewRequest(method, "/api/config", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if method == http.MethodPut {
				req.Header.Set("If-Match", configRevisionETag(snapshot.Revision))
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}

			updated, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			registry, ok := updated.Tools.Skills.Registries.Get("github")
			if !ok || registry.AuthToken.String() != "replacement-token" {
				t.Fatalf("updated registry = %#v", registry)
			}
			security, err := os.ReadFile(filepath.Join(filepath.Dir(configPath), config.SecurityConfigFile))
			if err != nil {
				t.Fatalf("ReadFile(.security.yml) error = %v", err)
			}
			if !bytes.Contains(security, []byte("file://replacement.key")) ||
				bytes.Contains(security, []byte("_auth_token")) {
				t.Fatalf("unexpected security document:\n%s", security)
			}
		})
	}
}

func TestHandlePatchConfig_RemovesRegistryWithStoredAuthToken(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	repository := config.NewRepository(configPath)
	if _, err := repository.Update(func(cfg *config.Config) error {
		cfg.Tools.Skills.Registries.Set("custom", config.SkillRegistryConfig{
			Enabled:   true,
			BaseURL:   "https://skills.example.com",
			AuthToken: *config.NewSecureString("custom-token"),
		})
		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"tools": {
			"skills": {
				"registries": {
					"custom": null
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if _, exists := cfg.Tools.Skills.Registries.Get("custom"); exists {
		t.Fatal("custom registry still exists after PATCH deletion")
	}
	securityData, err := os.ReadFile(filepath.Join(filepath.Dir(configPath), config.SecurityConfigFile))
	if err != nil {
		t.Fatalf("ReadFile(.security.yml) error = %v", err)
	}
	if strings.Contains(string(securityData), "registries:\n      custom:") ||
		strings.Contains(string(securityData), "custom-token") {
		t.Fatalf("removed registry still exists in .security.yml:\n%s", securityData)
	}
}

func TestHandleUpdateConfig_RemovesRegistryWithStoredAuthToken(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	repository := config.NewRepository(configPath)
	if _, err := repository.Update(func(cfg *config.Config) error {
		cfg.Tools.Skills.Registries.Set("custom", config.SkillRegistryConfig{
			Enabled:   true,
			BaseURL:   "https://skills.example.com",
			AuthToken: *config.NewSecureString("custom-token"),
		})
		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	snapshot, err := repository.ReadDurable()
	if err != nil {
		t.Fatalf("ReadDurable() error = %v", err)
	}
	delete(snapshot.Config.Tools.Skills.Registries, "custom")
	body, err := json.Marshal(snapshot.Config)
	if err != nil {
		t.Fatalf("Marshal(config) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", configRevisionETag(snapshot.Revision))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PUT /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if _, exists := cfg.Tools.Skills.Registries.Get("custom"); exists {
		t.Fatal("custom registry still exists after PUT deletion")
	}
	securityData, err := os.ReadFile(filepath.Join(filepath.Dir(configPath), config.SecurityConfigFile))
	if err != nil {
		t.Fatalf("ReadFile(.security.yml) error = %v", err)
	}
	if strings.Contains(string(securityData), "registries:\n      custom:") ||
		strings.Contains(string(securityData), "custom-token") {
		t.Fatalf("removed registry still exists in .security.yml:\n%s", securityData)
	}
}

func TestHandleResetConfig_DropsCustomRegistryCredential(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	repository := config.NewRepository(configPath)
	if _, err := repository.Update(func(cfg *config.Config) error {
		cfg.Tools.Skills.Registries.Set("custom", config.SkillRegistryConfig{
			Enabled:   true,
			BaseURL:   "https://skills.example.com",
			AuthToken: *config.NewSecureString("custom-token"),
		})
		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/config/reset", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"POST /api/config/reset status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if _, exists := cfg.Tools.Skills.Registries.Get("custom"); exists {
		t.Fatal("custom registry still exists after reset")
	}
	securityData, err := os.ReadFile(filepath.Join(filepath.Dir(configPath), config.SecurityConfigFile))
	if err != nil {
		t.Fatalf("ReadFile(.security.yml) error = %v", err)
	}
	if strings.Contains(string(securityData), "registries:\n      custom:") ||
		strings.Contains(string(securityData), "custom-token") {
		t.Fatalf("reset retained custom registry security:\n%s", securityData)
	}
}

func TestHandlePatchConfig_RejectsRemovedSkillRegistryShapes(t *testing.T) {
	tests := map[string]string{
		"github sibling": `{"tools":{"skills":{"github":{"token":"old"}}}}`,
		"registry list":  `{"tools":{"skills":{"registries":[{"name":"github"}]}}}`,
		"embedded name":  `{"tools":{"skills":{"registries":{"github":{"name":"github"}}}}}`,
		"token field":    `{"tools":{"skills":{"registries":{"github":{"token":"old"}}}}}`,
		"nested params":  `{"tools":{"skills":{"registries":{"github":{"param":{"proxy":"old"}}}}}}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			configPath, cleanup := setupOAuthTestEnv(t)
			defer cleanup()

			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)

			req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("PATCH /api/config status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body)
			}
		})
	}
}

func TestHandlePatchConfig_AllowsInvalidDenyRegexPatternsWhenDenyPatternsDisabled(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"tools": {
			"exec": {
				"enabled": true,
				"enable_deny_patterns": false,
				"custom_deny_patterns": ["("]
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// testCommandPatterns is a helper that sets up a handler and sends a test-command-patterns request.
func testCommandPatterns(t *testing.T, configPath string, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/config/test-command-patterns",
		bytes.NewBufferString(body),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandleTestCommandPatterns_MatchesWhitelist(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	rec := testCommandPatterns(t, configPath, `{
		"allow_patterns": ["^echo\\s+hello"],
		"deny_patterns": ["^rm\\s+-rf"],
		"command": "echo hello world"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"allowed":true`)) {
		t.Fatalf("expected allowed=true, body=%s", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"blocked":true`)) {
		t.Fatalf("expected blocked=false when whitelist matches, body=%s", rec.Body.String())
	}
}

func TestHandleTestCommandPatterns_MatchesBlacklistNotWhitelist(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	rec := testCommandPatterns(t, configPath, `{
		"allow_patterns": ["^echo\\s+hello"],
		"deny_patterns": ["^rm\\s+-rf"],
		"command": "rm -rf /tmp"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"blocked":true`)) {
		t.Fatalf("expected blocked=true, body=%s", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"allowed":true`)) {
		t.Fatalf(
			"expected allowed=false when blacklist matches but not whitelist, body=%s",
			rec.Body.String(),
		)
	}
}

func TestHandleTestCommandPatterns_MatchesNeither(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	rec := testCommandPatterns(t, configPath, `{
		"allow_patterns": ["^echo\\s+hello"],
		"deny_patterns": ["^rm\\s+-rf"],
		"command": "ls -la"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"allowed":true`)) {
		t.Fatalf("expected allowed=false, body=%s", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"blocked":true`)) {
		t.Fatalf("expected blocked=false, body=%s", rec.Body.String())
	}
}

func TestHandleTestCommandPatterns_CaseInsensitiveWithGoFlag(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	rec := testCommandPatterns(t, configPath, `{
		"allow_patterns": ["(?i)^ECHO"],
		"deny_patterns": [],
		"command": "echo hello"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"allowed":true`)) {
		t.Fatalf("expected allowed=true with Go (?i) flag, body=%s", rec.Body.String())
	}
}

func TestHandleTestCommandPatterns_EmptyPatterns(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	rec := testCommandPatterns(t, configPath, `{
		"allow_patterns": [],
		"deny_patterns": [],
		"command": "rm -rf /tmp"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"allowed":true`)) {
		t.Fatalf("expected allowed=false with empty patterns, body=%s", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"blocked":true`)) {
		t.Fatalf("expected blocked=false with empty patterns, body=%s", rec.Body.String())
	}
}

func TestHandleTestCommandPatterns_InvalidRegexSkipped(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	rec := testCommandPatterns(t, configPath, `{
		"allow_patterns": ["([[", "^echo"],
		"deny_patterns": [],
		"command": "echo hello"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"allowed":true`)) {
		t.Fatalf(
			"expected allowed=true, invalid pattern skipped and valid one matched, body=%s",
			rec.Body.String(),
		)
	}
}

func TestHandleTestCommandPatterns_ReturnsMatchedPattern(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	rec := testCommandPatterns(t, configPath, `{
		"allow_patterns": [],
		"deny_patterns": ["\\$(?i)[a-zA-Z_]*(SECRET|KEY|PASSWORD|TOKEN|AUTH)[a-zA-Z0-9_]*"],
		"command": "echo $GITHUB_API_KEY"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"blocked":true`)) {
		t.Fatalf("expected blocked=true, body=%s", rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`matched_blacklist`)) {
		t.Fatalf("expected matched_blacklist field, body=%s", rec.Body.String())
	}
}

func TestHandleTestCommandPatterns_InvalidJSON(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/config/test-command-patterns",
		bytes.NewBufferString(`{invalid json}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf(
			"status = %d, want %d, body=%s",
			rec.Code,
			http.StatusBadRequest,
			rec.Body.String(),
		)
	}
}

func TestApplyConfigSecretsFromMap_TelegramToken(t *testing.T) {
	cfg := config.DefaultConfig()
	bc := cfg.Channels["telegram"]
	bc.Enabled = true
	// Pre-decode so extend is populated
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	tgCfg := decoded.(*config.TelegramSettings)
	tgCfg.Token = *config.NewSecureString("original-token")

	raw := map[string]any{
		"channel_list": map[string]any{
			"telegram": map[string]any{
				"enabled": true,
				"token":   "secret-from-api",
			},
		},
	}

	if err := applyConfigSecretsFromMap(cfg, raw, filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("applyConfigSecretsFromMap() error = %v", err)
	}

	if got := tgCfg.Token.String(); got != "secret-from-api" {
		t.Fatalf("telegram token = %q, want %q", got, "secret-from-api")
	}
}

func TestApplyConfigSecretsFromMap_PreservesLegacyPlaceholders(t *testing.T) {
	cfg := config.DefaultConfig()
	telegram := cfg.Channels["telegram"]
	decoded, err := telegram.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	telegramSettings := decoded.(*config.TelegramSettings)
	telegramSettings.Token = *config.NewSecureString("current-channel-token")

	github, ok := cfg.Tools.Skills.Registries.Get("github")
	if !ok {
		t.Fatal("default GitHub registry is missing")
	}
	github.AuthToken = *config.NewSecureString("current-registry-token")
	cfg.Tools.Skills.Registries.Set("github", github)

	raw := map[string]any{
		"channel_list": map[string]any{
			"telegram": map[string]any{
				"settings": map[string]any{"token": legacySecretPlaceholder},
			},
		},
		"tools": map[string]any{
			"skills": map[string]any{
				"registries": map[string]any{
					"github": map[string]any{"auth_token": legacySecretPlaceholder},
				},
			},
		},
	}
	if err := applyConfigSecretsFromMap(cfg, raw, filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("applyConfigSecretsFromMap() error = %v", err)
	}

	if got := telegramSettings.Token.String(); got != "current-channel-token" {
		t.Fatalf("telegram token = %q, want preserved token", got)
	}
	github, _ = cfg.Tools.Skills.Registries.Get("github")
	if got := github.AuthToken.String(); got != "current-registry-token" {
		t.Fatalf("GitHub token = %q, want preserved token", got)
	}
}

func TestApplyConfigSecretsFromMap_ResolvesAndPersistsFileReferences(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(filepath.Join(configDir, "channel.key"), []byte("channel-secret\n"), 0o600); err != nil {
		t.Fatalf("write channel credential: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "registry.key"), []byte("registry-secret\n"), 0o600); err != nil {
		t.Fatalf("write registry credential: %v", err)
	}

	cfg := config.DefaultConfig()
	telegram := cfg.Channels["telegram"]
	telegram.Enabled = true
	decoded, err := telegram.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	telegramSettings := decoded.(*config.TelegramSettings)
	raw := map[string]any{
		"channel_list": map[string]any{
			"telegram": map[string]any{
				"enabled":  true,
				"settings": map[string]any{"token": "file://channel.key"},
			},
		},
		"tools": map[string]any{
			"skills": map[string]any{
				"registries": map[string]any{
					"github": map[string]any{"auth_token": "file://registry.key"},
				},
			},
		},
	}
	if err = applyConfigSecretsFromMap(cfg, raw, configPath); err != nil {
		t.Fatalf("applyConfigSecretsFromMap() error = %v", err)
	}

	if got := telegramSettings.Token.String(); got != "channel-secret" {
		t.Fatalf("telegram token = %q, want resolved file secret", got)
	}
	github, ok := cfg.Tools.Skills.Registries.Get("github")
	if !ok {
		t.Fatal("default GitHub registry is missing")
	}
	if got := github.AuthToken.String(); got != "registry-secret" {
		t.Fatalf("GitHub token = %q, want resolved file secret", got)
	}
	if _, err = config.NewRepository(configPath).Save(cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	security, err := os.ReadFile(filepath.Join(configDir, config.SecurityConfigFile))
	if err != nil {
		t.Fatalf("ReadFile(.security.yml) error = %v", err)
	}
	for _, reference := range []string{"file://channel.key", "file://registry.key"} {
		if !bytes.Contains(security, []byte(reference)) {
			t.Fatalf("security document omitted %q:\n%s", reference, security)
		}
	}
}

func TestApplyConfigSecretsFromMap_TeamsWebhook(t *testing.T) {
	// applyConfigSecretsFromMap recurses into nested maps to find
	// SecureString fields at any depth (e.g. webhook_url inside webhooks map).
	cfg := config.DefaultConfig()
	bc := &config.Channel{Enabled: true, Type: config.ChannelTeamsWebHook}
	cfg.Channels["teams_webhook"] = bc
	target := &config.TeamsWebhookSettings{
		Webhooks: map[string]config.TeamsWebhookTarget{
			"default": {
				WebhookURL: *config.NewSecureString("https://example.com/hook1"),
				Title:      "Default",
			},
		},
	}
	if err := bc.Decode(target); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	raw := map[string]any{
		"channel_list": map[string]any{
			"teams_webhook": map[string]any{
				"enabled": true,
				"settings": map[string]any{
					"webhooks": map[string]any{
						"default": map[string]any{
							"webhook_url": "https://example.com/hook-updated",
							"title":       "Default Updated",
						},
					},
				},
			},
		},
	}

	if err := applyConfigSecretsFromMap(cfg, raw, filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("applyConfigSecretsFromMap() error = %v", err)
	}

	// Verify the decoded struct has the updated SecureString value
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	twCfg, ok := decoded.(*config.TeamsWebhookSettings)
	if !ok {
		t.Fatalf("expected *TeamsWebhookSettings, got %T", decoded)
	}

	hookURL := twCfg.Webhooks["default"].WebhookURL
	if got := hookURL.String(); got != "https://example.com/hook-updated" {
		t.Fatalf("webhook_url = %q, want %q", got, "https://example.com/hook-updated")
	}
	// Note: title is a plain string, not a SecureString, so it is NOT updated
	// by applyConfigSecretsFromMap (only secure fields are handled).
}

func TestApplyConfigSecretsFromMap_MultipleChannels(t *testing.T) {
	cfg := config.DefaultConfig()

	// Setup telegram
	bc := cfg.Channels["telegram"]
	bc.Enabled = true
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() telegram error = %v", err)
	}
	tgCfg := decoded.(*config.TelegramSettings)
	tgCfg.Token = *config.NewSecureString("old-telegram-token")

	// Setup discord
	bc = cfg.Channels["discord"]
	bc.Enabled = true
	decoded, err = bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() discord error = %v", err)
	}
	discCfg := decoded.(*config.DiscordSettings)
	discCfg.Token = *config.NewSecureString("old-discord-token")

	raw := map[string]any{
		"channel_list": map[string]any{
			"telegram": map[string]any{
				"enabled": true,
				"settings": map[string]any{
					"token": "new-telegram-token",
				},
			},
			"discord": map[string]any{
				"enabled": true,
				"settings": map[string]any{
					"token": "new-discord-token",
				},
			},
		},
	}

	if err = applyConfigSecretsFromMap(cfg, raw, filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("applyConfigSecretsFromMap() error = %v", err)
	}

	if got := tgCfg.Token.String(); got != "new-telegram-token" {
		t.Fatalf("telegram token = %q, want %q", got, "new-telegram-token")
	}
	if got := discCfg.Token.String(); got != "new-discord-token" {
		t.Fatalf("discord token = %q, want %q", got, "new-discord-token")
	}
}

func TestApplyConfigSecretsFromMap_SkipsNonStringValues(t *testing.T) {
	cfg := config.DefaultConfig()
	bc := cfg.Channels["telegram"]
	bc.Enabled = true
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	tgCfg := decoded.(*config.TelegramSettings)
	tgCfg.Token = *config.NewSecureString("original-token")

	raw := map[string]any{
		"channel_list": map[string]any{
			"telegram": map[string]any{
				"enabled": true,
				"token":   12345, // not a string, should be skipped
			},
		},
	}

	if err = applyConfigSecretsFromMap(cfg, raw, filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("applyConfigSecretsFromMap() error = %v", err)
	}

	if got := tgCfg.Token.String(); got != "original-token" {
		t.Fatalf("telegram token = %q, want %q", got, "original-token")
	}
}

func TestApplyConfigSecretsFromMap_ChannelNotDecodedYet(t *testing.T) {
	cfg := config.DefaultConfig()
	bc := cfg.Channels["telegram"]
	bc.Enabled = true
	// Don't decode — let the function handle lazy decoding
	bc.Type = config.ChannelTelegram

	raw := map[string]any{
		"channel_list": map[string]any{
			"telegram": map[string]any{
				"enabled": true,
				"token":   "lazy-decoded-token",
			},
		},
	}

	if err := applyConfigSecretsFromMap(cfg, raw, filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("applyConfigSecretsFromMap() error = %v", err)
	}

	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	tgCfg := decoded.(*config.TelegramSettings)
	if got := tgCfg.Token.String(); got != "lazy-decoded-token" {
		t.Fatalf("telegram token = %q, want %q", got, "lazy-decoded-token")
	}
}
