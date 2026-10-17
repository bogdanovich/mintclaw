//go:build darwin

package desktop

import "testing"

// This test only preflights the current test process. It does not request a
// grant or claim to qualify screen capture/input when permissions are denied.
func TestNativeReadinessPreflight(t *testing.T) {
	app, err := CurrentApplication()
	if err != nil {
		t.Fatal(err)
	}
	report, err := Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.OS != "darwin" || report.BundleID != app.BundleID || report.Ready != (len(report.Blockers) == 0) {
		t.Fatalf("inconsistent native preflight: %+v", report)
	}
}
