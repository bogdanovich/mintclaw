package cliprovider

import (
	"context"
	"errors"
	"testing"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

type fakeCopilotSession struct {
	handler copilot.SessionEventHandler
	event   copilot.SessionEvent
	err     error
}

func (s *fakeCopilotSession) On(handler copilot.SessionEventHandler) func() {
	s.handler = handler
	return func() { s.handler = nil }
}

func (s *fakeCopilotSession) SendAndWait(
	ctx context.Context,
	options copilot.MessageOptions,
) (*copilot.SessionEvent, error) {
	if s.handler != nil {
		s.handler(s.event)
	}
	return nil, s.err
}

func TestGitHubCopilotProviderChatCapturesStructuredSessionError(t *testing.T) {
	errorType := "subscription"
	message := "rate limit exceeded"
	requestID := "github-request-123"
	status := int64(429)
	cause := errors.New("session error")
	session := &fakeCopilotSession{
		event: copilot.SessionEvent{
			Type: copilot.SessionEventTypeSessionError,
			Data: copilot.Data{
				ErrorType:      &errorType,
				Message:        &message,
				ProviderCallID: &requestID,
				StatusCode:     &status,
			},
		},
		err: cause,
	}
	provider := &GitHubCopilotProvider{session: session}

	_, err := provider.Chat(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil)
	providerErr := assertProviderError(t, err, cause, providererrors.KindBilling)
	if providerErr.HTTPStatus != int(status) {
		t.Fatalf("HTTPStatus = %d, want %d", providerErr.HTTPStatus, status)
	}
	if providerErr.RequestID != requestID {
		t.Fatalf("RequestID = %q, want %q", providerErr.RequestID, requestID)
	}
	if session.handler != nil {
		t.Fatal("Chat did not unsubscribe its session error handler")
	}
}

func TestGitHubCopilotProviderChatNormalizesCancellation(t *testing.T) {
	cause := context.Canceled
	provider := &GitHubCopilotProvider{session: &fakeCopilotSession{err: cause}}
	_, err := provider.Chat(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil)
	_ = assertProviderError(t, err, cause, providererrors.KindCanceled)
}
