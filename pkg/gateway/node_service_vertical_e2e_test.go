//go:build linux && integration

package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	codingscope "github.com/bogdanovich/mintclaw/pkg/coding/scope"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestNodeServiceStatusModelToSystemdRealProcessVerticalSlice(t *testing.T) {
	if _, err := os.Stat("/usr/bin/systemctl"); err != nil {
		if _, fallbackErr := os.Stat("/bin/systemctl"); fallbackErr != nil {
			t.Skip("systemctl is unavailable")
		}
	}
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ModelName = "node-service-e2e-model"
	cfg.Agents.Defaults.ResponseFooter.Enabled = false
	cfg.Nodes.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{
		"services": {
			Type: "node", Node: "service-node", Executor: companion.LocalExecutor,
			ServiceProfile: "server-services",
		},
	}
	cfg.Agents.Defaults.TargetPolicy = &config.TargetPolicy{
		DefaultTarget: "services", AllowedTargets: []string{"services"},
	}
	cfg.Gateway.CodingRemote = config.GatewayCodingRemoteListener{
		Enabled: true, SocketPath: "/tmp/mintclaw-service-vertical.sock",
	}
	cfg.Execution.CodingRemoteCapabilities = map[string]config.CodingRemoteCapability{
		"service-observe": {
			Revision: "service-observe-v1", Kind: config.CodingRemoteCapabilityNode,
			Target: "services", Operations: []string{"service.status.v1"},
		},
	}
	cfg.Execution.CodingRemoteGrants = map[string]config.CodingRemoteClientGrant{
		"local-development": {
			Revision: "grant-v1", Agent: "main",
			LocalProfiles: []codingscope.Profile{codingscope.ProfileInvestigate, codingscope.ProfileMutate},
			Capabilities:  []string{"service-observe"},
		},
	}
	if err := cfg.ValidateExecutionTargets(); err != nil {
		t.Fatal(err)
	}

	registry, admission, runtimeState := newVerticalSliceNodeRuntime(t, workspace)
	server := httptest.NewTLSServer(admission)
	defer server.Close()
	defer closeVerticalSliceAdmission(t, admission)

	tempDir := t.TempDir()
	binaryPath := buildVerticalSliceCompanion(t, tempDir)
	fingerprint := sha256.Sum256(server.Certificate().Raw)
	companionConfig := companion.Config{
		GatewayURL: strings.Replace(server.URL, "https://", "wss://", 1) +
			companion.GatewayPath,
		StateDir: filepath.Join(tempDir, "state"),
		TLS: companion.TLSConfig{
			CertificateSHA256: hex.EncodeToString(fingerprint[:]),
		},
		Reconnect: companion.ReconnectConfig{
			MinDelaySeconds: 1, MaxDelaySeconds: 1, PendingDelaySeconds: 1,
		},
		Policy: nodes.LocalCommandPolicy{
			Revision: "service-e2e-policy", AllowedCommands: []string{"service.status.v1"},
			MaximumRisk: nodes.RiskRead, MaxTimeoutSeconds: 5, MaxOutputBytes: 4096,
		},
		ServicePolicies: companion.ServicePolicies{
			"server-services": {
				Enabled: true, Revision: "server-services-e2e-v1", Manager: "systemd-system",
				Services: map[string]companion.ServicePolicyEntry{
					"vpn": {
						Unit: "mintclaw-p3-e2e.service", Description: "P3 service status fixture",
						Status: true,
					},
				},
			},
		},
	}
	configPath := filepath.Join(tempDir, "config.json")
	writeVerticalSliceConfig(t, configPath, companionConfig)
	process := startVerticalSliceCompanion(t, binaryPath, configPath)
	defer process.stop(t)

	pending := waitForVerticalSliceNodeState(t, registry, nodes.StatePendingPairing)
	if _, err := registry.Approve(pending.ID, nodes.PairingApproval{
		Aliases: []nodes.Alias{"service-node"}, AllowedCommands: []string{"service.status.v1"},
		At: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	waitForVerticalSliceNodeState(t, registry, nodes.StateConnected)

	eventBus := runtimeevents.NewBus()
	provider := &serviceVerticalSliceProvider{}
	agentLoop := agent.NewAgentLoop(
		cfg,
		bus.NewMessageBus(),
		provider,
		agent.WithIsolatedToolBootstrap(),
		agent.WithRuntimeEvents(eventBus),
	)
	defer agentLoop.Close()
	if err := setupNodeTools(cfg, agentLoop, runtimeState); err != nil {
		t.Fatal(err)
	}
	subscription, eventChannel, err := eventBus.Channel().
		KindPrefix("node.invocation.").
		SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{
			Name: "node-service-vertical-slice", Buffer: 8,
		})
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	response, err := agentLoop.ProcessDirect(
		t.Context(),
		"Inspect the configured VPN service status on the remote node.",
		"node-service-e2e-session",
	)
	if err != nil {
		t.Fatal(err)
	}
	if response != "Remote service status recovered." {
		t.Fatalf("service vertical-slice response = %q", response)
	}
	if err := provider.AssertExhausted(); err != nil {
		t.Fatal(err)
	}
	events := collectVerticalSliceEvents(t, eventChannel, 4)
	want := map[string]int{
		tools.NodeInvocationObservationPrepared: 1, tools.NodeInvocationObservationDispatched: 1,
		tools.NodeInvocationObservationCompleted: 1, tools.NodeInvocationObservationStatus: 1,
	}
	got := make(map[string]int, len(want))
	for _, event := range events {
		payload, ok := event.Payload.(tools.NodeInvocationEventPayload)
		if !ok || payload.Service != "vpn" || payload.Command != "service.status.v1" {
			t.Fatalf("service event = %#v", event)
		}
		got[payload.Observation]++
	}
	for observation, count := range want {
		if got[observation] != count {
			t.Fatalf("service observations = %#v, want %#v", got, want)
		}
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"mintclaw-p3-e2e.service", "systemctl", "plan_hash", "helper",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("service vertical-slice event leaked %q: %s", forbidden, encoded)
		}
	}

	assertCodingRemoteServiceStatusRealProcess(t, cfg, runtimeState)
}

func assertCodingRemoteServiceStatusRealProcess(
	t *testing.T,
	cfg *config.Config,
	runtimeState *nodeAdmissionRuntime,
) {
	t.Helper()
	handler := codingRemoteDiscoveryHandler{
		config: func() *config.Config { return cfg }, now: time.Now,
		source: func(current *config.Config) (tools.NodeInvocationSource, error) {
			return newNodeInvocationSource(current, runtimeState)
		},
	}
	threadID := uuid.NewString()
	sessionKey := "coding:" + threadID
	projectKey := "git_worktree:" + strings.Repeat("a", 64)
	discovery := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "service-capabilities",
		Operation: codingremote.OperationCapabilitiesList,
		Grant:     "local-development", GrantRevision: "grant-v1",
		ThreadID: threadID, SessionKey: sessionKey, ProjectKey: projectKey,
		LocalProfile: codingscope.ProfileInvestigate,
	}
	if err := discovery.Validate(); err != nil {
		t.Fatal(err)
	}
	discovered := handler.HandleCodingRemote(t.Context(), discovery)
	if discovered.Status != codingremote.ResponseOK || discovered.Snapshot == nil ||
		len(discovered.Snapshot.Capabilities) != 1 ||
		len(discovered.Snapshot.Capabilities[0].Operations) != 1 ||
		discovered.Snapshot.Capabilities[0].Operations[0].Alias != "service_status" {
		t.Fatalf("coding service discovery = %#v", discovered)
	}
	capability := discovered.Snapshot.Capabilities[0]
	invoke := codingremote.Request{
		Schema: codingremote.SchemaV1, RequestID: "service-invoke",
		Operation: codingremote.OperationCapabilityInvoke,
		Grant:     discovery.Grant, GrantRevision: discovery.GrantRevision,
		ThreadID: threadID, SessionKey: sessionKey, ProjectKey: projectKey,
		LocalProfile: discovery.LocalProfile,
		Principal: &runtimecap.Principal{
			Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
			SessionID: sessionKey, ExecutionID: "turn-1",
		},
		CallID: "service_call", DiscoveryRevision: discovered.Snapshot.DiscoveryRevision,
		Capability: capability.Alias, CapabilityRevision: capability.Revision,
		CapabilityOperation: "service_status", Arguments: json.RawMessage(`{"service":"vpn"}`),
		DeadlineUnixMS: time.Now().Add(30 * time.Second).UnixMilli(),
	}
	invoke.InvocationID = codingremote.DeriveInvocationID(invoke)
	if err := invoke.Validate(); err != nil {
		t.Fatal(err)
	}
	invoked := handler.HandleCodingRemote(t.Context(), invoke)
	if invoked.Status != codingremote.ResponseOK || invoked.Result == nil ||
		invoked.Result.State != string(nodes.InvocationSucceeded) ||
		invoked.Result.Operation != "service_status" || invoked.Result.Risk != codingremote.RiskRead {
		t.Fatalf("coding service invocation = %#v", invoked)
	}
	var result map[string]any
	if err := json.Unmarshal(invoked.Result.Result, &result); err != nil || result["service"] != "vpn" {
		t.Fatalf("coding service result = %#v, %v", result, err)
	}

	status := invoke
	status.RequestID = "service-status"
	status.Operation = codingremote.OperationInvocationStatus
	status.CallID = "service_status_call"
	status.Arguments = nil
	status.Principal = &runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:operator", AgentID: "main",
		SessionID: sessionKey, ExecutionID: "turn-2",
	}
	if err := status.Validate(); err != nil {
		t.Fatal(err)
	}
	observed := handler.HandleCodingRemote(t.Context(), status)
	if observed.Status != codingremote.ResponseOK || observed.Result == nil ||
		observed.Result.State != string(nodes.InvocationSucceeded) ||
		observed.Result.InvocationID != invoke.InvocationID || observed.Result.Operation != "service_status" {
		t.Fatalf("coding service retained status = %#v", observed)
	}
}

type serviceVerticalSliceProvider struct {
	mu                sync.Mutex
	step              int
	discoveryRevision string
	invocationID      string
}

func (*serviceVerticalSliceProvider) GetDefaultModel() string {
	return "node-service-e2e-model"
}

func (provider *serviceVerticalSliceProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	call := llmscenario.ProviderCall{Messages: messages, Tools: toolDefs}
	step := provider.step
	provider.step++
	switch step {
	case 0:
		if err := llmscenario.RequireToolDefinition("nodes")(call); err != nil {
			return nil, err
		}
		return llmscenario.ToolCallResponse(
			"Inspect the configured service command.",
			llmscenario.ToolCall("call-service-describe", "nodes", map[string]any{
				"action": "describe", "target": "services", "command": "service.status.v1",
			}),
		), nil
	case 1:
		payload, err := nodeP0LastToolPayload(call)
		if err != nil {
			return nil, err
		}
		command, ok := payload["command"].(map[string]any)
		revision, revisionOK := payload["discovery_revision"].(string)
		_, serviceOK := command["service"].(map[string]any)
		if !ok || command["name"] != "service.status.v1" ||
			command["availability"] != string(nodes.ModelAvailable) ||
			!revisionOK || revision == "" || !serviceOK {
			return nil, fmt.Errorf("service discovery is incomplete: %#v", payload)
		}
		provider.discoveryRevision = revision
		return llmscenario.ToolCallResponse(
			"Read the bounded service status.",
			llmscenario.ToolCall("call-service-invoke", "nodes_invoke", map[string]any{
				"target": "services", "command": "service.status.v1",
				"input": map[string]any{"service": "vpn"}, "discovery_revision": revision,
			}),
		), nil
	case 2:
		payload, err := nodeP0LastToolPayload(call)
		if err != nil {
			return nil, err
		}
		invocationID, ok := payload["invocation_id"].(string)
		result, resultOK := payload["result"].(map[string]any)
		if !ok || invocationID == "" || payload["state"] != string(nodes.InvocationSucceeded) ||
			!resultOK || result["service"] != "vpn" {
			return nil, fmt.Errorf("service invocation is incomplete: %#v", payload)
		}
		provider.invocationID = invocationID
		return llmscenario.ToolCallResponse(
			"Recover the durable result without replay.",
			llmscenario.ToolCall("call-service-status", "nodes_status", map[string]any{
				"invocation_id": invocationID,
			}),
		), nil
	case 3:
		payload, err := nodeP0LastToolPayload(call)
		if err != nil {
			return nil, err
		}
		result, ok := payload["result"].(map[string]any)
		if payload["invocation_id"] != provider.invocationID ||
			payload["state"] != string(nodes.InvocationSucceeded) ||
			!ok || result["service"] != "vpn" {
			return nil, fmt.Errorf("service recovery is incomplete: %#v", payload)
		}
		return llmscenario.TextResponse("Remote service status recovered."), nil
	default:
		return nil, errors.New("unexpected service vertical-slice model call")
	}
}

func (provider *serviceVerticalSliceProvider) AssertExhausted() error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.step != 4 {
		return fmt.Errorf("service vertical slice used %d model steps, want 4", provider.step)
	}
	return nil
}
