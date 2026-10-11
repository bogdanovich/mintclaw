package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	manualeval "github.com/bogdanovich/mintclaw/internal/eval"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestOfflineCommands(t *testing.T) {
	for _, name := range []string{"list", "validate"} {
		command := NewEvalCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetArgs([]string{name, "--root", "../../../../evals", "--json", "--tag", "multi-turn"})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		var result catalogResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.RuntimeRun || result.Outcome != "valid" || len(result.Cases) != 2 ||
			result.Cases[0].ID != "browser.fixture.followup" || result.Cases[1].ID != "general.context.followup" ||
			strings.Contains(output.String(), "MC_EVAL_CONTEXT") {
			t.Fatal("offline command leaked task contents or claimed a runtime run")
		}
	}
}

func TestOfflinePresetAndSelection(t *testing.T) {
	command := NewEvalCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{
		"validate",
		"--root",
		"../../../../evals",
		"--preset",
		"../../../../evals/presets/local.template.json",
		"--json",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var result catalogResult
	if err := json.Unmarshal(
		output.Bytes(),
		&result,
	); err != nil || result.PresetID != "local-eval" ||
		result.RuntimeRun {
		t.Fatal("preset validation did not remain offline", err)
	}
	command = NewEvalCommand()
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"list", "--root", "../../../../evals", "--case", "general.unknown"})
	if err := command.Execute(); !errors.Is(err, manualeval.ErrInvalidFilter) {
		t.Fatal("unknown case ignored", err)
	}
}

func TestRunRequiresExplicitPreparedTargetAndNeverEchoesCase(t *testing.T) {
	t.Setenv(config.EnvHome, filepath.Join(t.TempDir(), "not-prepared"))
	for _, args := range [][]string{
		{"run", "--root", "../../../../evals"},
		{"run", "--root", "../../../../evals", "--preset", "../../../../evals/presets/local.template.json"},
		{
			"run", "--root", "../../../../evals", "--preset", "../../../../evals/presets/local.template.json",
			"--out", t.TempDir(), "--json",
		},
	} {
		command := NewEvalCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs(args)
		err := command.Execute()
		if err == nil || strings.Contains(output.String(), "MC_EVAL_CONTEXT") ||
			strings.Contains(output.String(), `"verdict":"pass"`) {
			t.Fatal("unprepared target ran or failed CLI exposed task/success")
		}
	}
}

func TestRunRejectsInvalidRepeatBeforeLoadingCorpusOrProfile(t *testing.T) {
	for _, repeat := range []string{"0", "-1", "65"} {
		command := NewEvalCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs([]string{
			"run", "--repeat", repeat, "--root", "/nonexistent-private-corpus",
			"--preset", "/nonexistent-private-preset", "--out", "/nonexistent-private-report",
		})
		if err := command.Execute(); !errors.Is(err, manualeval.ErrRunOptions) ||
			strings.Contains(output.String(), `"verdict":"pass"`) {
			t.Fatal("invalid explicit repetition count reached profile/corpus admission")
		}
	}
}
