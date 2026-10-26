package eval

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/events"
)

const skillSelectionCapability = "skill_selection"

type SkillSelectionCase struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Output string `json:"output"`
}

func (selection SkillSelectionCase) path() string {
	return "skills/" + selection.Name + "/SKILL.md"
}

func (selection SkillSelectionCase) validate(item Case) error {
	if item.Suite != "general" || item.PDF != nil || item.Browser != nil || item.Steering != nil ||
		item.ReadRecovery != nil || item.NativeStop != nil || item.ApprovalDenied != nil || item.FinalDelivery != nil ||
		!validID(selection.Name) || !validID(selection.Value) || len(selection.Value) > 64 ||
		!boundedText(selection.Output, 128) || selection.Output == selection.Value ||
		len(item.Task) > 8192 || strings.Join(strings.Fields(item.Task), " ") != item.Task ||
		!strings.Contains(item.Task, selection.Value) || strings.Contains(item.Task, selection.Output) ||
		len(item.Fixtures) != 1 || item.Fixtures[0].Kind != "file" ||
		len(item.Requires) != 3 || !containsAll(item.Requires,
		[]string{"conversation", boundedCallsCapability, skillSelectionCapability}) ||
		item.Driver.Mode != "scripted" || len(item.Driver.Facts) != 0 || len(item.Driver.Steps) != 2 ||
		item.Limits.MaxUserTurns != 3 || item.Limits.MaxDriverCalls != 1 ||
		item.Limits.TurnTimeout != "2m" || item.Limits.CaseTimeout != "5m" || !requiredRubric(item.Rubric) {
		return ErrInvalidCase
	}
	for index, step := range item.Driver.Steps {
		wanted := item.Task
		if index == 0 {
			wanted = "/use " + selection.Name + " " + item.Task
		}
		if step.Action.Kind != "message" || step.Action.Text != wanted ||
			step.WaitFor.Kind != "final" || step.WaitFor.Contains != "" {
			return ErrInvalidCase
		}
	}
	native, session, final, control := false, false, false, false
	for _, check := range item.Checks {
		native = native || check.Kind == "skill_selection"
		session = session || check.Kind == "same_session"
		final = final || check.Kind == "last_outcome" && check.Expected == "final"
		control = control || check.Kind == "response_contains" && check.Expected == selection.Value
	}
	if !native || !session || !final || !control {
		return ErrInvalidCase
	}
	return nil
}

func hasSkillSelection(preset Preset) bool {
	return slices.Contains(preset.Capabilities, skillSelectionCapability)
}

func validateSkillSelectionPreset(preset Preset) error {
	if !hasSkillSelection(preset) {
		return nil
	}
	if len(preset.Capabilities) != 3 || !containsAll(preset.Capabilities,
		[]string{"conversation", boundedCallsCapability, skillSelectionCapability}) ||
		preset.TargetCallBudget == nil || !preset.CaptureModelMetadata || preset.ActiveTool != nil ||
		preset.UserModel != "" || preset.JudgeModel == "" || len(preset.WorkspaceFiles) != 1 ||
		!strings.HasPrefix(preset.WorkspaceFiles[0].Path, "skills/") ||
		!strings.HasSuffix(preset.WorkspaceFiles[0].Path, "/SKILL.md") ||
		strings.Count(preset.WorkspaceFiles[0].Path, "/") != 2 ||
		!safeRelativePath(preset.WorkspaceFiles[0].Path) || !validDigest(preset.WorkspaceFiles[0].SHA256) ||
		preset.ExpectedWorkspace != filepath.Join(preset.IsolationRoot, "workspace") {
		return ErrInvalidPreset
	}
	return nil
}

func skillSelectionLogging() config.EventLoggingConfig {
	return config.EventLoggingConfig{Enabled: true, MinSeverity: "info", Include: []string{
		events.KindAgentTurnEnd.String(),
	}}
}

func validateSkillSelectionPolicy(cfg *config.Config, preset Preset) error {
	if !hasSkillSelection(preset) {
		return nil
	}
	if cfg == nil || validateSkillSelectionPreset(preset) != nil || len(cfg.Agents.List) != 1 ||
		cfg.Agents.List[0].ToolPolicy == nil ||
		cfg.Agents.List[0].ToolPolicy.Default != config.AgentCapabilityDefaultDeny ||
		len(cfg.Agents.List[0].ToolPolicy.Allow) != 0 ||
		len(cfg.Agents.List[0].ToolPolicy.Deny) != 0 || len(cfg.Agents.List[0].Skills) != 0 ||
		cfg.Agents.Defaults.ContextManager != "seahorse" || cfg.Agents.List[0].Model != nil ||
		len(cfg.Agents.Defaults.ModelFallbacks) != 0 || len(cfg.ModelList) != 1 ||
		cfg.Agents.Defaults.MaxParallelTurns != 1 || cfg.Agents.List[0].MaxParallelTurns != 1 ||
		cfg.Tools.RequestUserInput.Enabled || cfg.Gateway.LogLevel != "info" ||
		!reflect.DeepEqual(cfg.Events.Logging, skillSelectionLogging()) || cfg.ModelList[0] == nil ||
		cfg.ModelList[0].Streaming.Enabled {
		return ErrUnsafeTarget
	}
	channel := cfg.Channels.GetByType(config.ChannelMintClaw)
	if channel == nil {
		return ErrUnsafeTarget
	}
	decoded, err := channel.GetDecoded()
	settings, ok := decoded.(*config.MintClawSettings)
	if err != nil || !ok || settings == nil || settings.Streaming.Enabled {
		return ErrUnsafeTarget
	}
	return nil
}

func validateSkillSelectionBinding(preset Preset, item Case) error {
	if hasSkillSelection(preset) != (item.SkillSelection != nil) {
		return ErrUnsafeTarget
	}
	if item.SkillSelection != nil && (item.Validate() != nil || validateSkillSelectionPreset(preset) != nil ||
		preset.WorkspaceFiles[0] != (WorkspaceFile{Path: item.SkillSelection.path(), SHA256: item.Fixtures[0].SHA256})) {
		return ErrUnsafeTarget
	}
	return nil
}
