package agent

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestPipelineBuildTurnMessagesUsesCurrentBuilder(t *testing.T) {
	pipeline := &Pipeline{}
	ts := &turnState{
		agent: &AgentInstance{
			ContextBuilder: NewContextBuilder(t.TempDir()),
		},
		userMessage: "hello from current builder",
	}

	got := pipeline.buildTurnMessages(ts, nil, "", ts.userMessage, nil, nil)
	if len(got) == 0 {
		t.Fatal("buildTurnMessages() returned no messages")
	}
	if !messagesContainContent(got, "hello from current builder") {
		t.Fatalf("buildTurnMessages() = %#v, want current message content", got)
	}
}

func messagesContainContent(messages []providers.Message, want string) bool {
	for _, msg := range messages {
		if strings.Contains(msg.Content, want) {
			return true
		}
		for _, block := range msg.SystemParts {
			if strings.Contains(block.Text, want) {
				return true
			}
		}
	}
	return false
}
