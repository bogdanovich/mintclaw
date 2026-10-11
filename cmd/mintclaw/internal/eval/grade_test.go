package eval

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestGradeRequiresExplicitEvidenceOrCalibrationAndIsolatedProfile(t *testing.T) {
	t.Setenv(config.EnvHome, filepath.Join(t.TempDir(), "not-prepared"))
	common := []string{"grade", "--preset", "../../../../evals/presets/local.template.json", "--out", t.TempDir()}
	for _, extra := range [][]string{
		nil,
		{"/missing/private-run"},
		{"--calibration", "../../../../evals/calibration/judge-v2.json"},
		{"--calibration", "missing.json", "/extra/source"},
		{"--calibration", "missing.json", "--suite", "all"},
		{"--calibration", "missing.json", "--case", "general.context.followup"},
		{"--calibration", "missing.json", "--root", "evals"},
	} {
		command := NewEvalCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs(append(append([]string{}, common...), extra...))
		if err := command.Execute(); err == nil || strings.Contains(output.String(), `"verdict":"pass"`) ||
			strings.Contains(output.String(), "MC_EVAL_CONTEXT") {
			t.Fatal("unprepared/ambiguous grading succeeded or exposed case facts")
		}
	}
}
