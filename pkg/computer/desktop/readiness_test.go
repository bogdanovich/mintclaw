package desktop

import (
	"context"
	"errors"
	"testing"
)

func TestReadinessDoesNotInferAuthority(t *testing.T) {
	ready := Readiness{
		OS:              "darwin",
		BundleID:        CompanionBundleID,
		ActiveGUI:       true,
		Accessibility:   true,
		ScreenRecording: true,
	}
	if report := evaluate(ready); !report.Ready || len(report.Blockers) != 0 {
		t.Fatal(report)
	}
	for _, test := range []struct {
		name    string
		change  func(*Readiness)
		blocker string
	}{
		{"SSH or terminal", func(r *Readiness) { r.BundleID = "" }, "companion_app_launch_required"},
		{"different app", func(r *Readiness) { r.BundleID = "herdr" }, "companion_app_launch_required"},
		{"inactive GUI", func(r *Readiness) { r.ActiveGUI = false }, "active_gui_session_required"},
		{"missing AX", func(r *Readiness) { r.Accessibility = false }, "accessibility_permission_required"},
		{"missing capture", func(r *Readiness) { r.ScreenRecording = false }, "screen_recording_permission_required"},
		{"different OS", func(r *Readiness) { r.OS = "linux" }, "unsupported_os"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := ready
			test.change(&r)
			// Caller-authored optimistic status must not survive native evaluation.
			r.Ready = true
			r.Blockers = nil
			got := evaluate(r)
			if got.Ready || len(got.Blockers) != 1 || got.Blockers[0] != test.blocker {
				t.Fatal(got)
			}
		})
	}
}

func TestCanceledInspection(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Inspect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
