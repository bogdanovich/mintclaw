package eval

import (
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

const ownedRestartCapability = "owned_restart"

type RestartCase struct {
	Marker string `json:"marker"`
	Recall string `json:"recall"`
}

func (restart RestartCase) validate(item Case) error {
	if item.Suite != "general" || item.PDF != nil || item.Browser != nil || item.Steering != nil ||
		item.ReadRecovery != nil || item.NativeStop != nil || item.ApprovalDenied != nil || item.FinalDelivery != nil ||
		item.SkillSelection != nil ||
		!validID(restart.Marker) || len(restart.Marker) > 64 || !boundedText(restart.Recall, 1024) ||
		!strings.Contains(item.Task, restart.Marker) || strings.Contains(restart.Recall, restart.Marker) ||
		len(item.Fixtures) != 0 || len(item.Requires) != 3 || !containsAll(item.Requires,
		[]string{"conversation", boundedCallsCapability, ownedRestartCapability}) ||
		item.Driver.Mode != "scripted" || len(item.Driver.Facts) != 0 || len(item.Driver.Steps) != 1 ||
		item.Driver.Steps[0].Action != (Action{Kind: "message", Text: restart.Recall}) ||
		item.Driver.Steps[0].WaitFor.Kind != "final" || item.Driver.Steps[0].WaitFor.Contains != "" ||
		item.Limits.MaxUserTurns != 2 || item.Limits.MaxDriverCalls != 1 ||
		item.Limits.TurnTimeout != "2m" || item.Limits.CaseTimeout != "5m" || !requiredRubric(item.Rubric) {
		return ErrInvalidCase
	}
	if !slices.ContainsFunc(item.Checks, func(check Check) bool { return check.Kind == "owned_restart" }) ||
		!slices.ContainsFunc(item.Checks, func(check Check) bool {
			return check.Kind == "response_contains" && check.Expected == restart.Marker
		}) {
		return ErrInvalidCase
	}
	return nil
}

func hasOwnedRestart(preset Preset) bool {
	return slices.Contains(preset.Capabilities, ownedRestartCapability)
}

func validateRestartPreset(preset Preset) error {
	if !hasOwnedRestart(preset) {
		return nil
	}
	if len(preset.Capabilities) != 3 || !containsAll(preset.Capabilities,
		[]string{"conversation", boundedCallsCapability, ownedRestartCapability}) ||
		preset.TargetCallBudget == nil || !preset.CaptureModelMetadata || preset.JudgeModel == "" ||
		preset.UserModel != "" || preset.ActiveTool != nil || len(preset.WorkspaceFiles) != 0 {
		return ErrInvalidPreset
	}
	return nil
}

func validateRestartPolicy(cfg *config.Config, preset Preset) error {
	if !hasOwnedRestart(preset) {
		return nil
	}
	if cfg == nil || validateRestartPreset(preset) != nil || len(cfg.Agents.List) != 1 ||
		cfg.Agents.List[0].ToolPolicy == nil ||
		len(cfg.Agents.List[0].ToolPolicy.Allow) != 0 || len(cfg.Agents.List[0].ToolPolicy.Deny) != 0 ||
		len(cfg.Agents.List[0].Skills) != 0 || cfg.Agents.List[0].Model != nil ||
		cfg.Agents.Defaults.ContextManager != "seahorse" || len(cfg.Agents.Defaults.ModelFallbacks) != 0 ||
		cfg.Agents.Defaults.MaxParallelTurns != 1 || cfg.Agents.List[0].MaxParallelTurns != 1 ||
		cfg.Tools.RequestUserInput.Enabled || len(cfg.ModelList) != 1 || cfg.ModelList[0] == nil ||
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

func validateRestartBinding(preset Preset, item Case) error {
	if hasOwnedRestart(preset) != (item.Restart != nil) ||
		item.Restart != nil && (item.Validate() != nil || validateRestartPreset(preset) != nil) {
		return ErrUnsafeTarget
	}
	return nil
}
