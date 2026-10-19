package agent

import (
	"github.com/bogdanovich/mintclaw/pkg/browseraction"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func browserInputObservationForSuspension(
	tool string, result *toolshared.ToolResult, observation browseraction.InputTargetObservation,
) browseraction.InputTargetObservation {
	if tool != "browser_input" || result == nil || result.IsError || result.Control.Async ||
		result.Control.Suspension == nil || !observation.Valid() {
		return browseraction.InputTargetObservation{}
	}
	request := result.Control.Suspension
	if request.Kind != interactions.KindQuestion || request.ProtectedAnswer == nil ||
		request.ProtectedAnswer.Namespace != tools.BrowserProtectedAnswerNamespace ||
		!observation.MatchesToken(request.ProtectedAnswer.Token) {
		return browseraction.InputTargetObservation{}
	}
	return observation
}

func diagnosticBrowserInputTarget(value ToolExecEndPayload) *diagnostictrace.BrowserInputTargetPayload {
	if value.Tool != "browser_input" || value.IsError || value.Async || !value.Suspended {
		return nil
	}
	payload := diagnostictrace.BrowserInputTargetPayload{
		InteractionID: value.InteractionID, Target: value.BrowserInputTarget,
	}
	if !payload.Valid() {
		return nil
	}
	return &payload
}
