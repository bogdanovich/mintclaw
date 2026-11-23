package eval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/internal/evaltarget"
	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	"github.com/bogdanovich/mintclaw/pkg/auth"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestOwnedBudgetProtocolUsesRealGatewayAndTerminalCounters(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "normal")
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20)).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["max_tokens"] != float64(128) && body["max_completion_tokens"] != float64(128) {
			t.Error("native provider omitted requested output ceiling")
		}
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"synthetic","object":"chat.completion","model":"synthetic-model",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"owned-budget-ready"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`))
	}))
	defer model.Close()
	fixture.cfg.ModelList[0].APIBase = model.URL
	fixture.cfg.Agents.Defaults.ContextManager = "none"
	fixture.cfg.Agents.Defaults.ContextWindow, fixture.cfg.Agents.Defaults.MaxTokens = 8192, 128
	fixture.cfg.Agents.Defaults.MaxToolIterations, fixture.cfg.Agents.Defaults.MaxLLMRetries = 2, 1
	fixture.writeConfig(t)
	profile, err := readPreparedProfile(fixture.preset, fixture.home)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateFreshOwnedProfile(profile); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "mintclaw")
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	buildCtx, buildCancel := context.WithTimeout(t.Context(), 3*time.Minute)
	build := exec.CommandContext(buildCtx, "go", "build", "-tags", "goolm,stdjson", "-o", binary, "./cmd/mintclaw")
	build.Dir, build.Env = root, append(os.Environ(), "CGO_ENABLED=0")
	buildOutput, buildErr := build.CombinedOutput()
	buildCancel()
	if buildErr != nil {
		t.Fatalf("build real gateway: %v\n%s", buildErr, buildOutput)
	}
	spec := evaltarget.Spec{
		Version: evaltarget.Version, Nonce: uuid.NewString(),
		Limits: providers.CallBudgetLimits{
			MaxCalls: 1, MaxInputBytes: 64 << 10, MaxOutputTokens: 128,
			MaxReportedTokens: 1000, MaxInFlight: 2,
		},
	}
	encoded, err := spec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Reuse the ownership launcher unchanged; exec keeps the exact owned PID.
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	launcher := filepath.Join(t.TempDir(), "launch")
	script := "#!/bin/sh\nexec " + quote(binary) + " gateway --" + evaltarget.FlagName + " " + quote(encoded) + "\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	child, err := launchOwnedGateway(profile, launcher)
	registerOwnedGatewayCleanup(t, child)
	if err != nil {
		t.Fatal(err)
	}
	startupCtx, startupCancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer startupCancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		target, admitErr := admitPreparedTarget(startupCtx, fixture.preset, fixture.home)
		if admitErr == nil {
			if target.probe.PID != child.command.Process.Pid {
				t.Fatal("real gateway admission did not match owned process")
			}
			child.admitted = true
			break
		}
		select {
		case <-child.done:
			t.Fatalf("real gateway exited before readiness: %v", child.err)
		case <-startupCtx.Done():
			t.Fatal("real gateway readiness unavailable", startupCtx.Err())
		case <-ticker.C:
		}
	}
	turnCtx, turnCancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer turnCancel()
	sessionID := uuid.NewString()
	client, err := gatewayclient.Connect(turnCtx, profile.config, sessionID, gatewayclient.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	requestID := uuid.NewString()
	if err := client.Send(turnCtx, protocol.MintClawMessage{
		Type: protocol.TypeMessageSend,
		ID:   requestID, SessionID: sessionID, Payload: map[string]any{
			protocol.PayloadKeyContent: "Reply with owned-budget-ready.",
		},
	}); err != nil {
		t.Fatal(err)
	}
	for {
		message, err := client.Receive(turnCtx)
		if err != nil {
			t.Fatal(err)
		}
		if message.Payload[protocol.PayloadKeyRequestID] != requestID ||
			message.Payload[protocol.PayloadKeyFinal] != true {
			continue
		}
		if content, _ := message.Payload[protocol.PayloadKeyContent].(string); !strings.Contains(
			content,
			"owned-budget-ready",
		) {
			t.Fatal("real native turn did not return fixture content")
		}
		break
	}
	client.Close()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 35*time.Second)
	receipt := child.stopAndWait(stopCtx)
	stopCancel()
	if receipt.Code != "native_exit_zero" || receipt.Forced || !receipt.Admitted {
		t.Fatalf("real owned shutdown = %+v", receipt)
	}
	name, _ := spec.ReceiptName()
	data, err := os.ReadFile(filepath.Join(profile.home, name))
	if err != nil {
		t.Fatal("real CLI did not retain terminal budget evidence", err)
	}
	proof, err := evaltarget.DecodeProof(data, spec, child.command.Process.Pid)
	if err != nil || !proof.GatewayClean || proof.Snapshot.Calls != 1 || proof.Snapshot.Completed != 1 ||
		proof.Snapshot.InFlight != 0 || !proof.Snapshot.UsageKnown || proof.Snapshot.ReportedTokens != 9 ||
		proof.Snapshot.StoppedReason != "" || requests.Load() != 1 {
		t.Fatalf("real CLI budget wiring = %+v/%v, provider requests %d", proof, err, requests.Load())
	}
	t.Run("subscription_startup", func(t *testing.T) { verifyOwnedSubscriptionStartup(t, binary) })
}

func verifyOwnedSubscriptionStartup(t *testing.T, binary string) {
	t.Helper()
	fixture := newOwnedGatewayFixture(t, "normal")
	fixture.preset = testSubscriptionPreset(fixture.preset)
	model := fixture.cfg.ModelList[0]
	model.Model, model.AuthMethod, model.APIBase, model.APIKeys = "gpt-5.6-terra", "oauth", "", nil
	fixture.writeConfig(t)
	const fakeAccess = "synthetic-owned-subscription-token-not-a-live-credential"
	name := filepath.Join(fixture.home, subscriptionAuthPath)
	writeAdmissionJSON(t, name, auth.AuthStore{Credentials: map[string]*auth.AuthCredential{
		"openai": {
			Provider:    "openai",
			AuthMethod:  "oauth",
			AccessToken: fakeAccess,
			ExpiresAt:   time.Now().Add(time.Hour),
		},
	}})
	// The parent can have an unrelated login and API key; neither belongs to the child.
	t.Setenv("MINTCLAW_AUTH_FILE", filepath.Join(fixture.home, "ambient.json"))
	t.Setenv("OPENAI_API_KEY", "synthetic-ambient-key")
	profile, err := readPreparedProfile(fixture.preset, fixture.home)
	if err != nil || validateConversationProfile(profile.config, fixture.preset, fixture.workspace) != nil ||
		validateFreshOwnedProfile(profile) != nil {
		t.Fatal("subscription profile not admitted", err)
	}
	child, err := launchOwnedGatewayWithBudget(profile, binary, fixture.preset.TargetCallBudget)
	registerOwnedGatewayCleanup(t, child)
	if err != nil {
		t.Fatal(err)
	}
	target, err := admitOwnedGateway(t.Context(), child, fixture.preset, fixture.home, nil, nil)
	if err != nil || target.evidenceFilter().Replace(fakeAccess) == fakeAccess {
		t.Fatal("native subscription gateway not admitted with guarded credentials", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if receipt := child.stopAndWait(
		stopCtx,
	); receipt.Code != "native_exit_zero" || !receipt.Admitted ||
		receipt.Forced {
		t.Fatalf("native subscription shutdown: %+v", receipt)
	}
	receipt, err := child.budgetSpec.ReceiptName()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(fixture.home, receipt))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := evaltarget.DecodeProof(data, *child.budgetSpec, child.command.Process.Pid)
	if err != nil || !proof.GatewayClean || proof.Snapshot.Calls != 0 {
		t.Fatalf("subscription startup performed model work: %+v/%v", proof, err)
	}
}
