package eval

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/events"
)

const compactionCapability = "compaction"

type CompactionProfile struct {
	HistoryMaxTokens   int `json:"historyMaxTokens"`
	SummaryMaxTokens   int `json:"summaryMaxTokens"`
	FreshTailMaxTokens int `json:"freshTailMaxTokens"`
	RecentTailTurns    int `json:"recentTailTurns"`
}

func (profile CompactionProfile) valid() bool {
	return profile.HistoryMaxTokens >= 128 && profile.HistoryMaxTokens <= 2048 &&
		profile.SummaryMaxTokens >= 128 && profile.SummaryMaxTokens <= 2048 &&
		profile.FreshTailMaxTokens == profile.HistoryMaxTokens && profile.RecentTailTurns == 0
}

type CompactionCase struct {
	OldMarker  string `json:"old_marker"`
	Marker     string `json:"marker"`
	Correction string `json:"correction"`
	Filler     string `json:"filler"`
	Recall     string `json:"recall"`
}

func (compaction CompactionCase) validate(item Case) error {
	if item.Suite != "general" || item.PDF != nil || item.Browser != nil || item.Steering != nil ||
		item.ReadRecovery != nil || item.NativeStop != nil || item.ApprovalDenied != nil || item.FinalDelivery != nil ||
		item.SkillSelection != nil || item.Restart != nil || !validID(compaction.OldMarker) ||
		!validID(compaction.Marker) || compaction.OldMarker == compaction.Marker ||
		len(compaction.OldMarker) > 64 || len(compaction.Marker) > 64 ||
		!strings.Contains(item.Task, compaction.OldMarker) || strings.Contains(item.Task, compaction.Marker) ||
		!boundedText(compaction.Correction, 2048) || !strings.Contains(compaction.Correction, compaction.Marker) ||
		strings.Contains(compaction.Correction, compaction.OldMarker) || !boundedText(compaction.Filler, 2048) ||
		!boundedText(compaction.Recall, 1024) || len(item.Fixtures) != 0 || len(item.Requires) != 3 ||
		!containsAll(item.Requires, []string{"conversation", boundedCallsCapability, compactionCapability}) ||
		item.Driver.Mode != "scripted" || len(item.Driver.Facts) != 0 || len(item.Driver.Steps) != 4 ||
		item.Limits.MaxUserTurns != 5 || item.Limits.MaxDriverCalls != 1 ||
		item.Limits.TurnTimeout != "2m" || item.Limits.CaseTimeout != "5m" || !requiredRubric(item.Rubric) {
		return ErrInvalidCase
	}
	for _, text := range []string{compaction.Filler, compaction.Recall} {
		if strings.Contains(text, compaction.Marker) || strings.Contains(text, compaction.OldMarker) {
			return ErrInvalidCase
		}
	}
	for index, text := range []string{compaction.Correction, compaction.Filler, compaction.Filler, compaction.Recall} {
		step := item.Driver.Steps[index]
		if step.Action != (Action{Kind: "message", Text: text}) || step.WaitFor != (Expectation{Kind: "final"}) {
			return ErrInvalidCase
		}
	}
	if !slices.ContainsFunc(item.Checks, func(check Check) bool { return check.Kind == "compaction" }) ||
		!slices.ContainsFunc(item.Checks, func(check Check) bool {
			return check.Kind == "response_contains" && check.Expected == compaction.Marker
		}) {
		return ErrInvalidCase
	}
	return nil
}

func hasCompaction(preset Preset) bool {
	return slices.Contains(preset.Capabilities, compactionCapability)
}

func validateCompactionPreset(preset Preset) error {
	if hasCompaction(preset) != (preset.Compaction != nil) {
		return ErrInvalidPreset
	}
	if !hasCompaction(preset) {
		return nil
	}
	if !preset.Compaction.valid() || len(preset.Capabilities) != 3 ||
		!containsAll(preset.Capabilities, []string{"conversation", boundedCallsCapability, compactionCapability}) ||
		preset.TargetCallBudget == nil || preset.TargetCallBudget.MaxOutputTokens < 4000 ||
		!preset.CaptureModelMetadata || preset.JudgeModel == "" || preset.UserModel != "" ||
		preset.ActiveTool != nil || len(preset.WorkspaceFiles) != 0 {
		return ErrInvalidPreset
	}
	return nil
}

func compactionLogging() config.EventLoggingConfig {
	return config.EventLoggingConfig{Enabled: true, MinSeverity: "info", Include: []string{
		events.KindAgentTurnStart.String(), events.KindAgentTurnEnd.String(),
		events.KindAgentContextCompressStart.String(), events.KindAgentContextCompressProgress.String(),
		events.KindAgentContextCompressEnd.String(),
	}}
}

func validEvalContextPolicy(cfg *config.Config, preset Preset) bool {
	if !hasCompaction(preset) {
		return len(cfg.Agents.Defaults.ContextManagerConfig) == 0
	}
	profile, err := decodeDocument[CompactionProfile](cfg.Agents.Defaults.ContextManagerConfig, 2048)
	if err != nil || preset.Compaction == nil || profile != *preset.Compaction || !profile.valid() {
		return false
	}
	// Native defaults depend on exact key presence, especially explicit zero tail.
	var fields map[string]json.RawMessage
	if json.Unmarshal(cfg.Agents.Defaults.ContextManagerConfig, &fields) != nil || len(fields) != 4 {
		return false
	}
	for _, name := range []string{"historyMaxTokens", "summaryMaxTokens", "freshTailMaxTokens", "recentTailTurns"} {
		if _, present := fields[name]; !present {
			return false
		}
	}
	return true
}

func validateCompactionPolicy(cfg *config.Config, preset Preset) error {
	if !hasCompaction(preset) {
		return nil
	}
	if cfg == nil || validateCompactionPreset(preset) != nil || !validEvalContextPolicy(cfg, preset) ||
		len(cfg.Agents.List) != 1 || cfg.Agents.List[0].ToolPolicy == nil ||
		len(cfg.Agents.List[0].ToolPolicy.Allow) != 0 || len(cfg.Agents.List[0].ToolPolicy.Deny) != 0 ||
		len(cfg.Agents.List[0].Skills) != 0 || cfg.Agents.List[0].Model != nil ||
		cfg.Agents.Defaults.ContextManager != "seahorse" || len(cfg.Agents.Defaults.ModelFallbacks) != 0 ||
		cfg.Agents.Defaults.MaxParallelTurns != 1 || cfg.Agents.List[0].MaxParallelTurns != 1 ||
		cfg.Tools.RequestUserInput.Enabled || len(cfg.ModelList) != 1 || cfg.ModelList[0] == nil ||
		cfg.ModelList[0].Streaming.Enabled || cfg.Gateway.LogLevel != "info" ||
		!reflect.DeepEqual(cfg.Events.Logging, compactionLogging()) || cfg.Agents.Defaults.MaxTokens < 64 ||
		cfg.Agents.Defaults.MaxTokens > 256 {
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

func validateCompactionBinding(preset Preset, item Case) error {
	if hasCompaction(preset) != (item.Compaction != nil) || item.Compaction != nil &&
		(item.Validate() != nil || validateCompactionPreset(preset) != nil) {
		return ErrUnsafeTarget
	}
	return nil
}
