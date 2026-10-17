// Package desktop implements local OS readiness and native window-lifetime
// checks. A report describes this process, not its parent terminal, and grants
// no authority.
package desktop

import (
	"context"
	"errors"
)

const CompanionBundleID = "io.github.bogdanovich.mintclaw.companion"

var ErrUnsupported = errors.New("native desktop capability is unavailable")

// Application is the actual process's main bundle, not a caller-supplied path
// or an inference from process ancestry. ConfigPath is local operator metadata.
type Application struct {
	BundleID   string
	ConfigPath string
}

type Readiness struct {
	OS              string   `json:"os"`
	BundleID        string   `json:"bundle_id,omitempty"`
	ActiveGUI       bool     `json:"active_gui"`
	Accessibility   bool     `json:"accessibility"`
	ScreenRecording bool     `json:"screen_recording"`
	Ready           bool     `json:"ready"`
	Blockers        []string `json:"blockers"`
}

// Inspect only preflights. It never requests permissions, captures the screen,
// enumerates windows, changes settings or launches a driver. Granted native
// permissions are still not a MintClaw policy grant or proof of an unlocked UI.
func Inspect(ctx context.Context) (Readiness, error) {
	if err := ctx.Err(); err != nil {
		return Readiness{}, err
	}
	report, err := inspectNative()
	if err != nil {
		return Readiness{}, err
	}
	if err = ctx.Err(); err != nil {
		return Readiness{}, err
	}
	return evaluate(report), nil
}

func evaluate(report Readiness) Readiness {
	report.Ready = false
	report.Blockers = []string{}
	if report.OS != "darwin" {
		report.Blockers = append(report.Blockers, "unsupported_os")
		return report
	}
	if report.BundleID != CompanionBundleID {
		report.Blockers = append(report.Blockers, "companion_app_launch_required")
	}
	if !report.ActiveGUI {
		report.Blockers = append(report.Blockers, "active_gui_session_required")
	}
	if !report.Accessibility {
		report.Blockers = append(report.Blockers, "accessibility_permission_required")
	}
	if !report.ScreenRecording {
		report.Blockers = append(report.Blockers, "screen_recording_permission_required")
	}
	report.Ready = len(report.Blockers) == 0
	return report
}
