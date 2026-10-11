package cua

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

type fixtureEvent struct {
	Kind    string   `json:"kind"`
	PID     uint32   `json:"pid"`
	Windows []uint64 `json:"windows"`
	Values  []string `json:"values"`
	Counts  []int    `json:"counts"`
}

// TestNativeQualification is opt-in and creates only the supplied disposable
// fixture. It does not enumerate/capture existing applications or install a
// driver. The native fixture owns two immutable window incarnations for its
// process lifetime, making its liveness guard sufficient for this test only.
func TestNativeQualification(t *testing.T) {
	driverPath := os.Getenv("MINTCLAW_COMPUTER_NATIVE_DRIVER")
	if driverPath == "" {
		t.Skip("native qualification requires a pinned private driver and disposable fixture")
	}
	fixturePath := os.Getenv("MINTCLAW_COMPUTER_NATIVE_FIXTURE")
	if fixturePath == "" {
		t.Fatal("disposable fixture is required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, fixturePath)
	if runtime.GOOS == "linux" {
		command = exec.CommandContext(ctx, "/usr/bin/python3", fixturePath)
	}
	command.Env = []string{"PATH=/usr/bin:/bin", "GSETTINGS_BACKEND=memory", "NO_AT_BRIDGE=0"}
	gui := make(map[string]string)
	for _, key := range []string{"DISPLAY", "XAUTHORITY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		if value := os.Getenv(key); value != "" {
			gui[key] = value
			command.Env = append(command.Env, key+"="+value)
		}
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal("could not start disposable fixture")
	}
	fixtureDone := make(chan struct{})
	go func() { _ = command.Wait(); close(fixtureDone) }()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		select {
		case <-fixtureDone:
		case <-time.After(3 * time.Second):
			t.Error("disposable fixture did not terminate")
		}
	})
	events := make(chan fixtureEvent, 128)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 32*1024)
		for scanner.Scan() {
			var event fixtureEvent
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				select {
				case events <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	waitEvent := func(t *testing.T, match func(fixtureEvent) bool) fixtureEvent {
		t.Helper()
		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		var last fixtureEvent
		for {
			select {
			case event, ok := <-events:
				last = event
				if !ok {
					t.Fatal("fixture exited before native effect verification")
				}
				if match(event) {
					return event
				}
			case <-timer.C:
				t.Fatalf("native effect not observed in disposable fixture: %#v", last)
			case <-ctx.Done():
				t.Fatal("native qualification timed out")
			}
		}
	}
	ready := waitEvent(t, func(event fixtureEvent) bool { return event.Kind == "ready" })
	if ready.PID != uint32(command.Process.Pid) || len(ready.Windows) != 2 || ready.Windows[0] == ready.Windows[1] {
		t.Fatal("fixture target identity is invalid")
	}
	target := computer.Target{
		Alias: "native-fixture", Placement: computer.PlacementGateway, OS: computer.OSLinux,
		Mode: computer.ModeAttached, Isolation: computer.IsolationNone, Backend: "cua",
		Scope: computer.Scope{
			DesktopID: "qualification", DisplayID: "fixture",
			WindowID: strconv.FormatUint(ready.Windows[0], 10), WindowGeneration: 1,
		},
	}
	if runtime.GOOS == "darwin" {
		target.OS = computer.OSMacOS
	}
	alive := func(_ context.Context, scope computer.Scope) bool {
		select {
		case <-fixtureDone:
			return false
		default:
			return scope == target.Scope
		}
	}
	config := Config{
		Executable: driverPath, SHA256: os.Getenv("MINTCLAW_COMPUTER_NATIVE_SHA256"),
		Target: target, PID: ready.PID, Env: gui, ScopeAlive: alive,
	}
	t.Run("ObservationLifecycle", func(t *testing.T) {
		driver, err := New(ctx, config)
		if err != nil {
			t.Fatal("read-only native worker admission:", err)
		}
		t.Cleanup(func() { _ = driver.Close() })
		principal := runtimecap.Principal{
			Runtime:     runtimecap.KindGateway,
			ActorID:     "fixture",
			AgentID:     "fixture",
			SessionID:   "fixture",
			ExecutionID: "fixture",
		}
		host, err := computer.NewReadOnlyHost(computer.ReadOnlyPolicy{
			WorkerID: "native-worker", Revision: "fixture-v1", Targets: []computer.Target{target},
			LeaseTTL: time.Minute, ObservationTTL: 15 * time.Second,
			Admit: func(p runtimecap.Principal, _ computer.Target) bool { return p == principal },
		}, driver, computer.NewArbiter())
		if err != nil {
			t.Fatal(err)
		}
		session, err := host.Open(ctx, computer.OpenRequest{Principal: principal, Target: target})
		if err != nil {
			t.Fatal(err)
		}
		observation, err := host.Observe(ctx, session)
		if err != nil {
			t.Fatal("exact-window native capture:", err)
		}
		t.Logf("native capture verified: %dx%d, scale %g, PNG bytes %d, AX bytes %d",
			observation.Geometry.Width, observation.Geometry.Height, observation.Geometry.Scale,
			len(observation.Screenshot), len(observation.AccessibilityText))
		human, err := host.Handoff(ctx, session.Binding)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = host.Observe(ctx, human); !errors.Is(err, computer.ErrDenied) {
			t.Fatal("human control allowed automated capture")
		}
		pending, err := host.RequestResume(ctx, human.Binding)
		if err != nil {
			t.Fatal(err)
		}
		session, err = host.Resume(ctx, pending.Binding)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = host.Observe(ctx, session); err != nil {
			t.Fatal("fresh native observe after resume:", err)
		}
		if _, err = host.Release(ctx, session.Binding); err != nil {
			t.Fatal(err)
		}
		if !alive(ctx, target.Scope) {
			t.Fatal("release closed disposable application")
		}
		if err = driver.Close(); err != nil || !alive(ctx, target.Scope) {
			t.Fatal("worker close altered application lifetime")
		}
		t.Log("handoff/resume/release and application lifetime verified")
	})

	// Qualification-only worker: input is not exposed by the public adapter.
	// Its manifest admits this exact fixture window and no other window/tool.
	t.Run("Input", func(t *testing.T) {
		tools := []string{"get_window_state", "click", "type_text", "set_value"}
		if runtime.GOOS == "darwin" {
			tools = append(tools, "check_permissions")
		}
		writer, err := newDriver(ctx, config, tools)
		if err != nil {
			t.Fatal("fixture-only input worker:", err)
		}
		t.Cleanup(func() { _ = writer.Close() })
		if runtime.GOOS == "darwin" {
			result, permissionErr := writer.call(ctx, "check_permissions", map[string]any{"prompt": false})
			if permissionErr != nil {
				t.Fatal("read-only native permission check:", permissionErr)
			}
			var permissions struct {
				Accessibility   *bool `json:"accessibility"`
				ScreenRecording *bool `json:"screen_recording"`
			}
			data, _ := json.Marshal(result.StructuredContent)
			if json.Unmarshal(data, &permissions) != nil ||
				permissions.Accessibility == nil || permissions.ScreenRecording == nil {
				t.Fatal("native permission status unavailable")
			}
			t.Logf("native worker TCC preflight: Accessibility=%t, Screen Recording=%t",
				*permissions.Accessibility, *permissions.ScreenRecording)
			if !*permissions.Accessibility {
				t.Fatal("native worker Accessibility permission denied; input is not qualified")
			}
		}
		windowArgs := map[string]any{"pid": ready.PID, "window_id": ready.Windows[0], "include_screenshot": false}
		tokens := func(t *testing.T) (string, string) {
			t.Helper()
			result, snapshotErr := writer.call(ctx, "get_window_state", windowArgs)
			if snapshotErr != nil {
				t.Fatal("fixture input snapshot:", snapshotErr)
			}
			var state struct {
				Elements []struct {
					Label string `json:"label"`
					Token string `json:"element_token"`
				} `json:"elements"`
			}
			data, _ := json.Marshal(result.StructuredContent)
			if json.Unmarshal(data, &state) != nil {
				t.Fatal("fixture element metadata unavailable")
			}
			var field, button string
			for _, element := range state.Elements {
				switch element.Label {
				case "Qualification text":
					field = element.Token
				case "Apply fixture":
					button = element.Token
				}
			}
			if field == "" || button == "" {
				t.Fatalf("native fixture AX text field/button not found: %d actionable elements", len(state.Elements))
			}
			return field, button
		}
		// Qualification keeps insertion and replacement distinct. A backend failing
		// Unicode insertion is not qualified for it just because replacement works.
		const sample = "MintClaw Привет 世界 café 😀"
		t.Run("UnicodeInsertion", func(t *testing.T) {
			field, _ := tokens(t)
			if _, typeErr := writer.call(ctx, "type_text", map[string]any{
				"pid": ready.PID, "window_id": ready.Windows[0], "element_token": field,
				"text": sample, "delivery_mode": "background",
			}); typeErr != nil {
				t.Fatal("fixture Unicode insertion:", typeErr)
			}
			waitEvent(t, func(e fixtureEvent) bool {
				return len(e.Values) == 2 && e.Values[0] == sample && e.Values[1] == "GUARD_UNCHANGED"
			})
		})
		t.Run("UnicodeReplacement", func(t *testing.T) {
			// Distinct from insertion and initial fixture values: a queued snapshot
			// cannot qualify a replacement that returns success without an effect.
			const replacement = "Обновлено MintClaw 世界 naïve 🌿"
			field, _ := tokens(t)
			if _, replaceErr := writer.call(ctx, "set_value", map[string]any{
				"pid": ready.PID, "window_id": ready.Windows[0], "element_token": field,
				"value": replacement, "delivery_mode": "background",
			}); replaceErr != nil {
				t.Fatal("fixture Unicode replacement:", replaceErr)
			}
			waitEvent(t, func(e fixtureEvent) bool {
				return len(e.Values) == 2 && e.Values[0] == replacement && e.Values[1] == "GUARD_UNCHANGED"
			})
		})
		t.Run("ButtonClick", func(t *testing.T) {
			_, button := tokens(t)
			if _, clickErr := writer.call(ctx, "click", map[string]any{
				"target":        map[string]any{"kind": "window", "pid": ready.PID, "window_id": ready.Windows[0]},
				"element_token": button, "delivery_mode": "background",
			}); clickErr != nil {
				t.Fatal("fixture button click:", clickErr)
			}
			waitEvent(t, func(e fixtureEvent) bool {
				return len(e.Counts) == 2 && e.Counts[0] == 1 && e.Counts[1] == 0 &&
					len(e.Values) == 2 && e.Values[1] == "GUARD_UNCHANGED"
			})
		})
		t.Run("ScopeDenial", func(t *testing.T) {
			if _, scopeErr := writer.call(ctx, "get_window_state", map[string]any{
				"pid": ready.PID, "window_id": ready.Windows[1], "include_screenshot": false,
			}); scopeErr == nil {
				t.Fatal("manifest allowed capture of non-admitted guard window")
			}
			if _, scopeErr := writer.call(ctx, "get_window_state", windowArgs); scopeErr != nil {
				t.Fatal("scope-denial check lost the admitted worker")
			}
		})
	})
}
