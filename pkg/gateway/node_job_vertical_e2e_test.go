//go:build (linux || darwin) && integration

package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
	nodews "github.com/bogdanovich/mintclaw/pkg/nodes/ws"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

const (
	nodeJobTarget          = "jobs"
	nodeJobAlias           = "jobs-node"
	nodeJobProfile         = "builds"
	nodeJobSession         = "node-job-e2e-session"
	nodeJobCancelSession   = "node-job-cancel-e2e-session"
	nodeJobChatID          = "chat-job-e2e"
	nodeJobCancelChatID    = "chat-job-cancel-e2e"
	nodeJobFinishMode      = "finish-e2e-private-mode"
	nodeJobCancelMode      = "cancel-e2e-private-mode"
	nodeJobArtifactPath    = "private-result-artifact.txt"
	nodeJobArtifactContent = "artifact-from-durable-job\n"
	nodeJobStdoutFirst     = "job-stdout-first\n"
	nodeJobStdoutSecret    = "job-stdout-private-content"
	nodeJobStderrSecret    = "job-stderr-private-content"
	nodeJobEnvSecret       = "job-environment-private-content"
)

func TestNodeJobVerticalSliceWithRestartArtifactAndCancellation(t *testing.T) {
	workspace := t.TempDir()
	projectRoot := t.TempDir()
	projectRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	companionRoot := t.TempDir()
	stateDir := filepath.Join(companionRoot, "state")
	scriptPath := filepath.Join(companionRoot, "job-fixture.sh")
	writeNodeJobFixture(t, scriptPath)

	cfg := nodeJobVerticalSliceConfig(workspace)
	if err := cfg.ValidateExecutionTargets(); err != nil {
		t.Fatal(err)
	}
	provider := newNodeJobEvidenceProvider()
	mediaStore := media.NewFileMediaStore()

	registry, firstAdmission, firstRuntime := newNodeJobVerticalSliceRuntime(t, workspace)
	admissionSwitch := &nodeJobAdmissionSwitch{}
	admissionSwitch.set(firstAdmission)
	server := httptest.NewTLSServer(admissionSwitch)
	defer server.Close()

	binaryPath := buildVerticalSliceCompanion(t, companionRoot)
	companionConfig := nodeJobVerticalSliceCompanionConfig(
		t,
		server,
		stateDir,
		projectRoot,
		scriptPath,
	)
	configPath := filepath.Join(companionRoot, "config.json")
	writeVerticalSliceConfig(t, configPath, companionConfig)
	process := startVerticalSliceCompanion(t, binaryPath, configPath)
	defer process.stop(t)

	pending := waitForVerticalSliceNodeState(t, registry, nodes.StatePendingPairing)
	if _, err := registry.Approve(pending.ID, nodes.PairingApproval{
		Aliases: []nodes.Alias{nodeJobAlias},
		AllowedCommands: []string{
			nodes.JobCommandStart,
			nodes.JobCommandStatus,
			nodes.JobCommandLogs,
			nodes.JobCommandArtifacts,
			nodes.JobCommandCancel,
		},
		At: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	connected := waitForVerticalSliceNodeState(t, registry, nodes.StateConnected)

	firstAgent := newNodeJobAgentHarness(t, cfg, provider, firstRuntime, mediaStore, "first")
	defer firstAgent.close(t)
	response, err := firstAgent.loop.ProcessDirectWithChannel(
		t.Context(),
		"Start the configured durable job and return its stable identity.",
		nodeJobSession,
		"telegram",
		nodeJobChatID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if response != "" {
		t.Fatalf("suspended job start response = %q", response)
	}
	startPrompt := firstAgent.channel.nextMessage(t)
	assertNodeJobApprovalPrompt(t, startPrompt, "Start one configured durable job", scriptPath, projectRoot)
	waitForVerticalSliceEvent(t, firstAgent.waitingEvents, runtimeevents.KindAgentInteractionWaiting)
	firstAgent.start(t)
	firstAgent.answerApproval(t, nodeJobSession, nodeJobChatID, "message-job-start-approval")
	if final := firstAgent.channel.nextMessage(t); final.Content != "Durable job accepted." {
		t.Fatalf("job start response = %#v", final)
	}
	waitForVerticalSliceEvent(t, firstAgent.interactionEndEvents, runtimeevents.KindAgentInteractionEnd)
	jobID, startInvocationID := provider.completedIdentity()
	waitForNodeJobFile(t, filepath.Join(projectRoot, "finish.started"))
	firstEvents := firstAgent.drainNodeEvents()
	firstAgent.close(t)

	// A gateway restart first removes every live WSS session. The companion
	// remains alive and owns the accepted process while no gateway handler is
	// available.
	admissionSwitch.set(nil)
	closeNodeJobVerticalSliceRuntime(t, firstRuntime)
	waitForVerticalSliceNodeState(t, registry, nodes.StateDisconnected)
	if err := os.WriteFile(filepath.Join(projectRoot, "release"), []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	completedRecord := waitForNodeJobStoreState(t, stateDir, jobID, string(companion.JobSucceeded))
	assertAvailableNodeJobArtifact(t, completedRecord)
	assertNodeJobLaunches(t, projectRoot, []string{nodeJobFinishMode})

	restartedRegistry, restartedAdmission, restartedRuntime := newNodeJobVerticalSliceRuntime(t, workspace)
	defer closeNodeJobVerticalSliceRuntime(t, restartedRuntime)
	admissionSwitch.set(restartedAdmission)
	reconnected := waitForVerticalSliceNodeState(t, restartedRegistry, nodes.StateConnected)
	if reconnected.ID != connected.ID {
		t.Fatalf("reconnected node ID = %q, want %q", reconnected.ID, connected.ID)
	}

	secondAgent := newNodeJobAgentHarness(t, cfg, provider, restartedRuntime, mediaStore, "second")
	defer secondAgent.close(t)
	recovered, err := secondAgent.loop.ProcessDirectWithChannel(
		t.Context(),
		"Recover the accepted job after restart, inspect its logs, and retain its artifact.",
		nodeJobSession,
		"telegram",
		nodeJobChatID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != "Recovered durable job and artifact." {
		t.Fatalf("job recovery response = %q", recovered)
	}
	if provider.startInvocationIDValue() != startInvocationID {
		t.Fatal("gateway recovery changed the original invocation identity")
	}
	assertNodeJobLaunches(t, projectRoot, []string{nodeJobFinishMode})

	transferRef, artifactRef, artifactDigest := provider.artifactEvidence()
	if artifactDigest != nodeJobArtifactDigest() {
		t.Fatalf("artifact digest = %q, want %q", artifactDigest, nodeJobArtifactDigest())
	}
	downloaded := resolveNodeJobTransferArtifact(t, workspace, restartedRuntime, transferRef)
	if !bytes.Equal(downloaded, []byte(nodeJobArtifactContent)) {
		t.Fatalf("downloaded artifact = %q", downloaded)
	}

	cancelStart, err := secondAgent.loop.ProcessDirectWithChannel(
		t.Context(),
		"Start the configured cancellation canary.",
		nodeJobCancelSession,
		"telegram",
		nodeJobCancelChatID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if cancelStart != "" {
		t.Fatalf("suspended cancellation canary response = %q", cancelStart)
	}
	cancelStartPrompt := secondAgent.channel.nextMessage(t)
	assertNodeJobApprovalPrompt(
		t,
		cancelStartPrompt,
		"Start one configured durable job",
		scriptPath,
		projectRoot,
	)
	waitForVerticalSliceEvent(t, secondAgent.waitingEvents, runtimeevents.KindAgentInteractionWaiting)
	secondAgent.start(t)
	secondAgent.answerApproval(
		t,
		nodeJobCancelSession,
		nodeJobCancelChatID,
		"message-cancel-job-start-approval",
	)
	if final := secondAgent.channel.nextMessage(t); final.Content != "Cancellation job accepted." {
		t.Fatalf("cancellation job start response = %#v", final)
	}
	waitForVerticalSliceEvent(t, secondAgent.interactionEndEvents, runtimeevents.KindAgentInteractionEnd)
	waitForNodeJobFile(t, filepath.Join(projectRoot, "cancel.started"))

	cancelResponse, err := secondAgent.loop.ProcessDirectWithChannel(
		t.Context(),
		"Cancel the accepted cancellation canary without replaying it.",
		nodeJobCancelSession,
		"telegram",
		nodeJobCancelChatID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if cancelResponse != "" {
		t.Fatalf("suspended job cancel response = %q", cancelResponse)
	}
	cancelPrompt := secondAgent.channel.nextMessage(t)
	assertNodeJobApprovalPrompt(t, cancelPrompt, "Cancel one configured durable job", scriptPath, projectRoot)
	waitForVerticalSliceEvent(t, secondAgent.waitingEvents, runtimeevents.KindAgentInteractionWaiting)
	secondAgent.answerApproval(t, nodeJobCancelSession, nodeJobCancelChatID, "message-job-cancel-approval")
	if final := secondAgent.channel.nextMessage(t); final.Content != "Cancellation requested." {
		t.Fatalf("job cancel response = %#v", final)
	}
	waitForVerticalSliceEvent(t, secondAgent.interactionEndEvents, runtimeevents.KindAgentInteractionEnd)
	cancelJobID := provider.cancelJobIdentity()
	waitForNodeJobStoreState(t, stateDir, cancelJobID, string(companion.JobCanceled))

	cancelStatus, err := secondAgent.loop.ProcessDirectWithChannel(
		t.Context(),
		"Report the proven terminal cancellation state.",
		nodeJobCancelSession,
		"telegram",
		nodeJobCancelChatID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if cancelStatus != "Cancellation proven." {
		t.Fatalf("job cancellation status response = %q", cancelStatus)
	}
	assertNodeJobLaunches(t, projectRoot, []string{nodeJobFinishMode, nodeJobCancelMode})

	secondEvents := secondAgent.drainNodeEvents()
	secondAgent.close(t)
	if err := provider.AssertExhausted(); err != nil {
		t.Fatal(err)
	}
	assertNodeJobEvents(t, append(firstEvents, secondEvents...))

	tracePaths := waitForNodeFileTraces(t, workspace)
	traces := make([]json.RawMessage, 0, len(tracePaths))
	for _, tracePath := range tracePaths {
		trace, err := os.ReadFile(tracePath)
		if err != nil {
			t.Fatal(err)
		}
		traces = append(traces, json.RawMessage(trace))
	}
	assertNodeJobEvidenceRedacted(
		t,
		traces,
		scriptPath,
		projectRoot,
		nodeJobFinishMode,
		nodeJobCancelMode,
		nodeJobArtifactPath,
		nodeJobArtifactContent,
		nodeJobStdoutSecret,
		nodeJobStderrSecret,
		nodeJobEnvSecret,
		artifactRef,
		artifactDigest,
		transferRef,
	)
}

func nodeJobVerticalSliceConfig(workspace string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ModelName = "node-job-e2e-model"
	cfg.Agents.Defaults.ResponseFooter.Enabled = false
	cfg.Agents.Defaults.SummarizeMessageThreshold = 1000
	cfg.Diagnostics.TraceCapture.Enabled = true
	cfg.Diagnostics.TraceCapture.ContentMode = "redacted_content"
	cfg.Nodes.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		nodeJobTarget: {
			Type: "node", Node: nodeJobAlias,
			Executor: companion.LocalExecutor, JobProfile: nodeJobProfile,
		},
	}
	cfg.Agents.Defaults.TargetPolicy = &config.TargetPolicy{
		DefaultTarget: nodeJobTarget, AllowedTargets: []string{nodeJobTarget},
	}
	return cfg
}

func nodeJobVerticalSliceCompanionConfig(
	t *testing.T,
	server *httptest.Server,
	stateDir string,
	projectRoot string,
	scriptPath string,
) companion.Config {
	t.Helper()
	fingerprint := sha256.Sum256(server.Certificate().Raw)
	return companion.Config{
		GatewayURL: strings.Replace(server.URL, "https://", "wss://", 1) + companion.GatewayPath,
		StateDir:   stateDir,
		TLS: companion.TLSConfig{
			CertificateSHA256: hex.EncodeToString(fingerprint[:]),
		},
		Reconnect: companion.ReconnectConfig{
			MinDelaySeconds: 1, MaxDelaySeconds: 1, PendingDelaySeconds: 1,
		},
		Policy: nodes.LocalCommandPolicy{
			Revision: "job-vertical-e2e-policy",
			AllowedCommands: []string{
				nodes.JobCommandStart,
				nodes.JobCommandStatus,
				nodes.JobCommandLogs,
				nodes.JobCommandArtifacts,
				nodes.JobCommandCancel,
			},
			MaximumRisk:       nodes.RiskWrite,
			MaxTimeoutSeconds: 10,
			MaxOutputBytes:    64 * 1024,
		},
		SystemExec: &companion.SystemExecPolicy{
			WorkingRoots: []string{projectRoot},
			Executables:  []string{scriptPath},
			Environment:  []string{"JOB_SECRET"},
			Discovery: &companion.SystemExecDiscovery{
				ExecutableAliases:   map[string]string{"job-fixture": scriptPath},
				WorkingScopeAliases: map[string]string{"workspace": projectRoot},
				EnvironmentNames:    []string{"JOB_SECRET"},
			},
		},
		JobProfiles: companion.JobProfiles{
			nodeJobProfile: {
				Enabled: true, Revision: "job-vertical-e2e-profile-v1", Executor: "system_exec",
				TimeoutSecondsMax: 30, ConcurrentJobs: 2,
				StdoutBytesMax: 64 * 1024, StderrBytesMax: 64 * 1024,
				ArtifactCountMax: 2, ArtifactBytesMax: 64 * 1024,
				ArtifactsTotalBytesMax: 128 * 1024, RetentionSeconds: 300,
				CancelGuarantee: companion.JobCancelProcessGroup,
				Approval: companion.JobProfileApproval{
					Start: "required", Read: "none", Cancel: "required",
				},
			},
		},
	}
}

func writeNodeJobFixture(t *testing.T, path string) {
	t.Helper()
	script := `#!/bin/sh
set -eu
printf '%s\n' "$1" >> launches.log
case "$1" in
  finish-e2e-private-mode)
    : > finish.started
    printf 'job-stdout-first\n'
    printf 'job-stdout-private-content\n'
    printf 'job-stderr-private-content\n' >&2
    printf 'artifact-from-durable-job\n' > private-result-artifact.txt
    while [ ! -f release ]; do /bin/sleep 0.05; done
    ;;
  cancel-e2e-private-mode)
    : > cancel.started
    while :; do /bin/sleep 1; done
    ;;
  *)
    exit 64
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

type nodeJobAdmissionSwitch struct {
	mu      sync.RWMutex
	handler http.Handler
}

type nodeJobNoopRoutes struct{}

func (*nodeJobNoopRoutes) RegisterHTTPHandler(string, http.Handler) error { return nil }

func (*nodeJobNoopRoutes) ReplaceHTTPHandler(string, http.Handler) error { return nil }

func (*nodeJobNoopRoutes) UnregisterHTTPHandler(string) {}

func newNodeJobVerticalSliceRuntime(
	t *testing.T,
	workspace string,
) (*nodes.FileRegistry, *nodews.AdmissionHandler, *nodeAdmissionRuntime) {
	t.Helper()
	registry, admission, runtimeState := newVerticalSliceNodeRuntime(t, workspace)
	runtimeState.routes = &nodeJobNoopRoutes{}
	return registry, admission, runtimeState
}

func closeNodeJobVerticalSliceRuntime(t *testing.T, runtimeState *nodeAdmissionRuntime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtimeState.Close(ctx); err != nil {
		t.Errorf("close node runtime: %v", err)
	}
}

func (switcher *nodeJobAdmissionSwitch) set(handler http.Handler) {
	switcher.mu.Lock()
	switcher.handler = handler
	switcher.mu.Unlock()
}

func (switcher *nodeJobAdmissionSwitch) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switcher.mu.RLock()
	handler := switcher.handler
	switcher.mu.RUnlock()
	if handler == nil {
		http.Error(writer, "gateway restarting", http.StatusServiceUnavailable)
		return
	}
	handler.ServeHTTP(writer, request)
}

type nodeJobAgentHarness struct {
	loop                 *agent.AgentLoop
	messageBus           *bus.MessageBus
	channel              *nodeVerticalSliceChannel
	manager              *channels.Manager
	nodeSubscription     runtimeevents.Subscription
	nodeEvents           <-chan runtimeevents.Event
	waitingSubscription  runtimeevents.Subscription
	waitingEvents        <-chan runtimeevents.Event
	endSubscription      runtimeevents.Subscription
	interactionEndEvents <-chan runtimeevents.Event
	runCancel            context.CancelFunc
	runDone              chan error
	running              bool
	closed               bool
}

func newNodeJobAgentHarness(
	t *testing.T,
	cfg *config.Config,
	provider providers.LLMProvider,
	runtimeState *nodeAdmissionRuntime,
	mediaStore media.MediaStore,
	label string,
) *nodeJobAgentHarness {
	t.Helper()
	messageBus := bus.NewMessageBus()
	eventBus := runtimeevents.NewBus()
	loop := agent.NewAgentLoop(
		cfg,
		messageBus,
		provider,
		agent.WithIsolatedToolBootstrap(),
		agent.WithRuntimeEvents(eventBus),
	)
	if err := setupNodeTools(cfg, loop, runtimeState); err != nil {
		t.Fatal(err)
	}
	loop.SetMediaStore(mediaStore)
	if err := loop.MountHook(agent.NamedHook("node-job-e2e-approval-"+label, nodeJobApprovalHook{})); err != nil {
		t.Fatal(err)
	}
	channel := newNodeVerticalSliceChannel()
	manager, err := channels.NewManager(cfg, messageBus, mediaStore)
	if err != nil {
		t.Fatal(err)
	}
	manager.RegisterChannel("telegram", channel)
	if err := manager.StartAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	loop.SetChannelManager(manager)

	nodeSubscription, nodeEvents, err := eventBus.Channel().
		KindPrefix("node.invocation.").
		SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{
			Name: "node-job-e2e-events-" + label, Buffer: 128,
		})
	if err != nil {
		t.Fatal(err)
	}
	waitingSubscription, waitingEvents, err := eventBus.Channel().
		OfKind(runtimeevents.KindAgentInteractionWaiting).
		SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{
			Name: "node-job-e2e-waiting-" + label, Buffer: 4,
		})
	if err != nil {
		t.Fatal(err)
	}
	endSubscription, interactionEndEvents, err := eventBus.Channel().
		OfKind(runtimeevents.KindAgentInteractionEnd).
		SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{
			Name: "node-job-e2e-end-" + label, Buffer: 4,
		})
	if err != nil {
		t.Fatal(err)
	}
	return &nodeJobAgentHarness{
		loop: loop, messageBus: messageBus, channel: channel, manager: manager,
		nodeSubscription: nodeSubscription, nodeEvents: nodeEvents,
		waitingSubscription: waitingSubscription, waitingEvents: waitingEvents,
		endSubscription: endSubscription, interactionEndEvents: interactionEndEvents,
	}
}

func (harness *nodeJobAgentHarness) start(t *testing.T) {
	t.Helper()
	if harness.running {
		return
	}
	runCtx, cancel := context.WithCancel(t.Context())
	harness.runCancel = cancel
	harness.runDone = make(chan error, 1)
	harness.running = true
	go func() {
		harness.runDone <- harness.loop.Run(runCtx)
	}()
}

func (harness *nodeJobAgentHarness) answerApproval(
	t *testing.T,
	sessionKey string,
	chatID string,
	messageID string,
) {
	t.Helper()
	if err := harness.messageBus.PublishInbound(t.Context(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: chatID, ChatType: "direct",
			SenderID: "cron", ActorID: "cron", MessageID: messageID,
		},
		Content: "allow_once", SessionKey: sessionKey,
	}); err != nil {
		t.Fatal(err)
	}
}

func (harness *nodeJobAgentHarness) drainNodeEvents() []runtimeevents.Event {
	var events []runtimeevents.Event
	for {
		select {
		case event := <-harness.nodeEvents:
			events = append(events, event)
		default:
			return events
		}
	}
}

func (harness *nodeJobAgentHarness) close(t *testing.T) {
	t.Helper()
	if harness.closed {
		return
	}
	harness.closed = true
	if harness.running {
		harness.runCancel()
		select {
		case err := <-harness.runDone:
			if err != nil {
				t.Errorf("agent loop: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("agent loop did not stop")
		}
	}
	if err := harness.manager.StopAll(context.Background()); err != nil {
		t.Errorf("stop channel manager: %v", err)
	}
	harness.loop.Close()
	if err := harness.nodeSubscription.Close(); err != nil {
		t.Errorf("close node event subscription: %v", err)
	}
	if err := harness.waitingSubscription.Close(); err != nil {
		t.Errorf("close waiting event subscription: %v", err)
	}
	if err := harness.endSubscription.Close(); err != nil {
		t.Errorf("close interaction event subscription: %v", err)
	}
}

type nodeJobApprovalHook struct{}

func (nodeJobApprovalHook) ApproveTool(
	_ context.Context,
	request *agent.ToolApprovalRequest,
) (agent.ApprovalDecision, error) {
	if request.Tool != "nodes_invoke" {
		return agent.ApprovalDecision{Approved: true}, nil
	}
	command, _ := request.Arguments["command"].(string)
	switch command {
	case nodes.JobCommandStart:
		return agent.ApprovalDecision{
			RequireHuman: true, ActionSummary: "Start one configured durable job",
		}, nil
	case nodes.JobCommandCancel:
		return agent.ApprovalDecision{
			RequireHuman: true, ActionSummary: "Cancel one configured durable job",
		}, nil
	default:
		return agent.ApprovalDecision{Approved: true}, nil
	}
}

type nodeJobEvidenceProvider struct {
	mu sync.Mutex

	step                  int
	startRevision         string
	statusRevision        string
	logsRevision          string
	artifactsRevision     string
	cancelRevision        string
	jobID                 string
	startInvocationID     string
	cancelJobID           string
	artifactRef           string
	artifactDigest        string
	transferRef           string
	stdout                string
	stdoutCursor          float64
	cancelDispositionSeen string
}

func newNodeJobEvidenceProvider() *nodeJobEvidenceProvider { return &nodeJobEvidenceProvider{} }

func (*nodeJobEvidenceProvider) GetDefaultModel() string { return "node-job-e2e-model" }

func (provider *nodeJobEvidenceProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	step := provider.step
	provider.step++
	call := llmscenario.ProviderCall{Messages: messages}

	switch step {
	case 0:
		return provider.describe("start", nodes.JobCommandStart), nil
	case 1:
		payload, err := nodeP0LastToolPayload(call)
		if err != nil {
			return nil, err
		}
		provider.startRevision, err = nodeJobContractRevision(payload, nodes.JobCommandStart)
		if err != nil {
			return nil, err
		}
		return provider.startJob("call-job-start", provider.startRevision, nodeJobFinishMode, true), nil
	case 2:
		payload, result, err := nodeJobInvocationPayload(call, nodes.JobCommandStart)
		if err != nil {
			return nil, err
		}
		provider.startInvocationID, _ = payload["invocation_id"].(string)
		provider.jobID, _ = result["job_id"].(string)
		if provider.startInvocationID == "" || provider.jobID == "" || result["state"] != "running" {
			return nil, fmt.Errorf("job start result is incomplete: %#v", payload)
		}
		return llmscenario.TextResponse("Durable job accepted."), nil
	case 3:
		return llmscenario.ToolCallResponse(
			"I will recover the exact start invocation without replaying it.",
			llmscenario.ToolCall("call-job-start-recovery", "nodes_status", map[string]any{
				"invocation_id": provider.startInvocationID,
			}),
		), nil
	case 4:
		payload, err := nodeP0LastToolPayload(call)
		if err != nil {
			return nil, err
		}
		result, ok := payload["result"].(map[string]any)
		if payload["invocation_id"] != provider.startInvocationID ||
			payload["command"] != nodes.JobCommandStart || payload["state"] != "succeeded" ||
			!ok || result["job_id"] != provider.jobID {
			return nil, fmt.Errorf("recovered start invocation is incomplete: %#v", payload)
		}
		return provider.describe("status", nodes.JobCommandStatus), nil
	case 5:
		payload, err := nodeP0LastToolPayload(call)
		if err != nil {
			return nil, err
		}
		provider.statusRevision, err = nodeJobContractRevision(payload, nodes.JobCommandStatus)
		if err != nil {
			return nil, err
		}
		return provider.invokeJob(
			"call-job-status",
			nodes.JobCommandStatus,
			provider.statusRevision,
			map[string]any{"job_id": provider.jobID},
		), nil
	case 6:
		_, result, err := nodeJobInvocationPayload(call, nodes.JobCommandStatus)
		if err != nil {
			return nil, err
		}
		if result["job_id"] != provider.jobID || result["state"] != "succeeded" ||
			result["artifact_count"] != float64(1) {
			return nil, fmt.Errorf("terminal job status is incomplete: %#v", result)
		}
		return provider.describe("logs", nodes.JobCommandLogs), nil
	case 7:
		payload, err := nodeP0LastToolPayload(call)
		if err != nil {
			return nil, err
		}
		provider.logsRevision, err = nodeJobContractRevision(payload, nodes.JobCommandLogs)
		if err != nil {
			return nil, err
		}
		return provider.readLog("call-job-stdout", "stdout", 0, len(nodeJobStdoutFirst)), nil
	case 8:
		_, result, err := nodeJobInvocationPayload(call, nodes.JobCommandLogs)
		if err != nil {
			return nil, err
		}
		provider.stdout, _ = result["data"].(string)
		provider.stdoutCursor, _ = result["next_cursor"].(float64)
		if provider.stdout != nodeJobStdoutFirst || provider.stdoutCursor != float64(len(nodeJobStdoutFirst)) {
			return nil, fmt.Errorf("stdout result is incomplete: %#v", result)
		}
		return provider.readLog("call-job-stdout-replay", "stdout", 0, len(nodeJobStdoutFirst)), nil
	case 9:
		_, result, err := nodeJobInvocationPayload(call, nodes.JobCommandLogs)
		if err != nil {
			return nil, err
		}
		if result["data"] != provider.stdout || result["next_cursor"] != provider.stdoutCursor {
			return nil, fmt.Errorf("stdout cursor replay changed: %#v", result)
		}
		return provider.readLog(
			"call-job-stdout-continue",
			"stdout",
			int(provider.stdoutCursor),
			4096,
		), nil
	case 10:
		_, result, err := nodeJobInvocationPayload(call, nodes.JobCommandLogs)
		if err != nil {
			return nil, err
		}
		continuation, _ := result["data"].(string)
		wantStdout := nodeJobStdoutFirst + nodeJobStdoutSecret + "\n"
		if provider.stdout+continuation != wantStdout || result["next_cursor"] != float64(len(wantStdout)) {
			return nil, fmt.Errorf("stdout cursor continuation is unordered: %#v", result)
		}
		return provider.readLog("call-job-stderr", "stderr", 0, 4096), nil
	case 11:
		_, result, err := nodeJobInvocationPayload(call, nodes.JobCommandLogs)
		if err != nil {
			return nil, err
		}
		stderr, _ := result["data"].(string)
		if !strings.Contains(stderr, nodeJobStderrSecret) {
			return nil, fmt.Errorf("stderr result is incomplete: %#v", result)
		}
		return provider.describe("artifacts", nodes.JobCommandArtifacts), nil
	case 12:
		payload, err := nodeP0LastToolPayload(call)
		if err != nil {
			return nil, err
		}
		provider.artifactsRevision, err = nodeJobContractRevision(payload, nodes.JobCommandArtifacts)
		if err != nil {
			return nil, err
		}
		return provider.invokeJob(
			"call-job-artifacts",
			nodes.JobCommandArtifacts,
			provider.artifactsRevision,
			map[string]any{"job_id": provider.jobID},
		), nil
	case 13:
		_, result, err := nodeJobInvocationPayload(call, nodes.JobCommandArtifacts)
		if err != nil {
			return nil, err
		}
		artifacts, ok := result["artifacts"].([]any)
		if !ok || len(artifacts) != 1 {
			return nil, fmt.Errorf("job artifacts are incomplete: %#v", result)
		}
		artifact, ok := artifacts[0].(map[string]any)
		if !ok || artifact["name"] != "result" || artifact["state"] != "available" ||
			artifact["size"] != float64(len(nodeJobArtifactContent)) {
			return nil, fmt.Errorf("job artifact is incomplete: %#v", artifact)
		}
		provider.artifactRef, _ = artifact["artifact_ref"].(string)
		provider.artifactDigest, _ = artifact["sha256"].(string)
		if provider.artifactRef == "" || provider.artifactDigest != nodeJobArtifactDigest() {
			return nil, fmt.Errorf("job artifact authority is incomplete: %#v", artifact)
		}
		return provider.describe("artifact-download", nodes.JobCommandArtifacts), nil
	case 14:
		payload, err := nodeP0LastToolPayload(call)
		if err != nil {
			return nil, err
		}
		provider.artifactsRevision, err = nodeJobContractRevision(payload, nodes.JobCommandArtifacts)
		if err != nil {
			return nil, err
		}
		return llmscenario.ToolCallResponse(
			"I will retain the immutable job artifact through the existing transfer spool.",
			llmscenario.ToolCall("call-job-artifact-download", "nodes_download", map[string]any{
				"target": nodeJobTarget, "job_id": provider.jobID,
				"artifact_ref": provider.artifactRef, "deliver": false,
				"discovery_revision": provider.artifactsRevision,
			}),
		), nil
	case 15:
		payload, err := nodeP0LastToolPayload(call)
		if err != nil {
			return nil, err
		}
		provider.transferRef, _ = payload["artifact_ref"].(string)
		if payload["state"] != "committed" ||
			payload["size"] != float64(len(nodeJobArtifactContent)) ||
			payload["sha256"] != nodeJobArtifactDigest() ||
			!strings.HasPrefix(provider.transferRef, "transfer-artifact://") {
			return nil, fmt.Errorf("job artifact download is incomplete: %#v", payload)
		}
		return llmscenario.TextResponse("Recovered durable job and artifact."), nil
	case 16:
		return provider.describe("cancel-start", nodes.JobCommandStart), nil
	case 17:
		payload, err := nodeP0LastToolPayload(call)
		if err != nil {
			return nil, err
		}
		provider.startRevision, err = nodeJobContractRevision(payload, nodes.JobCommandStart)
		if err != nil {
			return nil, err
		}
		return provider.startJob("call-cancel-job-start", provider.startRevision, nodeJobCancelMode, false), nil
	case 18:
		_, result, err := nodeJobInvocationPayload(call, nodes.JobCommandStart)
		if err != nil {
			return nil, err
		}
		provider.cancelJobID, _ = result["job_id"].(string)
		if provider.cancelJobID == "" || result["state"] != "running" {
			return nil, fmt.Errorf("cancellation job start is incomplete: %#v", result)
		}
		return llmscenario.TextResponse("Cancellation job accepted."), nil
	case 19:
		return provider.describe("cancel", nodes.JobCommandCancel), nil
	case 20:
		payload, err := nodeP0LastToolPayload(call)
		if err != nil {
			return nil, err
		}
		provider.cancelRevision, err = nodeJobContractRevision(payload, nodes.JobCommandCancel)
		if err != nil {
			return nil, err
		}
		return provider.invokeJob(
			"call-job-cancel",
			nodes.JobCommandCancel,
			provider.cancelRevision,
			map[string]any{"job_id": provider.cancelJobID},
		), nil
	case 21:
		_, result, err := nodeJobInvocationPayload(call, nodes.JobCommandCancel)
		if err != nil {
			return nil, err
		}
		provider.cancelDispositionSeen, _ = result["disposition"].(string)
		if result["job_id"] != provider.cancelJobID ||
			(provider.cancelDispositionSeen != "cancel_requested" &&
				provider.cancelDispositionSeen != "canceled") {
			return nil, fmt.Errorf("job cancellation request is incomplete: %#v", result)
		}
		return llmscenario.TextResponse("Cancellation requested."), nil
	case 22:
		return provider.describe("cancel-status", nodes.JobCommandStatus), nil
	case 23:
		payload, err := nodeP0LastToolPayload(call)
		if err != nil {
			return nil, err
		}
		provider.statusRevision, err = nodeJobContractRevision(payload, nodes.JobCommandStatus)
		if err != nil {
			return nil, err
		}
		return provider.invokeJob(
			"call-canceled-job-status",
			nodes.JobCommandStatus,
			provider.statusRevision,
			map[string]any{"job_id": provider.cancelJobID},
		), nil
	case 24:
		_, result, err := nodeJobInvocationPayload(call, nodes.JobCommandStatus)
		if err != nil {
			return nil, err
		}
		if result["job_id"] != provider.cancelJobID || result["state"] != "canceled" ||
			result["cancellation_signal"] != true || result["failure_code"] != "CANCELED" {
			return nil, fmt.Errorf("terminal cancellation truth is incomplete: %#v", result)
		}
		return llmscenario.TextResponse("Cancellation proven."), nil
	default:
		return nil, fmt.Errorf("unexpected node job evidence model call %d", step+1)
	}
}

func (provider *nodeJobEvidenceProvider) describe(
	label string,
	command string,
) *providers.LLMResponse {
	return llmscenario.ToolCallResponse(
		"I will inspect the "+label+" command contract.",
		llmscenario.ToolCall("call-job-describe-"+label, "nodes", map[string]any{
			"action": "describe", "target": nodeJobTarget, "command": command,
		}),
	)
}

func (provider *nodeJobEvidenceProvider) startJob(
	callID string,
	revision string,
	mode string,
	withArtifact bool,
) *providers.LLMResponse {
	artifacts := []any{}
	if withArtifact {
		artifacts = append(artifacts, map[string]any{"name": "result", "path": nodeJobArtifactPath})
	}
	return provider.invokeJob(callID, nodes.JobCommandStart, revision, map[string]any{
		"argv": []any{"job-fixture", mode}, "cwd": "workspace",
		"timeout_seconds": 20, "env": map[string]any{"JOB_SECRET": nodeJobEnvSecret},
		"artifacts": artifacts,
	})
}

func (provider *nodeJobEvidenceProvider) readLog(
	callID string,
	stream string,
	cursor int,
	limit int,
) *providers.LLMResponse {
	return provider.invokeJob(callID, nodes.JobCommandLogs, provider.logsRevision, map[string]any{
		"job_id": provider.jobID, "stream": stream, "cursor": cursor, "limit_bytes": limit,
	})
}

func (*nodeJobEvidenceProvider) invokeJob(
	callID string,
	command string,
	revision string,
	input map[string]any,
) *providers.LLMResponse {
	return llmscenario.ToolCallResponse(
		"I will invoke the discovered bounded job command.",
		llmscenario.ToolCall(callID, "nodes_invoke", map[string]any{
			"target": nodeJobTarget, "command": command, "input": input,
			"discovery_revision": revision, "timeout_seconds": 10, "output_limit_bytes": 64 * 1024,
		}),
	)
}

func (provider *nodeJobEvidenceProvider) completedIdentity() (string, string) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.jobID, provider.startInvocationID
}

func (provider *nodeJobEvidenceProvider) startInvocationIDValue() string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.startInvocationID
}

func (provider *nodeJobEvidenceProvider) artifactEvidence() (string, string, string) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.transferRef, provider.artifactRef, provider.artifactDigest
}

func (provider *nodeJobEvidenceProvider) cancelJobIdentity() string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.cancelJobID
}

func (provider *nodeJobEvidenceProvider) AssertExhausted() error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.step != 25 {
		return fmt.Errorf("node job evidence consumed %d model steps, want 25", provider.step)
	}
	return nil
}

func nodeJobContractRevision(payload map[string]any, wantCommand string) (string, error) {
	command, ok := payload["command"].(map[string]any)
	if !ok || command["name"] != wantCommand || command["availability"] != string(nodes.ModelAvailable) {
		return "", fmt.Errorf("job command contract is unavailable: %#v", payload)
	}
	revision, ok := payload["discovery_revision"].(string)
	if !ok || revision == "" {
		return "", errors.New("job command contract omitted discovery revision")
	}
	return revision, nil
}

func nodeJobInvocationPayload(
	call llmscenario.ProviderCall,
	wantCommand string,
) (map[string]any, map[string]any, error) {
	payload, err := nodeP0LastToolPayload(call)
	if err != nil {
		return nil, nil, err
	}
	result, ok := payload["result"].(map[string]any)
	if payload["command"] != wantCommand || payload["state"] != "succeeded" || !ok {
		return nil, nil, fmt.Errorf("job invocation result is incomplete: %#v", payload)
	}
	return payload, result, nil
}

func nodeJobArtifactDigest() string {
	digest := sha256.Sum256([]byte(nodeJobArtifactContent))
	return hex.EncodeToString(digest[:])
}

func waitForNodeJobFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for node job file %s", path)
}

func waitForNodeJobStoreState(
	t *testing.T,
	stateDir string,
	jobID string,
	want string,
) map[string]any {
	t.Helper()
	indexPath := filepath.Join(companion.JobStorePath(stateDir), "index.json")
	deadline := time.Now().Add(10 * time.Second)
	var lastState string
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(indexPath)
		if err == nil {
			var document struct {
				Records map[string]map[string]any `json:"records"`
			}
			if json.Unmarshal(data, &document) == nil {
				record := document.Records[jobID]
				lastState, _ = record["state"].(string)
				if lastState == want {
					return record
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s state = %q, want %q", jobID, lastState, want)
	return nil
}

func assertAvailableNodeJobArtifact(t *testing.T, record map[string]any) {
	t.Helper()
	artifacts, ok := record["artifacts"].([]any)
	if !ok || len(artifacts) != 1 {
		t.Fatalf("retained job artifacts = %#v", record["artifacts"])
	}
	artifact, ok := artifacts[0].(map[string]any)
	if !ok || artifact["state"] != "available" ||
		artifact["size"] != float64(len(nodeJobArtifactContent)) ||
		artifact["sha256"] != nodeJobArtifactDigest() {
		t.Fatalf("retained job artifact = %#v", artifact)
	}
}

func resolveNodeJobTransferArtifact(
	t *testing.T,
	workspace string,
	runtimeState *nodeAdmissionRuntime,
	ref string,
) []byte {
	t.Helper()
	index, err := os.ReadFile(filepath.Join(nodes.GatewayTransferSpoolPath(workspace), ".transfer-spool.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Records map[string]nodes.TransferArtifactRecord `json:"records"`
	}
	if err := json.Unmarshal(index, &document); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range document.Records {
		if artifact.Ref != ref {
			continue
		}
		file, retained, resolveErr := runtimeState.transferSpool.ResolveOwned(artifact.Owner, ref)
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		defer file.Close()
		if retained.Ref != ref || retained.State != nodes.TransferArtifactCommitted {
			t.Fatalf("resolved transfer artifact = %#v", retained)
		}
		data, readErr := io.ReadAll(file)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return data
	}
	t.Fatalf("transfer artifact %q was not durably indexed", ref)
	return nil
}

func assertNodeJobLaunches(t *testing.T, root string, want []string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "launches.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(data))
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("node job launches = %#v, want %#v", lines, want)
	}
}

func assertNodeJobApprovalPrompt(
	t *testing.T,
	message bus.OutboundMessage,
	action string,
	forbidden ...string,
) {
	t.Helper()
	for _, required := range []string{"nodes_invoke", action, "allow_once", "deny"} {
		if !strings.Contains(message.Content, required) {
			t.Fatalf("job approval prompt omitted %q: %q", required, message.Content)
		}
	}
	forbidden = append(forbidden, nodeJobEnvSecret, nodeJobFinishMode, nodeJobCancelMode, nodeJobArtifactPath)
	for _, value := range forbidden {
		if value != "" && strings.Contains(message.Content, value) {
			t.Fatalf("job approval prompt leaked %q: %q", value, message.Content)
		}
	}
}

func assertNodeJobEvents(t *testing.T, events []runtimeevents.Event) {
	t.Helper()
	want := map[string]int{
		tools.NodeInvocationObservationPrepared:   11,
		tools.NodeInvocationObservationDispatched: 11,
		tools.NodeInvocationObservationCompleted:  11,
		tools.NodeInvocationObservationStatus:     1,
	}
	got := make(map[string]int, len(want))
	for _, event := range events {
		payload, ok := event.Payload.(tools.NodeInvocationEventPayload)
		if event.Kind != runtimeevents.KindNodeInvocationObserved || !ok {
			t.Fatalf("node job event = %#v", event)
		}
		got[payload.Observation]++
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("node job observations = %#v, want %#v", got, want)
	}
	assertNodeJobEvidenceRedacted(
		t,
		events,
		nodeJobFinishMode,
		nodeJobCancelMode,
		nodeJobArtifactPath,
		nodeJobArtifactContent,
		nodeJobStdoutSecret,
		nodeJobStderrSecret,
		nodeJobEnvSecret,
	)
}

func assertNodeJobEvidenceRedacted(t *testing.T, value any, forbidden ...string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range forbidden {
		if secret != "" && bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("node job evidence leaked %q: %s", secret, encoded)
		}
	}
}
