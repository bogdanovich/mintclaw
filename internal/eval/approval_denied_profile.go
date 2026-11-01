package eval

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/bogdanovich/mintclaw/internal/evalfixture"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/events"
)

const approvalToolCapability = "approval_tool"

func hasApprovalTool(preset Preset) bool {
	return slices.Contains(preset.Capabilities, approvalToolCapability)
}

func validateApprovalToolPreset(preset Preset) error {
	if !hasApprovalTool(preset) {
		return nil
	}
	if len(preset.Capabilities) != 3 || !containsAll(preset.Capabilities,
		[]string{"conversation", boundedCallsCapability, approvalToolCapability}) ||
		preset.TargetCallBudget == nil || !preset.CaptureModelMetadata || preset.ActiveTool != nil ||
		preset.UserModel != "" || preset.JudgeModel == "" || len(preset.WorkspaceFiles) != 1 ||
		preset.WorkspaceFiles[0] != (WorkspaceFile{
			Path: evalfixture.ApprovalPath, SHA256: digest([]byte(evalfixture.ApprovalBefore)),
		}) {
		return ErrInvalidPreset
	}
	return nil
}

func approvalToolLogging() config.EventLoggingConfig {
	return config.EventLoggingConfig{Enabled: true, MinSeverity: "info", Include: []string{
		events.KindAgentToolExecStart.String(), events.KindAgentToolExecEnd.String(),
		events.KindAgentToolExecSkipped.String(), events.KindAgentTurnEnd.String(),
	}}
}

func validateApprovalToolPolicy(cfg *config.Config, preset Preset, workspace string) error {
	if !hasApprovalTool(preset) {
		return nil
	}
	if validateApprovalToolPreset(preset) != nil || cfg == nil || len(cfg.Agents.List) != 1 ||
		workspace != filepath.Join(preset.IsolationRoot, "workspace") || cfg.Gateway.LogLevel != "info" ||
		cfg.Agents.Defaults.ContextManager != "seahorse" ||
		!nativeApprovalToolEnabled(cfg) ||
		cfg.Hooks.Defaults != config.DefaultConfig().Hooks.Defaults ||
		!reflect.DeepEqual(cfg.Events.Logging, approvalToolLogging()) ||
		cfg.Tools.RequestUserInput.Enabled ||
		cfg.Agents.Defaults.MaxParallelTurns != 1 || cfg.Agents.List[0].MaxParallelTurns != 1 {
		return ErrUnsafeTarget
	}
	return nil
}

func nativeApprovalToolEnabled(cfg *config.Config) bool {
	return cfg != nil && len(cfg.Agents.List) == 1 && cfg.Hooks.Enabled && len(cfg.Hooks.Builtins) == 1 &&
		len(
			cfg.Hooks.Processes,
		) == 0 && evalfixture.ApprovalHookMatches(cfg.Hooks.Builtins[evalfixture.ApprovalHookName]) &&
		cfg.Agents.List[0].ToolPolicy != nil && cfg.Agents.List[0].ToolPolicy.Default == config.AgentCapabilityDefaultDeny &&
		slices.Equal(
			cfg.Agents.List[0].ToolPolicy.Allow,
			[]string{"write_file"},
		) && len(cfg.Agents.List[0].ToolPolicy.Deny) == 0
}

func approvalFixtureMatches(snapshot ReadFixtureSnapshot) bool {
	return snapshot.Code == "observed" && snapshot.SHA256 == digest([]byte(evalfixture.ApprovalBefore)) &&
		slices.Equal(snapshot.Entries, []string{"approval.txt"})
}

func observeApprovalFixture(ctx context.Context, workspace string) ReadFixtureSnapshot {
	return observeWorkspaceFixture(ctx, workspace, evalfixture.ApprovalPath)
}

func validApprovalToolWorkspace(ctx context.Context, preset Preset, workspace string) bool {
	return !hasApprovalTool(preset) || approvalFixtureMatches(observeApprovalFixture(ctx, workspace))
}

func validateApprovalDeniedBinding(preset Preset, item Case) error {
	if item.ApprovalDenied != nil && validateApprovalToolPreset(preset) != nil ||
		item.ApprovalDenied != nil && !hasApprovalTool(preset) {
		return ErrUnsafeTarget
	}
	return nil
}
