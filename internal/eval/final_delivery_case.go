package eval

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

const finalDeliveryCapability = "final_delivery"

type FinalDeliveryCase struct {
	Streaming bool   `json:"streaming"`
	Marker    string `json:"marker"`
}

func (delivery FinalDeliveryCase) validate(item Case) error {
	if item.Suite != "general" || item.PDF != nil || item.Browser != nil || item.Steering != nil ||
		item.ReadRecovery != nil || item.NativeStop != nil || len(item.Fixtures) != 0 ||
		!validID(delivery.Marker) || !strings.Contains(item.Task, delivery.Marker) ||
		len(item.Requires) != 3 || !containsAll(item.Requires,
		[]string{"conversation", boundedCallsCapability, finalDeliveryCapability}) ||
		item.Driver.Mode != "scripted" || len(item.Driver.Facts) != 0 || len(item.Driver.Steps) != 1 ||
		item.Driver.Steps[0].Action.Kind != "message" || item.Driver.Steps[0].WaitFor.Kind != "final" ||
		item.Driver.Steps[0].WaitFor.Contains != "" || item.Limits.MaxUserTurns != 2 ||
		item.Limits.MaxDriverCalls != 1 || item.Limits.TurnTimeout != "2m" || item.Limits.CaseTimeout != "5m" ||
		!requiredRubric(item.Rubric) {
		return ErrInvalidCase
	}
	if strings.Contains(item.Driver.Steps[0].Action.Text, delivery.Marker) {
		return ErrInvalidCase
	}
	native, session, final := false, false, false
	for _, check := range item.Checks {
		native = native || check.Kind == "final_delivery"
		session = session || check.Kind == "same_session"
		final = final || check.Kind == "last_outcome" && check.Expected == "final"
	}
	if !native || !session || !final {
		return ErrInvalidCase
	}
	return nil
}

func hasFinalDelivery(preset Preset) bool {
	return slices.Contains(preset.Capabilities, finalDeliveryCapability)
}

func validateFinalDeliveryPolicy(cfg *config.Config, preset Preset) error {
	if !hasFinalDelivery(preset) {
		return nil
	}
	if cfg == nil || validateFinalDeliveryPreset(preset) != nil ||
		len(cfg.Agents.List) != 1 || cfg.Agents.List[0].ToolPolicy == nil ||
		len(cfg.Agents.List[0].ToolPolicy.Allow) != 0 || len(cfg.Agents.List[0].ToolPolicy.Deny) != 0 ||
		cfg.Agents.Defaults.ContextManager != "seahorse" || cfg.Agents.List[0].Model != nil ||
		len(cfg.Agents.Defaults.ModelFallbacks) != 0 || len(cfg.ModelList) != 1 ||
		cfg.Agents.Defaults.MaxParallelTurns > 1 || cfg.Agents.List[0].MaxParallelTurns > 1 ||
		cfg.Tools.RequestUserInput.Enabled {
		return ErrUnsafeTarget
	}
	return nil
}

func validateFinalDeliveryPreset(preset Preset) error {
	if !hasFinalDelivery(preset) {
		return nil
	}
	if len(preset.Capabilities) != 3 ||
		!containsAll(preset.Capabilities, []string{"conversation", boundedCallsCapability, finalDeliveryCapability}) ||
		preset.TargetCallBudget == nil || !preset.CaptureModelMetadata || preset.ActiveTool != nil ||
		len(preset.WorkspaceFiles) != 0 || preset.UserModel != "" || preset.JudgeModel == "" ||
		preset.ExpectedWorkspace != filepath.Join(preset.IsolationRoot, "workspace") {
		return ErrInvalidPreset
	}
	return nil
}

func validateFinalDeliveryBinding(preset Preset, item Case) error {
	if item.FinalDelivery == nil {
		return nil
	}
	if !hasFinalDelivery(preset) || item.Validate() != nil {
		return ErrUnsafeTarget
	}
	return nil
}

func finalDeliveryStreamingMatches(target admittedTarget, item Case) bool {
	if item.FinalDelivery == nil || validateFinalDeliveryPolicy(target.config, target.preset) != nil {
		return false
	}
	wanted := item.FinalDelivery.Streaming
	model, err := target.config.GetModelConfig(target.config.Agents.Defaults.GetModelName())
	if err != nil || model == nil || model.Streaming.Enabled != wanted || strings.TrimSpace(model.ModelName) == "" {
		return false
	}
	settings, err := gatewayclient.ChannelSettings(target.config)
	return err == nil && settings.Streaming.Enabled == wanted
}
