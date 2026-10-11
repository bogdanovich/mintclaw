package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	manualeval "github.com/bogdanovich/mintclaw/internal/eval"
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
		if result.RuntimeRun || result.Outcome != "valid" || len(result.Cases) != 1 ||
			result.Cases[0].ID != "general.context.followup" ||
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
