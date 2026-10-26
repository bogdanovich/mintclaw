package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func TestOwnedRestartUsesTwoRealNativeGatewaysAndRemainingBudget(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("prepared runtime admission currently supports Linux and macOS")
	}
	binary := os.Getenv("MINTCLAW_EVAL_NATIVE_TEST_CLI")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "mintclaw")
		repository, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		buildSteeringTestCLI(t, binary, repository, nativeModelTestCommit)
	}
	fixture, parent := preparationFixture(t)
	fixture.preset.Capabilities = []string{"conversation", boundedCallsCapability}
	limits := targetBudgetTestLimits()
	fixture.preset.TargetCallBudget = &limits
	fixture.preset.CaptureModelMetadata, fixture.preset.JudgeModel = true, "eval-model"
	fixture.cfg.Agents.List[0].ToolPolicy.Allow = nil
	fixture.cfg.Tools.RequestUserInput.Enabled = false
	fixture.cfg.Agents.Defaults.ContextManager = "seahorse"
	fixture.cfg.Agents.Defaults.ContextWindow, fixture.cfg.Agents.Defaults.MaxTokens = 16384, 128
	fixture.cfg.Agents.Defaults.MaxLLMRetries = 1
	const marker = "mc-restart-104be82d"
	var calls atomic.Int32
	var restored atomic.Bool
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, request.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		number := calls.Add(1)
		content := "Noted."
		if number == 2 {
			restored.Store(bytes.Contains(raw, []byte(marker)))
			content = marker
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "synthetic-owned-restart", "model": "synthetic-model",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": content},
			}},
			"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 2, "total_tokens": 9},
		})
	}))
	defer model.Close()
	fixture.cfg.ModelList[0].APIBase = model.URL + "/v1"
	if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
	if err != nil {
		t.Fatal(err)
	}
	preset, err := LoadPreset(prepared.PresetPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvHome, prepared.Home)
	t.Setenv(config.EnvConfig, prepared.ConfigPath)
	profile, err := readPreparedProfile(preset, prepared.Home)
	if err != nil || validateFreshOwnedProfile(profile) != nil {
		t.Fatal("fresh owner", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	children := []*ownedGateway{}
	defer func() {
		for _, child := range children {
			cleanup, stop := context.WithTimeout(context.Background(), 35*time.Second)
			child.stopAndWait(cleanup)
			stop()
		}
	}()
	first, err := launchOwnedGatewayWithBudget(profile, binary, preset.TargetCallBudget)
	if err != nil {
		t.Fatal("first launch", err)
	}
	children = append(children, first)
	before, err := admitOwnedGateway(ctx, first, preset, prepared.Home, nil, nil)
	if err != nil {
		t.Fatal("first admission", err)
	}
	sessionID := uuid.NewString()
	firstFinal := ownedRestartNativeExchange(t, ctx, before, sessionID, "Remember the synthetic code "+marker+".")
	changed := preset
	changed.ID = "different-owner"
	if unexpected, _, _, err := restartOwnedGateway(ctx, first, changed); unexpected != nil ||
		!errors.Is(err, ErrOwnedGateway) {
		t.Fatal("changed preset admitted a successor", err)
	}
	originalConfig, err := os.ReadFile(prepared.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(prepared.ConfigPath, append(append([]byte{}, originalConfig...), '\n'), 0o600) != nil {
		t.Fatal("synthetic changed input")
	}
	if unexpected, _, _, err := restartOwnedGateway(ctx, first, preset); unexpected != nil ||
		!errors.Is(err, ErrOwnedGateway) {
		t.Fatal("changed private input admitted a successor", err)
	}
	if os.WriteFile(prepared.ConfigPath, originalConfig, 0o600) != nil {
		t.Fatal("restore own test input")
	}
	clear(originalConfig)
	select {
	case <-first.done:
		t.Fatal("pre-admission refusal stopped the owner")
	default:
	}
	next, after, generation, err := restartOwnedGateway(ctx, first, preset)
	if next != nil {
		children = append(children, next)
	}
	if err != nil {
		t.Fatalf("native successor: %v lifecycle=%+v budget=%+v surfaces=%+v", err,
			generation.Lifecycle, generation.Budget, generation.Surfaces)
	}
	if first.command.Process.Pid == next.command.Process.Pid || next.identity.generation != 2 ||
		first.budgetSpec.Nonce == next.budgetSpec.Nonce || next.budgetSpec.Limits.MaxCalls != limits.MaxCalls-1 ||
		next.budgetSpec.Limits.MaxReportedTokens != limits.MaxReportedTokens-9 ||
		!observedOwnedProfile(&generation.Lifecycle, generation.Surfaces) {
		t.Fatal("successor reset identity/budget or skipped native settlement")
	}
	if validateFreshOwnedProfile(profile) == nil {
		t.Fatal("used home became eligible for ordinary fresh launch")
	}
	if again, _, _, err := restartOwnedGateway(ctx, first, preset); again != nil || !errors.Is(err, ErrOwnedGateway) {
		t.Fatal("consumed predecessor launched another successor", err)
	}
	secondFinal := ownedRestartNativeExchange(
		t,
		ctx,
		after,
		sessionID,
		"Reply with the synthetic code I supplied earlier.",
	)
	cleanup, stop := context.WithTimeout(context.Background(), 35*time.Second)
	last := next.stopAndWait(cleanup)
	stop()
	budget := observeTargetBudget(next, nil, profile.config.SensitiveDataReplacer())
	if calls.Load() != 2 || !restored.Load() || !next.cleanOwnedExit() || last.Code != "native_exit_zero" ||
		budget == nil || budget.verdict() != Pass || firstFinal.SessionKey != secondFinal.SessionKey ||
		!strings.Contains(secondFinal.Content, marker) {
		t.Fatal("native restart lost history, duplicated work or failed cleanup", calls.Load(), last.Code)
	}
	history, err := readNativeSessionHistory(ctx, prepared.Home, prepared.Workspace, firstFinal.SessionKey,
		nil, profile.config.SensitiveDataReplacer())
	if err != nil {
		t.Fatal("persisted native history", err)
	}
	snapshot, err := memory.InspectJSONLSnapshot(ctx, firstFinal.SessionKey,
		[]byte(history.metadata), []byte(strings.Join(history.history, "\n")))
	if err != nil || len(snapshot.History) != 4 || snapshot.Metadata.Skip != 0 || snapshot.Metadata.Summary != "" {
		t.Fatal("restart did not preserve clean canonical messages", err)
	}
	rows, names, err := readSteeringOutbox(ctx, after)
	trial := Trial{
		SessionID: sessionID, SessionKey: firstFinal.SessionKey, AgentID: preset.ExpectedAgent,
		Events: []Event{firstFinal, secondFinal},
	}
	if err != nil || len(rows) != 2 || !validCoreFinalDeliveries(rows, names, trial, prepared.Workspace) {
		t.Fatal("restart replayed or lost native final delivery", err)
	}
}

func ownedRestartNativeExchange(
	t *testing.T, ctx context.Context, target admittedTarget, sessionID, task string,
) Event {
	t.Helper()
	client, err := gatewayclient.Connect(ctx, target.config, sessionID, gatewayclient.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request := protocol.MintClawMessage{
		Type: protocol.TypeMessageSend, ID: uuid.NewString(), SessionID: sessionID,
		Timestamp: time.Now().UnixMilli(), Payload: map[string]any{protocol.PayloadKeyContent: task},
	}
	if client.Send(ctx, request) != nil {
		t.Fatal("native input")
	}
	var final Event
	for frames := 0; frames < 256; frames++ {
		message, err := client.Receive(ctx)
		if err != nil {
			t.Fatal("native response", err)
		}
		if payloadString(message.Payload, protocol.PayloadKeyRequestID) != request.ID ||
			message.Type != protocol.TypeMessageCreate && message.Type != protocol.TypeMessageUpdate {
			continue
		}
		event, err := responseEvent(message, 1)
		if err != nil {
			t.Fatal(err)
		}
		if event.Kind == "final" {
			final = event
			break
		}
	}
	final.ID = uuid.NewString()
	if final.Kind != "final" || final.AgentID != target.preset.ExpectedAgent || final.SessionKey == "" {
		t.Fatal("missing native final identity")
	}
	settlement, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		rows, _, err := readSteeringOutbox(settlement, target)
		if err == nil {
			for _, row := range rows {
				intent, err := outbox.InspectIntent([]byte(row.JSON), strings.TrimSuffix(row.File, ".json"))
				if err == nil && intent.Status == outbox.StatusDelivered && intent.Message != nil &&
					intent.Message.Context.MessageID == request.ID && len(intent.PlatformMessageIDs) == 1 &&
					intent.PlatformMessageIDs[0] == final.MessageID {
					return final
				}
			}
		}
		select {
		case <-settlement.Done():
			t.Fatal("native delivery did not settle")
			return Event{}
		case <-ticker.C:
		}
	}
}
