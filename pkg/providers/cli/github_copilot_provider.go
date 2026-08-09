package cliprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	copilot "github.com/github/copilot-sdk/go"

	providercapabilities "github.com/bogdanovich/mintclaw/pkg/providers/capabilities"
)

type GitHubCopilotProvider struct {
	uri         string
	connectMode string // "stdio" or "grpc"

	client  *copilot.Client
	session copilotSession

	mu     sync.Mutex
	chatMu sync.Mutex
}

func (p *GitHubCopilotProvider) Capabilities() providercapabilities.ProviderCapabilities {
	return providercapabilities.ProviderCapabilities{}
}

type copilotSession interface {
	On(handler copilot.SessionEventHandler) func()
	SendAndWait(ctx context.Context, options copilot.MessageOptions) (*copilot.SessionEvent, error)
}

func NewGitHubCopilotProvider(uri string, connectMode string, model string) (*GitHubCopilotProvider, error) {
	if connectMode == "" {
		connectMode = "grpc"
	}

	switch connectMode {
	case "stdio":
		// TODO: Implement stdio mode for GitHub Copilot provider
		// See https://github.com/github/copilot-sdk/blob/main/docs/getting-started.md for details
		return nil, fmt.Errorf("stdio mode not implemented for GitHub Copilot provider; please use 'grpc' mode instead")
	case "grpc":
		client := copilot.NewClient(&copilot.ClientOptions{
			CLIUrl: uri,
		})
		if err := client.Start(context.Background()); err != nil {
			return nil, normalizeCopilotError(err, nil)
		}

		session, err := client.CreateSession(context.Background(), &copilot.SessionConfig{
			Model:               model,
			OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
			Hooks:               &copilot.SessionHooks{},
		})
		if err != nil {
			_ = client.Stop()
			return nil, normalizeCopilotError(err, nil)
		}

		return &GitHubCopilotProvider{
			uri:         uri,
			connectMode: connectMode,
			client:      client,
			session:     session,
		}, nil
	default:
		return nil, fmt.Errorf("unknown connect mode: %s", connectMode)
	}
}

func (p *GitHubCopilotProvider) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		_ = p.client.Stop()
		p.client = nil
		p.session = nil
	}
}

func (p *GitHubCopilotProvider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (*LLMResponse, error) {
	p.chatMu.Lock()
	defer p.chatMu.Unlock()

	type tempMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	out := make([]tempMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, tempMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	fullcontent, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal messages: %w", err)
	}
	p.mu.Lock()
	session := p.session
	p.mu.Unlock()

	if session == nil {
		return nil, normalizeCopilotError(errors.New("provider closed"), nil)
	}

	var sessionError *copilot.SessionEvent
	var sessionErrorMu sync.Mutex
	unsubscribe := session.On(func(event copilot.SessionEvent) {
		if event.Type != copilot.SessionEventTypeSessionError {
			return
		}
		eventCopy := event
		sessionErrorMu.Lock()
		sessionError = &eventCopy
		sessionErrorMu.Unlock()
	})
	defer unsubscribe()

	resp, err := session.SendAndWait(ctx, copilot.MessageOptions{
		Prompt: string(fullcontent),
	})
	if err != nil {
		sessionErrorMu.Lock()
		errorEvent := sessionError
		sessionErrorMu.Unlock()
		return nil, normalizeCopilotError(err, errorEvent)
	}

	if resp == nil {
		return nil, normalizeCopilotError(errors.New("empty response from copilot"), nil)
	}
	if resp.Data.Content == nil {
		return nil, normalizeCopilotError(errors.New("no content in copilot response"), nil)
	}
	content := *resp.Data.Content

	return &LLMResponse{
		FinishReason: "stop",
		Content:      content,
	}, nil
}

func (p *GitHubCopilotProvider) GetDefaultModel() string {
	return "gpt-4.1"
}
