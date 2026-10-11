package agent

import runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"

// HookMeta contains correlation fields shared by agent hook requests and
// runtime events emitted from turn processing.
type HookMeta struct {
	runtimeevents.TraceScope
	AgentID            string
	ParentTurnID       string
	ChildTurnID        string
	ModelAttemptID     string
	ToolCallID         string
	ToolCallGeneration string
	SessionKey         string
	ProjectHash        string
	Iteration          int
	TracePath          string
	Source             string
	turnContext        *TurnContext
}

func (m HookMeta) withModelAttemptID(attemptID string) HookMeta {
	m.ModelAttemptID = attemptID
	return m
}

func (m HookMeta) withToolCallCorrelation(toolCallID, generation string) HookMeta {
	m.ToolCallID = toolCallID
	m.ToolCallGeneration = generation
	return m
}
