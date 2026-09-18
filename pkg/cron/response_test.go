package cron

import "testing"

func TestMatchAgentTurnControl(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		response  string
		want      AgentTurnControl
		canonical bool
		matched   bool
	}{
		{
			name:      "canonical no reply",
			response:  "  NO_REPLY\n",
			want:      AgentTurnControlNoReply,
			canonical: true,
			matched:   true,
		},
		{
			name:      "case insensitive heartbeat is noncanonical",
			response:  "heartbeat_ok",
			want:      AgentTurnControlHeartbeatOK,
			canonical: false,
			matched:   true,
		},
		{
			name:      "terminal marker after explanation",
			response:  "No eligible candidate remains.\n\nNO_REPLY",
			want:      AgentTurnControlNoReply,
			canonical: false,
			matched:   true,
		},
		{
			name:      "terminal inline code",
			response:  "Workflow completed without a delivery.\n\n`NO_REPLY`",
			want:      AgentTurnControlNoReply,
			canonical: false,
			matched:   true,
		},
		{
			name:      "terminal fenced code",
			response:  "Nothing requires attention.\n\n```text\nHEARTBEAT_OK\n```",
			want:      AgentTurnControlHeartbeatOK,
			canonical: false,
			matched:   true,
		},
		{
			name:     "marker mentioned in prose",
			response: "The workflow documentation says to return NO_REPLY when idle.",
		},
		{
			name:     "marker before user content",
			response: "NO_REPLY\nA result is available after all.",
		},
		{
			name:     "multi line fenced content",
			response: "```\nReason\nNO_REPLY\n```",
		},
		{
			name:     "empty",
			response: " \n ",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			match, matched := MatchAgentTurnControl(test.response)
			if matched != test.matched {
				t.Fatalf("matched = %t, want %t (match = %+v)", matched, test.matched, match)
			}
			if match.Control != test.want {
				t.Fatalf("control = %q, want %q", match.Control, test.want)
			}
			if match.Canonical != test.canonical {
				t.Fatalf("canonical = %t, want %t", match.Canonical, test.canonical)
			}
		})
	}
}
