//go:build linux && amd64 && integration

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "document" && os.Args[2] == "_worker" {
		input := os.NewFile(document.WorkerInputFileDescriptor(), "document-snapshot")
		if input == nil {
			os.Exit(1)
		}
		err := document.ServeWorker(os.Stdin, input, os.Stdout)
		_ = input.Close()
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestDocumentPDFTelegramVerticalSlice(t *testing.T) {
	requireDocumentReadBackend(t)

	t.Run("bounded text answer keeps extracted content live only", func(t *testing.T) {
		workspace := documentE2EWorkspace(t)
		store, ref, digest, sourcePath := documentE2ESource(t, "text.pdf")
		provider := documentTextE2EProvider(ref, digest, sourcePath)
		fixture := newAgentLoopTestFixtureWithWorkspace(t, workspace, provider, func(cfg *config.Config) {
			configureDocumentE2E(cfg, provider.GetDefaultModel(), false)
		})
		fixture.Loop.SetMediaStore(store)

		channel := &fakeMediaChannel{fakeChannel: fakeChannel{id: "document-text-e2e"}}
		stop := startDocumentE2EChannel(t, fixture, store, channel)
		defer stop()
		publishDocumentE2EInbound(t, fixture.Bus, ref, "Read the marker in page 1 and cite the page.")

		waitDocumentE2E(t, func() bool { return len(channel.messagesSnapshot()) == 1 })
		messages := channel.messagesSnapshot()
		if messages[0].Content != "The marker is MintClaw text fixture [page 1]." {
			t.Fatalf("final answer = %q", messages[0].Content)
		}
		if err := provider.AssertExhausted(); err != nil {
			t.Fatal(err)
		}
		assertDocumentE2ETrace(t, workspace, digest, sourcePath, "same-name.pdf", "MintClaw text fixture")
		for _, sessionKey := range fixture.Agent.Sessions.ListSessions() {
			history := fixture.Agent.Sessions.GetHistory(sessionKey)
			for _, message := range history {
				if message.Role == "tool" && strings.Contains(message.Content, "MintClaw text fixture") {
					t.Fatalf("durable tool result retained extracted text: %#v", message)
				}
				if strings.Contains(message.Content, sourcePath) {
					t.Fatalf("durable history leaked source path: %#v", message)
				}
			}
		}
	})

	t.Run("retained render uses the durable Telegram outbox", func(t *testing.T) {
		workspace := documentE2EWorkspace(t)
		store, ref, digest, sourcePath := documentE2ESource(t, "rotated-crop.pdf")
		provider := documentRenderE2EProvider(ref, digest, sourcePath)
		fixture := newAgentLoopTestFixtureWithWorkspace(t, workspace, provider, func(cfg *config.Config) {
			configureDocumentE2E(cfg, provider.GetDefaultModel(), true)
		})
		fixture.Loop.SetMediaStore(store)

		channel := &fakeMediaChannel{fakeChannel: fakeChannel{id: "document-render-e2e"}}
		stop := startDocumentE2EChannel(t, fixture, store, channel)
		defer stop()
		publishDocumentE2EInbound(t, fixture.Bus, ref, "Render page 1, send it to me, then confirm the page.")

		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			channel.mu.Lock()
			ready := len(channel.sentMedia) >= 1 && len(channel.sentMessages) >= 1
			channel.mu.Unlock()
			if ready {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		channel.mu.Lock()
		mediaCount := len(channel.sentMedia)
		textCount := len(channel.sentMessages)
		if mediaCount == 0 || textCount == 0 {
			channel.mu.Unlock()
			t.Fatalf(
				"timed out waiting for Telegram delivery: media=%d text=%d",
				mediaCount,
				textCount,
			)
		}
		if mediaCount != 1 || textCount != 1 {
			channel.mu.Unlock()
			t.Fatalf("Telegram delivery count = (media %d, text %d), want exactly once", mediaCount, textCount)
		}
		delivered := channel.sentMedia[0]
		final := channel.sentMessages[0]
		channel.mu.Unlock()
		if delivered.Channel != "telegram" || delivered.ChatID != "pdf-chat" ||
			delivered.Context.TopicID != "pdf-topic" || len(delivered.Parts) != 1 {
			t.Fatalf("delivered media = %#v", delivered)
		}
		path, err := store.Resolve(delivered.Parts[0].Ref)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
			t.Fatal("Telegram render delivery is not a PNG")
		}
		if final.Content != "Rendered and delivered source page 1." {
			t.Fatalf("final answer = %q", final.Content)
		}
		if err = provider.AssertExhausted(); err != nil {
			t.Fatal(err)
		}
		assertDocumentE2ETrace(t, workspace, digest, sourcePath, "same-name.pdf")
	})
}

func TestDocumentRenderToolLinuxIntegration(t *testing.T) {
	requireDocumentReadBackend(t)
	workspace := t.TempDir()
	store, ref, _, _ := documentE2ESource(t, "rotated-crop.pdf")
	owner, err := media.NewMediaOwner(
		workspace,
		"main",
		"pdf-operator",
		"document-render-route",
		"document-render-session",
		"telegram",
		"pdf-chat",
		"pdf-topic",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	tool := tools.NewDocumentTool()
	tool.SetMediaStore(store)
	ctx := toolshared.WithToolInboundContext(t.Context(), "telegram", "pdf-chat", "pdf-message", "")
	ctx = toolshared.WithToolInboundMetadata(ctx, bus.InboundContext{
		Channel: "telegram", ChatID: "pdf-chat", TopicID: "pdf-topic",
		SenderID: "pdf-operator", ActorID: "pdf-operator",
	})
	ctx = toolshared.WithToolTopicID(ctx, "pdf-topic")
	ctx = toolshared.WithToolSessionContext(ctx, "main", "document-render-session", nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, "document-render-route")
	ctx = toolshared.WithToolExecutionIdentity(ctx, workspace, "document-render-execution")
	ctx = toolshared.WithToolDocumentContext(ctx, []string{ref}, true)
	result := tool.Execute(ctx, map[string]any{
		"action": "render", "source": ref, "pages": []any{float64(1)},
	})
	if result.IsError {
		t.Fatalf("render failed: safe=%s internal=%v", result.ForLLM, result.Err)
	}
	if len(result.ContextMedia) != 1 {
		t.Fatalf("rendered refs = %#v", result.ContextMedia)
	}
	if err = tool.CleanupTurn(ctx); err != nil {
		t.Fatal(err)
	}
}

func requireDocumentReadBackend(t *testing.T) {
	t.Helper()
	capabilities := document.Capabilities()
	for _, operation := range []string{"inspect", "extract", "render"} {
		if capabilities.Operations[operation].State != document.CapabilitySupported {
			if os.Getenv("MINTCLAW_REQUIRE_DOCUMENT_AGENT_E2E") == "1" {
				t.Fatalf(
					"document %s capability is required: %s",
					operation,
					capabilities.Operations[operation].Reason,
				)
			}
			t.Skipf("document %s capability is unavailable: %s", operation, capabilities.Operations[operation].Reason)
		}
	}
}

func configureDocumentE2E(cfg *config.Config, model string, vision bool) {
	cfg.Agents.Defaults.ModelName = model
	cfg.Agents.Defaults.ResponseFooter.Enabled = false
	cfg.Agents.Defaults.ToolFeedback.Enabled = false
	cfg.Tools.Document.Enabled = true
	cfg.Diagnostics.TraceCapture = config.DiagnosticTraceCaptureConfig{
		Enabled: true, ContentMode: "redacted_content", RetentionHours: 1, MaxTraces: 10,
	}
	if vision {
		cfg.ModelList = []*config.ModelConfig{{
			ModelName: model,
			Provider:  "openai",
			Model:     model,
			Enabled:   true,
			Capabilities: &config.ModelCapabilities{
				Vision: &config.ModelCapabilityOverride{},
			},
		}}
	}
}

func documentE2EWorkspace(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	source := filepath.Join(filepath.Dir(currentFile), "..", "..", "workspace", "skills", "pdf", "SKILL.md")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	destination := filepath.Join(workspace, "skills", "pdf")
	if err = os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(destination, "SKILL.md"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return workspace
}

func documentE2ESource(t *testing.T, fixture string) (*media.FileMediaStore, string, string, string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	source := filepath.Join(filepath.Dir(currentFile), "..", "document", "testdata", fixture)
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	store := media.NewFileMediaStore()
	ref, err := store.Store(source, media.MediaMeta{
		Filename:      "same-name.pdf",
		ContentType:   "application/octet-stream",
		Source:        "test:document-telegram-e2e",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "document-telegram-e2e-source")
	if err != nil {
		t.Fatal(err)
	}
	return store, ref, hex.EncodeToString(digest[:]), source
}

func documentTextE2EProvider(ref, digest, sourcePath string) *llmscenario.ScriptedProvider {
	return llmscenario.NewScriptedProvider(
		"document-text-e2e-model",
		llmscenario.ProviderStep{
			Name:   "discover document tool",
			Assert: documentFirstCallAssertion(ref, sourcePath),
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"search-document", tools.BM25SearchToolName, map[string]any{
					"query": "inspect extract render the exact current PDF attachment",
				},
			)),
		},
		llmscenario.ProviderStep{
			Name:   "inspect document",
			Assert: llmscenario.RequireToolDefinition("document"),
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"inspect-document", "document", map[string]any{"action": "inspect", "source": ref},
			)),
		},
		llmscenario.ProviderStep{
			Name: "extract page",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := llmscenario.RequireLastMessage("tool", digest)(call); err != nil {
					return err
				}
				return llmscenario.RequireLastMessage("tool", "\"page_count\":1")(call)
			},
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"extract-document", "document", map[string]any{
					"action": "extract", "source": ref, "pages": []any{float64(1)},
				},
			)),
		},
		llmscenario.ProviderStep{
			Name: "answer from protected text",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := llmscenario.RequireLastMessage("tool", "[page 1]")(call); err != nil {
					return err
				}
				return llmscenario.RequireLastMessage("tool", "MintClaw text fixture")(call)
			},
			Response: llmscenario.TextResponse("The marker is MintClaw text fixture [page 1]."),
		},
	)
}

func documentRenderE2EProvider(ref, digest, sourcePath string) *llmscenario.ScriptedProvider {
	return llmscenario.NewScriptedProvider(
		"document-render-e2e-model",
		llmscenario.ProviderStep{
			Name:   "discover document tool",
			Assert: documentFirstCallAssertion(ref, sourcePath),
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"search-document", tools.BM25SearchToolName, map[string]any{
					"query": "inspect and render the exact current PDF attachment",
				},
			)),
		},
		llmscenario.ProviderStep{
			Name:   "inspect document",
			Assert: llmscenario.RequireToolDefinition("document"),
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"inspect-document", "document", map[string]any{"action": "inspect", "source": ref},
			)),
		},
		llmscenario.ProviderStep{
			Name: "render retained page",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := llmscenario.RequireLastMessage("tool", digest)(call); err != nil {
					return err
				}
				return llmscenario.RequireLastMessage("tool", "\"page_count\":1")(call)
			},
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"render-document", "document", map[string]any{
					"action": "render", "source": ref, "pages": []any{float64(1)}, "retain": true,
				},
			)),
		},
		llmscenario.ProviderStep{
			Name: "confirm rendered page",
			Assert: func(call llmscenario.ProviderCall) error {
				foundReport := false
				foundImage := false
				for index := len(call.Messages) - 1; index >= 0; index-- {
					message := call.Messages[index]
					if message.Role == "tool" && strings.Contains(message.Content, "page_render") {
						foundReport = true
					}
					if len(message.Media) == 1 {
						foundImage = true
					}
				}
				if !foundReport || !foundImage {
					return fmt.Errorf("render report/image context = (%v, %v)", foundReport, foundImage)
				}
				return nil
			},
			Response: llmscenario.TextResponse("Rendered and delivered source page 1."),
		},
	)
}

func documentFirstCallAssertion(ref, sourcePath string) func(llmscenario.ProviderCall) error {
	return func(call llmscenario.ProviderCall) error {
		if err := llmscenario.RequireToolDefinition(tools.BM25SearchToolName)(call); err != nil {
			return err
		}
		for _, definition := range call.Tools {
			if definition.Function.Name == "document" {
				return fmt.Errorf("hidden document tool was exposed before discovery")
			}
		}
		joined := documentE2ECallText(call)
		if !strings.Contains(joined, "Use this skill only for the exact") || !strings.Contains(joined, ref) {
			return fmt.Errorf("PDF skill or exact ref is absent from first call")
		}
		if strings.Contains(joined, sourcePath) || strings.Contains(joined, "%PDF-") {
			return fmt.Errorf("first call leaked local path or PDF bytes")
		}
		return nil
	}
}

func documentE2ECallText(call llmscenario.ProviderCall) string {
	var builder strings.Builder
	for _, message := range call.Messages {
		builder.WriteString(message.Content)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func startDocumentE2EChannel(
	t *testing.T,
	fixture *agentLoopTestFixture,
	store media.MediaStore,
	channel *fakeMediaChannel,
) func() {
	t.Helper()
	coordinator, err := outbox.OpenCoordinator(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture.Loop.SetOutboundOutbox(coordinator)
	t.Cleanup(func() {
		fixture.Loop.SetOutboundOutbox(nil)
		if err := coordinator.Close(); err != nil {
			t.Errorf("close outbox: %v", err)
		}
	})
	manager := newStartedTestChannelManagerWithConfig(
		t,
		fixture.Config,
		fixture.Bus,
		store,
		"telegram",
		channel,
		channels.WithRuntimeEvents(fixture.Loop.runtimeEvents),
		channels.WithOutboundOutbox(coordinator),
	)
	fixture.Loop.SetChannelManager(manager)
	runContext, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- fixture.Loop.Run(runContext) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("agent loop: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("agent loop did not stop")
		}
	}
}

func publishDocumentE2EInbound(t *testing.T, messageBus *bus.MessageBus, ref, content string) {
	t.Helper()
	if err := messageBus.PublishInbound(t.Context(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:   "telegram",
			ChatID:    "pdf-chat",
			ChatType:  "direct",
			TopicID:   "pdf-topic",
			SenderID:  "pdf-operator",
			ActorID:   "pdf-operator",
			MessageID: "pdf-message",
		},
		Content:    content,
		Media:      []string{ref},
		SessionKey: "document-pdf1a-e2e",
		SpoolID:    "document-pdf1a-e2e-" + strings.TrimPrefix(ref, "media://"),
	}); err != nil {
		t.Fatal(err)
	}
}

func waitDocumentE2E(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for PDF1A Telegram vertical slice")
}

func assertDocumentE2ETrace(t *testing.T, workspace, digest string, forbidden ...string) {
	t.Helper()
	directory := filepath.Join(workspace, "state", "diagnostics", "traces")
	var trace []byte
	waitDocumentE2E(t, func() bool {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) == 0 {
			return false
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			trace, err = os.ReadFile(filepath.Join(directory, entry.Name()))
			if err == nil {
				return true
			}
		}
		return false
	})
	var decoded any
	if err := json.Unmarshal(trace, &decoded); err != nil {
		t.Fatalf("decode document trace: %v", err)
	}
	compact, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("normalize document trace: %v", err)
	}
	text := string(compact)
	if !strings.Contains(text, `"tool":"document"`) || !strings.Contains(text, digest) ||
		!strings.Contains(text, "selected_pages") {
		t.Fatalf("document lifecycle evidence is absent from trace: %s", text)
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(text, value) {
			t.Fatalf("document trace retained protected value %q: %s", value, text)
		}
	}
}
