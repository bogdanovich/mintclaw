package agent

import runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"

// HookMeta contains correlation fields shared by agent hook requests and
// runtime events emitted from turn processing.
type HookMeta struct {
	runtimeevents.TraceScope
	AgentID      string
	ParentTurnID string
	SessionKey   string
	Iteration    int
	TracePath    string
	Source       string
	turnContext  *TurnContext
}
