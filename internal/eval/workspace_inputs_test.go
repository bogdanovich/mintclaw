package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

func writeWorkspaceInput(t *testing.T, workspace, name, content string) {
	t.Helper()
	file := filepath.Join(workspace, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceInputsIdentifyContentNamesModesAndAbsenceWithoutPrivatePaths(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	for _, workspace := range []string{first, second} {
		writeWorkspaceInput(t, workspace, "AGENTS.md", "Synthetic instructions.")
		writeWorkspaceInput(t, workspace, "skills/example/SKILL.md", "Synthetic skill.")
		writeWorkspaceInput(t, workspace, "skills/example/scripts/tool.sh", "echo synthetic")
	}
	before := observeWorkspaceInputs(t.Context(), first, nil, nil)
	if !before.valid() || before.Code != "observed" || before != observeWorkspaceInputs(t.Context(), second, nil, nil) {
		t.Fatal("content identity depends on absolute placement or observation order")
	}
	for _, change := range []string{"content", "name", "mode", "empty", "outside_scope"} {
		t.Run(change, func(t *testing.T) {
			if change == "mode" && runtime.GOOS == "windows" {
				t.Skip("Windows does not expose Unix executable permission bits")
			}
			workspace := t.TempDir()
			writeWorkspaceInput(t, workspace, "AGENTS.md", "Synthetic instructions.")
			writeWorkspaceInput(t, workspace, "skills/example/SKILL.md", "Synthetic skill.")
			writeWorkspaceInput(t, workspace, "skills/example/scripts/tool.sh", "echo synthetic")
			switch change {
			case "content":
				writeWorkspaceInput(t, workspace, "skills/example/SKILL.md", "Changed skill.")
			case "name":
				if err := os.Rename(filepath.Join(workspace, "skills/example"),
					filepath.Join(workspace, "skills/renamed")); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(filepath.Join(workspace, "skills/example/scripts/tool.sh"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "empty":
				writeWorkspaceInput(t, workspace, "SOUL.md", "")
			case "outside_scope":
				writeWorkspaceInput(t, workspace, "memory/MEMORY.md", "Not an admitted fingerprint source.")
			}
			after := observeWorkspaceInputs(t.Context(), workspace, nil, nil)
			if !after.valid() || after.Code != "observed" || (before == after) != (change == "outside_scope") {
				t.Fatal("file identity omitted a change or included unadmitted state", change)
			}
		})
	}
	data, _ := json.Marshal(before)
	if strings.Contains(string(data), first) || strings.Contains(string(data), "Synthetic") ||
		strings.Contains(string(data), "SKILL.md") {
		t.Fatal("workspace fingerprint exposed contents or file identities")
	}
}

func TestWorkspaceInputsWithholdWholeSnapshotOnUnsafeOrOverBudgetSource(t *testing.T) {
	for _, change := range []string{
		"symlink", "nested_symlink", "directory", "large", "total_bytes", "entries", "credential", "fact",
	} {
		t.Run(change, func(t *testing.T) {
			if (change == "symlink" || change == "nested_symlink") && runtime.GOOS == "windows" {
				t.Skip("symlink creation requires separately admitted Windows privileges")
			}
			workspace := t.TempDir()
			writeWorkspaceInput(t, workspace, "AGENTS.md", "Synthetic instructions.")
			var cases []LoadedCase
			var filter *strings.Replacer
			switch change {
			case "symlink", "nested_symlink":
				name := "SOUL.md"
				if change == "nested_symlink" {
					name = "skills/linked"
					if err := os.Mkdir(filepath.Join(workspace, "skills"), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(workspace, name)); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(filepath.Join(workspace, "SOUL.md"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "large":
				writeWorkspaceInput(t, workspace, "SOUL.md", strings.Repeat("x", MaxManifestBytes+1))
			case "total_bytes":
				for ordinal := range maxWorkspaceInputBytes/MaxManifestBytes + 1 {
					writeWorkspaceInput(t, workspace, fmt.Sprintf("skills/f-%04d", ordinal),
						strings.Repeat("x", MaxManifestBytes))
				}
			case "entries":
				for ordinal := range maxCorpusEntries {
					writeWorkspaceInput(t, workspace, fmt.Sprintf("skills/f-%04d", ordinal), "")
				}
			case "credential":
				writeWorkspaceInput(t, workspace, "SOUL.md", "private-credential-canary")
				filter = strings.NewReplacer("private-credential-canary", "[redacted]")
			case "fact":
				writeWorkspaceInput(t, workspace, "skills/private-fact-canary/SKILL.md", "Synthetic skill.")
				item := loadedTestCase(t, "general.fingerprint")
				item.Case.Driver.Facts = map[string]string{"private": "private-fact-canary"}
				cases = []LoadedCase{item}
			}
			observed := observeWorkspaceInputs(t.Context(), workspace, cases, filter)
			if observed != (WorkspaceInputSnapshot{Code: "unavailable"}) || !observed.valid() {
				t.Fatal("unsafe source yielded a partial or misleading fingerprint", change)
			}
		})
	}
}

func TestWorkspaceInputsInterruptedObservationNeverReturnsPartialHashes(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if observed := observeWorkspaceInputs(ctx, t.TempDir(), nil, nil); observed.Code != "unavailable" ||
		!observed.valid() {
		t.Fatal("interrupted observer claimed a complete snapshot")
	}
}

func TestPreparedRunRecordsTwoWorkspaceObservationsAndComparisonWarnsOnDrift(t *testing.T) {
	fixture := newAdmissionFixture(t)
	writeWorkspaceInput(t, fixture.preset.ExpectedWorkspace, "AGENTS.md", "Before synthetic run.")
	writeErrors := make(chan error, 4)
	fixture.respond = func(_ protocol.MintClawMessage, _ *protocol.MintClawMessage) {
		writeErrors <- os.WriteFile(filepath.Join(fixture.preset.ExpectedWorkspace, "AGENTS.md"),
			[]byte("After synthetic run."), 0o600)
	}
	result, err := runAdmissionFixture(t, fixture, loadedTestCase(t, "general.fingerprint"))
	for range len(writeErrors) {
		if writeErr := <-writeErrors; writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err != nil || result.WorkspaceInputs == nil || !result.WorkspaceInputs.valid() ||
		result.WorkspaceInputs.Before == result.WorkspaceInputs.After {
		t.Fatal("run failed to retain actual before/after workspace observations", err)
	}
	comparison, err := Compare(result.ReportDir, result.ReportDir)
	if err != nil || !slices.Contains(comparison.EnvironmentUnknowns, "baseline_workspace_inputs_changed") ||
		!slices.Contains(comparison.EnvironmentUnknowns, "instructions_and_skills") ||
		comparison.Attribution != "not_established" {
		t.Fatal("two file observations certified effective runtime inputs or hid in-run drift", err)
	}
}

func TestCompareWorkspaceInputsRetainsLegacyAndUnavailableUnknowns(t *testing.T) {
	for _, change := range []string{"same", "instructions", "skills", "missing", "unavailable", "invalid"} {
		t.Run(change, func(t *testing.T) {
			baseline, current := comparisonFixture(t, Pass), comparisonFixture(t, Pass)
			snapshot := WorkspaceInputSnapshot{
				Code: "observed", InstructionsSHA256: strings.Repeat("a", 64), SkillsSHA256: strings.Repeat("b", 64),
			}
			baseline.WorkspaceInputs = &WorkspaceInputs{workspaceInputScope, snapshot, snapshot}
			current.WorkspaceInputs = &WorkspaceInputs{workspaceInputScope, snapshot, snapshot}
			switch change {
			case "instructions":
				current.WorkspaceInputs.Before.InstructionsSHA256 = strings.Repeat("c", 64)
				current.WorkspaceInputs.After = current.WorkspaceInputs.Before
			case "skills":
				current.WorkspaceInputs.Before.SkillsSHA256 = strings.Repeat("c", 64)
				current.WorkspaceInputs.After = current.WorkspaceInputs.Before
			case "missing":
				current.WorkspaceInputs = nil
			case "unavailable":
				current.WorkspaceInputs.Before = WorkspaceInputSnapshot{Code: "unavailable"}
			case "invalid":
				current.WorkspaceInputs.Before.Code = "invented"
			}
			writeAdmissionJSON(t, filepath.Join(baseline.ReportDir, "run.json"), baseline)
			writeAdmissionJSON(t, filepath.Join(current.ReportDir, "run.json"), current)
			result, err := Compare(baseline.ReportDir, current.ReportDir)
			if change == "invalid" {
				if !errors.Is(err, ErrCompare) {
					t.Fatal("malformed fingerprint metadata was accepted")
				}
				return
			}
			if err != nil || !result.CompatibleCases || result.Attribution != "not_established" ||
				result.EnvironmentCoverage != "recorded_runtime_roles_and_workspace_files_only" ||
				!slices.Contains(result.EnvironmentUnknowns, "instructions_and_skills") {
				t.Fatal("file observations claimed complete runtime input coverage", err)
			}
			if change == "instructions" && !slices.Contains(result.EnvironmentChanges, "workspace_instruction_files") ||
				change == "skills" && !slices.Contains(result.EnvironmentChanges, "workspace_skill_files") ||
				(change == "missing" || change == "unavailable") &&
					!slices.Contains(result.EnvironmentUnknowns, "current_workspace_inputs") ||
				change == "same" && len(result.EnvironmentChanges) != 0 {
				t.Fatal("comparison omitted input drift or uncertainty", result)
			}
		})
	}
}

func TestGradePreservesSourceWorkspaceObservationsWithoutReadingCurrentWorkspace(t *testing.T) {
	fixture, item, source, count := gradeFixture(t, false)
	if source.WorkspaceInputs == nil {
		t.Fatal("source fingerprint missing")
	}
	writeWorkspaceInput(t, fixture.preset.ExpectedWorkspace, "AGENTS.md", "Changed since source run.")
	result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir, RunOptions{
		Home: fixture.home, OutputParent: fixture.output,
	})
	if err != nil || result.WorkspaceInputs == nil || *result.WorkspaceInputs != *source.WorkspaceInputs ||
		count.Load() != 1 {
		t.Fatal("regrade replaced source inputs with current workspace", err)
	}
	source.WorkspaceInputs.Before.Code = "invented"
	writeAdmissionJSON(t, filepath.Join(source.ReportDir, "run.json"), source)
	_, err = Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir, RunOptions{
		Home: fixture.home, OutputParent: fixture.output,
	})
	if !errors.Is(err, ErrGrade) || count.Load() != 1 {
		t.Fatal("invalid fingerprint reached a billable judge")
	}
}
