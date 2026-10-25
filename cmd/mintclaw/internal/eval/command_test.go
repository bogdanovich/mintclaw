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
		if result.RuntimeRun || result.Outcome != "valid" || len(result.Cases) != 6 ||
			result.Cases[0].ID != "browser.fixture.followup" || result.Cases[1].ID != "general.context.correction" ||
			result.Cases[2].ID != "general.context.followup" || result.Cases[3].ID != "general.control.stop" ||
			result.Cases[4].ID != "general.interaction.answer" || result.Cases[5].ID != "general.tools.recovery" ||
			strings.Contains(output.String(), "MC_EVAL_CONTEXT") ||
			strings.Contains(output.String(), "use request_user_input to ask exactly") {
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

func TestOfflineCatalogShowsAllDeferredCases(t *testing.T) {
	for _, name := range []string{"list", "validate"} {
		command := NewEvalCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetArgs([]string{name, "--root", "../../../../evals", "--json"})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		var result catalogResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		active, pending := 0, 0
		for _, item := range result.Cases {
			switch item.Status {
			case "active":
				active++
				if !item.Default || item.PendingReason != "" || item.Suite != "general" {
					t.Fatal("active default metadata incorrect")
				}
			case "pending":
				pending++
				core := item.ID == "general.session.isolation" || item.ID == "general.tools.recovery" ||
					item.ID == "general.control.stop" || item.ID == "general.approval.denied"
				if item.Default || item.PendingReason == "" ||
					item.Suite == "general" && !core {
					t.Fatal("pending metadata incorrect")
				}
			default:
				t.Fatal("catalog status unavailable")
			}
		}
		if active != 6 || pending != 16 || result.RuntimeRun {
			t.Fatal("offline inspection hid deferred coverage or claimed execution")
		}
	}
	command := NewEvalCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"list", "--root", "../../../../evals", "--suite", "pdf"})
	if err := command.Execute(); err != nil || strings.Count(output.String(), "pending") != 6 ||
		!strings.Contains(
			output.String(),
			"default=false",
		) || !strings.Contains(output.String(), "Deferred by operator") {
		t.Fatal("human catalog omitted pending status/reason", err)
	}
}

func TestRunDefaultAndExplicitPendingAdmissionBeforePreset(t *testing.T) {
	for _, test := range []struct {
		name    string
		flags   []string
		pending bool
	}{
		{name: "defaults"},
		{name: "opt-in-alone-does-not-widen-defaults", flags: []string{"--include-pending"}},
		{name: "explicit-general", flags: []string{"--suite", "general"}, pending: true},
		{name: "general-opt-in", flags: []string{"--suite", "general", "--include-pending"}},
		{name: "qualified-core", flags: []string{"--case", "general.context.correction"}},
		{name: "core-opt-in", flags: []string{"--case", "general.context.correction", "--include-pending"}},
		{name: "active-id", flags: []string{"--case", "general.context.followup"}},
		{name: "all", flags: []string{"--suite", "all"}, pending: true},
		{name: "pdf", flags: []string{"--suite", "pdf"}, pending: true},
		{name: "browser", flags: []string{"--suite", "browser"}, pending: true},
		{name: "pending-id", flags: []string{"--case", "pdf.loan.fill"}, pending: true},
		{name: "native-stop-pending", flags: []string{"--case", "general.control.stop"}, pending: true},
		{name: "approval-denied-pending", flags: []string{"--case", "general.approval.denied"}, pending: true},
		{name: "mixed-id", flags: []string{"--case", "general.context.followup", "--case", "pdf.loan.fill"}, pending: true},
		{name: "broad-tag", flags: []string{"--tag", "multi-turn"}, pending: true},
		{name: "explicit-opt-in", flags: []string{"--suite", "all", "--include-pending"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := NewEvalCommand()
			var output bytes.Buffer
			command.SetOut(&output)
			command.SetErr(&output)
			args := []string{
				"run", "--root", "../../../../evals", "--preset", "/nonexistent-eval-preset",
				"--out", "/nonexistent-eval-reports", "--json",
			}
			command.SetArgs(append(args, test.flags...))
			err := command.Execute()
			want := manualeval.ErrInvalidPreset
			if test.pending {
				want = manualeval.ErrPendingCases
			}
			if !errors.Is(err, want) || strings.Contains(output.String(), `"verdict":"pass"`) ||
				strings.Contains(output.String(), "MC_EVAL_CONTEXT") {
				t.Fatal("default/pending admission reached wrong boundary", err, want)
			}
		})
	}
}
