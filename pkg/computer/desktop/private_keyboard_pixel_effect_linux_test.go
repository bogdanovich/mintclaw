//go:build linux && mintclaw_private_native_keyboard

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/computer/cua"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

type privateNativeEffectState struct {
	Text      string `json:"text"`
	Editable  bool   `json:"editable"`
	Sensitive bool   `json:"sensitive"`
}

// A short, read-only business probe can remain unverified while a GUI is busy.
// This is separate from native capture failure, which remains terminal. An
// unavailable probe never permits new input or retries an uncertain action.
func privateNativeEffectProbe(
	ctx context.Context, file *os.File, binding json.RawMessage, pid uint32, budget time.Duration,
) (privateNativeEffectState, bool, error) {
	var state privateNativeEffectState
	if err := ctx.Err(); err != nil {
		return state, false, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "/usr/bin/python3", "/proc/self/fd/3", "--pid",
		strconv.FormatUint(uint64(pid), 10))
	cmd.ExtraFiles = []*os.File{file}
	cmd.Stdin = bytes.NewReader(binding)
	var output privateNativeTextOutput
	cmd.Stdout = &output
	err := cmd.Run()
	data := output.Bytes()
	defer clear(data)
	if ctx.Err() != nil {
		return state, false, ctx.Err()
	}
	if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		return state, false, nil // No positive evidence yet, not a fabricated snapshot.
	}
	if err != nil || len(data) > computer.MaxAccessibilityBytes {
		return state, false, computer.ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || decoder.Decode(new(any)) != io.EOF {
		return state, false, computer.ErrInvalid
	}
	return state, true, nil
}

func TestPrivateNativeEffectProbe(t *testing.T) {
	for _, test := range []struct {
		name, script string
		available    bool
		errorIs      error
	}{
		{"valid", `print('{"text":"verified","editable":true,"sensitive":true}')`, true, nil},
		{"timeout", "import time; time.sleep(2)", false, nil},
		{"failed", "raise SystemExit(1)", false, computer.ErrUnavailable},
		{"invalid", "print('not-json')", false, computer.ErrInvalid},
		{"unknown-field", `print('{"extra":true}')`, false, computer.ErrInvalid},
		{"trailing-value", "print('{} {}')", false, computer.ErrInvalid},
		{"oversized", "print('x' * 300000)", false, computer.ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "probe.py")
			if err := os.WriteFile(path, []byte(test.script), 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			state, available, err := privateNativeEffectProbe(t.Context(), file, nil, 1, 200*time.Millisecond)
			if available != test.available || !errors.Is(err, test.errorIs) || t.Context().Err() != nil {
				t.Fatalf("availability=%v error=%v parent=%v", available, err, t.Context().Err())
			}
			if available && state.Text != "verified" {
				t.Fatal("wrong verified state")
			}
		})
	}
	t.Run("parent-cancellation", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "probe.py")
		if err := os.WriteFile(path, []byte("import time; time.sleep(2)"), 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		defer cancel()
		_, available, err := privateNativeEffectProbe(ctx, file, nil, 1, time.Second)
		if available || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("availability=%v error=%v", available, err)
		}
	})
}

// The candidate is opt-in test composition only: one original guard/lease and
// retained input channel, real Cua window pixels, and independent effect probes.
// It registers no production host, model command or runtime profile.
func TestPrivateNativePixelEffectQualification(t *testing.T) {
	if os.Getenv("MINTCLAW_PRIVATE_NATIVE_PIXEL_EFFECT_TEST") != "1" {
		t.Skip("explicit disposable native pixel/effect fixture required")
	}
	var fixture struct {
		Window       WindowRequest            `json:"window"`
		ServerPID    int32                    `json:"server_pid"`
		Batches      []privateNativeTextBatch `json:"batches"`
		Artifact     string                   `json:"artifact"`
		ExpectedHash string                   `json:"expected_hash"`
		Observer     string                   `json:"observer"`
		ObserverSHA  string                   `json:"observer_sha"`
		Binding      json.RawMessage          `json:"observer_binding"`
		OriginalText string                   `json:"original_text"`
		ExpectedText string                   `json:"expected_text"`
		Capture      string                   `json:"capture_executable"`
		CaptureSHA   string                   `json:"capture_sha256"`
	}
	configPath := os.Getenv("MINTCLAW_PRIVATE_NATIVE_APP_CONFIG")
	file, err := os.Open(configPath)
	if err != nil {
		t.Fatal("private fixture configuration unavailable")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 32*1024 {
		t.Fatal("invalid private fixture configuration file")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 32*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&fixture) != nil || decoder.Decode(new(any)) != io.EOF || fixture.ServerPID <= 0 ||
		!filepath.IsAbs(
			fixture.Artifact,
		) || len(fixture.ExpectedHash) != 64 || fixture.OriginalText == fixture.ExpectedText ||
		len(fixture.Batches) == 0 || len(fixture.Batches) > 16 ||
		fixture.Batches[len(fixture.Batches)-1].ExpectedText != fixture.ExpectedText {
		t.Fatal("invalid private pixel/effect fixture")
	}
	total, previous := 0, fixture.OriginalText
	for _, batch := range fixture.Batches {
		total += len(batch.Chords)
		if len(batch.Chords) == 0 || len(batch.Chords) > 32 || total > 256 ||
			len(batch.ExpectedText) > computer.MaxAccessibilityBytes || batch.ExpectedText == previous {
			t.Fatal("invalid preplanned pixel/effect action")
		}
		previous = batch.ExpectedText
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	guard, err := WatchWindow(ctx, fixture.Window)
	if err != nil {
		t.Fatal("original native window refused", err)
	}
	defer func() { _ = guard.Close() }()
	lease, err := guard.AcquireInputLease(ctx)
	if err != nil {
		t.Fatal("original native lease refused", err)
	}
	defer func() { _ = lease.Close() }()
	identity := guard.Identity()
	bootstrap, _, err := privateKeyboardAuthority(lease, fixture.Window, identity)
	clear(bootstrap.Auth[:])
	if err != nil || bootstrap.Server.PID != fixture.ServerPID || bootstrap.Server.UID != uint32(os.Getuid()) {
		t.Fatal("not the original owned fixture server", err)
	}
	channel, err := openPrivateKeyboardChannel(ctx, lease, fixture.Window, identity, privateKeyboardChannelConfig{
		Executable: os.Getenv("MINTCLAW_PRIVATE_NATIVE_HOST_WORKER"),
		SHA256:     os.Getenv("MINTCLAW_PRIVATE_NATIVE_HOST_WORKER_SHA256"), BudgetMS: 30000,
	})
	if err != nil {
		t.Fatal("original native channel refused", err)
	}
	defer func() { _ = channel.close() }()
	observerFile, err := openPrivateKeyboardExecutable(fixture.Observer, fixture.ObserverSHA)
	if err != nil {
		t.Fatal("effect observer identity refused", err)
	}
	defer func() { _ = observerFile.Close() }()
	target := computer.Target{
		Alias: "owned_fixture", Placement: computer.PlacementGateway, OS: computer.OSLinux,
		Mode: computer.ModeManaged, Isolation: computer.IsolationNone, Backend: "cua",
		Scope: computer.Scope{
			DesktopID: "owned_disposable_xvfb", DisplayID: "owned_display",
			WindowID: strconv.FormatUint(uint64(fixture.Window.WindowID), 10), WindowGeneration: identity.Generation,
		},
	}
	driver, err := cua.New(ctx, cua.Config{
		Executable: fixture.Capture, SHA256: fixture.CaptureSHA, Target: target, PID: fixture.Window.PID,
		Env: map[string]string{
			"DISPLAY": fixture.Window.Display, "XAUTHORITY": fixture.Window.XAuthority,
			"DBUS_SESSION_BUS_ADDRESS": "unix:path=" + filepath.Join(filepath.Dir(configPath), "no-capture-bus"),
		},
		ScopeAlive: func(checkCtx context.Context, scope computer.Scope) bool {
			return scope == target.Scope && guard.Alive(checkCtx) && lease.Check(checkCtx) == nil
		},
	})
	if err != nil {
		t.Fatal("pinned native window capture refused", err)
	}
	defer func() { _ = driver.Close() }()
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindGateway, ActorID: "controller", AgentID: "fixture",
		SessionID: "fixture_thread", ExecutionID: "fixture_run",
	}
	host, err := computer.NewReadOnlyHost(computer.ReadOnlyPolicy{
		WorkerID: "fixture_worker", Revision: "pixel_effect_v1", Targets: []computer.Target{target},
		LeaseTTL: 30 * time.Second, ObservationTTL: time.Second,
		Admit: func(p runtimecap.Principal, requested computer.Target) bool {
			return p == principal && requested == target
		},
	}, driver, computer.NewArbiter())
	if err != nil {
		t.Fatal(err)
	}
	session, err := host.Open(ctx, computer.OpenRequest{Principal: principal, Target: target})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = host.Release(context.Background(), session.Binding) }()
	probeTimeouts, pixelCaptures := 0, 0
	condition := func(want string) computer.ObservationCondition {
		return func(checkCtx context.Context, observation computer.Observation) (bool, error) {
			if len(observation.Screenshot) == 0 || observation.AccessibilityText != "" {
				return false, computer.ErrInvalid
			}
			pixelCaptures++
			budget := min(400*time.Millisecond, time.Until(observation.ExpiresAt)-150*time.Millisecond)
			if budget <= 0 {
				return false, nil
			}
			state, available, probeErr := privateNativeEffectProbe(
				checkCtx, observerFile, fixture.Binding, fixture.Window.PID, budget,
			)
			if !available && probeErr == nil {
				probeTimeouts++
			}
			return available && state.Text == want && state.Editable && state.Sensitive, probeErr
		}
	}
	original, err := host.ObserveUntil(ctx, session, condition(fixture.OriginalText))
	if err != nil {
		t.Fatal("original native pixels/effect unverified", err)
	}
	for index, batch := range fixture.Batches {
		if original.Validate(session, time.Now()) != nil {
			t.Fatal("original pixel observation expired")
		}
		func() {
			actionCtx, done := context.WithTimeout(ctx, 10*time.Second)
			defer done()
			owner, prepareErr := preparePrivateKeyboard(actionCtx, channel, lease, privateKeyboardConfig{
				Window: fixture.Window, Identity: identity, Chords: batch.Chords, BudgetMS: 10000,
			})
			if prepareErr != nil {
				t.Fatal("actual Go preparation failed", prepareErr)
			}
			defer func() { _ = owner.close() }()
			for owner.receipt == nil {
				if owner.pending == "dispatched" {
					observed, observeErr := host.ObserveUntil(actionCtx, session, condition(batch.ExpectedText))
					if observeErr != nil || observed.ID == original.ID ||
						observed.Validate(session, time.Now()) != nil ||
						!privateApplicationArtifactMatches(fixture.Artifact, fixture.ExpectedHash) {
						_ = owner.stop()
						_, _ = owner.receive()
						t.Fatalf("effect unverified; Finish withheld: segment=%d error=%v", index, observeErr)
					}
					original = observed
				}
				if owner.allow(actionCtx) != nil {
					t.Fatal("original authority refused a native phase")
				}
				if _, receiveErr := owner.receive(); receiveErr != nil {
					t.Fatal("native phase failed", receiveErr)
				}
			}
			if !owner.receipt.Acknowledged || !owner.receipt.InputQuiescent ||
				owner.receipt.Resources != privateKeyboardRetained || owner.close() != nil {
				t.Fatal("native effect/retention receipts disagree")
			}
		}()
		t.Logf("native pixels and independent effect %d/%d verified", index+1, len(fixture.Batches))
	}
	if channel.close() != nil || !guard.Alive(ctx) || lease.Check(ctx) != nil {
		t.Fatal("channel close released original caller ownership")
	}
	t.Logf("actual Go native pixels/effect verified; captures=%d unavailable_probes=%d; quiescent retained resources",
		pixelCaptures, probeTimeouts)
}
