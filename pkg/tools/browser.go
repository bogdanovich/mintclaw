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
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/identity"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
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

type BrowserDiagnosticsToolSource interface {
	Diagnostics(context.Context, browser.DiagnosticsRequest) (browser.DiagnosticSummary, error)
}

type browserTurnCleanupSource interface {
	CloseOwner(context.Context, browser.Owner) error
}

type browserAttachedConsentSource interface {
	AttachedConsentBinding(
		context.Context, browser.Owner, string, string,
	) (browser.AttachConsentBinding, error)
}

// BrowserTargetDiagnostics is one gateway-owned readiness and capability
// snapshot. Implementations must compute every field while holding the same
// runtime generation so discovery cannot combine stale capability flags with
// unavailable readiness.
type BrowserTargetDiagnostics struct {
	Profiles    map[string]browser.PassiveReadiness
	Actions     []browser.ActionKind
	Screenshot  bool
	Upload      bool
	Download    bool
	HeadedView  bool
	Handoff     bool
	Contexts    bool
	Diagnostics bool
}

type browserToolRuntime struct {
	config        config.BrowserToolsConfig
	source        BrowserToolSource
	allowedAgents map[string]struct{}
}

// BrowserToolOptions is the immutable browser-policy snapshot consumed by one
// runtime-tool generation. Construct a replacement when configuration reloads.
type BrowserToolOptions struct {
	config        config.BrowserToolsConfig
	allowedAgents map[string]struct{}
}

type (
	BrowserTargetsTool     struct{ runtime *browserToolRuntime }
	BrowserSessionTool     struct{ runtime *browserToolRuntime }
	BrowserContextsTool    struct{ runtime *browserToolRuntime }
	BrowserObserveTool     struct{ runtime *browserToolRuntime }
	BrowserCaptureTool     struct{ runtime *browserToolRuntime }
	BrowserDiagnosticsTool struct{ runtime *browserToolRuntime }
	BrowserActTool         struct{ runtime *browserToolRuntime }
)

func NewBrowserTargetsTool(options BrowserToolOptions, source BrowserToolSource) *BrowserTargetsTool {
	return &BrowserTargetsTool{runtime: newBrowserToolRuntime(options, source)}
}

func NewBrowserSessionTool(options BrowserToolOptions, source BrowserToolSource) *BrowserSessionTool {
	return &BrowserSessionTool{runtime: newBrowserToolRuntime(options, source)}
}

func NewBrowserDiagnosticsTool(options BrowserToolOptions, source BrowserToolSource) *BrowserDiagnosticsTool {
	return &BrowserDiagnosticsTool{runtime: newBrowserToolRuntime(options, source)}
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

func NewBrowserObserveTool(options BrowserToolOptions, source BrowserToolSource) *BrowserObserveTool {
	return &BrowserObserveTool{runtime: newBrowserToolRuntime(options, source)}
}

func NewBrowserCaptureTool(options BrowserToolOptions, source BrowserToolSource) *BrowserCaptureTool {
	return &BrowserCaptureTool{runtime: newBrowserToolRuntime(options, source)}
}

func NewBrowserContextsTool(options BrowserToolOptions, source BrowserToolSource) *BrowserContextsTool {
	return &BrowserContextsTool{runtime: newBrowserToolRuntime(options, source)}
}

func NewBrowserActTool(options BrowserToolOptions, source BrowserToolSource) *BrowserActTool {
	return &BrowserActTool{runtime: newBrowserToolRuntime(options, source)}
}

func NewBrowserToolOptions(cfg config.BrowserToolsConfig) BrowserToolOptions {
	snapshot := cfg
	snapshot.Agents = append([]string(nil), cfg.Agents...)
	snapshot.DefaultTarget = cfg.EffectiveDefaultTarget()
	snapshot.Limits = cfg.Limits.Effective()
	snapshot.Targets = make(map[string]config.BrowserTargetConfig, len(cfg.Targets))
	for targetName, target := range cfg.Targets {
		target.Placement = target.EffectivePlacement()
		target.Profiles = make(map[string]config.BrowserProfileConfig, len(target.Profiles))
		for profileName, profile := range cfg.Targets[targetName].Profiles {
			profile.AllowedAgents = append([]string(nil), profile.AllowedAgents...)
			profile.AllowedActors = append([]string(nil), profile.AllowedActors...)
			profile.AllowedOrigins = append([]string(nil), profile.AllowedOrigins...)
			profile.Attached.AllowedOrigins = append(
				[]string(nil), profile.Attached.AllowedOrigins...,
			)
			profile.Policy = browserpolicy.ClonePolicy(profile.Policy)
			target.Profiles[profileName] = profile
		}
		snapshot.Targets[targetName] = target
	}

	options := BrowserToolOptions{
		config:        snapshot,
		allowedAgents: make(map[string]struct{}, len(snapshot.Agents)),
	}
	for _, agentID := range snapshot.Agents {
		options.allowedAgents[routing.NormalizeAgentID(agentID)] = struct{}{}
	}
	return options
}

func newBrowserToolRuntime(options BrowserToolOptions, source BrowserToolSource) *browserToolRuntime {
	return &browserToolRuntime{
		config:        options.config,
		source:        source,
		allowedAgents: options.allowedAgents,
	}
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

func (tool *BrowserDiagnosticsTool) ToolEnabledForAgent(agentID string) bool {
	return tool != nil && tool.runtime.enabledForAgent(agentID)
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
	return "List browser targets and identity profiles granted to this agent and actor without starting a browser. " +
		"When the task does not name a target, use default_target when present; never infer preference from array order."
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
	DefaultTarget string              `json:"default_target,omitempty"`
	Targets       []browserTargetView `json:"targets"`
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
	Mode                 string                   `json:"mode"`
	Persistence          string                   `json:"persistence"`
	Status               string                   `json:"status"`
	Reason               string                   `json:"reason,omitempty"`
	NetworkMode          string                   `json:"network_mode"`
	CapabilityMode       string                   `json:"capability_mode"`
	ApprovalMode         string                   `json:"approval_mode"`
	DryRun               bool                     `json:"dry_run"`
	AllowApprovedActions bool                     `json:"allow_approved_actions"`
	HeadedView           bool                     `json:"headed_view"`
	Handoff              bool                     `json:"handoff"`
	AttachConsent        bool                     `json:"attach_consent"`
	ActionOriginMode     string                   `json:"action_origin_mode,omitempty"`
	NetworkBoundary      string                   `json:"network_boundary"`
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
	agentID := strings.TrimSpace(toolshared.ToolAgentID(ctx))
	actorID := browserCanonicalActorID(ctx)
	if !tool.runtime.enabledForAgent(agentID) || actorID == "" {
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
	defaultTarget := tool.runtime.config.EffectiveDefaultTarget()
	sort.Slice(targetNames, func(i, j int) bool {
		leftDefault := targetNames[i] == defaultTarget
		rightDefault := targetNames[j] == defaultTarget
		if leftDefault != rightDefault {
			return leftDefault
		}
		return targetNames[i] < targetNames[j]
	})
	views := make([]browserTargetView, 0, len(targetNames))
	for _, name := range targetNames {
		target := tool.runtime.config.Targets[name]
		profileNames := make([]string, 0, len(target.Profiles))
		for profileName, profile := range target.Profiles {
			if profile.Enabled && browserProfileGranted(profile, agentID, actorID) {
				profileNames = append(profileNames, profileName)
			}
		}
		if len(profileNames) == 0 {
			continue
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
			attached := profile.Mode == config.BrowserProfileAttachedUser
			networkBoundary := "managed_request_proxy"
			if attached {
				networkBoundary = "selected_top_level_action_only"
			}
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
				Profile:              profileName,
				Mode:                 profile.Mode,
				Persistence:          browserProfilePersistence(profile.Mode),
				Status:               status,
				Reason:               reason,
				NetworkMode:          profile.NetworkMode,
				CapabilityMode:       profile.CapabilityMode,
				ApprovalMode:         profile.ApprovalMode,
				DryRun:               profile.DryRun,
				AllowApprovedActions: profile.AllowApprovedActions,
				HeadedView:           attached || profile.Runtime.Headed,
				Handoff: !attached && profile.Mode == config.BrowserProfileManaged &&
					profile.Runtime.Headed,
				AttachConsent:    attached && profile.Attached.ConsentMode == config.BrowserAttachedConsentSession,
				ActionOriginMode: profile.Attached.ActionOriginMode,
				NetworkBoundary:  networkBoundary,
				Readiness:        readiness,
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
		popupsAvailable := contextsAvailable && slices.ContainsFunc(profiles, func(profile browserProfileView) bool {
			return profile.Mode != config.BrowserProfileAttachedUser &&
				browserContextProfileUsable(profile.Readiness)
		})
		framesPerTab, frameDepth, contextCatalogBytes, contextLabelBytes := 0, 0, 0, 0
		if contextsAvailable {
			framesPerTab = browser.MaxContextFramesPerTab
			frameDepth = browser.MaxContextFrameDepth
			contextCatalogBytes = browser.MaxContextCatalogBytes
			contextLabelBytes = browser.MaxContextLabelBytes
		}
		screenshotAvailable := capabilitiesAvailable && diagnostics.Screenshot
		headedViewAvailable := capabilitiesAvailable && (diagnostics.HeadedView || slices.ContainsFunc(
			profiles,
			func(profile browserProfileView) bool { return profile.HeadedView },
		))
		views = append(views, browserTargetView{
			Target: name, Status: targetStatus, Reason: targetReason, Profiles: profiles,
			Actions: actions,
			Features: browserFeatureView{
				Tabs:       contextsAvailable,
				Popups:     popupsAvailable,
				Frames:     contextsAvailable,
				Screenshot: screenshotAvailable, PageScreenshot: screenshotAvailable,
				ElementScreenshot: screenshotAvailable,
				Upload:            uploadAvailable,
				Download:          downloadAvailable,
				Diagnostics:       capabilitiesAvailable && diagnostics.Diagnostics,
				HeadedView:        headedViewAvailable,
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
	if !slices.ContainsFunc(views, func(view browserTargetView) bool {
		return view.Target == defaultTarget
	}) {
		defaultTarget = ""
	}
	return tool.runtime.result(browserTargetResult{DefaultTarget: defaultTarget, Targets: views})
}

func browserProfileGranted(profile config.BrowserProfileConfig, agentID, actorID string) bool {
	return slices.Contains(profile.AllowedAgents, routing.NormalizeAgentID(agentID)) &&
		slices.Contains(profile.AllowedActors, actorID)
}

func browserProfilePersistence(mode string) string {
	switch mode {
	case config.BrowserProfileManaged:
		return "retained"
	case "ephemeral":
		return "session_only"
	case "attached_user":
		return "user_owned"
	default:
		return "unknown"
	}
}

func browserContextProfileUsable(readiness browser.PassiveReadiness) bool {
	switch readiness.Status {
	case browser.ReadinessReady, browser.ReadinessConfigured:
		return readiness.Profile.Status == browser.ReadinessReady
	case browser.ReadinessBusy:
		return readiness.Profile.Status == browser.ReadinessBusy &&
			readiness.Profile.Reason == "profile_busy"
	default:
		return false
	}
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
	return "Open, inspect, close, hand off, or resume one broker-owned browser session. " +
		"For open, target is the browser target name from browser_targets; when the task does not name one, " +
		"use browser_targets.default_target and never infer preference from target array order. " +
		"For open, profile is the profile name nested under that target (for example managed). " +
		"For open, interaction_language is required and must match the natural language of the user request " +
		"that led to the browser operation, ignoring internal English instructions. Handoff pauses agent control, " +
		"gives the user the same visible local browser window, keeps the session open, and waits. Supply a " +
		"self-contained handoff_prompt in " +
		"the user's language that includes any useful result already found and clearly asks for the input needed " +
		"next. Use handoff for sign-in, 2FA, CAPTCHA, another manual browser step, or when the user explicitly asks " +
		"you to keep the browser open and wait for their next instruction. After the user replies, call resume on " +
		"the same session, then observe fresh state before continuing automation."
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
				"description": "For open only: exact target returned by browser_targets. When the task does not name one, copy browser_targets.default_target; do not infer preference from array order.",
			},
			"profile": map[string]any{
				"type":        "string",
				"description": "For open only: exact profile name listed inside the selected browser target, such as managed.",
			},
			"interaction_language": map[string]any{
				"type":      "string",
				"maxLength": interactions.MaxPromptLanguageLength,
				"description": "For open only: BCP-47 language tag matching the natural language of the user's " +
					"request, such as en or ru. Required so an attached-browser approval uses the user's language.",
			},
			"browser_session_id": map[string]any{
				"type":        "string",
				"description": "For status, close, handoff, and resume only: broker-issued browser session ID. Handoff and resume preserve the same live browser and managed profile.",
			},
			"handoff_prompt": browserHandoffPromptSchema(),
		},
		"required": []string{"operation"}, "additionalProperties": false,
	}
}

func (*BrowserSessionTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsMutating
}

func (*BrowserSessionTool) ObjectiveRecoveryParameters(kind string) (map[string]any, bool) {
	if strings.TrimSpace(kind) != taskresult.ObjectiveKindLiveHandoff {
		return nil, false
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation": map[string]any{"type": "string", "enum": []string{"handoff"}},
			"browser_session_id": map[string]any{
				"type":        "string",
				"description": "Broker-issued ID of the existing live browser session to hand to the user.",
			},
			"handoff_prompt": browserHandoffPromptSchema(),
		},
		"required":             []string{"operation", "browser_session_id", "handoff_prompt"},
		"additionalProperties": false,
	}, true
}

func browserHandoffPromptSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"description": "Required for handoff. Write all user-facing text in the language and style of the " +
			"user request. Include useful results already found before asking what the user should do next.",
		"properties": map[string]any{
			"header": map[string]any{
				"type":        "string",
				"maxLength":   interactions.MaxHeaderLength,
				"description": "Optional short user-facing label in the user's language and style.",
			},
			"question": map[string]any{
				"type":      "string",
				"maxLength": interactions.MaxQuestionLength,
				"description": "Self-contained user-facing message in the user's language. Include useful " +
					"results already found and explicitly ask for the input that will resume this same session.",
			},
			"options": map[string]any{
				"type":     "array",
				"minItems": 2,
				"maxItems": interactions.MaxOptions,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"label": map[string]any{
							"type":        "string",
							"maxLength":   interactions.MaxOptionLabelLength,
							"description": "Short user-facing choice label in the user's language and style.",
						},
						"description": map[string]any{
							"type":        "string",
							"maxLength":   interactions.MaxDescriptionLength,
							"description": "One user-facing sentence in the user's language describing the choice.",
						},
					},
					"required": []string{"label", "description"},
				},
			},
		},
		"required": []string{"question"},
	}
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
	Tabs                 []browserTabView        `json:"tabs,omitempty"`
	Reason               string                  `json:"reason,omitempty"`
}

type browserTabView struct {
	TabID              string `json:"tab_id"`
	SnapshotID         string `json:"snapshot_id,omitempty"`
	SnapshotGeneration uint64 `json:"snapshot_generation,omitempty"`
}

func browserSessionResult(session browser.Session) browserSessionView {
	tabs := []browserTabView{browserTabResult(session)}
	if session.State == browser.SessionAttachPending {
		tabs = nil
	}
	return browserSessionView{
		BrowserSessionID: session.ID, State: session.State, Target: session.Target,
		Profile: session.Profile, DryRun: session.DryRun,
		ControllerGeneration: session.ControllerGeneration, ExpiresAt: session.ExpiresAt,
		Controller: session.EffectiveController(), ControllerExpiresAt: session.ControllerExpiresAt,
		Tabs:   tabs,
		Reason: session.SafeFailure,
	}
}

func browserTabResult(session browser.Session) browserTabView {
	return browserTabView{
		TabID: session.TabID, SnapshotID: session.SnapshotID,
		SnapshotGeneration: session.SnapshotGeneration,
	}
}

func (tool *BrowserSessionTool) ApprovalArguments(
	ctx context.Context,
	args map[string]any,
) (map[string]any, error) {
	operation, _ := args["operation"].(string)
	targetName, _ := args["target"].(string)
	profileName, _ := args["profile"].(string)
	profile, attached := tool.attachedProfile(targetName, profileName)
	if operation != "open" || !attached {
		return cloneBrowserToolArguments(args)
	}
	promptLanguage, err := interactions.CanonicalPromptLanguage(
		browserStringArgument(args, "interaction_language"),
	)
	if err != nil {
		return nil, err
	}
	owner, err := browserOwnerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	source, ok := tool.runtime.source.(browserAttachedConsentSource)
	if !ok {
		return nil, browser.ErrWorkerUnavailable
	}
	binding, err := source.AttachedConsentBinding(ctx, owner, targetName, profileName)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"operation": "open", "target": targetName, "profile": profileName,
		"interaction_language": promptLanguage,
		"browser_session_id":   binding.SessionID,
		"profile_revision":     binding.ProfileRevision,
		"policy_revision":      binding.PolicyRevision,
		"connector_generation": binding.Generation,
		"expires_at":           binding.ExpiresAt,
		"consent_mode":         profile.Attached.ConsentMode,
	}, nil
}

func (tool *BrowserSessionTool) attachedProfile(
	targetName string,
	profileName string,
) (config.BrowserProfileConfig, bool) {
	if tool == nil || tool.runtime == nil {
		return config.BrowserProfileConfig{}, false
	}
	target, ok := tool.runtime.config.Targets[targetName]
	if !ok || !target.Enabled {
		return config.BrowserProfileConfig{}, false
	}
	profile, ok := target.Profiles[profileName]
	return profile, ok && profile.Enabled && profile.Mode == config.BrowserProfileAttachedUser
}

func approvedBrowserAttachConsent(
	ctx context.Context,
	target string,
	profile string,
	promptLanguage string,
) (*browser.AttachConsentBinding, error) {
	arguments, ok := toolshared.ToolApprovalArguments(ctx)
	if !ok || len(arguments) != 10 || arguments["operation"] != "open" ||
		arguments["target"] != target || arguments["profile"] != profile ||
		arguments["interaction_language"] != promptLanguage ||
		arguments["consent_mode"] != config.BrowserAttachedConsentSession {
		return nil, browser.ErrConsentExpired
	}
	sessionID, sessionOK := arguments["browser_session_id"].(string)
	profileRevision, profileRevisionOK := arguments["profile_revision"].(string)
	policyRevision, policyRevisionOK := arguments["policy_revision"].(string)
	generation, generationOK := arguments["connector_generation"].(uint64)
	expiresAt, expiresAtOK := arguments["expires_at"].(int64)
	if !sessionOK || !profileRevisionOK || !policyRevisionOK || !generationOK || !expiresAtOK {
		return nil, browser.ErrConsentExpired
	}
	return &browser.AttachConsentBinding{
		SessionID: sessionID, Target: target, Profile: profile,
		ProfileRevision: profileRevision, PolicyRevision: policyRevision,
		Generation: generation, ExpiresAt: expiresAt,
	}, nil
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
	var promptLanguage string
	switch operation {
	case "open":
		target, targetOK := args["target"].(string)
		profile, profileOK := args["profile"].(string)
		var languageErr error
		promptLanguage, languageErr = interactions.CanonicalPromptLanguage(
			browserStringArgument(args, "interaction_language"),
		)
		if !targetOK || !profileOK || languageErr != nil || len(args) != 4 {
			return browserErrorResult(
				"invalid_request",
				"Open requires exactly target, profile, and a valid interaction_language.",
				"correct_arguments",
			)
		}
		var attachConsent *browser.AttachConsentBinding
		if _, attached := tool.attachedProfile(target, profile); attached &&
			toolshared.ToolApprovalContinuation(ctx) {
			attachConsent, err = approvedBrowserAttachConsent(ctx, target, profile, promptLanguage)
			if err != nil {
				return browserToolError(err)
			}
		}
		session, err = tool.runtime.source.Open(ctx, browser.OpenRequest{
			Owner: owner, Target: target, Profile: profile, AttachConsent: attachConsent,
		})
	case "status", "close", "resume":
		sessionID, ok := args["browser_session_id"].(string)
		if !ok || len(args) != 2 {
			return browserErrorResult(
				"invalid_request",
				"Status, close, and resume require exactly browser_session_id.",
				"correct_arguments",
			)
		}
		switch operation {
		case "status":
			session, err = tool.runtime.source.Status(ctx, owner, sessionID)
		case "close":
			session, err = tool.runtime.source.Close(ctx, owner, sessionID)
		default:
			session, err = tool.runtime.source.Resume(ctx, owner, sessionID)
		}
	case "handoff":
		sessionID, ok := args["browser_session_id"].(string)
		question, questionErr := parseBrowserHandoffPrompt(args["handoff_prompt"])
		if !ok || questionErr != nil || len(args) != 3 {
			return browserErrorResult(
				"invalid_request",
				"Handoff requires exactly browser_session_id and a valid handoff_prompt.",
				"correct_arguments",
			)
		}
		if !tool.runtime.source.HandoffAvailable() {
			return browserToolError(browser.ErrDriverIncompatible)
		}
		session, err = tool.runtime.source.Handoff(ctx, owner, sessionID)
		if err == nil {
			return tool.browserHandoffResult(owner, session, question)
		}
	default:
		return browserErrorResult("invalid_request", "Unknown browser session operation.", "correct_arguments")
	}
	if err != nil {
		return browserToolError(err)
	}
	result := tool.runtime.result(browserSessionResult(session))
	if operation == "open" && session.State == browser.SessionAttachPending {
		profile, attached := tool.attachedProfile(session.Target, session.Profile)
		if !attached || toolshared.ToolApprovalContinuation(ctx) {
			return browserToolError(browser.ErrConsentExpired)
		}
		result.Control.Suspension = &interactions.SuspensionRequest{
			Kind:           interactions.KindApproval,
			PromptSummary:  interactions.PromptText(promptLanguage, interactions.PromptBrowserAttachAction),
			PromptLanguage: promptLanguage,
			Timeout:        time.Duration(profile.Attached.ConsentSeconds) * time.Second,
		}
		result.Delivery.Intent = toolshared.DeliverySilent
	}
	return result
}

func parseBrowserHandoffPrompt(raw any) (interactions.Question, error) {
	prompt, ok := raw.(map[string]any)
	if !ok {
		return interactions.Question{}, errors.New("handoff_prompt must be an object")
	}
	if len(prompt) < 1 || len(prompt) > 3 {
		return interactions.Question{}, errors.New("handoff_prompt contains unexpected fields")
	}
	withID := make(map[string]any, len(prompt)+1)
	for key, value := range prompt {
		if key != "header" && key != "question" && key != "options" {
			return interactions.Question{}, fmt.Errorf("handoff_prompt contains unexpected field %q", key)
		}
		withID[key] = value
	}
	withID["id"] = "release_browser"
	questions, err := parseInteractionQuestions([]any{withID})
	if err != nil {
		return interactions.Question{}, err
	}
	request := interactions.SuspensionRequest{
		Kind: interactions.KindQuestion, Questions: questions, Timeout: time.Minute,
	}
	if err := interactions.ValidateSuspensionRequest(request); err != nil {
		return interactions.Question{}, err
	}
	return questions[0], nil
}

func browserStringArgument(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func (tool *BrowserSessionTool) browserHandoffResult(
	owner browser.Owner,
	session browser.Session,
	question interactions.Question,
) *toolshared.ToolResult {
	result := tool.runtime.result(browserSessionResult(session))
	if result == nil || result.IsError {
		return result
	}
	handoff := toolshared.LiveResourceHandoff{
		ResourceKind: "browser_session",
		ResourceID:   session.ID,
	}
	result.Control.LiveHandoff = &handoff
	result.Control.Suspension = &interactions.SuspensionRequest{
		Kind:          interactions.KindQuestion,
		Questions:     []interactions.Question{question},
		PromptSummary: question.Question,
		Timeout:       time.Duration(tool.runtime.config.Limits.Effective().PreparedSeconds) * time.Second,
	}
	result.Control.ResolveSuspension = func(resolutionCtx context.Context, outcome interactions.Outcome) error {
		return tool.resolveLiveResourceHandoffForOwner(
			resolutionCtx,
			owner,
			handoff,
			toolshared.LiveResourceHandoffDispositionForOutcome(outcome),
		)
	}
	return result
}

// ResolveLiveResourceHandoff implements the durable, idempotent handoff
// binding used after interaction or gateway restart.
func (tool *BrowserSessionTool) ResolveLiveResourceHandoff(
	ctx context.Context,
	handoff toolshared.LiveResourceHandoff,
	disposition toolshared.LiveResourceHandoffDisposition,
) error {
	if tool == nil || tool.runtime == nil || tool.runtime.source == nil ||
		strings.TrimSpace(handoff.ResourceKind) != "browser_session" ||
		strings.TrimSpace(handoff.ResourceID) == "" {
		return errors.New("browser live-resource handoff binding is invalid")
	}
	owner, err := browserOwnerFromContext(ctx)
	if err != nil {
		return err
	}
	return tool.resolveLiveResourceHandoffForOwner(ctx, owner, handoff, disposition)
}

func (tool *BrowserSessionTool) resolveLiveResourceHandoffForOwner(
	ctx context.Context,
	owner browser.Owner,
	handoff toolshared.LiveResourceHandoff,
	disposition toolshared.LiveResourceHandoffDisposition,
) error {
	sessionID := strings.TrimSpace(handoff.ResourceID)
	if disposition != toolshared.LiveResourceHandoffResume {
		_, closeErr := tool.runtime.source.Close(ctx, owner, sessionID)
		return closeErr
	}
	released, releaseErr := tool.runtime.source.ReleaseHandoff(ctx, owner, sessionID)
	if releaseErr == nil {
		if released.State == browser.SessionReady && released.Controller == browser.ControllerResumePending {
			return nil
		}
		releaseErr = fmt.Errorf(
			"browser live-resource handoff release returned unusable state %q with controller %q",
			released.State,
			released.Controller,
		)
	}
	status, statusErr := tool.runtime.source.Status(context.WithoutCancel(ctx), owner, sessionID)
	if statusErr == nil && status.State == browser.SessionReady &&
		(status.Controller == browser.ControllerResumePending || status.Controller == browser.ControllerAgent) {
		return nil
	}
	_, closeErr := tool.runtime.source.Close(context.WithoutCancel(ctx), owner, sessionID)
	return errors.Join(releaseErr, statusErr, closeErr)
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
			"confirmation": map[string]any{
				"type": "string", "enum": []string{browserpolicy.ConfirmationRequest},
				"description": "Optional. Request owner confirmation for this exact tab mutation.",
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
	confirmation, _ := args["confirmation"].(string)
	if operation != string(browser.ContextClose) && confirmation != browserpolicy.ConfirmationRequest {
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
	request.Confirmation, _ = args["confirmation"].(string)
	if _, present := args["confirmation"]; present &&
		request.Confirmation != browserpolicy.ConfirmationRequest {
		return browser.ContextPreparation{}, browser.ErrInvalid
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
		"Allow browser %s action with %s effect for tab %q?",
		preparation.Request.Operation,
		preparation.Invocation.Effect,
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

func (*BrowserDiagnosticsTool) Name() string { return "browser_diagnostics" }
func (*BrowserDiagnosticsTool) Description() string {
	return "Return bounded privacy-safe console-error, failed-request, and page-crash summaries for one owned browser session."
}

func (*BrowserDiagnosticsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"browser_session_id": map[string]any{
				"type": "string", "minLength": 1,
				"description": "Owned browser session. Diagnostics never open or renew a session.",
			},
			"categories": map[string]any{
				"type": "array", "minItems": 1, "maxItems": 3, "uniqueItems": true,
				"description": "Privacy-safe diagnostic categories to return in canonical order.",
				"items": map[string]any{"type": "string", "enum": []string{
					"console_errors", "failed_requests", "page_crashes",
				}},
			},
			"tab_id": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Optional fresh tab authority; when supplied, copy all snapshot authority from one browser_observe result.",
			},
			"frame_id": map[string]any{
				"type": "string", "minLength": 1,
				"description": "Optional fresh frame authority copied with the tab and snapshot binding.",
			},
			"context_catalog_id": map[string]any{
				"type": "string", "minLength": 1,
				"description": "Optional fresh context catalog authority copied with context_generation.",
			},
			"context_generation": map[string]any{
				"type": "integer", "minimum": 1,
				"description": "Required when context_catalog_id is supplied.",
			},
			"snapshot_id": map[string]any{
				"type": "string", "minLength": 1,
				"description": "Required when tab_id is supplied; copy from the same fresh observation.",
			},
			"snapshot_generation": map[string]any{
				"type": "integer", "minimum": 1,
				"description": "Required when tab_id is supplied; copy from the same fresh observation.",
			},
		},
		"required": []string{"browser_session_id", "categories"},
	}
}

func (*BrowserDiagnosticsTool) ToolLoopSemantics() loopguard.Semantics {
	return loopguard.SemanticsReadOnlyIdempotent
}

func (*BrowserDiagnosticsTool) ProtectedDurableResult(map[string]any) bool { return true }

func (*BrowserDiagnosticsTool) DurableArguments(args map[string]any) (map[string]any, error) {
	return cloneBrowserToolArguments(args)
}

func (tool *BrowserDiagnosticsTool) Execute(
	ctx context.Context,
	args map[string]any,
) *toolshared.ToolResult {
	if !tool.runtime.enabledForAgent(toolshared.ToolAgentID(ctx)) {
		return browserErrorResult(
			"not_granted", "Browser access is not granted to this agent.", "use_an_authorized_agent",
		)
	}
	source, ok := tool.runtime.source.(BrowserDiagnosticsToolSource)
	if !ok {
		return browserToolError(browser.ErrDriverIncompatible)
	}
	owner, err := browserOwnerFromContext(ctx)
	if err != nil {
		return browserToolError(err)
	}
	sessionID, sessionOK := args["browser_session_id"].(string)
	categories, categoriesOK := browserDiagnosticCategories(args["categories"])
	if !sessionOK || !categoriesOK {
		return browserToolError(browser.ErrInvalid)
	}
	request := browser.DiagnosticsRequest{Owner: owner, SessionID: sessionID, Categories: categories}
	request.TabID, _ = args["tab_id"].(string)
	request.FrameID, _ = args["frame_id"].(string)
	request.ContextCatalogID, _ = args["context_catalog_id"].(string)
	request.SnapshotID, _ = args["snapshot_id"].(string)
	contextGeneration, contextOK := browserInteger(args["context_generation"])
	snapshotGeneration, snapshotOK := browserInteger(args["snapshot_generation"])
	if _, present := args["context_generation"]; present && (!contextOK || contextGeneration < 1) {
		return browserToolError(browser.ErrInvalid)
	}
	if _, present := args["snapshot_generation"]; present && (!snapshotOK || snapshotGeneration < 1) {
		return browserToolError(browser.ErrInvalid)
	}
	request.ContextGeneration = uint64(contextGeneration)
	request.SnapshotGeneration = uint64(snapshotGeneration)
	summary, err := source.Diagnostics(ctx, request)
	if err != nil {
		return browserToolError(err)
	}
	return tool.runtime.result(summary)
}

func browserDiagnosticCategories(value any) ([]browser.DiagnosticCategory, bool) {
	var values []string
	switch typed := value.(type) {
	case []string:
		values = append(values, typed...)
	case []any:
		values = make([]string, 0, len(typed))
		for _, item := range typed {
			value, ok := item.(string)
			if !ok {
				return nil, false
			}
			values = append(values, value)
		}
	default:
		return nil, false
	}
	categories := make([]browser.DiagnosticCategory, len(values))
	for index, value := range values {
		categories[index] = browser.DiagnosticCategory(value)
	}
	normalized, err := browser.NormalizeDiagnosticCategories(categories)
	return normalized, err == nil
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
		"effect is audit and recovery metadata; it does not by itself grant authority or determine confirmation. " +
		"Read the effective approval_mode from browser_targets: none never pauses, model_requested pauses only when " +
		"confirmation=request, and always_commit pauses external_commit or unknown actions plus explicit requests. " +
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
				"type":      "string",
				"minLength": 1,
				"description": "Optional. Copy exactly only when present in the fresh browser_observe result. " +
					"Never invent a placeholder; otherwise omit both context_catalog_id and context_generation.",
			},
			"context_generation": map[string]any{
				"type":    "integer",
				"minimum": 1,
				"description": "Optional. Copy exactly only when context_catalog_id is also present in the same fresh browser_observe result. " +
					"Never use zero or another placeholder; otherwise omit both fields.",
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
			"confirmation": map[string]any{
				"type":        "string",
				"enum":        []string{browserpolicy.ConfirmationRequest},
				"description": "Optional. Set to request only when the user asked to confirm this exact action before execution.",
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

var errBrowserActionContextAuthority = fmt.Errorf(
	"%w: browser action context authority is incomplete or invalid",
	browser.ErrInvalid,
)

// DurableArguments removes protected fill and dialog-prompt text before assistant intent can be
// persisted or reused. It deliberately leaves the current in-memory call
// untouched so the broker can consume the value exactly once.
func (tool *BrowserActTool) DurableArguments(args map[string]any) (map[string]any, error) {
	limits := config.BrowserLimitsConfig{}.Effective()
	if tool != nil && tool.runtime != nil {
		limits = tool.runtime.config.Limits.Effective()
	}
	projected, err := tool.CanonicalArguments(args)
	if err != nil {
		return nil, err
	}
	if _, err = browseraction.DecodeModelAction(projected["action"], limits.TextInputBytes); err != nil {
		return nil, fmt.Errorf("validate browser action before durable projection: %w", err)
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

// CanonicalArguments treats provider-emitted null optional fields exactly like
// omission while retaining a cloned execution map. Compatibility schema
// transforms flatten the action union for providers that cannot consume oneOf;
// those providers can consequently emit null placeholders for fields belonging
// to another action kind. Removing only null placeholders restores the strict
// action shape without admitting a non-null cross-kind value.
func (*BrowserActTool) CanonicalArguments(args map[string]any) (map[string]any, error) {
	projected, err := cloneBrowserToolArguments(args)
	if err != nil {
		return nil, err
	}
	// Providers sometimes encode omitted optional context authority as JSON
	// null. The live action path already treats those values as absent; make
	// the durable projection canonical before schema validation so persistence
	// does not reject an otherwise valid top-level page action.
	for _, field := range []string{
		"frame_id", "context_catalog_id", "context_generation", "effect", "confirmation",
	} {
		if value, present := projected[field]; present && value == nil {
			delete(projected, field)
		}
	}
	action, _ := projected["action"].(map[string]any)
	for field, value := range action {
		if value == nil {
			delete(action, field)
		}
	}
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

// SafeSchemaValidationFailure preserves browser-specific recovery guidance
// when malformed context-authority fields would otherwise be rejected by the
// registry before Execute can classify them. All other schema failures retain
// the registry's generic fail-closed response.
func (tool *BrowserActTool) SafeSchemaValidationFailure(args map[string]any) *toolshared.ToolResult {
	if !browserActionContextAuthorityInvalid(args) {
		return nil
	}
	withoutContextAuthority := make(map[string]any, len(args))
	for field, value := range args {
		if field != "context_catalog_id" && field != "context_generation" {
			withoutContextAuthority[field] = value
		}
	}
	if validateToolArgs(tool.Parameters(), withoutContextAuthority) != nil {
		return nil
	}
	return browserActionToolError(errBrowserActionContextAuthority)
}

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
		if errors.Is(err, browser.ErrNavigationFailed) &&
			!errors.Is(err, browser.ErrSnapshotInvalidation) &&
			invocation.State == browser.InvocationFailed {
			return browserNavigationFailureResult(invocation)
		}
		if errors.Is(err, browser.ErrSnapshotInvalidation) || invocation.AcceptedAt != 0 {
			result := browserPostActionStateError(
				invocation,
				errors.Is(err, browser.ErrSnapshotInvalidation),
			)
			attachBrowserExternalActionAudit(result, invocation, preparation, tool.Name())
			return result
		}
		return browserActionToolError(err)
	}
	if invocation.State == browser.InvocationFailed && invocation.SafeFailure == "navigation_failed" {
		return browserNavigationFailureResult(invocation)
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
	attachBrowserExternalActionAudit(toolResult, invocation, preparation, tool.Name())
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

func attachBrowserExternalActionAudit(
	result *toolshared.ToolResult,
	invocation browser.Invocation,
	preparation browser.Preparation,
	toolName string,
) {
	if result == nil || invocation.State != browser.InvocationSucceeded ||
		(invocation.Effect != browser.EffectExternalCommit && invocation.Effect != browser.EffectUnknown) {
		return
	}
	result.WithWriteAudit(toolshared.WriteAuditEntry{
		Kind: "external_action", Target: preparation.Action.CurrentOrigin,
		Action: string(preparation.Action.Action.Kind), Tool: toolName,
		Summary: "browser external action completed",
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

func browserNavigationFailureResult(invocation browser.Invocation) *toolshared.ToolResult {
	encoded, _ := json.Marshal(map[string]any{
		"status":             "failed",
		"code":               "navigation_failed",
		"message":            "The requested page navigation failed, but the same browser session remains available. A protected deep link or authentication redirect may be the cause; this result alone does not prove whether the user is signed in.",
		"action":             "observe_same_session_then_check_site_origin_for_authentication",
		"session_preserved":  true,
		"invocation_id":      invocation.ID,
		"browser_session_id": invocation.SessionID,
		"effect":             invocation.Effect,
		"state":              invocation.State,
		"reason":             invocation.SafeFailure,
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
	confirmation, confirmationOK := args["confirmation"].(string)
	if _, present := args["confirmation"]; present &&
		(!confirmationOK || confirmation != browserpolicy.ConfirmationRequest) {
		return browser.Preparation{}, browser.ErrInvalid
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
	contextGeneration, _ := browserInteger(args["context_generation"])
	if browserActionContextAuthorityInvalid(args) {
		return browser.Preparation{}, errBrowserActionContextAuthority
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
		DeclaredEffect: declaredEffect, Confirmation: confirmation,
	})
}

func browserActionContextAuthorityInvalid(args map[string]any) bool {
	catalogID, catalogOK := args["context_catalog_id"].(string)
	_, catalogPresent := args["context_catalog_id"]
	contextGeneration, contextGenerationOK := browserInteger(args["context_generation"])
	_, contextGenerationPresent := args["context_generation"]
	return catalogPresent != contextGenerationPresent ||
		(catalogPresent && (!catalogOK || catalogID == "" ||
			!contextGenerationOK || contextGeneration < 1))
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
	actorID := browserCanonicalActorID(ctx)
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
		ActorID:     browser.OpaqueActorID(actorID),
		AgentID:     browser.OpaqueAgentID(routing.NormalizeAgentID(agentID)),
		SessionKey:  browserContextID("session", sessionKey),
		ExecutionID: browserContextID("execution", executionID),
	}, nil
}

func browserCanonicalActorID(ctx context.Context) string {
	inbound := toolshared.ToolInboundContext(ctx)
	channel := strings.ToLower(strings.TrimSpace(inbound.Channel))
	actorID := strings.TrimSpace(inbound.ActorID)
	if actorID == "" {
		actorID = strings.TrimSpace(inbound.SenderID)
	}
	if channel == "" || actorID == "" {
		return ""
	}
	if platform, platformID, ok := identity.ParseCanonicalID(actorID); ok &&
		strings.EqualFold(strings.TrimSpace(platform), channel) {
		return identity.BuildCanonicalID(channel, platformID)
	}
	return identity.BuildCanonicalID(channel, actorID)
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
	case errors.Is(err, browser.ErrCleanupRequired):
		return browserErrorResult(
			"cleanup_required",
			"Browser cleanup could not be verified.",
			"contact_operator",
		)
	case errors.Is(err, browser.ErrBusy):
		return browserErrorResult("profile_busy", "The browser profile is already in use.", "close_or_wait")
	case errors.Is(err, browser.ErrCapacity):
		return browserErrorResult(
			"session_capacity",
			"Browser session capacity is exhausted.",
			"close_or_wait",
		)
	case errors.Is(err, browser.ErrNotFound):
		return browserErrorResult("not_found", "The browser session or action was not found.", "open_session")
	case errors.Is(err, browser.ErrStale):
		return browserErrorResult("stale_snapshot", "Browser authority is stale.", "observe_again")
	case errors.Is(err, browser.ErrConsentExpired):
		return browserErrorResult(
			"attach_consent_expired",
			"Browser attachment consent expired or no longer matches this session.",
			"open_session_again",
		)
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
	case errors.Is(err, browser.ErrSnapshotTransfer):
		return browserErrorResult(
			"snapshot_transfer_failed",
			"The browser snapshot could not be transferred.",
			"observe_again",
		)
	case errors.Is(err, browser.ErrWorkerUnavailable), errors.Is(err, browser.ErrDriverRejected):
		return browserErrorResult("driver_unavailable", "The browser driver is unavailable.", "retry_or_reopen")
	default:
		return browserErrorResult("runtime_unavailable", "Browser automation is unavailable.", "retry")
	}
}

func browserActionToolError(err error) *toolshared.ToolResult {
	if errors.Is(err, errBrowserActionContextAuthority) {
		return browserErrorResult(
			"invalid_context_authority",
			"Browser context authority is incomplete or invalid. Observe again; copy both context_catalog_id and "+
				"context_generation only when both are returned, otherwise omit both. Never invent placeholder values.",
			"observe_again_copy_returned_context_or_omit_both",
		)
	}
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
