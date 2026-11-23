package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	oauthprovider "github.com/bogdanovich/mintclaw/pkg/providers/oauth"
)

var ErrCallBudget = errors.New("provider call budget stopped")

const CodexSubscriptionBudget = "codex_subscription"

// CallBudgetLimits bounds logical adapter calls, not provider-internal HTTP
// retries or billed tokens. Reported-token exhaustion stops subsequent calls;
// already admitted calls can overshoot that threshold.
type CallBudgetLimits struct {
	MaxCalls          int    `json:"max_calls"`
	MaxInputBytes     int    `json:"max_input_bytes"`
	MaxOutputTokens   int    `json:"max_output_tokens"`
	MaxReportedTokens int64  `json:"max_reported_tokens"`
	MaxInFlight       int    `json:"max_in_flight"`
	Mode              string `json:"mode,omitempty"`
	MaxCallSeconds    int    `json:"max_call_seconds,omitempty"`
}

// CallBudgetSnapshot contains no request, response, model or credential data.
// UsageKnown covers completed wrapped calls only, never unwrapped calls or
// provider-internal work. Sealed and InFlight == 0 are required for final use.
type CallBudgetSnapshot struct {
	Limits         CallBudgetLimits `json:"limits"`
	Calls          int              `json:"calls"`
	Completed      int              `json:"completed"`
	InFlight       int              `json:"in_flight"`
	ReportedTokens int64            `json:"reported_tokens"`
	UnknownUsage   int              `json:"unknown_usage"`
	StoppedReason  string           `json:"stopped_reason,omitempty"`
	Sealed         bool             `json:"sealed"`
	UsageKnown     bool             `json:"usage_known"`
}

// CallBudget is shared by all provider instances owned by one bounded run.
// It neither retries nor queues work. Normal providers are unchanged unless
// explicitly wrapped; callers must wrap every provider creation path.
type CallBudget struct {
	mu    sync.Mutex
	state CallBudgetSnapshot
}

func NewCallBudget(limits CallBudgetLimits) (*CallBudget, error) {
	if limits.Mode != "" || limits.MaxCallSeconds != 0 ||
		limits.MaxOutputTokens < 1 || limits.MaxOutputTokens > 32768 {
		return nil, fmt.Errorf("%w: invalid_limits", ErrCallBudget)
	}
	return newCallBudget(limits)
}

// NewSubscriptionCallBudget opts into logical call limits for Codex OAuth.
// There is no upstream output ceiling; reported totals limit later calls only.
func NewSubscriptionCallBudget(limits CallBudgetLimits) (*CallBudget, error) {
	if limits.Mode != CodexSubscriptionBudget || limits.MaxOutputTokens != 0 ||
		limits.MaxCallSeconds < 1 || limits.MaxCallSeconds > 180 || limits.MaxInFlight != 1 {
		return nil, fmt.Errorf("%w: invalid_limits", ErrCallBudget)
	}
	return newCallBudget(limits)
}

func newCallBudget(limits CallBudgetLimits) (*CallBudget, error) {
	if limits.MaxCalls < 1 || limits.MaxCalls > 256 ||
		limits.MaxInputBytes < 1 || limits.MaxInputBytes > 2<<20 ||
		limits.MaxReportedTokens < 1 || limits.MaxReportedTokens > 2_000_000 ||
		limits.MaxInFlight < 1 || limits.MaxInFlight > 2 {
		return nil, fmt.Errorf("%w: invalid_limits", ErrCallBudget)
	}
	return &CallBudget{state: CallBudgetSnapshot{Limits: limits}}, nil
}

func (b *CallBudget) Wrap(delegate LLMProvider) (LLMProvider, error) {
	if b == nil || nilInterface(delegate) {
		return nil, fmt.Errorf("%w: unsupported_provider", ErrCallBudget)
	}
	capabilities := Capabilities(delegate)
	subscription := b.state.Limits.Mode == CodexSubscriptionBudget
	if !capabilities.CallerMediatedTools || (!subscription && !capabilities.OutputTokenLimit) {
		return nil, fmt.Errorf("%w: unsupported_provider", ErrCallBudget)
	}
	if wrapped, ok := delegate.(*callBudgetProvider); ok {
		if wrapped.budget == b {
			return wrapped, nil
		}
		return nil, fmt.Errorf("%w: foreign_budget", ErrCallBudget)
	}
	if subscription {
		codex, ok := delegate.(*oauthprovider.CodexProvider)
		if !ok {
			return nil, fmt.Errorf("%w: unsupported_provider", ErrCallBudget)
		}
		delegate = codex.ForSubscriptionEval()
	}
	if capabilities.Streaming {
		if _, ok := delegate.(StreamingProvider); !ok {
			return nil, ErrStreamingContract
		}
	}
	return &callBudgetProvider{delegate: delegate, budget: b}, nil
}

// Seal refuses new calls without canceling calls already owned by the caller.
func (b *CallBudget) Seal() CallBudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state.Sealed = true
	return b.snapshotLocked()
}

func (b *CallBudget) Snapshot() CallBudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotLocked()
}

func (b *CallBudget) snapshotLocked() CallBudgetSnapshot {
	result := b.state
	result.UsageKnown = result.Completed == result.Calls && result.UnknownUsage == 0
	return result
}

func (b *CallBudget) stopLocked(reason string) error {
	if b.state.StoppedReason == "" {
		b.state.StoppedReason = reason
	}
	return fmt.Errorf("%w: %s", ErrCallBudget, b.state.StoppedReason)
}

func (b *CallBudget) begin(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	options map[string]any,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.StoppedReason != "" {
		return b.stopLocked(b.state.StoppedReason)
	}
	if b.state.Sealed {
		return b.stopLocked("sealed")
	}
	if b.state.Calls >= b.state.Limits.MaxCalls {
		return b.stopLocked("calls")
	}
	if b.state.InFlight >= b.state.Limits.MaxInFlight {
		return b.stopLocked("concurrency")
	}
	if b.state.Limits.Mode == "" {
		outputTokens, ok := options["max_tokens"].(int)
		if !ok || outputTokens < 1 || outputTokens > b.state.Limits.MaxOutputTokens {
			return b.stopLocked("output_limit")
		}
	}
	for _, message := range messages {
		if len(message.Media) != 0 || len(message.Attachments) != 0 {
			return b.stopLocked("media_unsupported")
		}
	}
	request, err := json.Marshal(struct {
		Messages []Message        `json:"messages"`
		Tools    []ToolDefinition `json:"tools"`
		Options  map[string]any   `json:"options"`
	}{messages, tools, options})
	if err != nil || len(request) > b.state.Limits.MaxInputBytes {
		return b.stopLocked("input_limit")
	}
	b.state.Calls++
	b.state.InFlight++
	return nil
}

func (b *CallBudget) finish(response *LLMResponse, callErr error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state.Completed++
	b.state.InFlight--
	if b.state.Limits.Mode == CodexSubscriptionBudget {
		var failure *ProviderError
		if errors.As(callErr, &failure) && failure != nil &&
			(failure.Kind == ProviderErrorRateLimit || failure.Kind == ProviderErrorBilling) {
			b.state.UnknownUsage++
			return b.stopLocked("subscription_limit")
		}
	}
	tokens, known := reportedCallTokens(response)
	if !known || callErr != nil || tokens > math.MaxInt64-b.state.ReportedTokens {
		b.state.UnknownUsage++
		return b.stopLocked("unknown_usage")
	}
	b.state.ReportedTokens += tokens
	if b.state.ReportedTokens >= b.state.Limits.MaxReportedTokens {
		return b.stopLocked("reported_tokens")
	}
	return nil
}

func reportedCallTokens(response *LLMResponse) (int64, bool) {
	if response == nil || response.Usage == nil {
		return 0, false
	}
	usage := response.Usage
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 {
		return 0, false
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		return 0, false
	}
	if (usage.CacheReadInputTokens != nil && *usage.CacheReadInputTokens < 0) ||
		(usage.CacheWriteInputTokens != nil && *usage.CacheWriteInputTokens < 0) {
		return 0, false
	}
	input, output := int64(usage.PromptTokens), int64(usage.CompletionTokens)
	if output > math.MaxInt64-input {
		return 0, false
	}
	return max(input+output, int64(usage.TotalTokens)), true
}

type callBudgetProvider struct {
	delegate LLMProvider
	budget   *CallBudget
}

func (p *callBudgetProvider) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	seconds := p.budget.state.Limits.MaxCallSeconds
	if seconds == 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
}

func (p *callBudgetProvider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (*LLMResponse, error) {
	options = CallerMediatedToolsOptions(options)
	if err := p.budget.begin(ctx, messages, tools, options); err != nil {
		return nil, err
	}
	ctx, cancel := p.requestContext(ctx)
	defer cancel()
	response, err := p.delegate.Chat(ctx, messages, tools, model, options)
	budgetErr := p.budget.finish(response, err)
	if err != nil || budgetErr != nil {
		return nil, errors.Join(err, budgetErr)
	}
	return response, nil
}

func (p *callBudgetProvider) ChatStreamEvents(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(StreamChunk),
) (*LLMResponse, error) {
	if !p.Capabilities().Streaming {
		return nil, ErrStreamingContract
	}
	options = CallerMediatedToolsOptions(options)
	if err := p.budget.begin(ctx, messages, tools, options); err != nil {
		return nil, err
	}
	ctx, cancel := p.requestContext(ctx)
	defer cancel()
	response, _, err := ChatStreamEvents(ctx, p.delegate, messages, tools, model, options, onChunk)
	budgetErr := p.budget.finish(response, err)
	if err != nil || budgetErr != nil {
		return nil, errors.Join(err, budgetErr)
	}
	return response, nil
}

func (p *callBudgetProvider) GetDefaultModel() string {
	return p.delegate.GetDefaultModel()
}

func (p *callBudgetProvider) Capabilities() ProviderCapabilities {
	result := Capabilities(p.delegate)
	result.NativeSearch = false
	result.ImageGeneration = ImageGenerationCapabilities{}
	return result
}

func (p *callBudgetProvider) Close() {
	if stateful, ok := p.delegate.(StatefulProvider); ok {
		stateful.Close()
	}
}
