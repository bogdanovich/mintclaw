package agent

import "testing"

func TestNewTurnSpecAppliesModeContract(t *testing.T) {
	tests := []struct {
		name                        string
		mode                        turnMode
		defaultResponse             string
		enableSummary               bool
		sendResponse                bool
		expectFinalDelivery         bool
		allowInterimMintClawPublish bool
		suppressToolFeedback        bool
		noHistory                   bool
		bypassCommands              bool
		excludeInheritedNodeFiles   bool
		skipInitialSteeringPoll     bool
	}{
		{
			name: "inbound", mode: turnModeInbound, defaultResponse: defaultResponse,
			enableSummary: true, expectFinalDelivery: true, allowInterimMintClawPublish: true,
		},
		{
			name: "coding", mode: turnModeCoding, defaultResponse: defaultResponse,
			expectFinalDelivery: true, bypassCommands: true,
		},
		{
			name: "scheduled", mode: turnModeScheduled, defaultResponse: defaultResponse,
			suppressToolFeedback: true, noHistory: true, excludeInheritedNodeFiles: true,
		},
		{
			name: "heartbeat", mode: turnModeHeartbeat, defaultResponse: defaultResponse,
			suppressToolFeedback: true, noHistory: true, excludeInheritedNodeFiles: true,
		},
		{name: "system", mode: turnModeSystem, defaultResponse: "Background task completed."},
		{
			name: "async completion", mode: turnModeAsyncCompletion,
			suppressToolFeedback: true, noHistory: true,
		},
		{
			name: "recovery", mode: turnModeRecovery, defaultResponse: defaultResponse,
			enableSummary: true, sendResponse: true, allowInterimMintClawPublish: true,
		},
		{
			name: "steering", mode: turnModeSteering, defaultResponse: defaultResponse,
			enableSummary: true, skipInitialSteeringPoll: true,
		},
		{
			name: "interaction continuation", mode: turnModeInteractionContinuation,
			defaultResponse: defaultResponse, enableSummary: true,
			allowInterimMintClawPublish: true, skipInitialSteeringPoll: true,
		},
		{name: "child", mode: turnModeChild, skipInitialSteeringPoll: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatch := DispatchRequest{SessionKey: "session-1"}
			binding := effectiveModelBinding{RouteSessionKey: "route-1"}
			got := newTurnSpec(tt.mode, dispatch, binding)
			if got.Dispatch.SessionKey != dispatch.SessionKey ||
				got.ModelBinding.RouteSessionKey != binding.RouteSessionKey || got.mode != tt.mode {
				t.Fatalf("identity = (%q,%q), want (%q,%q)",
					got.Dispatch.SessionKey, got.ModelBinding.RouteSessionKey,
					dispatch.SessionKey, binding.RouteSessionKey)
			}
			if got.DefaultResponse != tt.defaultResponse || got.EnableSummary != tt.enableSummary ||
				got.SendResponse != tt.sendResponse || got.ExpectFinalDelivery != tt.expectFinalDelivery ||
				got.AllowInterimMintClawPublish != tt.allowInterimMintClawPublish ||
				got.SuppressToolFeedback != tt.suppressToolFeedback || got.NoHistory != tt.noHistory ||
				(got.mode == turnModeCoding) != tt.bypassCommands ||
				(got.mode == turnModeScheduled || got.mode == turnModeHeartbeat) !=
					tt.excludeInheritedNodeFiles ||
				got.mode.skipsInitialSteeringPoll() != tt.skipInitialSteeringPoll {
				t.Fatalf("mode contract = %#v", got)
			}
		})
	}
}

func TestNewTurnSpecRejectsUnknownMode(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("newTurnSpec accepted an unspecified mode")
		}
	}()
	_ = newTurnSpec(turnModeUnspecified, DispatchRequest{}, effectiveModelBinding{})
}
