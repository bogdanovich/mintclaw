//go:build linux || darwin

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/diagnostics"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/state"
)

// Exercise the real provider adapter and personal agent/session/trace pipeline.
// The HTTP peer scripts tool choices and captures actual request bytes; it is
// local and receives no real provider/vault credential or website traffic.
func credentialProviderFixture(
	t *testing.T,
	ctx context.Context,
	cfg *config.Config,
	running *services,
	messageBus *bus.MessageBus,
	providerKind, expectedOutcome string,
	capture func([]byte),
) (func() (browser.CredentialLoginResult, error), func()) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	sessionID := ""
	lastLogin := browser.CredentialLoginResult{}
	failure := ""
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(io.LimitReader(request.Body, 2*1024*1024))
		if err != nil {
			http.Error(writer, "fixture request unavailable", http.StatusBadRequest)
			return
		}
		wire, err := json.Marshal(struct {
			Headers http.Header     `json:"headers"`
			Body    json.RawMessage `json:"body"`
		}{request.Header, body})
		if err != nil {
			http.Error(writer, "fixture request invalid", http.StatusBadRequest)
			return
		}
		capture(wire)
		var input struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Input []struct {
				Type   string `json:"type"`
				Output string `json:"output"`
			} `json:"input"`
		}
		if json.Unmarshal(body, &input) != nil {
			http.Error(writer, "fixture request invalid", http.StatusBadRequest)
			return
		}
		var toolContent string
		for index := len(input.Messages) - 1; index >= 0; index-- {
			if input.Messages[index].Role == "tool" {
				_ = json.Unmarshal(input.Messages[index].Content, &toolContent)
				break
			}
		}
		if providerKind == "codex" {
			for index := len(input.Input) - 1; index >= 0; index-- {
				if input.Input[index].Type == "function_call_output" {
					toolContent = input.Input[index].Output
					break
				}
			}
		}
		mu.Lock()
		defer mu.Unlock()
		phase := calls % 6
		calls++
		decode := func(target any) bool {
			return json.NewDecoder(strings.NewReader(toolContent)).Decode(target) == nil
		}
		name := ""
		var arguments map[string]any
		switch phase {
		case 0:
			name = "browser_session"
			arguments = map[string]any{
				"operation": "open", "target": config.BrowserDefaultTarget, "profile": "ephemeral",
				"interaction_language": "en",
			}
		case 1:
			var opened struct {
				ID string `json:"browser_session_id"`
			}
			if !decode(&opened) || opened.ID == "" {
				failure = "provider did not receive the session result"
			}
			sessionID = opened.ID
			name, arguments = "browser_observe", map[string]any{"browser_session_id": sessionID}
		case 2:
			var observation browser.Observation
			if !decode(&observation) {
				failure = "provider did not receive an observation"
			}
			arguments, err = credentialFixtureLoginArguments(observation)
			if err != nil {
				failure = "provider did not receive usable current references"
			}
			name = "browser_login"
		case 3:
			if !decode(&lastLogin) || lastLogin.Alias != "fixture-account" || lastLogin.Outcome != expectedOutcome {
				failure = "provider did not receive the expected fixed alias outcome"
			}
			name, arguments = "browser_observe", map[string]any{"browser_session_id": sessionID}
			if expectedOutcome == "outcome_unknown" {
				name = "browser_session"
				arguments["operation"] = "status"
			}
		case 4:
			var observation browser.Observation
			if expectedOutcome != "outcome_unknown" && (!decode(&observation) || observation.SnapshotID == "") {
				failure = "provider did not receive an authenticated observation"
			}
			name = "browser_session"
			arguments = map[string]any{"operation": "close", "browser_session_id": sessionID}
		case 5:
			var closed struct {
				State browser.SessionState `json:"state"`
			}
			if !decode(&closed) || !closed.State.Terminal() {
				failure = "provider did not receive acknowledged browser cleanup"
			}
		}
		message := map[string]any{"role": "assistant", "content": "fixture complete"}
		finish := "stop"
		if name != "" {
			encoded, encodeErr := json.Marshal(arguments)
			if encodeErr != nil {
				failure = "provider fixture arguments unavailable"
			}
			message["content"] = ""
			message["tool_calls"] = []any{map[string]any{
				"index": 0, "id": "fixture-call-" + string(rune('a'+calls)), "type": "function",
				"function": map[string]any{"name": name, "arguments": string(encoded)},
			}}
			finish = "tool_calls"
		}
		if failure != "" {
			http.Error(writer, "fixture transition unavailable", http.StatusBadRequest)
			return
		}
		if providerKind == "codex" {
			output := map[string]any{
				"id": "fixture-message", "type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "fixture complete"}},
			}
			if name != "" {
				encoded, _ := json.Marshal(arguments)
				output = map[string]any{
					"id": "fixture-function", "type": "function_call", "status": "completed",
					"call_id": "fixture-call-" + string(rune('a'+calls)), "name": name, "arguments": string(encoded),
				}
			}
			payload, _ := json.Marshal(map[string]any{
				"type": "response.completed", "sequence_number": 1,
				"response": map[string]any{
					"id": "fixture-response", "object": "response", "status": "completed", "output": []any{output},
				},
			})
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = writer.Write(
				append(
					append([]byte("event: response.completed\ndata: "), payload...),
					[]byte("\n\ndata: [DONE]\n\n")...),
			)
			return
		}
		if input.Stream {
			delete(message, "role")
			payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
				"index": 0, "delta": message, "finish_reason": finish,
			}}})
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = writer.Write(append(append([]byte("data: "), payload...), []byte("\n\ndata: [DONE]\n\n")...))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"choices": []any{map[string]any{
			"index": 0, "message": message, "finish_reason": finish,
		}}})
	}))
	t.Cleanup(server.Close)
	cfg.Agents.List = []config.AgentConfig{{
		ID: "browser", Default: true,
		ToolPolicy: &config.AgentCapabilityPolicy{
			Default: config.AgentCapabilityDefaultDeny,
			Allow:   []string{"browser_session", "browser_observe", "browser_login"},
		},
	}}
	cfg.Agents.Defaults.ModelName, cfg.Agents.Defaults.Provider = "fixture-model", "openai"
	cfg.Agents.Defaults.ContextManager = "seahorse"
	cfg.Agents.Defaults.MaxToolIterations = 12
	cfg.Agents.Defaults.MaxLLMRetries = 0
	cfg.Diagnostics.TraceCapture.Enabled = true
	cfg.Diagnostics.TraceCapture.ContentMode = "redacted_content"
	cfg.Diagnostics.TraceCapture.StateDir = filepath.Join(cfg.WorkspacePath(), "state", "credential-fixture-traces")
	model := &config.ModelConfig{ModelName: "fixture-model", Provider: "openai", Model: "gpt-4o", APIBase: server.URL}
	model.SetAPIKey("synthetic-model-api-key")
	cfg.ModelList = config.SecureModelList{model}
	provider, _, err := providers.CreateProviderFromConfig(model)
	if err != nil {
		t.Fatal("local provider adapter unavailable")
	}
	if providerKind == "codex" {
		// Intercept the production SDK client without loading real OAuth
		// credentials or adding a runtime endpoint override. These fixtures
		// run sequentially; restore the client after the agent loop closes.
		endpoint, parseErr := url.Parse(server.URL)
		if parseErr != nil {
			t.Fatal("local Codex endpoint unavailable")
		}
		previousClient := http.DefaultClient
		http.DefaultClient = &http.Client{Transport: credentialCodexFixtureTransport{endpoint: endpoint}}
		t.Cleanup(func() { http.DefaultClient = previousClient })
		for _, key := range []string{"OPENAI_ORG_ID", "OPENAI_PROJECT_ID", "OPENAI_WEBHOOK_SECRET"} {
			t.Setenv(key, "")
		}
		provider = providers.NewCodexProvider("synthetic-model-api-key", "synthetic-model-account")
		model.Model = providers.CodexDefaultModel
		model.Streaming.Enabled = true
	}
	loop, err := agent.NewAgentLoopChecked(cfg, messageBus, provider,
		agent.WithStateManager(state.NewManager(cfg.WorkspacePath())))
	if err != nil {
		t.Fatalf("provider-backed loop fixture unavailable: %v", err)
	}
	if setupErr := setupBrowserTools(cfg, loop, running); setupErr != nil {
		t.Fatalf("provider-backed browser tool fixture unavailable: %v", setupErr)
	}
	messageBus.SetEventPublisher(loop.RuntimeEventBus())
	var closeOnce sync.Once
	closeLoop := func() { closeOnce.Do(loop.Close) }
	t.Cleanup(closeLoop)
	turns := 0
	previousHistoryMessages := 0
	perform := func() (browser.CredentialLoginResult, error) {
		turns++
		response, runErr := loop.ProcessDirectWithOptions(ctx,
			"Log in to fixture-account, observe the result, close the browser, then report completion.",
			"fixture-provider-session-"+string(rune('a'+turns)), "telegram", "fixture-owner-chat",
			agent.DirectTurnOptions{SuppressBackgroundCompaction: true})
		mu.Lock()
		result, fixedFailure := lastLogin, failure
		mu.Unlock()
		if runErr != nil || !strings.Contains(response, "fixture complete") || fixedFailure != "" {
			if fixedFailure == "" {
				fixedFailure = "provider-backed agent turn failed"
			}
			return browser.CredentialLoginResult{}, errors.New(fixedFailure)
		}
		store := loop.GetRegistry().GetDefaultAgent().Sessions
		keys := store.ListSessions()
		if len(keys) == 0 {
			return browser.CredentialLoginResult{}, errors.New("provider turn did not create durable history")
		}
		historyMessages := 0
		for _, key := range keys {
			history, historyErr := store.ReadTurnHistory(t.Context(), key)
			if historyErr != nil || len(history) == 0 {
				return browser.CredentialLoginResult{}, errors.New("provider turn history unavailable")
			}
			encoded, _ := json.Marshal(history)
			capture(encoded)
			historyMessages += len(history)
		}
		if historyMessages <= previousHistoryMessages {
			return browser.CredentialLoginResult{}, errors.New("provider turn did not append canonical history")
		}
		previousHistoryMessages = historyMessages
		return result, nil
	}
	var finalizeOnce sync.Once
	finalize := func() {
		finalizeOnce.Do(func() {
			closeLoop()
			qualifyCredentialTraceExports(t, cfg, capture)
			mu.Lock()
			defer mu.Unlock()
			if calls != 12 || failure != "" {
				t.Error("provider qualification did not complete its two real request sequences")
			}
		})
	}
	return perform, finalize
}

type credentialCodexFixtureTransport struct{ endpoint *url.URL }

func (transport credentialCodexFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Host != "chatgpt.com" ||
		request.URL.Path != "/backend-api/codex/responses" {
		return nil, errors.New("unexpected Codex fixture destination")
	}
	local := request.Clone(request.Context())
	local.URL.Scheme, local.URL.Host = transport.endpoint.Scheme, transport.endpoint.Host
	local.Host = transport.endpoint.Host
	return http.DefaultTransport.RoundTrip(local)
}

func qualifyCredentialTraceExports(t *testing.T, cfg *config.Config, capture func([]byte)) {
	t.Helper()
	root := diagnostictrace.ResolveStoreRoot(cfg.Diagnostics.TraceCapture.StateDir, cfg.WorkspacePath())
	count := 0
	parent := filepath.Join(cfg.WorkspacePath(), "state", "credential-fixture-bundles")
	if os.MkdirAll(parent, 0o700) != nil {
		t.Error("credential export qualification directory unavailable")
		return
	}
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, entryErr error) error {
		if entryErr != nil || entry.IsDir() {
			return entryErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var trace diagnostictrace.Trace
		if json.Unmarshal(data, &trace) != nil || diagnostictrace.Validate(trace) != nil {
			return errors.New("generated credential trace is invalid")
		}
		capture(data)
		query, queryErr := diagnostics.Query(diagnostics.QueryOptions{
			Stores:   []diagnostics.StoreSpec{{ID: "credential-fixture", Kind: "personal", Root: root}},
			Selector: diagnostics.Selector{TraceIDs: []string{trace.TraceID}},
		})
		if queryErr != nil || len(query.Matches) != 1 {
			return errors.New("generated credential trace was not discoverable")
		}
		for _, rich := range []bool{false, true} {
			suffix := "-metadata"
			if rich {
				suffix = "-private"
			}
			_, bundleErr := diagnostics.WriteBundle(diagnostics.BundleOptions{
				OutputDir: filepath.Join(parent, trace.TraceID+suffix), Query: query,
				Source:                 diagnostics.DefaultBundleSource("synthetic", strings.Repeat("a", 40)),
				IncludeRedactedContent: rich,
			})
			if bundleErr != nil {
				return errors.New("generated credential trace export failed")
			}
		}
		count++
		return nil
	})
	if walkErr != nil || count != 2 {
		stage := "count"
		if walkErr != nil {
			switch walkErr.Error() {
			case "generated credential trace is invalid":
				stage = "trace-validation"
			case "generated credential trace was not discoverable":
				stage = "trace-query"
			case "generated credential trace export failed":
				stage = "bundle-write"
			default:
				stage = "filesystem"
			}
		}
		t.Errorf("provider trace/export qualification failed at %s after %d traces", stage, count)
	}
}
