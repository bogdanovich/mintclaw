package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/browseraction"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/tools/loopguard"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// BrowserToolSource is the narrow gateway-owned boundary used by first-party
// browser tools. Implementations keep the runtime alive for the full method
// call so configuration reload cannot hand a tool a stale broker pointer.
type BrowserToolSource interface {
	Available() bool
	ScreenshotAvailable() bool
	ArtifactTransferAvailable() bool
	DownloadAvailable() bool
	HandoffAvailable() bool
	ProfileAvailability(context.Context, string, string) (browser.ProfileAvailability, error)
	PassiveTargetDiagnostics(context.Context, string, []string) (BrowserTargetDiagnostics, error)
	Open(context.Context, browser.OpenRequest) (browser.Session, error)
	Status(context.Context, browser.Owner, string) (browser.Session, error)
	Close(context.Context, browser.Owner, string) (browser.Session, error)
	Handoff(context.Context, browser.Owner, string) (browser.Session, error)
	ReleaseHandoff(context.Context, browser.Owner, string) (browser.Session, error)
	Resume(context.Context, browser.Owner, string) (browser.Session, error)
	Observe(context.Context, browser.Owner, string, string) (browser.Observation, error)
	LookupScreenshot(context.Context, browser.Owner, string, string) (browser.ScreenshotArtifact, bool, error)
	CaptureScreenshot(context.Context, browser.ScreenshotRequest) (browser.ScreenshotArtifact, error)
	ClaimScreenshotDelivery(context.Context, browser.ScreenshotDeliveryRequest) error
	ClaimDownloadDelivery(context.Context, browser.DownloadDeliveryRequest) error
	PrepareAction(context.Context, browser.PrepareActionRequest) (browser.Preparation, error)
	ExecuteAction(context.Context, browser.Owner, string, *browser.ApprovalBinding) (browser.Invocation, error)
}

type BrowserContextToolSource interface {
	ObserveContext(context.Context, browser.ObserveRequest) (browser.Observation, error)
	ListContexts(context.Context, browser.Owner, string) (browser.ContextCatalog, error)
	PrepareContext(context.Context, browser.ContextRequest) (browser.ContextPreparation, error)
	ExecuteContext(
		context.Context, browser.ContextPreparation, *browser.ApprovalBinding,
	) (browser.ContextResult, error)
}

type browserTurnCleanupSource interface {
	CloseOwner(context.Context, browser.Owner) error
}

// BrowserTargetDiagnostics is one gateway-owned readiness and capability
// snapshot. Implementations must compute every field while holding the same
// runtime generation so discovery cannot combine stale capability flags with
// unavailable readiness.
type BrowserTargetDiagnostics struct {
	Profiles   map[string]browser.PassiveReadiness
	Actions    []browser.ActionKind
	Screenshot bool
	Upload     bool
	Download   bool
	HeadedView bool
	Handoff    bool
	Contexts   bool
}

type browserToolRuntime struct {
	config        config.BrowserToolsConfig
	source        BrowserToolSource
	allowedAgents map[string]struct{}
}

type (
	BrowserTargetsTool  struct{ runtime *browserToolRuntime }
	BrowserSessionTool  struct{ runtime *browserToolRuntime }
	BrowserContextsTool struct{ runtime *browserToolRuntime }
	BrowserObserveTool  struct{ runtime *browserToolRuntime }
	BrowserCaptureTool  struct{ runtime *browserToolRuntime }
	BrowserActTool      struct{ runtime *browserToolRuntime }
)

func NewBrowserTargetsTool(cfg *config.Config, source BrowserToolSource) *BrowserTargetsTool {
	return &BrowserTargetsTool{runtime: newBrowserToolRuntime(cfg, source)}
}

func NewBrowserSessionTool(cfg *config.Config, source BrowserToolSource) *BrowserSessionTool {
	return &BrowserSessionTool{runtime: newBrowserToolRuntime(cfg, source)}
}

func (tool *BrowserSessionTool) CleanupTurn(ctx context.Context) error {
	if tool == nil || tool.runtime == nil || tool.runtime.source == nil {
		return nil
	}
	source, ok := tool.runtime.source.(browserTurnCleanupSource)
	if !ok {
		return nil
	}
	owner, err := browserOwnerFromContext(ctx)
	if err != nil {
		return err
	}
	return source.CloseOwner(ctx, owner)
}

func NewBrowserObserveTool(cfg *config.Config, source BrowserToolSource) *BrowserObserveTool {
	return &BrowserObserveTool{runtime: newBrowserToolRuntime(cfg, source)}
}

func NewBrowserCaptureTool(cfg *config.Config, source BrowserToolSource) *BrowserCaptureTool {
	return &BrowserCaptureTool{runtime: newBrowserToolRuntime(cfg, source)}
}

func NewBrowserContextsTool(cfg *config.Config, source BrowserToolSource) *BrowserContextsTool {
	return &BrowserContextsTool{runtime: newBrowserToolRuntime(cfg, source)}
}

func NewBrowserActTool(cfg *config.Config, source BrowserToolSource) *BrowserActTool {
	return &BrowserActTool{runtime: newBrowserToolRuntime(cfg, source)}
}

func newBrowserToolRuntime(cfg *config.Config, source BrowserToolSource) *browserToolRuntime {
	runtime := &browserToolRuntime{source: source, allowedAgents: make(map[string]struct{})}
	if cfg == nil {
		return runtime
	}
	runtime.config = cfg.Tools.Browser
	for _, agentID := range runtime.config.Agents {
		runtime.allowedAgents[routing.NormalizeAgentID(agentID)] = struct{}{}
	}
	return runtime
}

func (runtime *browserToolRuntime) enabledForAgent(agentID string) bool {
	if runtime == nil || !runtime.config.Enabled || runtime.source == nil {
		return false
	}
	_, ok := runtime.allowedAgents[routing.NormalizeAgentID(agentID)]
	return ok
}

func (runtime *browserToolRuntime) contextSource() (BrowserContextToolSource, bool) {
	if runtime == nil || runtime.source == nil {
		return nil, false
	}
	source, ok := runtime.source.(BrowserContextToolSource)
	return source, ok
}

func (tool *BrowserTargetsTool) ToolEnabledForAgent(agentID string) bool {
	return tool != nil && tool.runtime.enabledForAgent(agentID)
}

func (tool *BrowserSessionTool) ToolEnabledForAgent(agentID string) bool {
	return tool != nil && tool.runtime.enabledForAgent(agentID)
}

func (tool *BrowserObserveTool) ToolEnabledForAgent(agentID string) bool {
	return tool != nil && tool.runtime.enabledForAgent(agentID)
}

func (tool *BrowserCaptureTool) ToolEnabledForAgent(agentID string) bool {
	return tool != nil && tool.runtime.enabledForAgent(agentID) && tool.runtime.source.ScreenshotAvailable()
}

func (tool *BrowserContextsTool) ToolEnabledForAgent(agentID string) bool {
	if tool == nil || !tool.runtime.enabledForAgent(agentID) {
		return false
	}
	_, ok := tool.runtime.contextSource()
	return ok
}

func (tool *BrowserActTool) ToolEnabledForAgent(agentID string) bool {
	return tool != nil && tool.runtime.enabledForAgent(agentID)
}

func (*BrowserTargetsTool) Name() string { return "browser_targets" }
func (*BrowserTargetsTool) Description() string {
	return "List browser targets and managed profiles granted to this agent without starting a browser."
}

func (*BrowserTargetsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "properties": map[string]any{}, "additionalProperties": false,
	}
}

func (*BrowserTargetsTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsReadOnlyIdempotent
}

type browserTargetResult struct {
	Targets []browserTargetView `json:"targets"`
}

type browserTargetView struct {
	Target   string               `json:"target"`
	Status   string               `json:"status"`
	Reason   string               `json:"reason,omitempty"`
	Profiles []browserProfileView `json:"profiles"`
	Actions  []browser.ActionKind `json:"actions"`
	Features browserFeatureView   `json:"features"`
	Limits   browserLimitsView    `json:"limits"`
}

type browserFeatureView struct {
	Tabs              bool `json:"tabs"`
	Popups            bool `json:"popups"`
	Frames            bool `json:"frames"`
	Screenshot        bool `json:"screenshot"`
	PageScreenshot    bool `json:"page_screenshot"`
	ElementScreenshot bool `json:"element_screenshot"`
	Upload            bool `json:"upload"`
	Download          bool `json:"download"`
	Diagnostics       bool `json:"diagnostics"`
	HeadedView        bool `json:"headed_view"`
	Handoff           bool `json:"handoff"`
}

type browserProfileView struct {
	Profile              string                   `json:"profile"`
	Status               string                   `json:"status"`
	Reason               string                   `json:"reason,omitempty"`
	NetworkMode          string                   `json:"network_mode"`
	DryRun               bool                     `json:"dry_run"`
	AllowApprovedActions bool                     `json:"allow_approved_actions"`
	Readiness            browser.PassiveReadiness `json:"readiness"`
}

type browserLimitsView struct {
	Sessions            int `json:"sessions"`
	Tabs                int `json:"tabs"`
	SessionSeconds      int `json:"session_seconds"`
	IdleSeconds         int `json:"idle_seconds"`
	PreparedSeconds     int `json:"prepared_seconds"`
	ActionSeconds       int `json:"action_seconds"`
	SnapshotBytes       int `json:"snapshot_bytes"`
	ScreenshotBytes     int `json:"screenshot_bytes"`
	UploadBytes         int `json:"upload_bytes"`
	DownloadBytes       int `json:"download_bytes"`
	SnapshotRefs        int `json:"snapshot_refs"`
	TextInputBytes      int `json:"text_input_bytes"`
	ToolResultBytes     int `json:"tool_result_bytes"`
	RetentionSecs       int `json:"retention_seconds"`
	FramesPerTab        int `json:"frames_per_tab,omitempty"`
	FrameDepth          int `json:"frame_depth,omitempty"`
	ContextCatalogBytes int `json:"context_catalog_bytes,omitempty"`
	ContextLabelBytes   int `json:"context_label_bytes,omitempty"`
}

func (tool *BrowserTargetsTool) Execute(ctx context.Context, _ map[string]any) *toolshared.ToolResult {
	if !tool.runtime.enabledForAgent(toolshared.ToolAgentID(ctx)) {
		return browserErrorResult(
			"not_granted",
			"Browser access is not granted to this agent.",
			"use_an_authorized_agent",
		)
	}
	limits := tool.runtime.config.Limits.Effective()
	targetNames := make([]string, 0, len(tool.runtime.config.Targets))
	for name, target := range tool.runtime.config.Targets {
		if target.Enabled {
			targetNames = append(targetNames, name)
		}
	}
	sort.Strings(targetNames)
	views := make([]browserTargetView, 0, len(targetNames))
	for _, name := range targetNames {
		target := tool.runtime.config.Targets[name]
		profileNames := make([]string, 0, len(target.Profiles))
		for profileName, profile := range target.Profiles {
			if profile.Enabled {
				profileNames = append(profileNames, profileName)
			}
		}
		sort.Strings(profileNames)
		diagnostics, diagnosticsErr := tool.runtime.source.PassiveTargetDiagnostics(
			ctx, name, profileNames,
		)
		capabilitiesAvailable := diagnosticsErr == nil
		if capabilitiesAvailable {
			for _, profileName := range profileNames {
				readiness, ok := diagnostics.Profiles[profileName]
				if !ok || readiness.Status == "" || readiness.Profile.Status == "" {
					capabilitiesAvailable = false
					break
				}
			}
		}
		profiles := make([]browserProfileView, 0, len(profileNames))
		for _, profileName := range profileNames {
			profile := target.Profiles[profileName]
			status, reason := "unavailable", "driver_unavailable"
			readiness := browser.PassiveReadiness{
				Status: browser.ReadinessUnavailable, Broker: browser.ReadinessUnavailable,
				Worker: browser.ReadinessUnavailable, Driver: browser.ReadinessUnavailable,
				Browser: browser.ReadinessUnavailable, Proxy: browser.ReadinessUnavailable,
				Compatibility: browser.CompatibilityUnchecked,
				Profile:       browser.ProfileAvailability{Status: status, Reason: reason},
				Code:          "runtime_unavailable", Action: "contact_operator", Passive: true,
			}
			if capabilitiesAvailable {
				readiness = diagnostics.Profiles[profileName]
				status, reason = readiness.Profile.Status, readiness.Profile.Reason
			}
			profiles = append(profiles, browserProfileView{
				Profile: profileName, Status: status, Reason: reason,
				NetworkMode: profile.EffectiveNetworkMode(), DryRun: profile.DryRun,
				AllowApprovedActions: profile.AllowApprovedActions,
				Readiness:            readiness,
			})
		}
		targetStatus, targetReason, targetRank := browser.ReadinessReady, "", readinessRank(browser.ReadinessReady)
		for _, profile := range profiles {
			if rank := readinessRank(profile.Readiness.Status); rank > targetRank {
				targetStatus, targetReason, targetRank = profile.Readiness.Status, profile.Readiness.Code, rank
			}
		}
		actions := []browser.ActionKind(nil)
		if capabilitiesAvailable {
			actions = append(actions, diagnostics.Actions...)
		}
		artifactTransferAvailable := tool.runtime.source.ArtifactTransferAvailable()
		if !artifactTransferAvailable {
			actions = slices.DeleteFunc(actions, func(action browser.ActionKind) bool {
				return action == browser.ActionFileChooser || action == browser.ActionUpload ||
					action == browser.ActionDownload
			})
		}
		uploadAvailable := capabilitiesAvailable && artifactTransferAvailable && diagnostics.Upload
		downloadAvailable := capabilitiesAvailable && artifactTransferAvailable && diagnostics.Download
		if uploadAvailable && !slices.Contains(actions, browser.ActionUpload) {
			actions = append(actions, browser.ActionUpload)
		}
		if uploadAvailable && !slices.Contains(actions, browser.ActionFileChooser) {
			actions = append(actions, browser.ActionFileChooser)
		}
		if downloadAvailable && !slices.Contains(actions, browser.ActionDownload) {
			actions = append(actions, browser.ActionDownload)
		}
		slices.Sort(actions)
		contextsAvailable := capabilitiesAvailable && diagnostics.Contexts
		framesPerTab, frameDepth, contextCatalogBytes, contextLabelBytes := 0, 0, 0, 0
		if contextsAvailable {
			framesPerTab = browser.MaxContextFramesPerTab
			frameDepth = browser.MaxContextFrameDepth
			contextCatalogBytes = browser.MaxContextCatalogBytes
			contextLabelBytes = browser.MaxContextLabelBytes
		}
		screenshotAvailable := capabilitiesAvailable && diagnostics.Screenshot
		views = append(views, browserTargetView{
			Target: name, Status: targetStatus, Reason: targetReason, Profiles: profiles,
			Actions: actions,
			Features: browserFeatureView{
				Tabs:       contextsAvailable,
				Popups:     contextsAvailable,
				Frames:     contextsAvailable,
				Screenshot: screenshotAvailable, PageScreenshot: screenshotAvailable,
				ElementScreenshot: screenshotAvailable,
				Upload:            uploadAvailable,
				Download:          downloadAvailable,
				Diagnostics:       true,
				HeadedView:        capabilitiesAvailable && diagnostics.HeadedView,
				Handoff:           capabilitiesAvailable && diagnostics.Handoff,
			},
			Limits: browserLimitsView{
				Sessions: limits.Sessions, Tabs: limits.Tabs,
				SessionSeconds: limits.SessionSeconds, IdleSeconds: limits.IdleSeconds,
				PreparedSeconds: limits.PreparedSeconds, ActionSeconds: limits.ActionSeconds,
				SnapshotBytes:   limits.SnapshotBytes,
				ScreenshotBytes: limits.ScreenshotBytes, UploadBytes: limits.UploadBytes,
				DownloadBytes: limits.DownloadBytes, SnapshotRefs: limits.SnapshotRefs,
				TextInputBytes: limits.TextInputBytes, ToolResultBytes: limits.ToolResultBytes,
				RetentionSecs: limits.RetentionSecs,
				FramesPerTab:  framesPerTab, FrameDepth: frameDepth,
				ContextCatalogBytes: contextCatalogBytes, ContextLabelBytes: contextLabelBytes,
			},
		})
	}
	return tool.runtime.result(browserTargetResult{Targets: views})
}

func readinessRank(status string) int {
	switch status {
	case browser.ReadinessUnavailable:
		return 5
	case browser.ReadinessDegraded:
		return 4
	case browser.ReadinessBusy:
		return 3
	case browser.ReadinessConfigured:
		return 2
	case browser.ReadinessReady:
		return 1
	default:
		return 5
	}
}

func (*BrowserSessionTool) Name() string { return "browser_session" }
func (*BrowserSessionTool) Description() string {
	return "Open, inspect, or close one broker-owned browser session. " +
		"For open, target is the browser target name from browser_targets (for example gateway or companion), " +
		"and profile is the profile name nested under that target (for example managed)."
}

func (*BrowserSessionTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation": map[string]any{
				"type": "string", "enum": []string{"open", "status", "close", "handoff", "resume"},
			},
			"target": map[string]any{
				"type":        "string",
				"description": "For open only: exact browser target name returned by browser_targets, such as gateway or companion.",
			},
			"profile": map[string]any{
				"type":        "string",
				"description": "For open only: exact profile name listed inside the selected browser target, such as managed.",
			},
			"browser_session_id": map[string]any{
				"type":        "string",
				"description": "For status, close, handoff, and resume only: broker-issued browser session ID.",
			},
		},
		"required": []string{"operation"}, "additionalProperties": false,
	}
}

func (*BrowserSessionTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

type browserSessionView struct {
	BrowserSessionID     string                  `json:"browser_session_id"`
	State                browser.SessionState    `json:"state"`
	Target               string                  `json:"target"`
	Profile              string                  `json:"profile"`
	DryRun               bool                    `json:"dry_run"`
	ControllerGeneration uint64                  `json:"controller_generation"`
	Controller           browser.ControllerState `json:"controller"`
	ControllerExpiresAt  int64                   `json:"controller_expires_at,omitempty"`
	ExpiresAt            int64                   `json:"expires_at"`
	Tabs                 []browserTabView        `json:"tabs"`
	Reason               string                  `json:"reason,omitempty"`
}

type browserTabView struct {
	TabID              string `json:"tab_id"`
	SnapshotID         string `json:"snapshot_id,omitempty"`
	SnapshotGeneration uint64 `json:"snapshot_generation,omitempty"`
}

func browserSessionResult(session browser.Session) browserSessionView {
	return browserSessionView{
		BrowserSessionID: session.ID, State: session.State, Target: session.Target,
		Profile: session.Profile, DryRun: session.DryRun,
		ControllerGeneration: session.ControllerGeneration, ExpiresAt: session.ExpiresAt,
		Controller: session.EffectiveController(), ControllerExpiresAt: session.ControllerExpiresAt,
		Tabs: []browserTabView{{
			TabID: session.TabID, SnapshotID: session.SnapshotID,
			SnapshotGeneration: session.SnapshotGeneration,
		}},
		Reason: session.SafeFailure,
	}
}

func (tool *BrowserSessionTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	if !tool.runtime.enabledForAgent(toolshared.ToolAgentID(ctx)) {
		return browserErrorResult(
			"not_granted",
			"Browser access is not granted to this agent.",
			"use_an_authorized_agent",
		)
	}
	owner, err := browserOwnerFromContext(ctx)
	if err != nil {
		return browserToolError(err)
	}
	operation, _ := args["operation"].(string)
	var session browser.Session
	switch operation {
	case "open":
		target, targetOK := args["target"].(string)
		profile, profileOK := args["profile"].(string)
		if !targetOK || !profileOK || len(args) != 3 {
			return browserErrorResult(
				"invalid_request",
				"Open requires exactly target and profile.",
				"correct_arguments",
			)
		}
		session, err = tool.runtime.source.Open(ctx, browser.OpenRequest{
			Owner: owner, Target: target, Profile: profile,
		})
	case "status", "close", "handoff", "resume":
		sessionID, ok := args["browser_session_id"].(string)
		if !ok || len(args) != 2 {
			return browserErrorResult(
				"invalid_request",
				"Status and close require exactly browser_session_id.",
				"correct_arguments",
			)
		}
		switch operation {
		case "status":
			session, err = tool.runtime.source.Status(ctx, owner, sessionID)
		case "close":
			session, err = tool.runtime.source.Close(ctx, owner, sessionID)
		case "handoff":
			if !tool.runtime.source.HandoffAvailable() {
				return browserToolError(browser.ErrDriverIncompatible)
			}
			session, err = tool.runtime.source.Handoff(ctx, owner, sessionID)
		default:
			session, err = tool.runtime.source.Resume(ctx, owner, sessionID)
		}
	default:
		return browserErrorResult("invalid_request", "Unknown browser session operation.", "correct_arguments")
	}
	if err != nil {
		return browserToolError(err)
	}
	result := tool.runtime.result(browserSessionResult(session))
	if operation == "handoff" && result != nil && !result.IsError {
		result.Control.Suspension = &interactions.SuspensionRequest{
			Kind: interactions.KindQuestion,
			Questions: []interactions.Question{{
				ID: "release_browser", Header: "Browser control",
				Question: "Use the visible local browser window. When you are finished, reply to release control.",
			}},
			PromptSummary: "Browser automation is paused for exclusive local human control.",
			Timeout:       time.Duration(tool.runtime.config.Limits.Effective().PreparedSeconds) * time.Second,
		}
		result.Control.ResolveSuspension = func(resolutionCtx context.Context, outcome interactions.Outcome) error {
			if outcome == interactions.OutcomeAnswered {
				_, resolutionErr := tool.runtime.source.ReleaseHandoff(resolutionCtx, owner, session.ID)
				if resolutionErr == nil {
					return nil
				}
				_, closeErr := tool.runtime.source.Close(context.WithoutCancel(resolutionCtx), owner, session.ID)
				return errors.Join(resolutionErr, closeErr)
			}
			_, resolutionErr := tool.runtime.source.Close(resolutionCtx, owner, session.ID)
			return resolutionErr
		}
	}
	return result
}

func (*BrowserContextsTool) Name() string { return "browser_contexts" }
func (*BrowserContextsTool) Description() string {
	return "List, open, select, or close bounded opaque browser tabs and frames for one owned session. " +
		"For list and open, send only operation and browser_session_id; open creates and selects a new tab. " +
		"For select and close, use the fresh context_catalog_id, context_generation, and tab_id from list; " +
		"select may also include a frame_id, while close must not."
}

func (*BrowserContextsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation": map[string]any{
				"type":        "string",
				"enum":        []string{"list", "open", "select", "close"},
				"description": "List current contexts, open and select a new tab, select a fresh listed tab/frame, or close a fresh listed tab.",
			},
			"browser_session_id": map[string]any{
				"type":        "string",
				"description": "Owned browser session. This and operation are the only arguments allowed for list and open.",
			},
			"context_catalog_id": map[string]any{
				"type":        "string",
				"description": "Fresh broker-issued catalog ID required for select and close; omit for list and open.",
			},
			"context_generation": map[string]any{
				"type":        "integer",
				"description": "Fresh catalog generation required for select and close; omit for list and open.",
			},
			"tab_id": map[string]any{
				"type":        "string",
				"description": "Fresh broker-issued tab ID required for select and close; omit for list and open.",
			},
			"frame_id": map[string]any{
				"type":        "string",
				"description": "Optional fresh broker-issued frame ID for select only; omit for list, open, and close.",
			},
		},
		"required": []string{"operation", "browser_session_id"}, "additionalProperties": false,
	}
}

func (*BrowserContextsTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

// Browser context results can contain live page titles, URLs, and selected
// frame observations. Keep them available to the current tool loop, but never
// retain them in canonical history or diagnostics.
func (*BrowserContextsTool) DurableArguments(args map[string]any) (map[string]any, error) {
	return cloneBrowserToolArguments(args)
}

func (*BrowserContextsTool) ProtectedDurableResult(map[string]any) bool { return true }

func (tool *BrowserContextsTool) ApprovalArguments(
	ctx context.Context,
	args map[string]any,
) (map[string]any, error) {
	if !tool.runtime.enabledForAgent(toolshared.ToolAgentID(ctx)) {
		return nil, &browserSafeDenialError{cause: browser.ErrDenied}
	}
	operation, _ := args["operation"].(string)
	if operation != string(browser.ContextClose) {
		return args, nil
	}
	preparation, err := tool.prepare(ctx, args)
	if err != nil {
		return nil, &browserSafeDenialError{cause: err}
	}
	return map[string]any{
		"context_invocation_id": preparation.Invocation.ID,
		"action_hash":           preparation.Invocation.ActionHash,
		"expires_at":            preparation.Invocation.ExpiresAt,
		"preview":               browserContextApprovalSummary(preparation),
	}, nil
}

type browserContextResultView struct {
	ContextCatalog browser.ContextCatalog  `json:"context_catalog"`
	Observation    *browserObservationView `json:"observation,omitempty"`
	InvocationID   string                  `json:"invocation_id,omitempty"`
	Effect         browser.Effect          `json:"effect,omitempty"`
	State          browser.InvocationState `json:"state,omitempty"`
}

func (tool *BrowserContextsTool) Execute(
	ctx context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	if !tool.runtime.enabledForAgent(toolshared.ToolAgentID(ctx)) {
		return browserErrorResult(
			"not_granted", "Browser access is not granted to this agent.", "use_an_authorized_agent",
		)
	}
	owner, err := browserOwnerFromContext(ctx)
	if err != nil {
		return browserToolError(err)
	}
	operation, operationOK := args["operation"].(string)
	sessionID, sessionOK := args["browser_session_id"].(string)
	if !operationOK || !sessionOK {
		return browserToolError(browser.ErrInvalid)
	}
	if operation == string(browser.ContextList) {
		if len(args) != 2 {
			return browserToolError(browser.ErrInvalid)
		}
		source, available := tool.runtime.contextSource()
		if !available {
			return browserContextToolError(browser.ErrDriverIncompatible)
		}
		catalog, listErr := source.ListContexts(ctx, owner, sessionID)
		if listErr != nil {
			return browserContextToolError(listErr)
		}
		return tool.runtime.result(browserContextResultView{ContextCatalog: catalog})
	}
	preparation, err := tool.prepare(ctx, args)
	if err != nil {
		return browserContextToolError(err)
	}
	if preparation.RequiresApproval &&
		!toolshared.ToolApprovalContinuation(ctx) && !toolshared.ToolApprovalBypass(ctx) {
		return &toolshared.ToolResult{
			Control: toolshared.ToolControl{Suspension: &interactions.SuspensionRequest{
				Kind: interactions.KindApproval, PromptSummary: browserContextApprovalSummary(preparation),
				Timeout: time.Duration(tool.runtime.config.Limits.Effective().PreparedSeconds) * time.Second,
			}},
			Delivery: toolshared.ToolDelivery{Intent: toolshared.DeliverySilent},
		}
	}
	var approval *browser.ApprovalBinding
	if preparation.RequiresApproval {
		binding := preparation.Approval
		approval = &binding
	}
	source, available := tool.runtime.contextSource()
	if !available {
		return browserContextToolError(browser.ErrDriverIncompatible)
	}
	contextResult, err := source.ExecuteContext(ctx, preparation, approval)
	if err != nil {
		return browserContextToolError(err)
	}
	view := browserContextResultView{ContextCatalog: contextResult.Catalog}
	if contextResult.Invocation != nil {
		view.InvocationID = contextResult.Invocation.ID
		view.Effect = contextResult.Invocation.Effect
		view.State = contextResult.Invocation.State
	}
	if contextResult.Observation != nil {
		observation := tool.runtime.observationResult(*contextResult.Observation)
		view.Observation = &observation
	}
	return tool.runtime.result(view)
}

func (tool *BrowserContextsTool) prepare(
	ctx context.Context,
	args map[string]any,
) (browser.ContextPreparation, error) {
	owner, err := browserOwnerFromContext(ctx)
	if err != nil {
		return browser.ContextPreparation{}, err
	}
	requestID, err := browserRequestID(ctx)
	if err != nil {
		return browser.ContextPreparation{}, err
	}
	operation, operationOK := args["operation"].(string)
	sessionID, sessionOK := args["browser_session_id"].(string)
	if !operationOK || !sessionOK {
		return browser.ContextPreparation{}, browser.ErrInvalid
	}
	request := browser.ContextRequest{
		Owner: owner, RequestID: requestID, SessionID: sessionID,
		Operation: browser.ContextOperation(operation),
	}
	request.ContextCatalogID, _ = args["context_catalog_id"].(string)
	request.TabID, _ = args["tab_id"].(string)
	request.FrameID, _ = args["frame_id"].(string)
	generation, generationOK := browserInteger(args["context_generation"])
	if _, present := args["context_generation"]; present && (!generationOK || generation < 1) {
		return browser.ContextPreparation{}, browser.ErrInvalid
	}
	request.ContextGeneration = uint64(generation)
	source, available := tool.runtime.contextSource()
	if !available {
		return browser.ContextPreparation{}, browser.ErrDriverIncompatible
	}
	return source.PrepareContext(ctx, request)
}

func browserContextApprovalSummary(preparation browser.ContextPreparation) string {
	return fmt.Sprintf(
		"Allow browser close action with unknown effect for tab %q?",
		preparation.Request.TabID,
	)
}

func (*BrowserObserveTool) Name() string { return "browser_observe" }
func (*BrowserObserveTool) Description() string {
	return "Observe the current page as a bounded accessibility snapshot with scoped element references and optionally retain a PNG screenshot. " +
		"Before repeating a collection search, verify that its scope can contain the target: inactive, expired, deleted, or historical items require all, old, history, or archive views. " +
		"After one empty or mismatched search, widen scope; when the snapshot is truncated, ambiguous, or follows a no-progress action, request one bounded screenshot."
}

func (*BrowserObserveTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"browser_session_id": map[string]any{"type": "string"},
			"tab_id":             map[string]any{"type": "string"},
			"frame_id":           map[string]any{"type": "string"},
			"context_catalog_id": map[string]any{"type": "string"},
			"context_generation": map[string]any{"type": "integer"},
			"screenshot":         map[string]any{"type": "boolean"},
		},
		"required": []string{"browser_session_id"}, "additionalProperties": false,
	}
}

func (*BrowserObserveTool) ToolLoopSemantics() loopguard.Semantics {
	// Observe is page-read-only, but it advances snapshot authority and session
	// activity, so it is not runtime-idempotent.
	return loopguard.SemanticsMutating
}

// Accessibility snapshots are live page data and may include values entered
// by an earlier protected fill. They are intentionally ephemeral even when
// the observe arguments themselves are non-sensitive.
func (*BrowserObserveTool) DurableArguments(args map[string]any) (map[string]any, error) {
	return cloneBrowserToolArguments(args)
}

func (*BrowserObserveTool) ProtectedDurableResult(map[string]any) bool { return true }

type browserObservationView struct {
	BrowserSessionID   string                      `json:"browser_session_id"`
	TabID              string                      `json:"tab_id"`
	FrameID            string                      `json:"frame_id,omitempty"`
	ContextCatalogID   string                      `json:"context_catalog_id,omitempty"`
	ContextGeneration  uint64                      `json:"context_generation,omitempty"`
	SnapshotID         string                      `json:"snapshot_id"`
	SnapshotGeneration uint64                      `json:"snapshot_generation"`
	URL                string                      `json:"url"`
	Origin             string                      `json:"origin"`
	Title              string                      `json:"title,omitempty"`
	Snapshot           string                      `json:"snapshot"`
	Tabs               []browserTabView            `json:"tabs"`
	PendingDialog      *browser.DialogObservation  `json:"pending_dialog,omitempty"`
	Truncated          bool                        `json:"truncated"`
	Limits             browserObservationLimits    `json:"limits"`
	Artifact           *browser.ScreenshotArtifact `json:"artifact,omitempty"`
	Replayed           bool                        `json:"replayed,omitempty"`
	StaleRecovered     bool                        `json:"stale_recovered,omitempty"`
	PageStateHash      string                      `json:"page_state_hash,omitempty"`
}

type browserObservationLimits struct {
	SnapshotBytes   int `json:"snapshot_bytes"`
	SnapshotRefs    int `json:"snapshot_refs"`
	ScreenshotBytes int `json:"screenshot_bytes"`
}

func (runtime *browserToolRuntime) observationResult(observation browser.Observation) browserObservationView {
	limits := runtime.config.Limits.Effective()
	return browserObservationView{
		BrowserSessionID: observation.SessionID, TabID: observation.TabID,
		FrameID: observation.FrameID, ContextCatalogID: observation.ContextCatalogID,
		ContextGeneration: observation.ContextGeneration,
		SnapshotID:        observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		URL: observation.URL, Origin: observation.Origin, Title: observation.Title,
		Snapshot: observation.Snapshot, PendingDialog: observation.PendingDialog,
		PageStateHash: observation.PageStateHash,
		Tabs: []browserTabView{{
			TabID: observation.TabID, SnapshotID: observation.SnapshotID,
			SnapshotGeneration: observation.SnapshotGeneration,
		}},
		Truncated: observation.Truncated,
		Limits: browserObservationLimits{
			SnapshotBytes: limits.SnapshotBytes, SnapshotRefs: limits.SnapshotRefs,
			ScreenshotBytes: limits.ScreenshotBytes,
		},
	}
}

func (tool *BrowserObserveTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	if !tool.runtime.enabledForAgent(toolshared.ToolAgentID(ctx)) {
		return browserErrorResult(
			"not_granted",
			"Browser access is not granted to this agent.",
			"use_an_authorized_agent",
		)
	}
	owner, err := browserOwnerFromContext(ctx)
	if err != nil {
		return browserToolError(err)
	}
	sessionID, ok := args["browser_session_id"].(string)
	if !ok {
		return browserErrorResult("invalid_request", "browser_session_id is required.", "correct_arguments")
	}
	wantScreenshot, _ := args["screenshot"].(bool)
	requestID := ""
	if wantScreenshot {
		if !tool.runtime.source.ScreenshotAvailable() {
			return browserErrorResult(
				"unsupported_platform",
				"Browser screenshot delivery is unavailable on this gateway platform.",
				"omit_screenshot",
			)
		}
		if !toolshared.ToolRecoverableOutbound(ctx) {
			return browserErrorResult(
				"delivery_unavailable",
				"Browser screenshots require a durable outbound delivery transaction.",
				"retry_from_a_routed_turn",
			)
		}
		requestID, err = browserRequestID(ctx)
		if err != nil {
			return browserToolError(err)
		}
		artifact, found, lookupErr := tool.runtime.source.LookupScreenshot(
			ctx, owner, requestID, sessionID,
		)
		if lookupErr != nil {
			return browserToolError(lookupErr)
		}
		if found {
			limits := tool.runtime.config.Limits.Effective()
			return tool.screenshotResult(ctx, browserObservationView{
				BrowserSessionID: artifact.SessionID, TabID: artifact.TabID,
				SnapshotID: artifact.SnapshotID, SnapshotGeneration: artifact.SnapshotGeneration,
				Truncated: false, Replayed: true, Artifact: &artifact,
				Limits: browserObservationLimits{
					SnapshotBytes: limits.SnapshotBytes, SnapshotRefs: limits.SnapshotRefs,
					ScreenshotBytes: limits.ScreenshotBytes,
				},
			}, owner, requestID, artifact)
		}
	}
	tabID, _ := args["tab_id"].(string)
	tabSupplied := strings.TrimSpace(tabID) != ""
	frameID, _ := args["frame_id"].(string)
	catalogID, _ := args["context_catalog_id"].(string)
	contextGeneration, contextGenerationOK := browserInteger(args["context_generation"])
	if _, present := args["context_generation"]; present &&
		(!contextGenerationOK || contextGeneration < 1) {
		return browserToolError(browser.ErrInvalid)
	}
	var observation browser.Observation
	staleRecovered := false
	contextSource, contextAvailable := tool.runtime.contextSource()
	explicitContext := frameID != "" || catalogID != "" || contextGeneration != 0
	var observedSession browser.Session
	if tabID == "" || !explicitContext {
		session, statusErr := tool.runtime.source.Status(ctx, owner, sessionID)
		if statusErr != nil {
			return browserToolError(statusErr)
		}
		if tabID == "" {
			tabID = session.TabID
		}
		if tabSupplied && strings.TrimSpace(session.TabID) != "" && session.TabID != tabID {
			return browserContextToolError(browser.ErrStale)
		}
		observedSession = session
	}
	if explicitContext {
		if !contextAvailable {
			return browserToolError(browser.ErrDriverIncompatible)
		}
		observation, err = contextSource.ObserveContext(ctx, browser.ObserveRequest{
			Owner: owner, SessionID: sessionID, TabID: tabID, FrameID: frameID,
			ContextCatalogID: catalogID, ContextGeneration: uint64(contextGeneration),
		})
	} else {
		if contextAvailable {
			observation, err = contextSource.ObserveContext(
				ctx,
				browserObserveRequestForSession(owner, sessionID, tabID, observedSession),
			)
		} else {
			observation, err = tool.runtime.source.Observe(ctx, owner, sessionID, tabID)
		}
		if errors.Is(err, browser.ErrStale) {
			if strings.TrimSpace(observedSession.FrameID) != "" {
				return browserContextToolError(browser.ErrStale)
			}
			if !contextAvailable {
				return browserToolError(browser.ErrStale)
			}
			refreshedSession, statusErr := tool.runtime.source.Status(ctx, owner, sessionID)
			if statusErr != nil {
				return browserToolError(statusErr)
			}
			if strings.TrimSpace(refreshedSession.FrameID) != "" {
				return browserContextToolError(browser.ErrStale)
			}
			retryTabID := tabID
			if !tabSupplied {
				retryTabID = refreshedSession.TabID
			} else if strings.TrimSpace(refreshedSession.TabID) != "" && refreshedSession.TabID != tabID {
				return browserContextToolError(browser.ErrStale)
			}
			if strings.TrimSpace(retryTabID) == "" {
				return browserContextToolError(browser.ErrStale)
			}
			observation, err = contextSource.ObserveContext(
				ctx,
				browserObserveRequestForSession(owner, sessionID, retryTabID, refreshedSession),
			)
			if err == nil {
				staleRecovered = true
			}
			if errors.Is(err, browser.ErrStale) {
				return browserErrorResult(
					"stale_snapshot",
					"Browser observation remained stale after one read-only refresh.",
					"list_contexts_again",
				)
			}
		}
	}
	if err != nil {
		if explicitContext {
			return browserContextToolError(err)
		}
		return browserToolError(err)
	}
	view := tool.runtime.observationResult(observation)
	view.StaleRecovered = staleRecovered
	if !wantScreenshot {
		return tool.runtime.result(view)
	}
	artifact, err := tool.runtime.source.CaptureScreenshot(ctx, browser.ScreenshotRequest{
		Owner: owner, RequestID: requestID, SessionID: observation.SessionID,
		TabID: observation.TabID, FrameID: observation.FrameID,
		ContextCatalogID:  observation.ContextCatalogID,
		ContextGeneration: observation.ContextGeneration,
		SnapshotID:        observation.SnapshotID, SnapshotGeneration: observation.SnapshotGeneration,
		Target: browser.ScreenshotTargetPage,
	})
	if err != nil {
		return browserToolError(err)
	}
	view.Artifact = &artifact
	return tool.screenshotResult(ctx, view, owner, requestID, artifact)
}

func browserObserveRequestForSession(
	owner browser.Owner,
	sessionID string,
	tabID string,
	session browser.Session,
) browser.ObserveRequest {
	request := browser.ObserveRequest{
		Owner: owner, SessionID: sessionID, TabID: tabID, FrameID: session.FrameID,
	}
	if session.ContextAuthority != nil {
		request.ContextCatalogID = session.ContextAuthority.ID
		request.ContextGeneration = session.ContextAuthority.Generation
	}
	return request
}

func (tool *BrowserObserveTool) screenshotResult(
	ctx context.Context,
	view browserObservationView,
	owner browser.Owner,
	requestID string,
	artifact browser.ScreenshotArtifact,
) *toolshared.ToolResult {
	result := tool.runtime.result(view)
	if result.IsError || !toolshared.ToolRecoverableOutbound(ctx) ||
		(artifact.DeliveryState != browser.ScreenshotDeliveryPending &&
			artifact.DeliveryState != browser.ScreenshotDeliveryAlreadyClaimed) ||
		artifact.MediaRef == "" {
		return result
	}
	if artifact.Recovery == nil {
		return browserErrorResult(
			"delivery_unavailable",
			"Browser screenshot recovery metadata is unavailable.",
			"retry_observation",
		)
	}
	deliveryRecovery := *artifact.Recovery
	delivery := browser.ScreenshotDeliveryRequest{
		Owner: owner, RequestID: requestID, SessionID: artifact.SessionID,
		Ref: artifact.Ref, MediaRef: artifact.MediaRef, Recovery: &deliveryRecovery,
	}
	recovery := artifact.Recovery
	result.Media = []string{artifact.MediaRef}
	return result.WithOutboundDelivery(toolshared.OutboundDelivery{
		Media: []bus.MediaPart{{
			Type: "image", Ref: artifact.MediaRef, Filename: artifact.Filename,
			ContentType: artifact.ContentType,
		}},
		Recovery: &bus.OutboundRecovery{
			Kind:        bus.OutboundRecoveryBrowserScreenshot,
			ArtifactRef: artifact.Ref, MediaRef: artifact.MediaRef,
			WorkspaceID: recovery.WorkspaceID, AgentID: recovery.AgentID,
			ActorID: recovery.ActorID, RouteID: recovery.RouteID,
			SessionID: recovery.SessionID, ToolCallID: recovery.ToolCallID,
		},
	}).WithOutboundCommit(func(commitCtx context.Context) error {
		return tool.runtime.source.ClaimScreenshotDelivery(commitCtx, delivery)
	}).WithDeliveryIntent(toolshared.DeliveryImmediateContinue)
}

func (*BrowserCaptureTool) Name() string { return "browser_capture" }
func (*BrowserCaptureTool) Description() string {
	return "Capture one retained PNG for an exact fresh browser observation, either the page or one semantic element reference."
}

func (*BrowserCaptureTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}
func (*BrowserCaptureTool) ProtectedDurableResult(map[string]any) bool { return true }
func (*BrowserCaptureTool) DurableArguments(args map[string]any) (map[string]any, error) {
	return cloneBrowserToolArguments(args)
}

func (*BrowserCaptureTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{
			"browser_session_id", "tab_id", "snapshot_id", "snapshot_generation", "target",
		},
		"properties": map[string]any{
			"browser_session_id":  map[string]any{"type": "string"},
			"tab_id":              map[string]any{"type": "string"},
			"frame_id":            map[string]any{"type": "string"},
			"context_catalog_id":  map[string]any{"type": "string"},
			"context_generation":  map[string]any{"type": "integer"},
			"snapshot_id":         map[string]any{"type": "string"},
			"snapshot_generation": map[string]any{"type": "integer"},
			"target":              map[string]any{"type": "string", "enum": []string{"page", "element"}},
			"ref":                 map[string]any{"type": "string"},
		},
	}
}

type browserCaptureView struct {
	Artifact browser.ScreenshotArtifact `json:"artifact"`
	Replayed bool                       `json:"replayed,omitempty"`
}

func (tool *BrowserCaptureTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	if !tool.runtime.enabledForAgent(toolshared.ToolAgentID(ctx)) {
		return browserErrorResult(
			"not_granted",
			"Browser access is not granted to this agent.",
			"use_an_authorized_agent",
		)
	}
	if !tool.runtime.source.ScreenshotAvailable() {
		return browserErrorResult(
			"unsupported_platform",
			"Browser screenshot delivery is unavailable.",
			"choose_another_target",
		)
	}
	if !toolshared.ToolRecoverableOutbound(ctx) {
		return browserErrorResult(
			"delivery_unavailable",
			"Browser screenshots require a durable outbound delivery transaction.",
			"retry_from_a_routed_turn",
		)
	}
	owner, err := browserOwnerFromContext(ctx)
	if err != nil {
		return browserToolError(err)
	}
	requestID, err := browserRequestID(ctx)
	if err != nil {
		return browserToolError(err)
	}
	sessionID, sessionOK := args["browser_session_id"].(string)
	tabID, tabOK := args["tab_id"].(string)
	snapshotID, snapshotOK := args["snapshot_id"].(string)
	targetValue, targetOK := args["target"].(string)
	snapshotGeneration, generationOK := browserInteger(args["snapshot_generation"])
	contextGeneration, contextGenerationOK := browserInteger(args["context_generation"])
	frameID, _ := args["frame_id"].(string)
	contextID, _ := args["context_catalog_id"].(string)
	ref, _ := args["ref"].(string)
	target := browser.ScreenshotTarget(targetValue)
	if !sessionOK || sessionID == "" || !tabOK || tabID == "" || !snapshotOK || snapshotID == "" ||
		!targetOK || (target != browser.ScreenshotTargetPage && target != browser.ScreenshotTargetElement) ||
		!generationOK || snapshotGeneration < 1 ||
		(target == browser.ScreenshotTargetPage && ref != "") ||
		(target == browser.ScreenshotTargetElement && ref == "") ||
		(frameID != "" && contextID == "") ||
		(contextID != "" && (!contextGenerationOK || contextGeneration < 1)) ||
		(contextID == "" && contextGeneration != 0) {
		return browserToolError(browser.ErrInvalid)
	}
	if artifact, found, lookupErr := tool.runtime.source.LookupScreenshot(
		ctx,
		owner,
		requestID,
		sessionID,
	); lookupErr != nil {
		return browserToolError(lookupErr)
	} else if found {
		return tool.result(ctx, owner, requestID, artifact, true)
	}
	artifact, err := tool.runtime.source.CaptureScreenshot(ctx, browser.ScreenshotRequest{
		Owner: owner, RequestID: requestID, SessionID: sessionID, TabID: tabID,
		FrameID: frameID, ContextCatalogID: contextID, ContextGeneration: uint64(contextGeneration),
		SnapshotID: snapshotID, SnapshotGeneration: uint64(snapshotGeneration), Target: target, Ref: ref,
	})
	if err != nil {
		return browserToolError(err)
	}
	return tool.result(ctx, owner, requestID, artifact, false)
}

func (tool *BrowserCaptureTool) result(
	ctx context.Context,
	owner browser.Owner,
	requestID string,
	artifact browser.ScreenshotArtifact,
	replayed bool,
) *toolshared.ToolResult {
	result := tool.runtime.result(browserCaptureView{Artifact: artifact, Replayed: replayed})
	if result.IsError || artifact.MediaRef == "" || artifact.Recovery == nil ||
		(artifact.DeliveryState != browser.ScreenshotDeliveryPending &&
			artifact.DeliveryState != browser.ScreenshotDeliveryAlreadyClaimed) {
		return result
	}
	recovery := artifact.Recovery
	delivery := browser.ScreenshotDeliveryRequest{
		Owner: owner, RequestID: requestID, SessionID: artifact.SessionID,
		Ref: artifact.Ref, MediaRef: artifact.MediaRef, Recovery: recovery,
	}
	result.Media = []string{artifact.MediaRef}
	return result.WithOutboundDelivery(toolshared.OutboundDelivery{
		Media: []bus.MediaPart{
			{Type: "image", Ref: artifact.MediaRef, Filename: artifact.Filename, ContentType: artifact.ContentType},
		},
		Recovery: &bus.OutboundRecovery{
			Kind: bus.OutboundRecoveryBrowserScreenshot, ArtifactRef: artifact.Ref, MediaRef: artifact.MediaRef,
			WorkspaceID: recovery.WorkspaceID, AgentID: recovery.AgentID, ActorID: recovery.ActorID,
			RouteID: recovery.RouteID, SessionID: recovery.SessionID, ToolCallID: recovery.ToolCallID,
		},
	}).WithOutboundCommit(func(commitCtx context.Context) error {
		return tool.runtime.source.ClaimScreenshotDelivery(commitCtx, delivery)
	}).WithDeliveryIntent(toolshared.DeliveryImmediateContinue)
}

func (*BrowserActTool) Name() string { return "browser_act" }
func (*BrowserActTool) Description() string {
	return "Prepare and execute exactly one fresh-reference browser action. For every click, declare its workflow effect: " +
		"read, navigation, or local_edit executes without human approval; external_commit or unknown suspends for durable approval. " +
		"For non-click actions, omit effect; a redundant value is ignored because the broker derives the fixed effect. " +
		"Classify from the user request and runtime objective checklist, not from the element role or HTTP method. " +
		"Use external_commit immediately before an important external state change such as publishing, submitting an order, " +
		"sending, deleting, or replying; use navigation for ordinary page/tab/form-step transitions. " +
		"Copy the session, tab, frame, context catalog, context generation, snapshot, and snapshot generation " +
		"from one fresh browser_observe result. When that result contains context_catalog_id and " +
		"context_generation, copy both together; missing or incomplete context authority fails closed. " +
		"A third equivalent effect-tracked action in a repeated one-state or alternating two-state loop is rejected before " +
		"another approval and requires replanning."
}

func (tool *BrowserActTool) Parameters() map[string]any {
	limits := config.BrowserLimitsConfig{}.Effective()
	actions := browseraction.Kinds()
	fileChooserAvailable, downloadAvailable := false, false
	if tool != nil && tool.runtime != nil {
		limits = tool.runtime.config.Limits.Effective()
		downloadAvailable = tool.runtime.source.ArtifactTransferAvailable() &&
			tool.runtime.source.DownloadAvailable()
		fileChooserAvailable = tool.runtime.fileChooserAvailable()
	}
	actions = slices.DeleteFunc(actions, func(action browseraction.ActionKind) bool {
		return (action == browseraction.ActionFileChooser || action == browseraction.ActionUpload) &&
			!fileChooserAvailable ||
			action == browseraction.ActionDownload && !downloadAvailable
	})
	actionSchema := browseraction.Schema(actions, limits.TextInputBytes, false)
	actionSchema["description"] = "Use only fields belonging to the selected action kind; do not add unrelated action fields."
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"browser_session_id": map[string]any{
				"type":        "string",
				"description": "Copy exactly from the same fresh browser_observe result used for this action.",
			},
			"tab_id": map[string]any{
				"type":        "string",
				"description": "Copy exactly from the same fresh browser_observe result used for this action.",
			},
			"frame_id": map[string]any{
				"type":        "string",
				"description": "Copy exactly when present in the fresh browser_observe result; otherwise omit.",
			},
			"context_catalog_id": map[string]any{
				"type":        "string",
				"description": "Conditionally required: copy exactly when present in the fresh browser_observe result; otherwise omit.",
			},
			"context_generation": map[string]any{
				"type":        "integer",
				"description": "Conditionally required: copy exactly when context_catalog_id is present in the fresh browser_observe result; otherwise omit.",
			},
			"snapshot_id": map[string]any{
				"type":        "string",
				"description": "Copy exactly from the same fresh browser_observe result used for this action.",
			},
			"snapshot_generation": map[string]any{
				"type":        "integer",
				"description": "Copy exactly from the same fresh browser_observe result used for this action.",
			},
			"action": actionSchema,
			"effect": map[string]any{
				"type": "string",
				"enum": []string{"read", "navigation", "local_edit", "external_commit", "unknown"},
				"description": "Required for click and ignored for other action kinds. Declare workflow impact: " +
					"external_commit only immediately before an important external state change; unknown when genuinely unsure.",
			},
		},
		"required": []string{
			"browser_session_id", "tab_id", "snapshot_id", "snapshot_generation", "action",
		},
		"additionalProperties": false,
	}
}

func (runtime *browserToolRuntime) fileChooserAvailable() bool {
	if runtime == nil || runtime.source == nil || !runtime.source.ArtifactTransferAvailable() {
		return false
	}
	for _, target := range runtime.config.Targets {
		if target.Enabled {
			return true
		}
	}
	return false
}
func (*BrowserActTool) ToolLoopSemantics() loopguard.Semantics { return loopguard.SemanticsMutating }

const browserProtectedInputRedaction = "*"

// DurableArguments removes protected fill and dialog-prompt text before assistant intent can be
// persisted or reused. It deliberately leaves the current in-memory call
// untouched so the broker can consume the value exactly once.
func (tool *BrowserActTool) DurableArguments(args map[string]any) (map[string]any, error) {
	limits := config.BrowserLimitsConfig{}.Effective()
	if tool != nil && tool.runtime != nil {
		limits = tool.runtime.config.Limits.Effective()
	}
	if _, err := browseraction.DecodeModelAction(args["action"], limits.TextInputBytes); err != nil {
		return nil, fmt.Errorf("validate browser action before durable projection: %w", err)
	}
	projected, err := tool.CanonicalArguments(args)
	if err != nil {
		return nil, err
	}
	action, ok := projected["action"].(map[string]any)
	if !ok {
		return nil, errors.New("browser action is unavailable")
	}
	kind, ok := action["kind"].(string)
	if !ok || kind == "" {
		return nil, errors.New("browser action kind is unavailable")
	}
	protectedInput := kind == "fill"
	if kind == "dialog" {
		_, protectedInput = action["value"]
	}
	if !protectedInput {
		return projected, nil
	}
	if _, ok = action["value"].(string); !ok {
		return nil, errors.New("browser protected value is unavailable")
	}
	action["value"] = browserProtectedInputRedaction
	return projected, nil
}

// CanonicalArguments treats provider-emitted null optional context authority
// exactly like omission while retaining a cloned execution map.
func (*BrowserActTool) CanonicalArguments(args map[string]any) (map[string]any, error) {
	projected, err := cloneBrowserToolArguments(args)
	if err != nil {
		return nil, err
	}
	// Providers sometimes encode omitted optional context authority as JSON
	// null. The live action path already treats those values as absent; make
	// the durable projection canonical before schema validation so persistence
	// does not reject an otherwise valid top-level page action.
	for _, field := range []string{"frame_id", "context_catalog_id", "context_generation"} {
		if value, present := projected[field]; present && value == nil {
			delete(projected, field)
		}
	}
	action, _ := projected["action"].(map[string]any)
	kind, _ := action["kind"].(string)
	if kind != string(browser.ActionClick) {
		delete(projected, "effect")
	}
	return projected, nil
}

// Fill and a dialog prompt are the actions whose model-authored arguments
// contain protected input. Keep singleton batching and assistant-envelope
// stripping scoped to those intents.
func (*BrowserActTool) ProtectedDurableArguments(args map[string]any) bool {
	action, _ := args["action"].(map[string]any)
	kind, _ := action["kind"].(string)
	if kind == "fill" {
		return true
	}
	_, promptProvided := action["value"]
	return kind == "dialog" && promptProvided
}

// Every action may return a fresh page observation containing data from a
// protected fill. Keep that live result out of durable state independently of
// whether the current action arguments are sensitive.
func (*BrowserActTool) ProtectedDurableResult(map[string]any) bool { return true }

func cloneBrowserToolArguments(args map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var projected map[string]any
	if err = json.Unmarshal(encoded, &projected); err != nil {
		return nil, err
	}
	return projected, nil
}

func (tool *BrowserActTool) ApprovalArguments(ctx context.Context, args map[string]any) (map[string]any, error) {
	if !tool.runtime.enabledForAgent(toolshared.ToolAgentID(ctx)) {
		return nil, &browserActionSafeDenialError{cause: browser.ErrDenied}
	}
	preparation, err := tool.prepare(ctx, args)
	if err != nil {
		return nil, &browserActionSafeDenialError{cause: err}
	}
	return map[string]any{
		"prepared_action_id": preparation.Approval.PreparedActionID,
		"action_hash":        preparation.Approval.ActionHash,
		"policy_revision":    preparation.Approval.PolicyRevision,
		"expires_at":         preparation.Approval.ExpiresAt,
		"preview":            browserApprovalSummary(preparation),
	}, nil
}

type browserActionResult struct {
	InvocationID  string                      `json:"invocation_id"`
	Effect        browser.Effect              `json:"effect"`
	State         browser.InvocationState     `json:"state"`
	Reason        string                      `json:"reason,omitempty"`
	FailureClass  browser.OutcomeFailureClass `json:"failure_class,omitempty"`
	Observation   *browserObservationView     `json:"observation,omitempty"`
	Artifact      *browser.DownloadArtifact   `json:"artifact,omitempty"`
	ArtifactState string                      `json:"artifact_state,omitempty"`
}

func (tool *BrowserActTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	if !tool.runtime.enabledForAgent(toolshared.ToolAgentID(ctx)) {
		return browserErrorResult(
			"not_granted",
			"Browser access is not granted to this agent.",
			"use_an_authorized_agent",
		)
	}
	preparation, err := tool.prepare(ctx, args)
	if err != nil {
		return browserActionToolError(err)
	}
	if preparation.RequiresApproval &&
		!toolshared.ToolApprovalContinuation(ctx) && !toolshared.ToolApprovalBypass(ctx) {
		return &toolshared.ToolResult{
			Control: toolshared.ToolControl{Suspension: &interactions.SuspensionRequest{
				Kind:          interactions.KindApproval,
				PromptSummary: browserApprovalSummary(preparation),
				Timeout:       time.Duration(tool.runtime.config.Limits.Effective().PreparedSeconds) * time.Second,
			}},
			Delivery: toolshared.ToolDelivery{Intent: toolshared.DeliverySilent},
		}
	}
	var approval *browser.ApprovalBinding
	if preparation.RequiresApproval {
		binding := preparation.Approval
		approval = &binding
	}
	owner, err := browserOwnerFromContext(ctx)
	if err != nil {
		return browserActionToolError(err)
	}
	invocation, err := tool.runtime.source.ExecuteAction(
		ctx, owner, preparation.Action.ID, approval,
	)
	if err != nil {
		if errors.Is(err, browser.ErrSnapshotInvalidation) || invocation.AcceptedAt != 0 {
			result := browserPostActionStateError(
				invocation,
				errors.Is(err, browser.ErrSnapshotInvalidation),
			)
			attachBrowserExternalCommitAudit(result, invocation, preparation, tool.Name())
			return result
		}
		return browserActionToolError(err)
	}
	result := browserActionResult{
		InvocationID: invocation.ID, Effect: invocation.Effect,
		State: invocation.State, Reason: invocation.SafeFailure,
	}
	if invocation.Diagnostic != nil {
		result.FailureClass = invocation.Diagnostic.FailureClass
	}
	result.Artifact = invocation.Download
	if preparation.Action.Action.Kind == browser.ActionDownload {
		result.ArtifactState = "unavailable"
		if invocation.Download != nil {
			result.ArtifactState = "committed"
		}
	}
	if invocation.State == browser.InvocationSucceeded {
		var observation browser.Observation
		var observeErr error
		contextSource, contextAvailable := tool.runtime.contextSource()
		if preparation.Action.ContextCatalogID != "" && contextAvailable {
			observation, observeErr = contextSource.ObserveContext(ctx, browser.ObserveRequest{
				Owner: owner, SessionID: invocation.SessionID, TabID: preparation.Action.TabID,
				FrameID:           preparation.Action.FrameID,
				ContextCatalogID:  preparation.Action.ContextCatalogID,
				ContextGeneration: preparation.Action.ContextGeneration,
			})
		} else {
			observation, observeErr = tool.runtime.source.Observe(
				ctx, owner, invocation.SessionID, preparation.Action.TabID,
			)
		}
		if observeErr == nil {
			view := tool.runtime.observationResult(observation)
			result.Observation = &view
		}
	}
	toolResult := tool.runtime.result(result)
	attachBrowserExternalCommitAudit(toolResult, invocation, preparation, tool.Name())
	if invocation.Download == nil || !invocation.Download.Deliver {
		return toolResult
	}
	artifact := invocation.Download
	if artifact.MediaRef == "" || artifact.Recovery == nil ||
		(artifact.DeliveryState != browser.ScreenshotDeliveryPending &&
			artifact.DeliveryState != browser.ScreenshotDeliveryAlreadyClaimed) {
		return browserErrorResult(
			"delivery_unavailable", "Browser download delivery is unavailable.", "retry_from_a_routed_turn",
		)
	}
	recovery := artifact.Recovery
	delivery := browser.DownloadDeliveryRequest{
		Owner: owner, RequestID: preparation.Action.RequestID, SessionID: artifact.SessionID,
		Ref: artifact.Ref, MediaRef: artifact.MediaRef, Recovery: recovery,
	}
	return toolResult.WithOutboundDelivery(toolshared.OutboundDelivery{
		Media: []bus.MediaPart{{
			Type: "file", Ref: artifact.MediaRef, Filename: artifact.Filename, ContentType: artifact.ContentType,
		}},
		Recovery: &bus.OutboundRecovery{
			Kind: bus.OutboundRecoveryBrowserDownload, ArtifactRef: artifact.Ref, MediaRef: artifact.MediaRef,
			WorkspaceID: recovery.WorkspaceID, AgentID: recovery.AgentID, ActorID: recovery.ActorID,
			RouteID: recovery.RouteID, SessionID: recovery.SessionID, ToolCallID: recovery.ToolCallID,
		},
	}).WithOutboundCommit(func(commitCtx context.Context) error {
		return tool.runtime.source.ClaimDownloadDelivery(commitCtx, delivery)
	}).WithDeliveryIntent(toolshared.DeliveryImmediateContinue)
}

func attachBrowserExternalCommitAudit(
	result *toolshared.ToolResult,
	invocation browser.Invocation,
	preparation browser.Preparation,
	toolName string,
) {
	if result == nil || invocation.State != browser.InvocationSucceeded ||
		invocation.Effect != browser.EffectExternalCommit {
		return
	}
	result.WithWriteAudit(toolshared.WriteAuditEntry{
		Kind: "external_action", Target: preparation.Action.CurrentOrigin,
		Action: string(preparation.Action.Action.Kind), Tool: toolName,
		Summary: "browser external action committed",
		Metadata: map[string]string{
			"invocation_id": invocation.ID, "browser_session_id": invocation.SessionID,
			"effect": string(invocation.Effect), "element_role": preparation.Action.ElementRole,
		},
	})
}

func browserPostActionStateError(invocation browser.Invocation, quarantined bool) *toolshared.ToolResult {
	action, reason := "do_not_retry_check_session", "state_persistence_failed"
	if quarantined {
		action, reason = "do_not_retry_reopen_session", "session_quarantined"
	}
	encoded, _ := json.Marshal(map[string]any{
		"status":         "failed",
		"code":           "post_action_state_unavailable",
		"message":        "The browser action reached a terminal state, but fresh snapshot authority could not be persisted.",
		"action":         action,
		"invocation_id":  invocation.ID,
		"effect":         invocation.Effect,
		"state":          invocation.State,
		"reason":         reason,
		"outcome_reason": invocation.SafeFailure,
		"failure_class":  invocationFailureClass(invocation),
	})
	return toolshared.ErrorResult(string(encoded))
}

func invocationFailureClass(invocation browser.Invocation) browser.OutcomeFailureClass {
	if invocation.Diagnostic == nil {
		return ""
	}
	return invocation.Diagnostic.FailureClass
}

func (tool *BrowserActTool) prepare(ctx context.Context, args map[string]any) (browser.Preparation, error) {
	owner, err := browserOwnerFromContext(ctx)
	if err != nil {
		return browser.Preparation{}, err
	}
	requestID, err := browserRequestID(ctx)
	if err != nil {
		return browser.Preparation{}, err
	}
	action, err := browseraction.DecodeModelAction(
		args["action"],
		tool.runtime.config.Limits.Effective().TextInputBytes,
	)
	if err != nil {
		return browser.Preparation{}, err
	}
	declaredEffect := browser.Effect("")
	rawEffect, effectPresent := args["effect"]
	if action.Kind == browser.ActionClick {
		effect, ok := rawEffect.(string)
		declaredEffect = browser.Effect(effect)
		if !effectPresent || !ok || !declaredEffect.Valid() {
			return browser.Preparation{}, browser.ErrInvalid
		}
	}
	if action.Kind == browser.ActionDownload && action.Deliver && !toolshared.ToolRecoverableOutbound(ctx) {
		return browser.Preparation{}, browser.ErrDenied
	}
	if (action.Kind == browser.ActionFileChooser || action.Kind == browser.ActionUpload) &&
		!tool.runtime.source.ArtifactTransferAvailable() {
		return browser.Preparation{}, browser.ErrDriverIncompatible
	}
	if action.Kind == browser.ActionDownload &&
		(!tool.runtime.source.ArtifactTransferAvailable() || !tool.runtime.source.DownloadAvailable()) {
		return browser.Preparation{}, browser.ErrDriverIncompatible
	}
	sessionID, sessionOK := args["browser_session_id"].(string)
	tabID, tabOK := args["tab_id"].(string)
	frameID, _ := args["frame_id"].(string)
	catalogID, _ := args["context_catalog_id"].(string)
	contextGeneration, contextGenerationOK := browserInteger(args["context_generation"])
	if _, present := args["context_generation"]; present &&
		(!contextGenerationOK || contextGeneration < 1) {
		return browser.Preparation{}, browser.ErrInvalid
	}
	snapshotID, snapshotOK := args["snapshot_id"].(string)
	generation, generationOK := browserInteger(args["snapshot_generation"])
	if !sessionOK || !tabOK || !snapshotOK || !generationOK || generation < 1 {
		return browser.Preparation{}, browser.ErrInvalid
	}
	return tool.runtime.source.PrepareAction(ctx, browser.PrepareActionRequest{
		Owner: owner, RequestID: requestID, SessionID: sessionID, TabID: tabID,
		FrameID: frameID, ContextCatalogID: catalogID, ContextGeneration: uint64(contextGeneration),
		SnapshotID: snapshotID, SnapshotGeneration: uint64(generation), Action: action,
		DeclaredEffect: declaredEffect,
	})
}

func browserInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), int64(int(typed)) == typed
	case float64:
		integer := int(typed)
		return integer, typed == float64(integer)
	default:
		return 0, false
	}
}

func browserApprovalSummary(preparation browser.Preparation) string {
	action := preparation.Action
	origin := action.CurrentOrigin
	if action.DestinationOrigin != "" {
		origin = action.DestinationOrigin
	}
	return fmt.Sprintf(
		"%s on %s; effect: `%s`",
		browserApprovalDescription(action),
		origin,
		action.Effect,
	)
}

func browserApprovalDescription(action browser.PreparedAction) string {
	switch action.Action.Kind {
	case browser.ActionDialog:
		description := browserDialogApprovalVerb(action.Action.Decision) + " " + action.DialogType + " dialog"
		if action.Action.PromptProvided {
			description += " with prompt input provided"
		}
		return description
	case browser.ActionPress:
		return fmt.Sprintf("Press document key %q", action.Action.Key)
	}

	description := "Browser " + string(action.Action.Kind) + " action"
	if action.ElementRole != "" {
		description = browserApprovalVerb(action.Action.Kind) + " " + action.ElementRole
		if action.ElementName != "" {
			description += fmt.Sprintf(" %q", action.ElementName)
		}
		if action.Action.Kind == browser.ActionDrag {
			description += " to " + action.DestinationElementRole
			if action.DestinationElementName != "" {
				description += fmt.Sprintf(" %q", action.DestinationElementName)
			}
		}
	}
	return description
}

func browserDialogApprovalVerb(decision string) string {
	if decision == "accept" {
		return "Accept"
	}
	return "Dismiss"
}

func browserApprovalVerb(kind browser.ActionKind) string {
	switch kind {
	case browser.ActionClick:
		return "Click"
	case browser.ActionDrag:
		return "Drag"
	case browser.ActionDownload:
		return "Download"
	default:
		return "Use"
	}
}

func browserOwnerFromContext(ctx context.Context) (browser.Owner, error) {
	actorID := strings.TrimSpace(toolshared.ToolActorID(ctx))
	if actorID == "" {
		actorID = strings.TrimSpace(toolshared.ToolSenderID(ctx))
	}
	agentID := strings.TrimSpace(toolshared.ToolAgentID(ctx))
	sessionKey := strings.TrimSpace(toolshared.ToolRouteSessionKey(ctx))
	if sessionKey == "" {
		sessionKey = strings.TrimSpace(toolshared.ToolSessionKey(ctx))
	}
	executionID := strings.TrimSpace(toolshared.ToolExecutionID(ctx))
	if actorID == "" || agentID == "" || sessionKey == "" || executionID == "" {
		return browser.Owner{}, errors.New("browser tool context is incomplete")
	}
	return browser.Owner{
		ActorID:     browserContextID("actor", actorID),
		AgentID:     browser.OpaqueAgentID(routing.NormalizeAgentID(agentID)),
		SessionKey:  browserContextID("session", sessionKey),
		ExecutionID: browserContextID("execution", executionID),
	}, nil
}

func browserRequestID(ctx context.Context) (string, error) {
	callID := strings.TrimSpace(toolshared.ToolCallID(ctx))
	executionID := strings.TrimSpace(toolshared.ToolExecutionID(ctx))
	if callID == "" || executionID == "" {
		return "", errors.New("browser tool call identity is incomplete")
	}
	return browserContextID("request", executionID+"\x00"+callID), nil
}

func browserContextID(prefix, value string) string {
	digest := sha256.Sum256([]byte(prefix + "\x00" + value))
	return prefix + "_" + hex.EncodeToString(digest[:16])
}

func (runtime *browserToolRuntime) result(value any) *toolshared.ToolResult {
	encoded, err := json.Marshal(value)
	if err != nil {
		return browserErrorResult("result_unavailable", "Browser result could not be encoded.", "retry")
	}
	limit := runtime.config.Limits.Effective().ToolResultBytes
	if len(encoded) > limit {
		return browserErrorResult("result_too_large", "Browser result exceeded the configured limit.", "observe_again")
	}
	return toolshared.NewToolResult(string(encoded))
}

type browserErrorView struct {
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Action  string `json:"action"`
}

func browserErrorResult(code, message, action string) *toolshared.ToolResult {
	encoded, _ := json.Marshal(browserErrorView{
		Status: "denied", Code: code, Message: message, Action: action,
	})
	return toolshared.ErrorResult(string(encoded))
}

func browserToolError(err error) *toolshared.ToolResult {
	switch {
	case errors.Is(err, browser.ErrBusy):
		return browserErrorResult("profile_busy", "The browser profile is already in use.", "close_or_wait")
	case errors.Is(err, browser.ErrNotFound):
		return browserErrorResult("not_found", "The browser session or action was not found.", "open_session")
	case errors.Is(err, browser.ErrStale):
		return browserErrorResult("stale_snapshot", "Browser authority is stale.", "observe_again")
	case errors.Is(err, browser.ErrDenied):
		return browserErrorResult("policy_denied", "Browser policy denied the operation.", "choose_allowed_action")
	case errors.Is(err, browser.ErrApprovalRequired):
		return browserErrorResult("approval_required", "The browser action requires human approval.", "ask_operator")
	case errors.Is(err, browser.ErrInvalid):
		return browserErrorResult("invalid_request", "The browser request is invalid.", "correct_arguments")
	case errors.Is(err, browser.ErrConflict):
		return browserErrorResult("state_conflict", "Browser state changed concurrently.", "observe_again")
	case errors.Is(err, browser.ErrDriverIncompatible):
		return browserErrorResult("driver_incompatible", "The browser driver is incompatible.", "contact_operator")
	case errors.Is(err, browser.ErrWorkerUnavailable), errors.Is(err, browser.ErrDriverRejected):
		return browserErrorResult("driver_unavailable", "The browser driver is unavailable.", "retry_or_reopen")
	default:
		return browserErrorResult("runtime_unavailable", "Browser automation is unavailable.", "retry")
	}
}

func browserActionToolError(err error) *toolshared.ToolResult {
	if errors.Is(err, browser.ErrNoProgress) {
		return browserErrorResult(
			"no_progress",
			"Equivalent browser actions did not change page state.",
			"replan_collection_scope",
		)
	}
	if errors.Is(err, browser.ErrStale) {
		return browserErrorResult(
			"stale_snapshot",
			"Browser action authority is stale. Observe again and copy every returned authority field into the action.",
			"observe_again_and_copy_authority",
		)
	}
	return browserToolError(err)
}

func browserContextToolError(err error) *toolshared.ToolResult {
	switch {
	case errors.Is(err, browser.ErrStale):
		return browserErrorResult(
			"context_catalog_stale", "Browser context authority is stale.", "list_contexts_again",
		)
	case errors.Is(err, browser.ErrNotFound):
		return browserErrorResult("tab_not_found", "The browser tab was not found.", "list_contexts_again")
	case errors.Is(err, browser.ErrDriverIncompatible):
		return browserErrorResult(
			"context_unsupported", "Browser contexts are unavailable for this target.", "choose_supported_target",
		)
	default:
		return browserToolError(err)
	}
}

type browserSafeDenialError struct{ cause error }

func (err *browserSafeDenialError) Error() string { return "browser approval preparation denied" }
func (err *browserSafeDenialError) Unwrap() error { return err.cause }
func (err *browserSafeDenialError) SafeApprovalDenialResult() *toolshared.ToolResult {
	return browserToolError(err.cause)
}

type browserActionSafeDenialError struct{ cause error }

func (err *browserActionSafeDenialError) Error() string {
	return "browser action approval preparation denied"
}
func (err *browserActionSafeDenialError) Unwrap() error { return err.cause }
func (err *browserActionSafeDenialError) SafeApprovalDenialResult() *toolshared.ToolResult {
	return browserActionToolError(err.cause)
}
