package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/evalfixture"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func TestApprovalDeniedCaseRequiresGenuineScopedDenial(t *testing.T) {
	for _, kind := range []string{
		"positive", "question", "cancel", "grant", "missing_hard_check", "unbounded",
		"model_user", "no_judge", "facts", "no_native", "no_fixture", "wrong_fixture",
	} {
		t.Run(kind, func(t *testing.T) {
			item := approvalDeniedDefinition(t).Case
			switch kind {
			case "question":
				item.Driver.Steps[0].WaitFor.Kind = "question"
			case "cancel":
				item.Driver.Steps[0].Action.Kind = "cancel"
			case "grant":
				item.Driver.Steps[0].Action.Kind = "allow_once"
			case "missing_hard_check":
				item.Checks = item.Checks[1:]
			case "unbounded":
				item.Limits.MaxUserTurns++
			case "model_user":
				item.Driver = Driver{Mode: "llm_user", Persona: "Deny the operation"}
			case "no_judge":
				item.Rubric = nil
			case "facts":
				item.Driver.Facts = map[string]string{"hidden": "private"}
			case "no_native":
				item.ApprovalDenied = nil
			case "no_fixture":
				item.Fixtures = nil
			case "wrong_fixture":
				item.Fixtures[0].SHA256 = digest([]byte("other"))
			}
			if (item.Validate() == nil) != (kind == "positive") {
				t.Fatal("invalid approval case admission")
			}
		})
	}
}

func TestApprovalDeniedArgumentWitnessIsNativeReadOnlyAndExact(t *testing.T) {
	workspace := t.TempDir()
	if _, err := observeApprovalArgumentBinding(workspace, digest([]byte("not-an-argument-hash"))); err == nil {
		t.Fatal("missing native key was admitted")
	}
	if _, err := os.Lstat(filepath.Join(workspace, "state")); !os.IsNotExist(err) {
		t.Fatal("passive verifier created native state")
	}
	args := map[string]any{"path": evalfixture.ApprovalPath, "content": evalfixture.ApprovalAfter, "overwrite": true}
	hash, err := interactions.HashArguments(workspace, args)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(workspace, "state", "interaction_hmac.key")
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(before)
	observed, err := observeApprovalArgumentBinding(workspace, hash)
	if err != nil || observed != digest(before) {
		t.Fatal("exact native HMAC was not observed", err)
	}
	args["path"] = "foreign.txt"
	foreign, err := interactions.HashArguments(workspace, args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := observeApprovalArgumentBinding(workspace, foreign); err == nil {
		t.Fatal("foreign arguments admitted")
	}
	after, err := os.ReadFile(keyPath)
	if err != nil || digest(after) != digest(before) {
		t.Fatal("verifier changed native key", err)
	}
	clear(after)
}

func TestApprovalDeniedProfileCannotWidenToolsHooksOrBypass(t *testing.T) {
	for _, kind := range []string{
		"positive", "no_pin", "wrong_pin", "no_budget", "no_metadata", "no_judge",
		"model_user", "read_capability", "no_hooks", "extra_hook", "process_hook", "custom_hook", "no_tool",
		"extra_tool", "allow_default", "bypass", "outside_write", "question_tool", "parallel", "quiet_log", "missing_history",
	} {
		t.Run(kind, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			enableTestApprovalTool(t, fixture)
			switch kind {
			case "no_pin":
				fixture.preset.WorkspaceFiles = nil
			case "wrong_pin":
				fixture.preset.WorkspaceFiles[0].SHA256 = digest([]byte("other"))
			case "no_budget":
				fixture.preset.TargetCallBudget = nil
			case "no_metadata":
				fixture.preset.CaptureModelMetadata = false
			case "no_judge":
				fixture.preset.JudgeModel = ""
			case "model_user":
				fixture.preset.UserModel = "eval-model"
			case "read_capability":
				fixture.preset.Capabilities = append(fixture.preset.Capabilities, readToolCapability)
			case "no_hooks":
				fixture.cfg.Hooks.Enabled = false
			case "extra_hook":
				fixture.cfg.Hooks.Builtins["foreign"] = config.BuiltinHookConfig{Enabled: true}
			case "process_hook":
				fixture.cfg.Hooks.Processes = map[string]config.ProcessHookConfig{"foreign": {Enabled: true}}
			case "custom_hook":
				fixture.cfg.Hooks.Builtins[evalfixture.ApprovalHookName] = config.BuiltinHookConfig{
					Enabled: true, Config: []byte(`{"tool":"exec"}`),
				}
			case "no_tool":
				fixture.cfg.Agents.List[0].ToolPolicy.Allow = nil
			case "extra_tool":
				fixture.cfg.Agents.List[0].ToolPolicy.Allow = append(
					fixture.cfg.Agents.List[0].ToolPolicy.Allow,
					"exec",
				)
			case "allow_default":
				fixture.cfg.Agents.List[0].ToolPolicy.Default = config.AgentCapabilityDefaultAllow
			case "bypass":
				fixture.cfg.Tools.Approval.Mode = config.ToolApprovalModeAllowAll
			case "outside_write":
				fixture.cfg.Tools.AllowWritePaths = []string{".*"}
			case "question_tool":
				fixture.cfg.Tools.RequestUserInput.Enabled = true
			case "parallel":
				fixture.cfg.Agents.List[0].MaxParallelTurns = 2
			case "quiet_log":
				fixture.cfg.Gateway.LogLevel = "warn"
			case "missing_history":
				fixture.cfg.Agents.Defaults.ContextManager = "none"
			}
			err := fixture.preset.Validate()
			if err == nil {
				err = validateConversationPolicy(fixture.cfg, fixture.preset, fixture.workspace)
			}
			if (err == nil) != (kind == "positive") || fixture.tasks.Load() != 0 || fixture.probes.Load() != 0 {
				t.Fatal("approval admission was not pre-work and narrow", err)
			}
		})
	}
}

func TestApprovalDeniedFixtureRejectsMissingChangedAndExtraFiles(t *testing.T) {
	for _, kind := range []string{"positive", "missing", "changed", "extra", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			enableTestApprovalTool(t, fixture)
			path := filepath.Join(fixture.workspace, evalfixture.ApprovalPath)
			var err error
			switch kind {
			case "missing":
				err = os.Remove(path)
			case "changed":
				err = os.WriteFile(path, []byte(evalfixture.ApprovalAfter), 0o600)
			case "extra":
				err = os.WriteFile(filepath.Join(filepath.Dir(path), "other.txt"), []byte("other"), 0o600)
			case "symlink":
				if err = os.Remove(path); err == nil {
					err = os.Symlink(filepath.Join(t.TempDir(), "missing"), path)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if validApprovalToolWorkspace(t.Context(), fixture.preset, fixture.workspace) != (kind == "positive") {
				t.Fatal("effect census accepted unknown or changed target")
			}
		})
	}
}
