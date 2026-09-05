package agent

import (
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

// turnSpec is the mutable admission request assembled by entrypoints. It is
// normalized and frozen into turnInput before runtime state is created.
type turnSpec struct {
	mode                         turnMode
	Dispatch                     DispatchRequest
	ModelBinding                 effectiveModelBinding
	TaskID                       string // Durable task owning this turn, when one exists
	ObjectiveChecklist           []runtimeObjectiveItem
	InteractionWorkspace         string              // Workspace owning inbound interaction routing
	InteractionSessionKey        string              // User-facing session that owns interaction answers
	InteractionRouteKey          string              // Routed scope key that owns interaction answers
	InteractionOriginExecution   string              // Original non-approval execution identity for a continuation
	InteractionOriginContext     *bus.InboundContext // Original tool identity for a continuation
	ApprovalGrant                *ToolApprovalGrant  // Internal one-time durable approval capability
	SenderDisplayName            string              // Current sender display name for dynamic context
	CodingContext                CodingPromptContext // Runtime-owned coding identity for prompt assembly
	ForcedSkills                 []string            // Skills explicitly requested for this message
	TurnProfile                  config.EffectiveTurnProfile
	InitialSteeringMessages      []providers.Message       // Steering messages from refactor/agent
	ActiveGoal                   string                    // Dynamic session goal reminder for normal LLM turns
	DefaultResponse              string                    // Response when LLM returns empty
	EnableSummary                bool                      // Whether to trigger summarization
	SuppressBackgroundCompaction bool                      // Whether this short-lived caller can outlive background work
	SendResponse                 bool                      // Whether to send response via bus
	ExpectFinalDelivery          bool                      // Whether an outer coordinator will publish the final response
	FinalDeliveryObservation     *finalDeliveryObservation // Collects state settled by an outer final response
	AllowInterimMintClawPublish  bool                      // Whether mintclaw tool-call interim text can be published when SendResponse is false
	DirectStreaming              bool                      // Whether a direct frontend supplies its own stream delegate
	OnTurnReady                  func()                    // Signals that direct turn controls can target the registered owner
	SuppressToolUserDelivery     bool                      // Whether direct user-facing delivery from tools is suppressed for this turn
	SuppressToolFeedback         bool                      // Whether to suppress inline tool feedback messages
	NoHistory                    bool                      // If true, don't load session history (for heartbeat)
}

// turnIdentity is the normalized identity and routing snapshot for one
// admitted turn. Runtime code receives it by value through turnInput.
type turnIdentity struct {
	mode                       turnMode
	Dispatch                   DispatchRequest
	ModelBinding               effectiveModelBinding
	TaskID                     string
	ObjectiveChecklist         []runtimeObjectiveItem
	InteractionWorkspace       string
	InteractionSessionKey      string
	InteractionRouteKey        string
	InteractionOriginExecution string
	InteractionOriginContext   *bus.InboundContext
}

type turnPromptInput struct {
	SenderDisplayName       string
	CodingContext           CodingPromptContext
	ForcedSkills            []string
	InitialSteeringMessages []providers.Message
	ActiveGoal              string
}

type turnExecutionPolicy struct {
	TurnProfile                  config.EffectiveTurnProfile
	DefaultResponse              string
	EnableSummary                bool
	SuppressBackgroundCompaction bool
	SendResponse                 bool
	ExpectFinalDelivery          bool
	AllowInterimMintClawPublish  bool
	DirectStreaming              bool
	SuppressToolUserDelivery     bool
	SuppressToolFeedback         bool
	NoHistory                    bool
}

// turnObservationHooks are caller-owned observers, kept separate from the
// execution policy so observation cannot masquerade as runtime input state.
type turnObservationHooks struct {
	FinalDelivery *finalDeliveryObservation
	OnReady       func()
}

// turnInput is the immutable-by-construction snapshot consumed by turnState.
// Mutable one-time capabilities and execution results live on turnState or in
// the returned turnResult instead of being written through this value.
type turnInput struct {
	turnIdentity
	turnPromptInput
	turnExecutionPolicy
	observers turnObservationHooks
}

func freezeTurnInput(spec turnSpec) turnInput {
	return turnInput{
		turnIdentity: turnIdentity{
			mode:                       spec.mode,
			Dispatch:                   cloneDispatchRequest(spec.Dispatch),
			ModelBinding:               cloneEffectiveModelBinding(spec.ModelBinding),
			TaskID:                     spec.TaskID,
			ObjectiveChecklist:         cloneRuntimeObjectiveChecklist(spec.ObjectiveChecklist),
			InteractionWorkspace:       spec.InteractionWorkspace,
			InteractionSessionKey:      spec.InteractionSessionKey,
			InteractionRouteKey:        spec.InteractionRouteKey,
			InteractionOriginExecution: spec.InteractionOriginExecution,
			InteractionOriginContext:   cloneInboundContext(spec.InteractionOriginContext),
		},
		turnPromptInput: turnPromptInput{
			SenderDisplayName:       spec.SenderDisplayName,
			CodingContext:           spec.CodingContext,
			ForcedSkills:            append([]string(nil), spec.ForcedSkills...),
			InitialSteeringMessages: cloneProviderMessages(spec.InitialSteeringMessages),
			ActiveGoal:              spec.ActiveGoal,
		},
		turnExecutionPolicy: turnExecutionPolicy{
			TurnProfile:                  cloneEffectiveTurnProfile(spec.TurnProfile),
			DefaultResponse:              spec.DefaultResponse,
			EnableSummary:                spec.EnableSummary,
			SuppressBackgroundCompaction: spec.SuppressBackgroundCompaction,
			SendResponse:                 spec.SendResponse,
			ExpectFinalDelivery:          spec.ExpectFinalDelivery,
			AllowInterimMintClawPublish:  spec.AllowInterimMintClawPublish,
			DirectStreaming:              spec.DirectStreaming,
			SuppressToolUserDelivery:     spec.SuppressToolUserDelivery,
			SuppressToolFeedback:         spec.SuppressToolFeedback,
			NoHistory:                    spec.NoHistory,
		},
		observers: turnObservationHooks{
			FinalDelivery: spec.FinalDeliveryObservation,
			OnReady:       spec.OnTurnReady,
		},
	}
}

func cloneEffectiveTurnProfile(profile config.EffectiveTurnProfile) config.EffectiveTurnProfile {
	if profile.AllowedSkills != nil {
		profile.AllowedSkills = append([]string{}, profile.AllowedSkills...)
	}
	if profile.AllowedTools != nil {
		profile.AllowedTools = append([]string{}, profile.AllowedTools...)
	}
	return profile
}
