//go:build (linux || darwin) && integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
	"github.com/bogdanovich/mintclaw/pkg/nodes/protocol"
	nodews "github.com/bogdanovich/mintclaw/pkg/nodes/ws"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

type nativeFixtureState struct {
	Kind      string   `json:"kind"`
	InspectID string   `json:"inspect_id"`
	PID       uint32   `json:"pid"`
	Windows   []uint32 `json:"windows"`
}

func TestCompanionNativeComputerLifecycleOverWSS(t *testing.T) {
	driver, fixturePath := os.Getenv("MINTCLAW_COMPUTER_NATIVE_DRIVER"), os.Getenv("MINTCLAW_COMPUTER_NATIVE_FIXTURE")
	if driver == "" || fixturePath == "" {
		t.Skip("explicit pinned driver, disposable fixture and real companion required")
	}
	if runtime.GOOS == "darwin" && os.Getenv("MINTCLAW_COMPUTER_COMPANION_APP") == "" {
		t.Fatal("macOS qualification requires the actual companion app, not a test/SSH permission identity")
	}
	fixture, input, events := startNativeComputerFixture(t, fixturePath)
	ready := nextNativeFixtureEvent(t, events, func(event nativeFixtureState) bool { return event.Kind == "ready" })
	if ready.PID != uint32(fixture.Process.Pid) || len(ready.Windows) != 2 || ready.Windows[0] == ready.Windows[1] {
		t.Fatal("owned native fixture did not attest its two windows")
	}
	registry, admission, sessions := newProcessTestGateway(t)
	server := httptest.NewTLSServer(admission)
	defer server.Close()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := admission.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	root := t.TempDir()
	gui := map[string]string{}
	if runtime.GOOS == "linux" {
		for _, key := range []string{"DISPLAY", "XAUTHORITY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
			if value := os.Getenv(key); value != "" {
				gui[key] = value
			}
		}
	}
	profile := companion.ComputerProfilePolicy{
		Enabled: true, Executable: driver, SHA256: os.Getenv("MINTCLAW_COMPUTER_NATIVE_SHA256"),
		PID: ready.PID, WindowID: ready.Windows[0], Environment: gui,
		AllowedActors: []string{"native-fixture"}, AllowedAgents: []string{"native-fixture"},
		AllowedRuntimes: []runtimecap.Kind{runtimecap.KindGateway}, LeaseSeconds: 120, ObservationSeconds: 30,
	}
	profiles, err := companion.NormalizeComputerProfiles(
		map[string]companion.ComputerProfilePolicy{"fixture": profile},
		root,
	)
	if err != nil {
		t.Fatal(err)
	}
	metadata := nodes.ComputerProfileDescriptor{
		Alias: "fixture", Revision: profiles["fixture"].Revision,
		OS: computer.OSLinux, Mode: computer.ModeAttached, Isolation: computer.IsolationNone, Backend: "cua",
	}
	if runtime.GOOS == "darwin" {
		metadata.OS = computer.OSMacOS
	}
	descriptors, err := nodes.ComputerCommandDescriptors([]nodes.ComputerProfileDescriptor{metadata})
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]string, len(descriptors))
	for index, descriptor := range descriptors {
		commands[index] = descriptor.Name
	}
	fingerprint := sha256.Sum256(server.Certificate().Raw)
	cfg := companion.Config{
		GatewayURL: strings.Replace(server.URL, "https://", "wss://", 1) + companion.GatewayPath,
		StateDir: filepath.Join(
			root,
			"state",
		),
		TLS:       companion.TLSConfig{CertificateSHA256: hex.EncodeToString(fingerprint[:])},
		Reconnect: companion.ReconnectConfig{MinDelaySeconds: 1, MaxDelaySeconds: 1, PendingDelaySeconds: 1},
		Policy: nodes.LocalCommandPolicy{
			Revision: "native-fixture-policy", AllowedCommands: commands,
			MaximumRisk: nodes.RiskWrite, MaxTimeoutSeconds: 30, MaxOutputBytes: 64 * 1024,
		},
		ComputerProfiles: profiles,
	}
	configPath := filepath.Join(root, "config.json")
	writeProcessTestConfig(t, configPath, cfg)
	var stopCompanion func()
	if runtime.GOOS == "darwin" {
		stopCompanion = startNativeMacCompanion(t, os.Getenv("MINTCLAW_COMPUTER_COMPANION_APP"), configPath)
	} else {
		process := startCompanionProcess(t, buildCompanionBinary(t, root), configPath)
		stopCompanion = func() { process.stop(t) }
	}
	defer stopCompanion()
	pending := waitForOnlyNodeState(t, registry, nodes.StatePendingPairing)
	if _, err = registry.Approve(
		pending.ID,
		nodes.PairingApproval{AllowedCommands: commands, At: time.Now().Unix()},
	); err != nil {
		t.Fatal(err)
	}
	connected := waitForOnlyNodeState(t, registry, nodes.StateConnected)
	registration, exists, err := registry.Registration(connected.ID)
	if err != nil || !exists {
		t.Fatal("native companion registration unavailable", err)
	}
	turn := nodes.ComputerTurn{Runtime: runtimecap.KindGateway, ExecutionID: "native-fixture-turn"}
	invocations := 0
	invoke := func(command string, input any, output any) {
		t.Helper()
		invocations++
		descriptor, descriptorErr := registration.ApprovedCommand(command)
		if descriptorErr != nil {
			t.Fatal(descriptorErr)
		}
		encoded, encodeErr := json.Marshal(input)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		id := fmt.Sprintf("native-call-%d", invocations)
		plan, planErr := nodes.PrepareExecutionPlan(nodes.InvocationRequest{
			InvocationID: id, IdempotencyKey: id, NodeID: connected.ID,
			CatalogHash: registration.Snapshot.CatalogHash, Command: command, Input: encoded,
			AgentID: "native-fixture", ActorID: "native-fixture", SessionID: "native-fixture-thread",
			TimeoutSeconds: 30, OutputLimitBytes: 64 * 1024,
		}, descriptor, registration.Snapshot.Executor, registration.Snapshot.PolicyRevision, time.Now(), time.Minute)
		if planErr != nil {
			t.Fatal(planErr)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
		defer cancel()
		result, _, invokeErr := admission.Invoke(ctx, connected.ID, plan, nil, nil)
		if invokeErr != nil {
			t.Fatalf("native command %s failed: %v", command, invokeErr)
		}
		if err := json.Unmarshal(result, output); err != nil {
			t.Fatal("native result invalid", err)
		}
	}
	var targets nodes.ComputerTargetsResult
	invoke(nodes.ComputerCommandTargets, nodes.ComputerTargetsInput{ComputerTurn: turn}, &targets)
	if len(targets.Targets) != 1 || targets.Targets[0].Node != string(connected.ID) {
		t.Fatal("wrong native discovery")
	}
	var opened nodes.ComputerSessionResult
	invoke(
		nodes.ComputerCommandOpen,
		nodes.ComputerOpenInput{ComputerTurn: turn, Profile: "fixture", ProfileRevision: metadata.Revision},
		&opened,
	)
	control := func(current computer.Session, command string) computer.Session {
		var result nodes.ComputerSessionResult
		invoke(
			command,
			nodes.ComputerControlInput{
				ComputerTurn: turn,
				SessionID:    current.Binding.SessionID,
				Generation:   current.Binding.Generation,
			},
			&result,
		)
		return result.Session
	}
	observe := func(current computer.Session) nodes.ComputerObservationResult {
		var result nodes.ComputerObservationResult
		invoke(nodes.ComputerCommandObserve, nodes.ComputerObserveInput{
			ComputerControlInput: nodes.ComputerControlInput{
				ComputerTurn: turn,
				SessionID:    current.Binding.SessionID,
				Generation:   current.Binding.Generation,
			},
			WorkspaceID: "native-fixture-workspace",
		}, &result)
		if result.Validate([]nodes.ComputerProfileDescriptor{metadata}) != nil || result.AccessibilityText != "" ||
			result.Output == nil {
			t.Fatal("native observation contract failed")
		}
		verifyNativeImageTransfer(t, sessions, connected.ID, result)
		return result
	}
	first := observe(opened.Session)
	human := control(opened.Session, nodes.ComputerCommandHandoff)
	if err := json.NewEncoder(input).Encode(map[string]any{
		"inspect_id": "after-visible-change", "advance_visual": true,
	}); err != nil {
		t.Fatal(err)
	}
	changed := nextNativeFixtureEvent(t, events, func(event nativeFixtureState) bool {
		return event.Kind == "lifetime" && event.InspectID == "after-visible-change"
	})
	if changed.PID != ready.PID || !slices.Equal(changed.Windows, ready.Windows) {
		t.Fatal("fixture changed native identity during handoff")
	}
	resumePending := control(human, nodes.ComputerCommandRequestResume)
	resumed := control(resumePending, nodes.ComputerCommandResume)
	second := observe(resumed)
	if first.Session.Binding.SessionID != resumed.Binding.SessionID ||
		first.Session.Binding.WorkerID != resumed.Binding.WorkerID ||
		first.Session.Binding.Target != resumed.Binding.Target ||
		first.Observation.ID == second.Observation.ID || first.Output.SHA256 == second.Output.SHA256 {
		t.Fatal("real companion resume replaced the native resource or reused evidence")
	}
	released := control(resumed, nodes.ComputerCommandRelease)
	if released.Control != computer.ControlReleased {
		t.Fatal("native release not confirmed")
	}
	inspectNativeFixture(t, input, events, ready, "after-release")
	windowsAtExit := ready
	if os.Getenv("MINTCLAW_COMPUTER_NATIVE_LOSS_CASE") == "1" {
		var lossOpened nodes.ComputerSessionResult
		invoke(nodes.ComputerCommandOpen, nodes.ComputerOpenInput{
			ComputerTurn: turn, Profile: "fixture", ProfileRevision: metadata.Revision,
		}, &lossOpened)
		lossHuman := control(lossOpened.Session, nodes.ComputerCommandHandoff)
		if err := json.NewEncoder(input).Encode(map[string]any{
			"inspect_id": "after-native-window-loss", "close_target": true,
		}); err != nil {
			t.Fatal(err)
		}
		remaining := nextNativeFixtureEvent(t, events, func(event nativeFixtureState) bool {
			return event.Kind == "lifetime" && event.InspectID == "after-native-window-loss"
		})
		if remaining.PID != ready.PID || !slices.Equal(remaining.Windows, ready.Windows[1:]) {
			t.Fatal("loss fixture did not preserve its original process and guard window")
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			var status nodes.ComputerSessionResult
			invoke(nodes.ComputerCommandStatus, nodes.ComputerStatusInput{
				ComputerTurn: turn, SessionID: lossHuman.Binding.SessionID,
			}, &status)
			if status.Session.Control == computer.ControlLost {
				if status.Session.Binding.Target != lossHuman.Binding.Target ||
					status.Session.Binding.WorkerID != lossHuman.Binding.WorkerID {
					t.Fatal("loss silently rebound native authority")
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("native window loss not detected during handoff")
			}
			time.Sleep(25 * time.Millisecond)
		}
		windowsAtExit.Windows = slices.Clone(ready.Windows[1:])
		t.Log(
			"native window destruction during human control revoked the original session; process and guard remained alive",
		)
	}
	stopCompanion()
	inspectNativeFixture(t, input, events, windowsAtExit, "after-companion-exit")
	ledger, err := os.ReadFile(companion.InvocationLedgerPath(cfg.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ledger, []byte("accessibility_text")) || bytes.Contains(ledger, []byte("screenshot")) ||
		!bytes.Contains(ledger, []byte("protected_result")) {
		t.Fatal("real companion ledger retained private observation contents")
	}
	t.Logf(
		"real %s companion WSS: native pixels %dx%d; same session/worker/window fresh resume; release preserves both windows and process exit preserves remaining owned windows",
		runtime.GOOS,
		first.Observation.Geometry.Width,
		first.Observation.Geometry.Height,
	)
}

func verifyNativeImageTransfer(
	t *testing.T,
	sessions *nodews.SessionHub,
	nodeID nodes.ID,
	result nodes.ComputerObservationResult,
) {
	t.Helper()
	descriptor := *result.Output
	digest, err := hex.DecodeString(descriptor.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	binding := nodews.TransferBinding{
		TransferID: descriptor.TransferID, Direction: protocol.TransferDownload,
		PolicyRevision: descriptor.Binding.PolicyRevision, TotalSize: descriptor.Size,
	}
	copy(binding.SHA256[:], digest)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stream, err := sessions.OpenTransfer(ctx, nodeID, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	prepare := transferFrame(binding, protocol.TransferFramePrepare)
	prepare.Payload, err = json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(ctx, prepare); err != nil {
		t.Fatal(err)
	}
	expectTransferType(t, ctx, stream, protocol.TransferFrameAccept)
	var data bytes.Buffer
	sequence := uint64(1)
	for uint64(data.Len()) < descriptor.Size {
		chunk := expectTransferType(t, ctx, stream, protocol.TransferFrameChunk)
		if chunk.Sequence != sequence || uint64(data.Len()+len(chunk.Payload)) > descriptor.Size {
			t.Fatal("image sequence/size mismatch")
		}
		data.Write(chunk.Payload)
		ack := transferFrame(binding, protocol.TransferFrameAck)
		ack.Sequence = sequence
		if err := stream.Send(ctx, ack); err != nil {
			t.Fatal(err)
		}
		sequence++
	}
	expectTransferType(t, ctx, stream, protocol.TransferFrameStatus)
	if sha256.Sum256(data.Bytes()) != binding.SHA256 {
		t.Fatal("native image digest mismatch")
	}
	image, err := png.DecodeConfig(bytes.NewReader(data.Bytes()))
	if err != nil || image.Width != result.Observation.Geometry.Width ||
		image.Height != result.Observation.Geometry.Height {
		t.Fatal("native image dimensions mismatch", err)
	}
	if directory := os.Getenv("MINTCLAW_COMPUTER_NATIVE_CAPTURE_DIR"); directory != "" {
		info, statErr := os.Stat(directory)
		if !filepath.IsAbs(directory) || statErr != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			t.Fatal("fixture capture retention requires an explicit private directory")
		}
		path := filepath.Join(directory, "fixture-"+runtime.GOOS+"-"+result.Observation.ID+".png")
		file, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if openErr != nil {
			t.Fatal(openErr)
		}
		_, writeErr := file.Write(data.Bytes())
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("retain owned fixture image failed")
		}
		t.Log("owned fixture image retained", path)
	}
	if err := stream.Send(ctx, transferFrame(binding, protocol.TransferFrameCommit)); err != nil {
		t.Fatal(err)
	}
	expectTransferType(t, ctx, stream, protocol.TransferFrameCommitted)
}

func startNativeComputerFixture(t *testing.T, path string) (*exec.Cmd, io.WriteCloser, <-chan nativeFixtureState) {
	t.Helper()
	command := exec.Command(path)
	if runtime.GOOS == "linux" {
		command = exec.Command("/usr/bin/python3", path)
	}
	command.Env = []string{"PATH=/usr/bin:/bin", "GSETTINGS_BACKEND=memory", "NO_AT_BRIDGE=0"}
	if os.Getenv("MINTCLAW_COMPUTER_NATIVE_LOSS_CASE") == "1" {
		command.Env = append(command.Env, "MINTCLAW_FIXTURE_KEEP_GUARD=1")
	}
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
	events := make(chan nativeFixtureState, 16)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 32*1024)
		for scanner.Scan() {
			var event nativeFixtureState
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

func nextNativeFixtureEvent(
	t *testing.T,
	events <-chan nativeFixtureState,
	match func(nativeFixtureState) bool,
) nativeFixtureState {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("owned fixture exited")
			}
			if match(event) {
				return event
			}
		case <-deadline.C:
			t.Fatal("owned fixture inspection timed out")
		}
	}
}

func inspectNativeFixture(
	t *testing.T,
	input io.Writer,
	events <-chan nativeFixtureState,
	ready nativeFixtureState,
	nonce string,
) {
	t.Helper()
	if err := json.NewEncoder(input).Encode(map[string]string{"inspect_id": nonce}); err != nil {
		t.Fatal(err)
	}
	state := nextNativeFixtureEvent(
		t,
		events,
		func(event nativeFixtureState) bool { return event.Kind == "lifetime" && event.InspectID == nonce },
	)
	if state.PID != ready.PID || !slices.Equal(state.Windows, ready.Windows) {
		t.Fatal("same owned native windows did not survive")
	}
}

func startNativeMacCompanion(t *testing.T, app, config string) func() {
	t.Helper()
	if !filepath.IsAbs(app) || !strings.HasSuffix(app, ".app") {
		t.Fatal("explicit companion app path required")
	}
	command := exec.Command("/usr/bin/open", "-n", "-W", app, "--args", "run", "--config", config)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	// Only the unique private config path identifies this newly launched app.
	// Never quit by bundle ID: another companion may share that identity.
	find := func() *os.Process {
		data, err := exec.Command("/usr/bin/pgrep", "-f", regexp.QuoteMeta(config)).Output()
		if err != nil {
			return nil
		}
		for _, value := range strings.Fields(string(data)) {
			pid, parseErr := strconv.Atoi(value)
			if parseErr != nil || pid == command.Process.Pid {
				continue
			}
			args, argsErr := exec.Command("/bin/ps", "-p", value, "-o", "command=").Output()
			if argsErr == nil &&
				strings.HasPrefix(
					strings.TrimSpace(string(args)),
					filepath.Join(app, "Contents/MacOS/mintclaw-node")+" ",
				) {
				process, _ := os.FindProcess(pid)
				return process
			}
		}
		return nil
	}
	var once bool
	return func() {
		if once {
			return
		}
		once = true
		if process := find(); process != nil {
			_ = process.Signal(os.Interrupt)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error("owned companion app launch failed", err)
			}
		case <-time.After(10 * time.Second):
			if process := find(); process != nil {
				_ = process.Kill()
			}
			_ = command.Process.Kill()
			t.Error("owned companion app did not stop gracefully")
		}
	}
}
