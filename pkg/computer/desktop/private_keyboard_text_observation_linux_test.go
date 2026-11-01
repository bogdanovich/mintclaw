//go:build linux && mintclaw_private_native_keyboard

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

// This controller-only observation scope is the entire disposable server. It
// does not establish an AT-SPI root/native-window mapping or register input.
func TestPrivateNativeTextObservationQualification(t *testing.T) {
	if os.Getenv("MINTCLAW_PRIVATE_NATIVE_APP_TEST") != "1" {
		t.Skip("explicit disposable application fixture required")
	}
	var fixture struct {
		Window          WindowRequest   `json:"window"`
		ServerPID       int32           `json:"server_pid"`
		Chords          [][]byte        `json:"chords"`
		Artifact        string          `json:"artifact"`
		ExpectedHash    string          `json:"expected_hash"`
		Observer        string          `json:"observer"`
		ObserverSHA     string          `json:"observer_sha"`
		ObserverBinding json.RawMessage `json:"observer_binding"`
		ExpectedText    string          `json:"expected_text"`
		OriginalText    string          `json:"original_text"`
	}
	file, err := os.Open(os.Getenv("MINTCLAW_PRIVATE_NATIVE_APP_CONFIG"))
	if err != nil {
		t.Fatal("private fixture configuration unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 32*1024 {
		t.Fatal("invalid private fixture configuration file")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 32*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&fixture) != nil || decoder.Decode(new(any)) != io.EOF || fixture.ServerPID <= 0 ||
		!filepath.IsAbs(fixture.Artifact) || len(fixture.ExpectedHash) != 64 {
		t.Fatal("invalid private fixture configuration")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	guard, err := WatchWindow(ctx, fixture.Window)
	if err != nil {
		t.Fatal("original application guard refused", err)
	}
	defer guard.Close()
	lease, err := guard.AcquireInputLease(ctx)
	if err != nil {
		t.Fatal("original application lease refused", err)
	}
	defer lease.Close()
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
		t.Fatal("original native resource channel refused", err)
	}
	defer channel.close()
	observerFile, err := openPrivateKeyboardExecutable(fixture.Observer, fixture.ObserverSHA)
	if err != nil {
		t.Fatal("fixture observer identity refused", err)
	}
	defer observerFile.Close()
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindGateway, ActorID: "controller", AgentID: "fixture",
		SessionID: "fixture_thread", ExecutionID: "fixture_run",
	}
	target := computer.Target{
		Alias: "owned_fixture", Placement: computer.PlacementGateway, OS: computer.OSLinux,
		Mode: computer.ModeManaged, Isolation: computer.IsolationNone, Backend: "private_fixture_observer",
		Scope: computer.Scope{DesktopID: "owned_disposable_xvfb", DisplayID: "owned_display"},
	}
	driver := privateNativeTextCaptureFunc(
		func(captureCtx context.Context, requested computer.Target) (computer.Capture, error) {
			if captureCtx.Err() != nil {
				return computer.Capture{}, captureCtx.Err()
			}
			if requested != target {
				return computer.Capture{}, computer.ErrDenied
			}
			if !guard.Alive(captureCtx) || lease.Check(captureCtx) != nil {
				if captureCtx.Err() != nil {
					return computer.Capture{}, captureCtx.Err()
				}
				return computer.Capture{}, computer.ErrStale
			}
			captureCtx, done := context.WithTimeout(captureCtx, 2*time.Second)
			defer done()
			cmd := exec.CommandContext(captureCtx, "/usr/bin/python3", "/proc/self/fd/3", "--pid",
				strconv.FormatUint(uint64(fixture.Window.PID), 10))
			cmd.ExtraFiles = []*os.File{observerFile}
			cmd.Stdin = bytes.NewReader(fixture.ObserverBinding)
			var output privateNativeTextOutput
			cmd.Stdout = &output
			captureErr := cmd.Run()
			data := output.Bytes()
			defer clear(data)
			alive, leaseErr := guard.Alive(captureCtx), lease.Check(captureCtx)
			if captureErr != nil || len(data) > computer.MaxAccessibilityBytes || !alive || leaseErr != nil {
				t.Logf("fixture capture failure: error=%v bytes=%d guard=%v lease_error=%v ctx=%v",
					captureErr, len(data), alive, leaseErr, captureCtx.Err())
				t.Logf("original authority after capture: guard=%v lease_error=%v", guard.Alive(ctx), lease.Check(ctx))
				return computer.Capture{}, computer.ErrUnavailable
			}
			var observed struct {
				Text      string `json:"text"`
				Editable  bool   `json:"editable"`
				Sensitive bool   `json:"sensitive"`
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&observed) != nil || decoder.Decode(new(any)) != io.EOF {
				return computer.Capture{}, computer.ErrUnavailable
			}
			return computer.Capture{
				Scope: target.Scope, Geometry: computer.Geometry{Width: 640, Height: 480, Scale: 1},
				AccessibilityText: string(data),
			}, nil
		},
	)
	host, err := computer.NewReadOnlyHost(computer.ReadOnlyPolicy{
		WorkerID: "fixture_worker", Revision: "fixture_policy", Targets: []computer.Target{target},
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
	defer host.Release(context.Background(), session.Binding)
	original, err := host.ObserveUntil(ctx, session, func(_ context.Context, o computer.Observation) (bool, error) {
		var state struct {
			Text      string
			Editable  bool
			Sensitive bool
		}
		if json.Unmarshal([]byte(o.AccessibilityText), &state) != nil {
			return false, computer.ErrInvalid
		}
		ready := state.Editable && state.Sensitive
		if !ready {
			t.Logf("valid capture not yet ready: editable=%v sensitive=%v", state.Editable, state.Sensitive)
		}
		return state.Text == fixture.OriginalText && ready, nil
	})
	if err != nil {
		t.Fatal("original fixture text unverified", err)
	}
	if fixture.OriginalText == fixture.ExpectedText || original.Validate(session, time.Now()) != nil {
		t.Fatal("fixture would not distinguish a no-input control")
	}
	actionCtx, actionCancel := context.WithTimeout(ctx, 10*time.Second)
	defer actionCancel()
	owner, err := preparePrivateKeyboard(actionCtx, channel, lease, privateKeyboardConfig{
		Window: fixture.Window, Identity: identity, Chords: fixture.Chords, BudgetMS: 10000,
	})
	if err != nil {
		t.Fatal("actual Go preparation failed", err)
	}
	defer owner.close()
	for owner.receipt == nil {
		if owner.pending == "dispatched" {
			observed, observeErr := host.ObserveUntil(actionCtx, session,
				func(_ context.Context, o computer.Observation) (bool, error) {
					var state struct {
						Text      string
						Editable  bool
						Sensitive bool
					}
					if json.Unmarshal([]byte(o.AccessibilityText), &state) != nil {
						return false, computer.ErrInvalid
					}
					ready := state.Editable && state.Sensitive
					if !ready {
						t.Logf("valid post capture not yet ready: editable=%v sensitive=%v text_matches=%v",
							state.Editable, state.Sensitive, state.Text == fixture.ExpectedText)
					}
					return state.Text == fixture.ExpectedText && ready, nil
				})
			if observeErr != nil || observed.ID == original.ID || observed.Validate(session, time.Now()) != nil ||
				!privateApplicationArtifactMatches(fixture.Artifact, fixture.ExpectedHash) {
				_ = owner.stop()
				_, _ = owner.receive()
				t.Fatalf(
					"independent text effect unverified; Finish withheld: observe_error=%v fresh_id=%v valid=%v artifact=%v",
					observeErr,
					observed.ID != original.ID,
					observed.Validate(session, time.Now()) == nil,
					privateApplicationArtifactMatches(fixture.Artifact, fixture.ExpectedHash),
				)
			}
		}
		if err = owner.allow(actionCtx); err != nil {
			t.Fatal("original Go lease/generation refused a native phase", err)
		}
		if _, err = owner.receive(); err != nil {
			t.Fatal("actual Go native phase failed", err)
		}
	}
	if !owner.receipt.Acknowledged || !owner.receipt.InputQuiescent ||
		owner.receipt.Resources != privateKeyboardRetained || !guard.Alive(ctx) || lease.Check(ctx) != nil {
		t.Fatalf(
			"native diagnostic: ack=%v canceled=%v cancelDelivered=%v quiescent=%v resources=%v guard=%v leaseOK=%v",
			owner.receipt.Acknowledged,
			owner.receipt.Canceled,
			owner.receipt.CancellationDelivered,
			owner.receipt.InputQuiescent,
			owner.receipt.Resources,
			guard.Alive(ctx),
			lease.Check(ctx) == nil,
		)
	}
	if owner.close() != nil || channel.close() != nil || !guard.Alive(ctx) || lease.Check(ctx) != nil {
		t.Fatal("worker/action close released the caller's original guard or lease")
	}
	t.Log(
		"actual Go Unicode text effect independently observed; original file/guard/lease preserved; quiescent retained resources",
	)
}

type privateNativeTextCaptureFunc func(context.Context, computer.Target) (computer.Capture, error)

func (f privateNativeTextCaptureFunc) Capture(
	ctx context.Context, target computer.Target,
) (computer.Capture, error) {
	return f(ctx, target)
}

// Limit the subprocess transfer before decoding its protected UI payload.
type privateNativeTextOutput struct{ buffer bytes.Buffer }

func (b *privateNativeTextOutput) Bytes() []byte { return b.buffer.Bytes() }
func (b *privateNativeTextOutput) Len() int      { return b.buffer.Len() }
func (b *privateNativeTextOutput) Write(p []byte) (int, error) {
	if len(p) > computer.MaxAccessibilityBytes-b.Len() {
		return 0, io.ErrShortBuffer
	}
	return b.buffer.Write(p)
}

func TestPrivateKeyboardTextObserverOutputBound(t *testing.T) {
	var b privateNativeTextOutput
	if _, err := b.Write(make([]byte, computer.MaxAccessibilityBytes-1)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte{1, 2}); err == nil || b.Len() != computer.MaxAccessibilityBytes-1 {
		t.Fatal("fragmented oversized observer output was accepted")
	}
	var single privateNativeTextOutput
	if _, err := single.Write(make([]byte, computer.MaxAccessibilityBytes+1)); err == nil || single.Len() != 0 {
		t.Fatal("single oversized observer output was accepted")
	}
	var streamed privateNativeTextOutput
	reader := struct{ io.Reader }{bytes.NewReader(make([]byte, computer.MaxAccessibilityBytes+1))}
	if _, err := io.Copy(&streamed, reader); err == nil || streamed.Len() > computer.MaxAccessibilityBytes {
		t.Fatal("stream copying bypassed the observer transfer bound")
	}
}
