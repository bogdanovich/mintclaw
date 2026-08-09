package cliprovider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/isolation"
	"github.com/bogdanovich/mintclaw/pkg/providers/providererrors"
)

// CodexCliProvider implements LLMProvider by wrapping the codex CLI as a subprocess.
type CodexCliProvider struct {
	command   string
	workspace string
}

// NewCodexCliProvider creates a new Codex CLI provider.
func NewCodexCliProvider(workspace string) *CodexCliProvider {
	return &CodexCliProvider{
		command:   "codex",
		workspace: workspace,
	}
}

// Chat implements LLMProvider.Chat by executing the codex CLI in non-interactive mode.
func (p *CodexCliProvider) Chat(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any,
) (*LLMResponse, error) {
	if p.command == "" {
		return nil, fmt.Errorf("codex command not configured")
	}

	prompt := p.buildPrompt(messages, tools)

	args := []string{
		"exec",
		"--json",
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"--color", "never",
	}
	if model != "" && model != "codex-cli" {
		args = append(args, "-m", model)
	}
	if p.workspace != "" {
		args = append(args, "-C", p.workspace)
	}
	args = append(args, "-") // read prompt from stdin

	cmd := exec.CommandContext(ctx, p.command, args...)
	cmd.Stdin = bytes.NewReader([]byte(prompt))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Execute the CLI through the shared isolation wrapper so external provider
	// processes honor the configured isolation policy.
	err := isolation.Run(cmd)

	// Parse JSONL from stdout even if exit code is non-zero,
	// because codex writes diagnostic noise to stderr (e.g. rollout errors)
	// but still produces valid JSONL output.
	var parsedResp *LLMResponse
	var parsedErr error
	if stdoutStr := stdout.String(); stdoutStr != "" {
		parsedResp, parsedErr = p.parseJSONLEvents(stdoutStr)
		if parsedErr == nil && parsedResp != nil &&
			(parsedResp.Content != "" || len(parsedResp.ToolCalls) > 0) {
			return parsedResp, nil
		}
	}

	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, normalizeCLIError(errors.Join(ctxErr, err), stderr.String())
		}
		var providerErr *providererrors.ProviderError
		if errors.As(parsedErr, &providerErr) && providerErr != nil {
			providerErrCopy := *providerErr
			providerErrCopy.Cause = errors.Join(providerErr.Cause, err)
			return nil, &providerErrCopy
		}
		if stderrStr := stderr.String(); stderrStr != "" {
			return nil, normalizeCLIError(err, stderrStr)
		}
		return nil, normalizeCLIError(err, "")
	}

	if parsedErr != nil {
		return nil, parsedErr
	}
	if parsedResp != nil {
		return parsedResp, nil
	}
	return p.parseJSONLEvents("")
}

// GetDefaultModel returns the default model identifier.
func (p *CodexCliProvider) GetDefaultModel() string {
	return "codex-cli"
}

// buildPrompt converts messages to a prompt string for the Codex CLI.
// System messages are prepended as instructions since Codex CLI has no --system-prompt flag.
func (p *CodexCliProvider) buildPrompt(messages []Message, tools []ToolDefinition) string {
	var systemParts []string
	var conversationParts []string

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			systemParts = append(systemParts, msg.Content)
		case "user":
			conversationParts = append(conversationParts, msg.Content)
		case "assistant":
			conversationParts = append(conversationParts, "Assistant: "+msg.Content)
		case "tool":
			conversationParts = append(conversationParts,
				fmt.Sprintf("[Tool Result for %s]: %s", msg.ToolCallID, msg.Content))
		}
	}

	var sb strings.Builder

	if len(systemParts) > 0 {
		sb.WriteString("## System Instructions\n\n")
		sb.WriteString(strings.Join(systemParts, "\n\n"))
		sb.WriteString("\n\n## Task\n\n")
	}

	if len(tools) > 0 {
		sb.WriteString(buildCLIToolsPrompt(tools))
		sb.WriteString("\n\n")
	}

	// Simplify single user message (no prefix)
	if len(conversationParts) == 1 && len(systemParts) == 0 && len(tools) == 0 {
		return conversationParts[0]
	}

	sb.WriteString(strings.Join(conversationParts, "\n"))
	return sb.String()
}

// codexEvent represents a single JSONL event from `codex exec --json`.
type codexEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	Message  string          `json:"message,omitempty"`
	Item     *codexEventItem `json:"item,omitempty"`
	Usage    *codexUsage     `json:"usage,omitempty"`
	Error    *codexEventErr  `json:"error,omitempty"`
	Code     string          `json:"code,omitempty"`
}

type codexEventItem struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Command  string `json:"command,omitempty"`
	Status   string `json:"status,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Output   string `json:"output,omitempty"`
}

type codexUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
}

type codexEventErr struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// parseJSONLEvents processes the JSONL output from codex exec --json.
func (p *CodexCliProvider) parseJSONLEvents(output string) (*LLMResponse, error) {
	var lastAgentMessage string
	var usage *UsageInfo
	var lastError string
	var lastErrorCode string

	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var event codexEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue // skip malformed lines
		}

		switch event.Type {
		case "item.completed":
			if event.Item != nil && event.Item.Type == "agent_message" && event.Item.Text != "" {
				lastAgentMessage = event.Item.Text
			}
		case "turn.completed":
			if event.Usage != nil {
				promptTokens := event.Usage.InputTokens + event.Usage.CachedInputTokens
				usage = &UsageInfo{
					PromptTokens:     promptTokens,
					CompletionTokens: event.Usage.OutputTokens,
					TotalTokens:      promptTokens + event.Usage.OutputTokens,
				}
			}
		case "error":
			lastError = event.Message
			if event.Code != "" {
				lastErrorCode = event.Code
			}
		case "turn.failed":
			if event.Error != nil {
				lastError = event.Error.Message
				if event.Error.Code != "" {
					lastErrorCode = event.Error.Code
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, normalizeCLIError(fmt.Errorf("scan codex CLI events: %w", err), "")
	}

	if lastError != "" && lastAgentMessage == "" {
		return nil, normalizeCodedCLIError(lastErrorCode, lastError, errors.New(lastError))
	}

	content := lastAgentMessage

	// Extract tool calls from response text (same pattern as ClaudeCliProvider)
	toolCalls := extractToolCallsFromText(content)

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
		content = stripToolCallsFromText(content)
	}

	return &LLMResponse{
		Content:      strings.TrimSpace(content),
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		Usage:        usage,
	}, nil
}
