//go:build (linux || darwin) && integration

package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image/png"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type computerGatewayFixtureState struct {
	Kind      string   `json:"kind"`
	InspectID string   `json:"inspect_id"`
	PID       uint32   `json:"pid"`
	Windows   []uint32 `json:"windows"`
}

func TestGatewayNativeComputerToolsOverWSS(t *testing.T) {
	driver, fixture := os.Getenv("MINTCLAW_COMPUTER_NATIVE_DRIVER"), os.Getenv("MINTCLAW_COMPUTER_NATIVE_FIXTURE")
	if driver == "" || fixture == "" {
		t.Skip("explicit pinned driver and disposable native fixture required")
	}
	app := os.Getenv("MINTCLAW_COMPUTER_COMPANION_APP")
	if runtime.GOOS == "darwin" && app == "" {
		t.Fatal("the real companion app permission identity is required")
	}
	process, input, events := startComputerGatewayFixture(t, fixture)
	ready := nextComputerGatewayFixtureEvent(t, events, "ready", "")
	if ready.PID != uint32(process.Process.Pid) || len(ready.Windows) != 2 || ready.Windows[0] == ready.Windows[1] {
		t.Fatal("fixture did not attest its two distinct owned windows")
	}
	workspace, root := t.TempDir(), t.TempDir()
	registry, admission, nodeRuntime := newVerticalSliceNodeRuntime(t, workspace)
	server := httptest.NewTLSServer(admission)
	defer server.Close()
	defer closeVerticalSliceAdmission(t, admission)
	principal := computerClientPrincipal()
	gui := map[string]string{}
	if runtime.GOOS == "linux" {
		for _, key := range []string{"DISPLAY", "XAUTHORITY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
			if value := os.Getenv(key); value != "" {
				gui[key] = value
			}
		}
	}
	profiles, err := companion.NormalizeComputerProfiles(map[string]companion.ComputerProfilePolicy{
		"fixture": {
			Enabled: true, Executable: driver, SHA256: os.Getenv("MINTCLAW_COMPUTER_NATIVE_SHA256"),
			PID: ready.PID, WindowID: ready.Windows[0], Environment: gui,
			AllowedActors: []string{principal.ActorID}, AllowedAgents: []string{principal.AgentID},
			AllowedRuntimes: []runtimecap.Kind{principal.Runtime}, LeaseSeconds: 120, ObservationSeconds: 30,
		},
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	profile := nodes.ComputerProfileDescriptor{
		Alias: "fixture", Revision: profiles["fixture"].Revision,
		OS: computer.OSLinux, Mode: computer.ModeAttached, Isolation: computer.IsolationNone, Backend: "cua",
	}
	if runtime.GOOS == "darwin" {
		profile.OS = computer.OSMacOS
	}
	descriptors, err := nodes.ComputerCommandDescriptors([]nodes.ComputerProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]string, len(descriptors))
	for index, descriptor := range descriptors {
		commands[index] = descriptor.Name
	}
	certificate := sha256.Sum256(server.Certificate().Raw)
	nodeConfig := companion.Config{
		GatewayURL: strings.Replace(server.URL, "https://", "wss://", 1) + companion.GatewayPath,
		StateDir:   filepath.Join(root, "state"),
		TLS:        companion.TLSConfig{CertificateSHA256: hex.EncodeToString(certificate[:])},
		Reconnect:  companion.ReconnectConfig{MinDelaySeconds: 1, MaxDelaySeconds: 1, PendingDelaySeconds: 1},
		Policy: nodes.LocalCommandPolicy{
			Revision: "gateway-native-fixture", AllowedCommands: commands,
			MaximumRisk: nodes.RiskWrite, MaxTimeoutSeconds: 30, MaxOutputBytes: 64 * 1024,
		},
		ComputerProfiles: profiles,
	}
	configPath := filepath.Join(root, "node.json")
	writeVerticalSliceConfig(t, configPath, nodeConfig)
	var stopCompanion func()
	if runtime.GOOS == "darwin" {
		stopCompanion = startComputerGatewayMacApp(t, app, configPath)
	} else {
		node := startVerticalSliceCompanion(t, buildVerticalSliceCompanion(t, root), configPath)
		stopCompanion = func() { node.stop(t) }
	}
	defer stopCompanion()
	pending := waitForVerticalSliceNodeState(t, registry, nodes.StatePendingPairing)
	if _, err := registry.Approve(pending.ID, nodes.PairingApproval{
		Aliases: []nodes.Alias{"native-companion"}, AllowedCommands: commands, At: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	waitForVerticalSliceNodeState(t, registry, nodes.StateConnected)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Nodes.Enabled = true
	cfg.Execution.Targets = map[string]config.ExecutionTarget{"native": {Type: "node", Node: "native-companion"}}
	cfg.Tools.Computer = config.ComputerToolsConfig{Enabled: true, Targets: map[string]config.ComputerTargetConfig{
		"desktop": {
			NodeTarget: "native", Profile: "fixture", Revision: profile.Revision,
			Actors: []string{principal.ActorID}, Agents: []string{principal.AgentID},
			Runtimes: []runtimecap.Kind{principal.Runtime}, LeaseSeconds: 120, ObservationSeconds: 30,
		},
	}}
	running := &services{NodeAdmission: nodeRuntime}
	if err := setupComputerRuntime(cfg, running); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := closeComputerRuntime(ctx, running); err != nil {
			t.Error("gateway computer cleanup failed", err)
		}
	}()
	source := &gatewayComputerToolSource{
		services: running, revision: cfg.Tools.Computer.PolicyRevision(cfg.Execution.Targets),
		workspaceID: running.Computer.workspaceID,
	}
	options := tools.NewComputerToolOptions(cfg.Tools.Computer)
	sessionTool := tools.NewComputerSessionTool(options, source)
	observeTool := tools.NewComputerObserveTool(options, source)
	ctx := toolshared.WithRuntimeCapabilities(t.Context(), runtimecap.NewContext(runtimecap.Inputs{
		Kind: principal.Runtime,
	}).BindPrincipal(principal))
	execute := func(tool toolshared.Tool, id string, args map[string]any) *toolshared.ToolResult {
		t.Helper()
		result := tool.Execute(toolshared.WithToolCallID(ctx, id), args)
		if result == nil || result.IsError {
			t.Fatalf("native gateway %s failed: %v", id, result)
		}
		return result
	}
	execute(tools.NewComputerTargetsTool(options, source), "targets", map[string]any{})
	openedResult := execute(sessionTool, "open", map[string]any{"operation": "open", "target": "desktop"})
	var opened struct {
		SessionID string `json:"computer_session_id"`
	}
	if json.Unmarshal([]byte(openedResult.ForLLM), &opened) != nil || opened.SessionID == "" {
		t.Fatal("public tool omitted native session identity")
	}
	before, err := source.Status(ctx, principal, opened.SessionID, "before-status")
	if err != nil {
		t.Fatal(err)
	}
	observe := func(id string) [32]byte {
		t.Helper()
		result := execute(observeTool, id, map[string]any{"computer_session_id": opened.SessionID})
		if len(result.ContextMedia) != 1 || !result.ContextMediaRequiresVision || len(result.Media) != 0 {
			t.Fatal("native PNG was not protected vision-required context")
		}
		image, err := base64.StdEncoding.DecodeString(
			strings.TrimPrefix(result.ContextMedia[0], "data:image/png;base64,"),
		)
		if err != nil {
			t.Fatal("model PNG encoding failed", err)
		}
		defer clear(image)
		dimensions, err := png.DecodeConfig(bytes.NewReader(image))
		if err != nil || dimensions.Width <= 0 || dimensions.Height <= 0 ||
			runtime.GOOS == "linux" && (dimensions.Width != 440 || dimensions.Height != 240) {
			t.Fatal("public gateway did not transfer exact owned window pixels", err)
		}
		encoded, err := json.Marshal(result)
		if err != nil || bytes.Contains(encoded, []byte("base64,")) || bytes.Contains(encoded, image) {
			t.Fatal("native pixels entered ordinary tool-result serialization")
		}
		return sha256.Sum256(image)
	}
	firstImage := observe("observe-before")
	handoff := execute(sessionTool, "handoff", map[string]any{
		"operation": "handoff", "computer_session_id": opened.SessionID, "interaction_language": "ru",
		"handoff_prompt": map[string]any{"header": "Проверка окна", "question": "Окно проверено. Что сделать дальше?"},
	})
	if handoff.Control.Suspension == nil || handoff.Control.Suspension.PromptLanguage != "ru" ||
		handoff.Control.LiveHandoff == nil || handoff.Control.LiveHandoff.ResourceID != opened.SessionID {
		t.Fatal("public native handoff lost its language or original identity")
	}
	if result := observeTool.Execute(toolshared.WithToolCallID(ctx, "observe-human"), map[string]any{
		"computer_session_id": opened.SessionID,
	}); result == nil || !result.IsError {
		t.Fatal("public observe ran during human control")
	}
	if err := json.NewEncoder(input).Encode(map[string]any{
		"inspect_id": "visual-change", "advance_visual": true,
	}); err != nil {
		t.Fatal(err)
	}
	changed := nextComputerGatewayFixtureEvent(t, events, "lifetime", "visual-change")
	if changed.PID != ready.PID || !slices.Equal(changed.Windows, ready.Windows) {
		t.Fatal("fixture window identity changed")
	}
	if err := handoff.Control.ResolveSuspension(ctx, interactions.OutcomeAnswered); err != nil {
		t.Fatal(err)
	}
	after, err := source.Status(ctx, principal, opened.SessionID, "after-status")
	if err != nil || after.Control != computer.ControlAgent || before.Binding.SessionID != after.Binding.SessionID ||
		before.Binding.WorkerID != after.Binding.WorkerID || before.Binding.Target != after.Binding.Target {
		t.Fatal("public handoff resume rebound native authority", err)
	}
	if firstImage == observe("observe-after") {
		t.Fatal("fresh public observe reused stale native pixels")
	}
	released := execute(
		sessionTool,
		"release",
		map[string]any{"operation": "release", "computer_session_id": opened.SessionID},
	)
	if !sessionTool.VerifyResourceDisposition(ctx, map[string]any{
		"operation": "release", "computer_session_id": opened.SessionID,
	}, released) {
		t.Fatal("public release omitted terminal ownership proof")
	}
	inspect := func(nonce string) {
		t.Helper()
		if err := json.NewEncoder(input).Encode(map[string]string{"inspect_id": nonce}); err != nil {
			t.Fatal(err)
		}
		state := nextComputerGatewayFixtureEvent(t, events, "lifetime", nonce)
		if state.PID != ready.PID || !slices.Equal(state.Windows, ready.Windows) {
			t.Fatal("release or companion exit closed an application window")
		}
	}
	inspect("after-release")
	if err := closeComputerRuntime(ctx, running); err != nil {
		t.Fatal(err)
	}
	stopCompanion()
	inspect("after-companion-exit")
	t.Logf(
		"public gateway -> real %s companion -> native PNG: same window/session/worker fresh resume; release and companion exit preserve both windows",
		runtime.GOOS,
	)
}

func startComputerGatewayFixture(
	t *testing.T,
	path string,
) (*exec.Cmd, io.WriteCloser, <-chan computerGatewayFixtureState) {
	t.Helper()
	command := exec.Command(path)
	if runtime.GOOS == "linux" {
		command = exec.Command("/usr/bin/python3", path)
	}
	command.Env = []string{"PATH=/usr/bin:/bin", "GSETTINGS_BACKEND=memory", "NO_AT_BRIDGE=0"}
	for _, key := range []string{"DISPLAY", "XAUTHORITY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		if value := os.Getenv(key); value != "" {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	t.Cleanup(func() { _ = input.Close(); _ = command.Process.Kill(); <-done })
	events := make(chan computerGatewayFixtureState, 16)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			var event computerGatewayFixtureState
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				select {
				case events <- event:
				case <-t.Context().Done():
					return
				}
			}
		}
	}()
	return command, input, events
}

func nextComputerGatewayFixtureEvent(
	t *testing.T,
	events <-chan computerGatewayFixtureState,
	kind, nonce string,
) computerGatewayFixtureState {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("owned fixture exited")
			}
			if event.Kind == kind && event.InspectID == nonce {
				return event
			}
		case <-timer.C:
			t.Fatal("owned fixture inspection timed out")
		}
	}
}

func startComputerGatewayMacApp(t *testing.T, app, configPath string) func() {
	t.Helper()
	if !filepath.IsAbs(app) || !strings.HasSuffix(app, ".app") {
		t.Fatal("explicit companion app required")
	}
	canonical, err := filepath.EvalSymlinks(app)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/usr/bin/open", "-n", "-W", canonical, "--args", "run", "--config", configPath)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	find := func() *os.Process {
		output, err := exec.Command("/usr/bin/pgrep", "-f", regexp.QuoteMeta(configPath)).Output()
		if err != nil {
			return nil
		}
		for _, value := range strings.Fields(string(output)) {
			pid, err := strconv.Atoi(value)
			if err != nil || pid == command.Process.Pid {
				continue
			}
			args, err := exec.Command("/bin/ps", "-p", value, "-o", "command=").Output()
			if err == nil &&
				strings.HasPrefix(
					strings.TrimSpace(string(args)),
					filepath.Join(canonical, "Contents/MacOS/mintclaw-node")+" ",
				) {
				process, _ := os.FindProcess(pid)
				return process
			}
		}
		return nil
	}
	stopped := false
	return func() {
		if stopped {
			return
		}
		stopped = true
		if process := find(); process != nil {
			_ = process.Signal(os.Interrupt)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error("owned companion app failed", err)
			}
		case <-time.After(10 * time.Second):
			if process := find(); process != nil {
				_ = process.Kill()
			}
			_ = command.Process.Kill()
			t.Error("owned companion app did not exit gracefully")
		}
	}
}
