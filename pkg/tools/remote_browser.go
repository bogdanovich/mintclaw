package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

var ErrRemoteBrowserUnavailable = errors.New("remote browser unavailable")

// RemoteBrowserArtifactRequestID reconstructs the opaque browser request
// identity used by the authenticated broker for one retained invocation. It
// is not model-facing and grants no authority without the complete runtime
// principal and spool owner tuple.
func RemoteBrowserArtifactRequestID(ctx context.Context) (string, error) {
	return browserRequestID(ctx)
}

// RemoteBrowserOperation is the model-safe projection of one exact operation
// on one operator-selected node browser profile. The companion target, driver,
// profile revision, credentials, runtime paths, and raw browser node commands
// remain inside the gateway browser broker.
type RemoteBrowserOperation struct {
	Target           string
	Profile          string
	Available        bool
	Risk             nodes.Risk
	InputSchema      json.RawMessage
	ResultKind       string
	SupportsProgress bool
	SupportsCancel   bool
	actions          []browser.ActionKind
}

// RemoteBrowserProfileRouter is the closed browser adapter used by the coding
// capability broker. It composes the first-party browser lifecycle and action
// implementations; it never exposes the generic node browser command family.
type RemoteBrowserProfileRouter struct {
	config  config.BrowserToolsConfig
	source  BrowserToolSource
	target  string
	profile string
	agentID string
	runtime *browserToolRuntime
}

// remoteBrowserToolSource admits only retained output artifacts. Uploads,
// file choosers, handoff, and routed delivery remain unavailable; screenshot
// and download bytes cross the coding boundary later through the existing
// owner-bound artifact protocol rather than inline IPC payloads.
type remoteBrowserToolSource struct {
	BrowserToolSource
}

func (source remoteBrowserToolSource) ScreenshotAvailable() bool {
	return source.BrowserToolSource.ScreenshotAvailable()
}

func (source remoteBrowserToolSource) ArtifactTransferAvailable() bool {
	return source.BrowserToolSource.ArtifactTransferAvailable()
}

func (source remoteBrowserToolSource) DownloadAvailable() bool {
	return source.BrowserToolSource.DownloadAvailable()
}
func (remoteBrowserToolSource) FileChooserAvailable() bool { return false }
func (remoteBrowserToolSource) HandoffAvailable() bool     { return false }

func NewRemoteBrowserProfileRouter(
	cfg *config.Config,
	source BrowserToolSource,
	agentID string,
	targetName string,
	profileName string,
) (*RemoteBrowserProfileRouter, error) {
	if cfg == nil || source == nil || strings.TrimSpace(agentID) == "" {
		return nil, fmt.Errorf("remote browser router requires config, source, and agent")
	}
	target, targetFound := cfg.Tools.Browser.Targets[targetName]
	profile, profileFound := target.Profiles[profileName]
	if !cfg.Tools.Browser.Enabled || !targetFound || !target.Enabled ||
		target.EffectivePlacement() != config.BrowserPlacementNode || !profileFound || !profile.Enabled ||
		profile.Mode == config.BrowserProfileAttachedUser ||
		!slices.Contains(cfg.Tools.Browser.Agents, agentID) || !slices.Contains(profile.AllowedAgents, agentID) {
		return nil, ErrRemoteBrowserUnavailable
	}
	restricted := remoteBrowserToolSource{BrowserToolSource: source}
	options := NewBrowserToolOptions(cfg.Tools.Browser)
	return &RemoteBrowserProfileRouter{
		config: cfg.Tools.Browser, source: restricted, target: targetName,
		profile: profileName, agentID: agentID, runtime: newBrowserToolRuntime(options, restricted),
	}, nil
}

// Describe revalidates the exact browser target/profile, passive runtime
// readiness, feature set, approval posture, and bounded operation schema.
func (router *RemoteBrowserProfileRouter) Describe(
	ctx context.Context,
	operation string,
) (RemoteBrowserOperation, error) {
	if router == nil || router.runtime == nil || router.source == nil ||
		!remoteBrowserOperationSupported(operation) {
		return RemoteBrowserOperation{}, ErrRemoteBrowserUnavailable
	}
	target, targetFound := router.config.Targets[router.target]
	profile, profileFound := target.Profiles[router.profile]
	if !router.config.Enabled || !targetFound || !target.Enabled ||
		target.EffectivePlacement() != config.BrowserPlacementNode || !profileFound || !profile.Enabled ||
		profile.Mode == config.BrowserProfileAttachedUser ||
		!slices.Contains(router.config.Agents, router.agentID) ||
		!slices.Contains(profile.AllowedAgents, router.agentID) {
		return RemoteBrowserOperation{}, ErrRemoteBrowserUnavailable
	}
	if remoteBrowserOperationNeedsApprovalFreeProfile(operation) &&
		profile.ApprovalMode != config.BrowserApprovalNone {
		return RemoteBrowserOperation{}, ErrRemoteBrowserUnavailable
	}
	diagnostics, err := router.source.PassiveTargetDiagnostics(ctx, router.target, []string{router.profile})
	if err != nil {
		return RemoteBrowserOperation{}, ErrRemoteBrowserUnavailable
	}
	readiness, found := diagnostics.Profiles[router.profile]
	if !found {
		return RemoteBrowserOperation{}, ErrRemoteBrowserUnavailable
	}
	if remoteBrowserContextOperation(operation) && !diagnostics.Contexts ||
		operation == "browser_diagnostics" && !diagnostics.Diagnostics ||
		operation == "browser_capture" && (!diagnostics.Screenshot || !router.source.ScreenshotAvailable()) ||
		operation == "browser_act" && len(diagnostics.Actions) == 0 {
		return RemoteBrowserOperation{}, ErrRemoteBrowserUnavailable
	}
	schema := remoteBrowserInputSchema(router, operation, diagnostics.Actions)
	encoded, err := json.Marshal(schema)
	if err != nil || len(encoded) == 0 {
		return RemoteBrowserOperation{}, ErrRemoteBrowserUnavailable
	}
	return RemoteBrowserOperation{
		Target: router.target, Profile: router.profile,
		Available: router.source.Available() && remoteBrowserProfileReady(readiness),
		Risk:      remoteBrowserRisk(operation), InputSchema: encoded,
		ResultKind: remoteBrowserResultKind(operation), SupportsProgress: false, SupportsCancel: false,
		actions: slices.Clone(diagnostics.Actions),
	}, nil
}

func (router *RemoteBrowserProfileRouter) Execute(
	ctx context.Context,
	operation string,
	arguments map[string]any,
) *toolshared.ToolResult {
	described, err := router.Describe(ctx, operation)
	if err != nil || !described.Available {
		return remoteBrowserError("unavailable", "remote browser operation is unavailable")
	}
	schema := remoteBrowserInputSchema(router, operation, described.actions)
	if validateToolArgs(schema, arguments) != nil {
		return remoteBrowserError("invalid_request", "remote browser arguments are invalid")
	}
	args := cloneRemoteBrowserArguments(arguments)
	if operation != "browser_open" {
		if err = router.validateBoundSession(ctx, args); err != nil {
			return remoteBrowserError("not_found", "remote browser session is unavailable")
		}
	}
	var result *toolshared.ToolResult
	switch operation {
	case "browser_open":
		result = (&BrowserSessionTool{runtime: router.runtime}).Execute(ctx, map[string]any{
			"operation": "open", "target": router.target, "profile": router.profile,
			"interaction_language": "en",
		})
	case "browser_status", "browser_close":
		args["operation"] = strings.TrimPrefix(operation, "browser_")
		result = (&BrowserSessionTool{runtime: router.runtime}).Execute(ctx, args)
	case "browser_context_list", "browser_context_open", "browser_context_select", "browser_context_close":
		args["operation"] = strings.TrimPrefix(operation, "browser_context_")
		result = (&BrowserContextsTool{runtime: router.runtime}).Execute(ctx, args)
	case "browser_observe":
		result = (&BrowserObserveTool{runtime: router.runtime}).Execute(ctx, args)
	case "browser_diagnostics":
		result = (&BrowserDiagnosticsTool{runtime: router.runtime}).Execute(ctx, args)
	case "browser_capture":
		result = (&BrowserCaptureTool{runtime: router.runtime}).executeRetained(ctx, args)
	case "browser_act":
		action, _ := args["action"].(map[string]any)
		kind, _ := action["kind"].(string)
		if !slices.Contains(described.actions, browser.ActionKind(kind)) ||
			kind == string(browser.ActionFileChooser) || kind == string(browser.ActionUpload) ||
			(kind == string(browser.ActionDownload) && action["deliver"] != nil) {
			return remoteBrowserError("not_granted", "remote browser action is unavailable")
		}
		result = (&BrowserActTool{runtime: router.runtime}).Execute(ctx, args)
	default:
		return remoteBrowserError("unavailable", "remote browser operation is unavailable")
	}
	if result == nil {
		return remoteBrowserError("unavailable", "remote browser result is unavailable")
	}
	if result.Control.Suspension != nil || len(result.Media) != 0 || len(result.ContextMedia) != 0 {
		return remoteBrowserError("approval_required", "remote browser operation requires routed interaction")
	}
	return result
}

func (router *RemoteBrowserProfileRouter) validateBoundSession(
	ctx context.Context,
	arguments map[string]any,
) error {
	sessionID, ok := arguments["browser_session_id"].(string)
	if !ok || sessionID == "" {
		return browser.ErrInvalid
	}
	owner, err := browserOwnerFromContext(ctx)
	if err != nil {
		return err
	}
	session, err := router.source.Status(ctx, owner, sessionID)
	if err != nil || session.Target != router.target || session.Profile != router.profile {
		return browser.ErrNotFound
	}
	return nil
}

func remoteBrowserInputSchema(
	router *RemoteBrowserProfileRouter,
	operation string,
	actions []browser.ActionKind,
) map[string]any {
	session := map[string]any{"type": "string", "minLength": 1, "maxLength": browser.MaxIdentifierBytes}
	base := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": properties, "required": required,
		}
	}
	switch operation {
	case "browser_open":
		return base(map[string]any{})
	case "browser_status", "browser_close", "browser_context_list", "browser_context_open":
		return base(map[string]any{"browser_session_id": session}, "browser_session_id")
	case "browser_context_select", "browser_context_close":
		properties := map[string]any{
			"browser_session_id": session,
			"context_catalog_id": map[string]any{"type": "string", "minLength": 1},
			"context_generation": map[string]any{"type": "integer", "minimum": 1},
			"tab_id":             map[string]any{"type": "string", "minLength": 1},
		}
		if operation == "browser_context_select" {
			properties["frame_id"] = map[string]any{"type": "string", "minLength": 1}
		}
		return base(
			properties,
			"browser_session_id",
			"context_catalog_id",
			"context_generation",
			"tab_id",
		)
	case "browser_observe":
		schema := (&BrowserObserveTool{}).Parameters()
		properties, _ := schema["properties"].(map[string]any)
		delete(properties, "screenshot")
		return schema
	case "browser_diagnostics":
		return (&BrowserDiagnosticsTool{}).Parameters()
	case "browser_capture":
		return (&BrowserCaptureTool{}).Parameters()
	case "browser_act":
		return remoteBrowserActInputSchema(router, actions)
	default:
		return nil
	}
}

func remoteBrowserOperationSupported(operation string) bool {
	switch operation {
	case "browser_open", "browser_status", "browser_close",
		"browser_context_list", "browser_context_open", "browser_context_select", "browser_context_close",
		"browser_observe", "browser_diagnostics", "browser_capture", "browser_act":
		return true
	default:
		return false
	}
}

func remoteBrowserOperationNeedsApprovalFreeProfile(operation string) bool {
	return operation == "browser_act" ||
		operation == "browser_context_open" || operation == "browser_context_select" ||
		operation == "browser_context_close"
}

func remoteBrowserContextOperation(operation string) bool {
	return strings.HasPrefix(operation, "browser_context_")
}

func remoteBrowserRisk(operation string) nodes.Risk {
	switch operation {
	case "browser_status", "browser_context_list", "browser_observe", "browser_diagnostics", "browser_capture":
		return nodes.RiskRead
	default:
		return nodes.RiskWrite
	}
}

func remoteBrowserResultKind(operation string) string {
	switch operation {
	case "browser_open", "browser_status", "browser_close":
		return "browser_session"
	case "browser_context_list", "browser_context_open", "browser_context_select", "browser_context_close":
		return "browser_contexts"
	case "browser_observe":
		return "browser_observation"
	case "browser_diagnostics":
		return "browser_diagnostics"
	case "browser_capture":
		return "browser_artifact"
	case "browser_act":
		return "browser_action"
	default:
		return "browser_result"
	}
}

func remoteBrowserActInputSchema(
	router *RemoteBrowserProfileRouter,
	actions []browser.ActionKind,
) map[string]any {
	schema := (&BrowserActTool{runtime: router.runtime}).Parameters()
	properties, _ := schema["properties"].(map[string]any)
	action, _ := properties["action"].(map[string]any)
	branches, _ := action["oneOf"].([]any)
	for _, rawBranch := range branches {
		branch, _ := rawBranch.(map[string]any)
		branchProperties, _ := branch["properties"].(map[string]any)
		kind, _ := branchProperties["kind"].(map[string]any)
		kindName, _ := kind["const"].(string)
		if !slices.Contains(actions, browser.ActionKind(kindName)) {
			continue
		}
		if kindName == string(browser.ActionDownload) {
			delete(branchProperties, "deliver")
		}
	}
	filtered := make([]any, 0, len(branches))
	for _, rawBranch := range branches {
		branch, _ := rawBranch.(map[string]any)
		branchProperties, _ := branch["properties"].(map[string]any)
		kind, _ := branchProperties["kind"].(map[string]any)
		kindName, _ := kind["const"].(string)
		if slices.Contains(actions, browser.ActionKind(kindName)) {
			filtered = append(filtered, rawBranch)
		}
	}
	action["oneOf"] = filtered
	return schema
}

func remoteBrowserProfileReady(readiness browser.PassiveReadiness) bool {
	return readiness.Status == browser.ReadinessReady || readiness.Status == browser.ReadinessBusy
}

func cloneRemoteBrowserArguments(arguments map[string]any) map[string]any {
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(arguments))
	if json.Unmarshal(encoded, &cloned) != nil {
		return map[string]any{}
	}
	return cloned
}

func remoteBrowserError(code, message string) *toolshared.ToolResult {
	encoded, _ := json.Marshal(map[string]string{
		"status": "denied", "code": code, "message": message,
	})
	return toolshared.ErrorResult(string(encoded))
}
