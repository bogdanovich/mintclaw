//go:build linux && amd64 && integration

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
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

	t.Run("verified form fill is delivered exactly once without retaining values", func(t *testing.T) {
		requireDocumentFormBackend(t)
		workspace := documentE2EWorkspace(t)
		home := filepath.Join(workspace, "instance")
		t.Setenv(config.EnvHome, home)
		store, ref, digest, sourcePath := documentE2ESource(t, "acroform-fields.pdf")
		fieldID := documentE2EFieldID(t, sourcePath, "full_name")
		privateValue := "MintClaw Agent Private Value"
		provider := documentFormE2EProvider(ref, digest, sourcePath, fieldID, privateValue)
		fixture := newAgentLoopTestFixtureWithWorkspace(t, workspace, provider, func(cfg *config.Config) {
			configureDocumentE2E(cfg, provider.GetDefaultModel(), false)
		})
		fixture.Loop.SetMediaStore(store)

		channel := &fakeMediaChannel{fakeChannel: fakeChannel{id: "document-form-e2e"}}
		stop := startDocumentE2EChannel(t, fixture, store, channel)
		defer stop()
		publishDocumentE2EInbound(
			t,
			fixture.Bus,
			ref,
			"Fill full_name with the protected value supplied for this call and send me the verified PDF.",
		)

		waitDocumentE2EChannel(t, channel, func() bool {
			channel.mu.Lock()
			defer channel.mu.Unlock()
			return len(channel.sentMedia) == 1 && len(channel.sentMessages) == 1
		})
		channel.mu.Lock()
		if len(channel.sentMedia) != 1 || len(channel.sentMessages) != 1 ||
			len(channel.sentMedia[0].Parts) != 1 {
			channel.mu.Unlock()
			t.Fatalf(
				"Telegram form delivery count = media %d text %d",
				len(channel.sentMedia),
				len(channel.sentMessages),
			)
		}
		delivered := channel.sentMedia[0]
		final := channel.sentMessages[0]
		channel.mu.Unlock()
		if delivered.DeliveryID == "" || delivered.Parts[0].ContentType != "application/pdf" ||
			delivered.Parts[0].Filename != "filled-document.pdf" ||
			final.Content != "Filled form delivered." {
			t.Fatalf("delivered form = %#v final = %#v", delivered, final)
		}
		path, err := store.Resolve(delivered.Parts[0].Ref)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) < 5 || string(data[:5]) != "%PDF-" {
			t.Fatalf("delivered form is not a PDF: size=%d err=%v", len(data), err)
		}
		if err = provider.AssertExhausted(); err != nil {
			t.Fatal(err)
		}
		assertDocumentE2ETrace(t, workspace, digest, sourcePath, "same-name.pdf", privateValue)
		record := assertDocumentFormJournal(
			t,
			home,
			privateValue,
			delivered.Parts[0].Ref,
			document.WriteDelivered,
		)
		if record.OutboxDeliveryID != delivered.DeliveryID {
			t.Fatalf(
				"document outbox correlation = %q, want %q",
				record.OutboxDeliveryID,
				delivered.DeliveryID,
			)
		}
		for _, sessionKey := range fixture.Agent.Sessions.ListSessions() {
			history := fixture.Agent.Sessions.GetHistory(sessionKey)
			for _, message := range history {
				if strings.Contains(message.Content, privateValue) {
					t.Fatalf("durable history retained form value: %#v", message)
				}
			}
		}
	})

	for _, scenario := range []struct {
		name              string
		state             document.WriteOperationState
		result            func() channels.DeliveryResult[bus.OutboundMediaMessage]
		modelContinuation bool
	}{
		{
			name:  "definite form delivery rejection retries safely without duplicate identity",
			state: document.WriteDeliveryFailed,
			result: func() channels.DeliveryResult[bus.OutboundMediaMessage] {
				return channels.RejectedDelivery[bus.OutboundMediaMessage](errors.New("synthetic preflight rejection"))
			},
			modelContinuation: true,
		},
		{
			name:  "ambiguous form delivery stops the turn without replay",
			state: document.WriteDeliveryAmbiguous,
			result: func() channels.DeliveryResult[bus.OutboundMediaMessage] {
				return channels.FailedDelivery[bus.OutboundMediaMessage](
					nil,
					nil,
					0,
					errors.New("synthetic response loss"),
				)
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			requireDocumentFormBackend(t)
			workspace := documentE2EWorkspace(t)
			home := filepath.Join(workspace, "instance")
			t.Setenv(config.EnvHome, home)
			store, ref, digest, sourcePath := documentE2ESource(t, "acroform-fields.pdf")
			fieldID := documentE2EFieldID(t, sourcePath, "full_name")
			privateValue := "MintClaw Failed Delivery Private Value"
			provider := documentFormDeliveryE2EProvider(
				ref,
				digest,
				sourcePath,
				fieldID,
				privateValue,
				scenario.modelContinuation,
			)
			fixture := newAgentLoopTestFixtureWithWorkspace(t, workspace, provider, func(cfg *config.Config) {
				configureDocumentE2E(cfg, provider.GetDefaultModel(), false)
			})
			fixture.Loop.SetMediaStore(store)

			var attempts atomic.Int32
			var deliveryIdentity atomic.Value
			channel := &fakeMediaChannel{fakeChannel: fakeChannel{id: "document-form-delivery-e2e"}}
			channel.mediaDelivery = func(
				_ context.Context,
				pending []bus.OutboundMediaMessage,
			) channels.DeliveryResult[bus.OutboundMediaMessage] {
				if len(pending) != 1 || len(pending[0].Parts) != 1 {
					return channels.RejectedDelivery[bus.OutboundMediaMessage](
						fmt.Errorf("unexpected form payload count: %d", len(pending)),
					)
				}
				if current := deliveryIdentity.Load(); current == nil {
					deliveryIdentity.Store(pending[0].DeliveryID)
				} else if current.(string) != pending[0].DeliveryID {
					return channels.RejectedDelivery[bus.OutboundMediaMessage](
						fmt.Errorf("delivery identity changed from %s to %s", current, pending[0].DeliveryID),
					)
				}
				attempts.Add(1)
				return scenario.result()
			}
			stop := startDocumentE2EChannel(t, fixture, store, channel)
			defer stop()
			publishDocumentE2EInbound(
				t,
				fixture.Bus,
				ref,
				"Fill full_name with the protected value supplied for this call and send me the verified PDF.",
			)

			record := waitDocumentFormJournalState(t, home, scenario.state, channel)
			assertDocumentE2ETrace(t, workspace, digest, sourcePath, "same-name.pdf", privateValue)
			wantAttempts := int32(1)
			if scenario.state == document.WriteDeliveryFailed {
				wantAttempts = 4
			}
			if attempts.Load() != wantAttempts {
				t.Fatalf("form delivery attempts = %d, want %d", attempts.Load(), wantAttempts)
			}
			if deliveryIdentity.Load() == nil {
				t.Fatal("form delivery identity was never observed")
			}
			if record.OutboxDeliveryID != deliveryIdentity.Load().(string) {
				t.Fatalf(
					"document outbox correlation = %q, want %q",
					record.OutboxDeliveryID,
					deliveryIdentity.Load().(string),
				)
			}
			channel.mu.Lock()
			mediaCount := len(channel.sentMedia)
			textCount := len(channel.sentMessages)
			channel.mu.Unlock()
			if mediaCount != 0 || (scenario.modelContinuation && textCount != 1) ||
				(!scenario.modelContinuation && textCount != 0) {
				t.Fatalf("failed form deliveries = media %d text %d", mediaCount, textCount)
			}
			if record.ArtifactRef == "" || record.DeliveryID == "" {
				t.Fatalf("terminal delivery lost durable correlations: %#v", record)
			}
			if err := provider.AssertExhausted(); err != nil {
				t.Fatal(err)
			}
			assertDocumentFormJournal(t, home, privateValue, record.ArtifactRef, scenario.state)
		})
	}
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

func TestDocumentLocalPathToolLinuxIntegration(t *testing.T) {
	requireDocumentReadBackend(t)
	workspace := t.TempDir()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	source := filepath.Join(filepath.Dir(currentFile), "..", "document", "testdata", "text.pdf")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "server-local.pdf")
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	store := media.NewFileMediaStore()
	tool := tools.NewDocumentTool(tools.WithDocumentLocalPathPolicy(workspace, true, nil))
	tool.SetMediaStore(store)
	ctx := documentLocalPathToolContext(t, workspace, path)
	inspected := tool.Execute(ctx, map[string]any{"action": "inspect", "path": path})
	if inspected.IsError || strings.Contains(inspected.ForLLM, path) {
		t.Fatalf("inspect failed or leaked path: safe=%s internal=%v", inspected.ForLLM, inspected.Err)
	}
	var projection struct {
		Source struct {
			Ref string `json:"ref"`
		} `json:"source"`
	}
	if err = json.Unmarshal([]byte(inspected.ForLLM), &projection); err != nil ||
		!strings.HasPrefix(projection.Source.Ref, "media://") {
		t.Fatalf("inspect projection = %#v, %v", projection, err)
	}
	if err = os.WriteFile(path, []byte("%PDF-1.7\nREPLACEMENT_BYTES\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	extracted := tool.Execute(ctx, map[string]any{
		"action": "extract", "source": projection.Source.Ref, "pages": []any{float64(1)},
	})
	if extracted.IsError || !strings.Contains(extracted.ContextText, "MintClaw text fixture") ||
		strings.Contains(extracted.ContextText, "REPLACEMENT_BYTES") || strings.Contains(extracted.ForLLM, path) {
		t.Fatalf("extract did not use immutable admission: %#v", extracted)
	}
	if err = tool.CleanupTurn(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.ResolveWithMeta(projection.Source.Ref); err == nil {
		t.Fatal("local snapshot survived turn cleanup")
	}
}

func TestDocumentFormToolLinuxIntegration(t *testing.T) {
	requireDocumentFormBackend(t)
	workspace := t.TempDir()
	stateRoot := filepath.Join(workspace, "state", "document-writes")
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	sourcePath = filepath.Join(filepath.Dir(sourcePath), "..", "document", "testdata", "acroform-fields.pdf")
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(workspace, "local-form.pdf")
	if err = os.WriteFile(localPath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	store := media.NewFileMediaStore()
	owner, err := media.NewMediaOwner(
		workspace,
		"main",
		"pdf-operator",
		"document-form-route",
		"document-form-session",
		"telegram",
		"pdf-chat",
		"pdf-topic",
	)
	if err != nil {
		t.Fatal(err)
	}
	tool := tools.NewDocumentTool(
		tools.WithDocumentStateRoot(stateRoot),
		tools.WithDocumentLocalPathPolicy(workspace, true, nil),
	)
	tool.SetMediaStore(store)
	ctx := documentFormToolContext(t, workspace)
	ctx = toolshared.WithToolDocumentLocalPaths(ctx, []string{localPath})
	inspected := tool.Execute(ctx, map[string]any{"action": "inspect", "path": localPath})
	if inspected.IsError {
		t.Fatalf("local inspect failed: safe=%s internal=%v", inspected.ForLLM, inspected.Err)
	}
	var inspectedReport struct {
		Source struct {
			Ref string `json:"ref"`
		} `json:"source"`
	}
	if err = json.Unmarshal([]byte(inspected.ForLLM), &inspectedReport); err != nil ||
		inspectedReport.Source.Ref == "" {
		t.Fatalf("local inspect report = %#v err=%v", inspectedReport, err)
	}
	ref := inspectedReport.Source.Ref
	fields := tool.Execute(ctx, map[string]any{"action": "fields", "source": ref})
	if fields.IsError {
		t.Fatalf("fields failed: safe=%s internal=%v", fields.ForLLM, fields.Err)
	}
	fieldID := documentFieldIDFromSafeReport(t, fields.ForLLM, "full_name")
	privateValue := "MintClaw Tool Private Value"
	fillArgs := map[string]any{
		"action": "fill",
		"source": ref,
		"assignments": []any{map[string]any{
			"field_id": fieldID,
			"value":    map[string]any{"type": "text", "text": privateValue},
		}},
	}
	filled := tool.Execute(ctx, fillArgs)
	if filled.IsError || len(filled.Media) != 1 || filled.Deliverable == nil ||
		strings.Contains(filled.ForLLM, privateValue) || filled.Delivery.Commit == nil ||
		filled.Delivery.Settle == nil {
		t.Fatalf("fill result = %#v", filled)
	}
	operationID, deliveryID := documentWriteIDsFromSafeReport(t, filled.ForLLM)
	outboxDeliveryID := "out_" + strings.Repeat("a", 32)
	if err = filled.Delivery.Commit(toolshared.WithToolOutboundDeliveryID(ctx, outboxDeliveryID)); err != nil {
		t.Fatal(err)
	}
	if err = filled.Delivery.Settle(ctx, toolshared.DeliverySettlement{
		Status: toolshared.DeliverySettlementDelivered, DeliveryID: outboxDeliveryID,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filled.ForLLM, `"state":"delivered"`) {
		t.Fatalf("settled fill result did not expose confirmed delivery: %s", filled.ForLLM)
	}
	assertDocumentWriteState(t, stateRoot, operationID, owner, document.WriteDelivered)

	verifyCtx := documentFormToolContext(t, workspace, ref)
	verifyCtx = toolshared.WithToolExecutionIdentity(
		verifyCtx,
		workspace,
		"document-form-verify-execution",
	)
	verified := tool.Execute(verifyCtx, map[string]any{
		"action": "verify", "source": filled.Media[0], "operation_id": operationID,
	})
	if verified.IsError || !strings.Contains(verified.ForLLM, `"operation":"verify"`) ||
		!strings.Contains(verified.ForLLM, operationID) || strings.Contains(verified.ForLLM, sourcePath) ||
		strings.Contains(verified.ForLLM, privateValue) {
		t.Fatalf("verify result = %#v", verified)
	}
	wrongOperation := tool.Execute(verifyCtx, map[string]any{
		"action": "verify", "source": filled.Media[0], "operation_id": document.NewWriteOperationID(),
	})
	if !wrongOperation.IsError ||
		!strings.Contains(wrongOperation.ForLLM, string(document.FailureSourceUnauthorized)) {
		t.Fatalf("wrong-operation verify result = %#v", wrongOperation)
	}

	retryArgs := map[string]any{
		"action":       "fill",
		"source":       ref,
		"assignments":  fillArgs["assignments"],
		"operation_id": operationID,
	}
	retryCtx := toolshared.WithToolCallID(ctx, "document-form-fill-retry")
	replayed := tool.Execute(retryCtx, retryArgs)
	if replayed.IsError || len(replayed.Media) != 0 || replayed.Delivery.Commit != nil ||
		!strings.Contains(replayed.ForLLM, string(document.WriteDelivered)) {
		t.Fatalf("delivered operation was replayed: %#v", replayed)
	}
	if err = store.ReleaseAll("document-form-" + deliveryID); err != nil {
		t.Fatal(err)
	}
	if err = tool.CleanupTurn(ctx); err != nil {
		t.Fatal(err)
	}
}

func documentFormToolContext(t *testing.T, workspace string, refs ...string) context.Context {
	t.Helper()
	ctx := toolshared.WithToolInboundContext(t.Context(), "telegram", "pdf-chat", "pdf-message", "")
	ctx = toolshared.WithToolInboundMetadata(ctx, bus.InboundContext{
		Channel: "telegram", ChatID: "pdf-chat", TopicID: "pdf-topic",
		SenderID: "pdf-operator", ActorID: "pdf-operator",
	})
	ctx = toolshared.WithToolTopicID(ctx, "pdf-topic")
	ctx = toolshared.WithToolSessionContext(ctx, "main", "document-form-session", nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, "document-form-route")
	ctx = toolshared.WithToolExecutionIdentity(ctx, workspace, "document-form-execution")
	ctx = toolshared.WithToolCallID(ctx, "document-form-fill-call")
	return toolshared.WithToolDocumentContext(ctx, refs, false)
}

func documentFieldIDFromSafeReport(t *testing.T, encoded string, name string) string {
	t.Helper()
	var report struct {
		Fields struct {
			Fields []document.FormField `json:"fields"`
		} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(encoded), &report); err != nil {
		t.Fatal(err)
	}
	for _, field := range report.Fields.Fields {
		if field.Name == name {
			return field.ID
		}
	}
	t.Fatalf("field %q is absent from %s", name, encoded)
	return ""
}

func documentWriteIDsFromSafeReport(t *testing.T, encoded string) (string, string) {
	t.Helper()
	var report struct {
		OperationID string `json:"operation_id"`
		Delivery    struct {
			DeliveryID string `json:"delivery_id"`
		} `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(encoded), &report); err != nil {
		t.Fatal(err)
	}
	if report.OperationID == "" || report.Delivery.DeliveryID == "" {
		t.Fatalf("write identities are absent from %s", encoded)
	}
	return report.OperationID, report.Delivery.DeliveryID
}

func assertDocumentWriteState(
	t *testing.T,
	stateRoot string,
	operationID string,
	owner media.MediaOwner,
	want document.WriteOperationState,
) {
	t.Helper()
	journal, err := document.NewWriteJournal(filepath.Join(stateRoot, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := journal.Lookup(t.Context(), operationID, document.Authority{
		Kind: "inbound_media", WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID,
		ActorID: owner.ActorID, RouteID: owner.RouteID, SessionID: owner.SessionID,
	})
	if err != nil || !found || record.State != want {
		t.Fatalf("write state = %#v found=%v err=%v want=%s", record, found, err, want)
	}
}

func documentLocalPathToolContext(t *testing.T, workspace string, path string) context.Context {
	t.Helper()
	ctx := toolshared.WithToolInboundContext(t.Context(), "telegram", "pdf-chat", "pdf-message", "")
	ctx = toolshared.WithToolInboundMetadata(ctx, bus.InboundContext{
		Channel: "telegram", ChatID: "pdf-chat", TopicID: "pdf-topic",
		SenderID: "pdf-operator", ActorID: "pdf-operator",
	})
	ctx = toolshared.WithToolTopicID(ctx, "pdf-topic")
	ctx = toolshared.WithToolSessionContext(ctx, "main", "document-local-session", nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, "document-local-route")
	ctx = toolshared.WithToolExecutionIdentity(ctx, workspace, "document-local-execution")
	ctx = toolshared.WithToolDocumentContext(ctx, nil, true)
	return toolshared.WithToolDocumentLocalPaths(ctx, []string{path})
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

func requireDocumentFormBackend(t *testing.T) {
	t.Helper()
	capabilities := document.Capabilities()
	for _, operation := range []string{"fields", "fill", "verify"} {
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
	cfg.Tools.Approval.Mode = config.ToolApprovalModeAllowAll
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
				foundUnexpectedImage := false
				for index := len(call.Messages) - 1; index >= 0; index-- {
					message := call.Messages[index]
					if message.Role == "tool" && strings.Contains(message.Content, "page_render") {
						foundReport = true
					}
					if len(message.Media) > 0 {
						foundUnexpectedImage = true
					}
				}
				if !foundReport || foundUnexpectedImage {
					return fmt.Errorf(
						"render report/unexpected image context = (%v, %v)",
						foundReport,
						foundUnexpectedImage,
					)
				}
				return nil
			},
			Response: llmscenario.TextResponse("Rendered and delivered source page 1."),
		},
	)
}

func documentFormE2EProvider(
	ref string,
	digest string,
	sourcePath string,
	fieldID string,
	privateValue string,
) *llmscenario.ScriptedProvider {
	steps := documentFormE2ESteps(ref, digest, sourcePath, fieldID, privateValue)
	steps = append(steps, llmscenario.ProviderStep{
		Name: "confirm safe verified delivery",
		Assert: func(call llmscenario.ProviderCall) error {
			joined := documentE2ECallText(call)
			if strings.Contains(joined, privateValue) {
				return errors.New("private form value survived into the post-tool model context")
			}
			for _, required := range []string{
				`"operation":"fill"`,
				`"operation_id":"document_write_`,
				`"output_sha256"`,
				`"delivered":true`,
			} {
				if !strings.Contains(joined, required) {
					return fmt.Errorf("safe fill evidence %q is absent from %s", required, joined)
				}
			}
			for _, message := range call.Messages {
				if message.Role == "tool" && len(message.Media) > 0 {
					return errors.New("delivered form was reattached to the model context")
				}
			}
			return nil
		},
		Response: llmscenario.TextResponse("Filled form delivered."),
	})
	return llmscenario.NewScriptedProvider("document-form-e2e-model", steps...)
}

func documentFormDeliveryE2EProvider(
	ref string,
	digest string,
	sourcePath string,
	fieldID string,
	privateValue string,
	modelContinuation bool,
) *llmscenario.ScriptedProvider {
	steps := documentFormE2ESteps(ref, digest, sourcePath, fieldID, privateValue)
	if modelContinuation {
		steps = append(steps, llmscenario.ProviderStep{
			Name: "report definite delivery rejection",
			Assert: func(call llmscenario.ProviderCall) error {
				joined := documentE2ECallText(call)
				if strings.Contains(joined, privateValue) {
					return errors.New("private form value survived into the delivery failure context")
				}
				if !strings.Contains(joined, "definitely failed before remote acceptance") {
					return fmt.Errorf("definite delivery failure was not propagated safely: %s", joined)
				}
				return nil
			},
			Response: llmscenario.TextResponse("The verified PDF was not delivered."),
		})
	}
	return llmscenario.NewScriptedProvider("document-form-delivery-e2e-model", steps...)
}

func documentFormE2ESteps(
	ref string,
	digest string,
	sourcePath string,
	fieldID string,
	privateValue string,
) []llmscenario.ProviderStep {
	return []llmscenario.ProviderStep{
		{
			Name:   "discover document form tool",
			Assert: documentFirstCallAssertion(ref, sourcePath),
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"search-document-form", tools.BM25SearchToolName, map[string]any{
					"query": "inspect fields fill and verify the exact current PDF form attachment",
				},
			)),
		},
		{
			Name:   "inspect form",
			Assert: llmscenario.RequireToolDefinition("document"),
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"inspect-document-form", "document", map[string]any{"action": "inspect", "source": ref},
			)),
		},
		{
			Name: "discover form fields",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := llmscenario.RequireLastMessage("tool", digest)(call); err != nil {
					return err
				}
				return llmscenario.RequireLastMessage("tool", `"page_count":2`)(call)
			},
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"fields-document-form", "document", map[string]any{"action": "fields", "source": ref},
			)),
		},
		{
			Name: "fill verified form",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := llmscenario.RequireLastMessage("tool", fieldID)(call); err != nil {
					return err
				}
				return llmscenario.RequireLastMessage("tool", `"name":"full_name"`)(call)
			},
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"fill-document-form", "document", map[string]any{
					"action": "fill",
					"source": ref,
					"assignments": []any{map[string]any{
						"field_id": fieldID,
						"value":    map[string]any{"type": "text", "text": privateValue},
					}},
				},
			)),
		},
	}
}

func documentE2EFieldID(t *testing.T, sourcePath string, name string) string {
	t.Helper()
	snapshot, report := document.Fields(
		t.Context(),
		sourcePath,
		document.AcquireOptions{ScratchRoot: filepath.Join(t.TempDir(), "scratch")},
	)
	if snapshot != nil {
		defer func() { _ = snapshot.Close() }()
	}
	if report.State != document.StateSucceeded || report.Fields == nil {
		t.Fatalf("field discovery report = %#v", report)
	}
	for _, field := range report.Fields.Fields {
		if field.Name == name {
			return field.ID
		}
	}
	t.Fatalf("field %q is absent", name)
	return ""
}

func assertDocumentFormJournal(
	t *testing.T,
	home string,
	forbidden string,
	artifactRef string,
	want document.WriteOperationState,
) document.WriteOperationRecord {
	t.Helper()
	directory := filepath.Join(home, "state", "document-writes", "journal")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var records int
	var matched document.WriteOperationRecord
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("document journal retained a protected value: %s", data)
		}
		var record document.WriteOperationRecord
		if err = json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record.State != want || record.ArtifactRef != artifactRef || record.DeliveryID == "" {
			t.Fatalf("document journal record = %#v", record)
		}
		matched = record
		records++
	}
	if records != 1 {
		t.Fatalf("document journal record count = %d, want 1", records)
	}
	return matched
}

func waitDocumentFormJournalState(
	t *testing.T,
	home string,
	want document.WriteOperationState,
	channel *fakeMediaChannel,
) document.WriteOperationRecord {
	t.Helper()
	directory := filepath.Join(home, "state", "document-writes", "journal")
	var matched document.WriteOperationRecord
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(directory)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				data, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
				if readErr != nil || json.Unmarshal(data, &matched) != nil {
					continue
				}
				if matched.State == want {
					return matched
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	channel.mu.Lock()
	messages := append([]bus.OutboundMessage(nil), channel.sentMessages...)
	media := append([]bus.OutboundMediaMessage(nil), channel.sentMedia...)
	channel.mu.Unlock()
	t.Fatalf(
		"timed out waiting for document journal state %s: last_record=%#v messages=%#v media=%#v",
		want,
		matched,
		messages,
		media,
	)
	return document.WriteOperationRecord{}
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
		if !strings.Contains(joined, "# PDF") || !strings.Contains(joined, ref) {
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
	t.Fatal("timed out waiting for document Telegram vertical slice")
}

func waitDocumentE2EChannel(t *testing.T, channel *fakeMediaChannel, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	channel.mu.Lock()
	messages := append([]bus.OutboundMessage(nil), channel.sentMessages...)
	media := append([]bus.OutboundMediaMessage(nil), channel.sentMedia...)
	channel.mu.Unlock()
	t.Fatalf("timed out waiting for document Telegram vertical slice: messages=%#v media=%#v", messages, media)
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
		(!strings.Contains(text, "selected_pages") && !strings.Contains(text, "affected_pages") &&
			!strings.Contains(text, "output_sha256")) {
		t.Fatalf("document lifecycle evidence is absent from trace: %s", text)
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(text, value) {
			t.Fatalf("document trace retained protected value %q: %s", value, text)
		}
	}
}
