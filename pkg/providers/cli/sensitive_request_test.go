package cliprovider

import (
	"context"
	"errors"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
)

type sensitiveCopilotCanary struct {
	calls int
}

func (session *sensitiveCopilotCanary) On(copilot.SessionEventHandler) func() {
	session.calls++
	return func() {}
}

func (session *sensitiveCopilotCanary) SendAndWait(
	context.Context, copilot.MessageOptions,
) (*copilot.SessionEvent, error) {
	session.calls++
	return nil, errors.New("unexpected session send")
}

func TestSensitiveRequestsRefuseCLIAndSessionBeforeAnyExecution(t *testing.T) {
	const sentinel = "MINTCLAW_SENSITIVE_CLI_SENTINEL_d9e3"
	session := &sensitiveCopilotCanary{}
	for _, test := range []struct {
		name     string
		provider LLMProvider
	}{
		{"codex-nil", (*CodexCliProvider)(nil)},
		{"claude-nil", (*ClaudeCliProvider)(nil)},
		{"copilot-nil", (*GitHubCopilotProvider)(nil)},
		{"copilot-live-session", &GitHubCopilotProvider{session: session}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := test.provider.Chat(protocoltypes.WithSensitiveRequest(t.Context()),
				[]Message{{Role: "user", Content: sentinel}}, nil, "configured-model", nil)
			if response != nil || !errors.Is(err, protocoltypes.ErrSensitiveRequestUnsupported) ||
				strings.Contains(err.Error(), sentinel) {
				t.Fatal("unsupported private provider did not refuse safely before configuration/session access")
			}
		})
	}
	if session.calls != 0 {
		t.Fatal("protected content was offered to the persistent session")
	}
}
