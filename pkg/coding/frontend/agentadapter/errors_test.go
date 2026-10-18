package agentadapter

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

func TestAdapterProjectsSafeActionableProviderErrors(t *testing.T) {
	tests := []struct {
		name      string
		payload   agent.ErrorPayload
		want      string
		wantLogin bool
	}{
		{
			name: "OpenAI OAuth refresh failure",
			payload: agent.ErrorPayload{
				ErrorCode: "auth", ProviderErrorKind: "authentication", Provider: "openai", AuthMethod: "oauth",
			},
			want: "mintclaw auth login --provider openai --device-code", wantLogin: true,
		},
		{
			name: "OpenAI token route",
			payload: agent.ErrorPayload{
				ErrorCode: "auth", Provider: "openai", AuthMethod: "token",
			},
			want: "mintclaw auth login --provider openai --device-code", wantLogin: true,
		},
		{
			name: "API key authentication is not OAuth",
			payload: agent.ErrorPayload{
				ProviderErrorKind: "authentication", Provider: "openai",
			},
			want: "API key",
		},
		{
			name: "Anthropic OAuth",
			payload: agent.ErrorPayload{
				ErrorCode: "auth", Provider: "anthropic", AuthMethod: "oauth",
			},
			want: "mintclaw auth login --provider anthropic", wantLogin: true,
		},
		{
			name:    "ambiguous fallback authentication",
			payload: agent.ErrorPayload{ErrorCode: "auth", ProviderErrorKind: "authentication"},
			want:    "Authentication failed",
		},
		{
			name: "provider identity cannot become a shell command",
			payload: agent.ErrorPayload{
				ErrorCode: "auth", Provider: "secret-provider; touch /tmp/unsafe", AuthMethod: "oauth",
			},
			want: "Authentication failed",
		},
		{
			name: "rate limit is not login failure",
			payload: agent.ErrorPayload{
				ErrorCode: "rate_limit", ProviderErrorKind: "rate_limit", Provider: "openai", AuthMethod: "oauth",
			},
			want: "rate limit",
		},
		{
			name:    "billing",
			payload: agent.ErrorPayload{ProviderErrorKind: "billing"},
			want:    "billing settings",
		},
		{
			name:    "network",
			payload: agent.ErrorPayload{ProviderErrorKind: "network"},
			want:    "network connection",
		},
		{
			name:    "timeout",
			payload: agent.ErrorPayload{ProviderErrorKind: "timeout"},
			want:    "timed out",
		},
		{
			name:    "context overflow",
			payload: agent.ErrorPayload{ProviderErrorKind: "context_overflow"},
			want:    "Compact this session",
		},
		{
			name:    "invalid request",
			payload: agent.ErrorPayload{ProviderErrorKind: "invalid_request"},
			want:    "selected model",
		},
		{
			name:    "unknown error keeps a safe fallback",
			payload: agent.ErrorPayload{},
			want:    "agent error during llm",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projector, err := frontend.NewProjector("thread-1", frontend.ProjectionLimits{})
			if err != nil {
				t.Fatal(err)
			}
			adapter := &Adapter{projector: projector, sessionKey: "thread-1"}
			test.payload.Stage = "llm"
			test.payload.Message = "secret-message: provider credential refresh failed"
			test.payload.RequestID = "secret-request-id"
			adapter.project(runtimeevents.Event{
				Kind: runtimeevents.KindAgentError, Source: runtimeevents.Source{Component: "agent"},
				Scope: runtimeevents.Scope{
					SessionKey: "thread-1", TraceScope: runtimeevents.NewTraceScope("/repo", "turn-1"),
				},
				Payload: test.payload,
			})
			snapshot, err := projector.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			messages := snapshot.Messages()
			if len(messages) != 1 || !strings.Contains(messages[0].Text, test.want) {
				t.Fatalf("error messages = %+v, want %q", messages, test.want)
			}
			if strings.Contains(messages[0].Text, "mintclaw auth login") != test.wantLogin {
				t.Fatalf("wrong authentication recovery: %q", messages[0].Text)
			}
			if strings.Contains(fmt.Sprintf("%+v", snapshot), "secret-") {
				t.Fatal("provider diagnostic data reached the frontend")
			}
			if snapshot.LastTurn == nil || snapshot.LastTurn.Outcome != frontend.TurnOutcomeFailed {
				t.Fatalf("terminal state = %+v", snapshot.LastTurn)
			}
		})
	}
}
