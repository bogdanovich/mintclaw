package providers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

type budgetTestProvider struct {
	caps   ProviderCapabilities
	calls  atomic.Int32
	closed atomic.Bool
	chat   func(context.Context, map[string]any) (*LLMResponse, error)
}

func (p *budgetTestProvider) Chat(
	ctx context.Context, _ []Message, _ []ToolDefinition, _ string, options map[string]any,
) (*LLMResponse, error) {
	p.calls.Add(1)
	if p.chat != nil {
		return p.chat(ctx, options)
	}
	return &LLMResponse{Content: "private response", Usage: &UsageInfo{
		PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30,
	}}, nil
}

func (p *budgetTestProvider) ChatStreamEvents(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string,
	options map[string]any, chunk func(StreamChunk),
) (*LLMResponse, error) {
	chunk(StreamChunk{Content: "private chunk"})
	return p.Chat(ctx, messages, tools, model, options)
}

func (p *budgetTestProvider) Capabilities() ProviderCapabilities { return p.caps }
func (p *budgetTestProvider) GetDefaultModel() string            { return "private model" }
func (p *budgetTestProvider) Close()                             { p.closed.Store(true) }

func testCallBudget(t *testing.T, modify func(*CallBudgetLimits)) *CallBudget {
	t.Helper()
	limits := CallBudgetLimits{
		MaxCalls: 3, MaxInputBytes: 4096, MaxOutputTokens: 128,
		MaxReportedTokens: 1000, MaxInFlight: 2,
	}
	if modify != nil {
		modify(&limits)
	}
	budget, err := NewCallBudget(limits)
	if err != nil {
		t.Fatal(err)
	}
	return budget
}

func budgetProvider(t *testing.T, budget *CallBudget, provider *budgetTestProvider) LLMProvider {
	t.Helper()
	provider.caps.CallerMediatedTools = true
	wrapped, err := budget.Wrap(provider)
	if err != nil {
		t.Fatal(err)
	}
	return wrapped
}

func budgetChat(ctx context.Context, provider LLMProvider) (*LLMResponse, error) {
	return provider.Chat(ctx, []Message{{Role: "user", Content: "private prompt"}}, nil,
		"private model", map[string]any{"max_tokens": 128})
}

func TestCallBudgetSharesChatAndStreamingCalls(t *testing.T) {
	budget := testCallBudget(t, nil)
	first := &budgetTestProvider{caps: ProviderCapabilities{
		Streaming: true, NativeSearch: true,
		ImageGeneration: ImageGenerationCapabilities{Supported: true},
	}}
	second := &budgetTestProvider{}
	a, b := budgetProvider(t, budget, first), budgetProvider(t, budget, second)
	if _, err := budgetChat(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	chunks := 0
	_, attempted, err := ChatStreamEvents(t.Context(), a, nil, nil, "test",
		map[string]any{"max_tokens": 128}, func(StreamChunk) { chunks++ })
	if err != nil || !attempted || chunks != 1 {
		t.Fatalf("stream = %v/%v, chunks %d", attempted, err, chunks)
	}
	if _, err := budgetChat(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if _, err := budgetChat(t.Context(), a); !errors.Is(err, ErrCallBudget) {
		t.Fatalf("call limit error = %v", err)
	}
	state := budget.Seal()
	if state.Calls != 3 || state.Completed != 3 || state.InFlight != 0 ||
		state.ReportedTokens != 90 || !state.UsageKnown || !state.Sealed || state.StoppedReason != "calls" {
		t.Fatalf("state = %+v", state)
	}
	if first.calls.Load() != 2 || second.calls.Load() != 1 {
		t.Fatal("global limit did not cover both delegates")
	}
	if Capabilities(a).NativeSearch || Capabilities(a).ImageGeneration.Supported {
		t.Fatal("unbudgeted provider-owned operations advertised")
	}
	if _, ok := a.(ImageGenerationProvider); ok {
		t.Fatal("unbudgeted image operation exposed")
	}
	if a.GetDefaultModel() != first.GetDefaultModel() {
		t.Fatal("default model not forwarded")
	}
	a.(StatefulProvider).Close()
	if !first.closed.Load() {
		t.Fatal("close not forwarded")
	}
	encoded, err := json.Marshal(state)
	if err != nil || strings.Contains(string(encoded), "private") {
		t.Fatalf("snapshot retained content: %s, %v", encoded, err)
	}
}

func TestCallBudgetRefusesBeforeDelegating(t *testing.T) {
	for _, test := range []struct {
		name    string
		options map[string]any
		input   string
		reason  string
	}{
		{"missing output bound", nil, "x", "output_limit"},
		{"zero output bound", map[string]any{"max_tokens": 0}, "x", "output_limit"},
		{"negative output bound", map[string]any{"max_tokens": -1}, "x", "output_limit"},
		{"excess output bound", map[string]any{"max_tokens": 129}, "x", "output_limit"},
		{"ambiguous output type", map[string]any{"max_tokens": float64(128)}, "x", "output_limit"},
		{"excess input", map[string]any{"max_tokens": 128}, strings.Repeat("x", 4096), "input_limit"},
		{"invalid options", map[string]any{"max_tokens": 128, "bad": func() {}}, "x", "input_limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget := testCallBudget(t, nil)
			delegate := &budgetTestProvider{}
			provider := budgetProvider(t, budget, delegate)
			response, err := provider.Chat(t.Context(), []Message{{Content: test.input}}, nil, "test", test.options)
			if response != nil || !errors.Is(err, ErrCallBudget) || delegate.calls.Load() != 0 {
				t.Fatalf("refusal = %+v/%v, calls %d", response, err, delegate.calls.Load())
			}
			if state := budget.Snapshot(); state.Calls != 0 || state.StoppedReason != test.reason {
				t.Fatalf("state = %+v", state)
			}
		})
	}
}

func TestCallBudgetUnknownUsageStopsFurtherCalls(t *testing.T) {
	upstreamErr := errors.New("provider failed")
	overflowReason := "reported_tokens"
	if strconv.IntSize == 64 {
		overflowReason = "unknown_usage"
	}
	for _, test := range []struct {
		name     string
		response *LLMResponse
		err      error
		reason   string
	}{
		{"nil response", nil, nil, "unknown_usage"},
		{"missing usage", &LLMResponse{}, nil, "unknown_usage"},
		{"ambiguous zero usage", &LLMResponse{Usage: &UsageInfo{}}, nil, "unknown_usage"},
		{"negative usage", &LLMResponse{Usage: &UsageInfo{PromptTokens: -1}}, nil, "unknown_usage"},
		{"negative cache usage", &LLMResponse{Usage: &UsageInfo{CacheReadInputTokens: new(-1)}}, nil, "unknown_usage"},
		{"failed call", &LLMResponse{Usage: &UsageInfo{TotalTokens: 30}}, upstreamErr, "unknown_usage"},
		{
			"integer overflow", &LLMResponse{Usage: &UsageInfo{PromptTokens: math.MaxInt, CompletionTokens: math.MaxInt}}, nil,
			overflowReason,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget := testCallBudget(t, nil)
			delegate := &budgetTestProvider{chat: func(context.Context, map[string]any) (*LLMResponse, error) {
				return test.response, test.err
			}}
			provider := budgetProvider(t, budget, delegate)
			response, err := budgetChat(t.Context(), provider)
			if response != nil || !errors.Is(err, ErrCallBudget) ||
				(test.err != nil && !errors.Is(err, test.err)) {
				t.Fatalf("unknown response = %+v/%v", response, err)
			}
			_, _ = budgetChat(t.Context(), provider)
			state := budget.Seal()
			unknown := 1
			if test.reason == "reported_tokens" {
				unknown = 0
			}
			if delegate.calls.Load() != 1 || state.Calls != 1 || state.Completed != 1 ||
				state.UnknownUsage != unknown || state.UsageKnown != (unknown == 0) || state.StoppedReason != test.reason {
				t.Fatalf("state = %+v, delegate calls %d", state, delegate.calls.Load())
			}
		})
	}
}

func TestCallBudgetReportedTokensUseConservativeTotal(t *testing.T) {
	budget := testCallBudget(t, func(limits *CallBudgetLimits) { limits.MaxReportedTokens = 100 })
	delegate := &budgetTestProvider{chat: func(context.Context, map[string]any) (*LLMResponse, error) {
		return &LLMResponse{Usage: &UsageInfo{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 60}}, nil
	}}
	provider := budgetProvider(t, budget, delegate)
	if _, err := budgetChat(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	if response, err := budgetChat(t.Context(), provider); response != nil || !errors.Is(err, ErrCallBudget) {
		t.Fatalf("threshold = %+v/%v", response, err)
	}
	_, _ = budgetChat(t.Context(), provider)
	state := budget.Seal()
	if state.ReportedTokens != 120 || !state.UsageKnown || state.StoppedReason != "reported_tokens" ||
		delegate.calls.Load() != 2 {
		t.Fatalf("state = %+v", state)
	}
}

func TestCallBudgetConcurrentCallsAndSeal(t *testing.T) {
	budget := testCallBudget(t, nil)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	delegate := &budgetTestProvider{chat: func(ctx context.Context, _ map[string]any) (*LLMResponse, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return &LLMResponse{Usage: &UsageInfo{TotalTokens: 20}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	provider := budgetProvider(t, budget, delegate)
	finished := make(chan error, 2)
	for range 2 {
		go func() { _, err := budgetChat(t.Context(), provider); finished <- err }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-t.Context().Done():
			t.Fatal("calls never entered")
		}
	}
	state := budget.Seal()
	if state.InFlight != 2 || state.UsageKnown || !state.Sealed {
		t.Fatalf("in-flight state = %+v", state)
	}
	if _, err := budgetChat(t.Context(), provider); !errors.Is(err, ErrCallBudget) {
		t.Fatalf("sealed call = %v", err)
	}
	close(release)
	for range 2 {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
	state = budget.Snapshot()
	if state.InFlight != 0 || !state.UsageKnown || state.Completed != 2 || state.StoppedReason != "sealed" {
		t.Fatalf("settled state = %+v", state)
	}
}

func TestCallBudgetPreservesCancellationAndCallerMediatedOptions(t *testing.T) {
	budget := testCallBudget(t, nil)
	delegate := &budgetTestProvider{chat: func(ctx context.Context, options map[string]any) (*LLMResponse, error) {
		if options["native_search"] != false {
			t.Error("provider-native tools not disabled")
		}
		return &LLMResponse{Usage: &UsageInfo{PromptTokens: 1, TotalTokens: 1}}, ctx.Err()
	}}
	provider := budgetProvider(t, budget, delegate)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := budgetChat(ctx, provider); !errors.Is(err, context.Canceled) || delegate.calls.Load() != 0 {
		t.Fatalf("pre-cancel = %v, calls %d", err, delegate.calls.Load())
	}
	options := map[string]any{"max_tokens": 128, "native_search": true}
	if _, err := provider.Chat(t.Context(), nil, nil, "test", options); err != nil {
		t.Fatal(err)
	}
	if options["native_search"] != true || budget.Seal().ReportedTokens != 1 {
		t.Fatal("caller options mutated or zero output usage lost")
	}
}

func TestCallBudgetAdmissionAndNonStreamingFallback(t *testing.T) {
	budget := testCallBudget(t, nil)
	for _, delegate := range []LLMProvider{
		nil, (*budgetTestProvider)(nil), &descriptorTestProvider{},
		&descriptorTestProvider{capabilities: ProviderCapabilities{CallerMediatedTools: true, Streaming: true}},
	} {
		if _, err := budget.Wrap(delegate); err == nil {
			t.Fatal("unsupported provider admitted")
		}
	}
	delegate := &budgetTestProvider{}
	provider := budgetProvider(t, budget, delegate)
	if _, attempted, err := ChatStreamEvents(
		t.Context(),
		provider,
		nil,
		nil,
		"test",
		nil,
		nil,
	); attempted ||
		err != nil {
		t.Fatalf("non-stream fallback = %v/%v", attempted, err)
	}
	if budget.Snapshot().Calls != 0 {
		t.Fatal("unattempted stream consumed a call")
	}
	if again, err := budget.Wrap(provider); err != nil || again != provider {
		t.Fatal("same-budget wrapping was not idempotent")
	}
	if _, err := testCallBudget(t, nil).Wrap(provider); !errors.Is(err, ErrCallBudget) {
		t.Fatal("foreign budget accepted")
	}
	for _, limits := range []CallBudgetLimits{
		{},
		{MaxCalls: -1},
		{MaxCalls: 257},
		{MaxCalls: 1, MaxInputBytes: 1, MaxOutputTokens: 1, MaxReportedTokens: 1, MaxInFlight: 3},
	} {
		if _, err := NewCallBudget(limits); !errors.Is(err, ErrCallBudget) {
			t.Fatalf("invalid limits accepted: %+v", limits)
		}
	}
}

func TestCallBudgetConcurrencyRefusalAndInFlightCancellation(t *testing.T) {
	for _, cancelActive := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelActive), func(t *testing.T) {
			budget := testCallBudget(t, func(limits *CallBudgetLimits) { limits.MaxInFlight = 1 })
			entered, release := make(chan struct{}), make(chan struct{})
			delegate := &budgetTestProvider{chat: func(ctx context.Context, _ map[string]any) (*LLMResponse, error) {
				close(entered)
				select {
				case <-release:
					return &LLMResponse{Usage: &UsageInfo{TotalTokens: 20}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}}
			provider := budgetProvider(t, budget, delegate)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			finished := make(chan error, 1)
			go func() { _, err := budgetChat(ctx, provider); finished <- err }()
			select {
			case <-entered:
			case <-t.Context().Done():
				t.Fatal("call never entered")
			}
			if cancelActive {
				cancel()
				if err := <-finished; !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCallBudget) {
					t.Fatalf("in-flight cancellation = %v", err)
				}
			} else {
				if _, err := budgetChat(t.Context(), provider); !errors.Is(err, ErrCallBudget) {
					t.Fatalf("concurrent refusal = %v", err)
				}
				close(release)
				if err := <-finished; err != nil {
					t.Fatal(err)
				}
			}
			state := budget.Seal()
			wantReason := "concurrency"
			if cancelActive {
				wantReason = "unknown_usage"
			}
			if state.Calls != 1 || state.Completed != 1 || state.InFlight != 0 ||
				state.StoppedReason != wantReason || state.UsageKnown == cancelActive || delegate.calls.Load() != 1 {
				t.Fatalf("state = %+v", state)
			}
		})
	}
}

func TestCallBudgetCountsAllInputSurfacesAndRejectsMedia(t *testing.T) {
	for _, test := range []struct {
		name    string
		message Message
		tools   []ToolDefinition
		extra   any
		reason  string
	}{
		{"tool schema", Message{}, []ToolDefinition{{Function: ToolFunctionDefinition{
			Description: strings.Repeat("x", 4096),
		}}}, nil, "input_limit"},
		{"options", Message{}, nil, strings.Repeat("x", 4096), "input_limit"},
		{"media reference", Message{Media: []string{"media://private"}}, nil, nil, "media_unsupported"},
		{"attachment", Message{Attachments: []Attachment{{Ref: "media://private"}}}, nil, nil, "media_unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget := testCallBudget(t, nil)
			delegate := &budgetTestProvider{}
			provider := budgetProvider(t, budget, delegate)
			_, err := provider.Chat(t.Context(), []Message{test.message}, test.tools, "test",
				map[string]any{"max_tokens": 128, "extra": test.extra})
			if !errors.Is(err, ErrCallBudget) || delegate.calls.Load() != 0 ||
				budget.Snapshot().StoppedReason != test.reason {
				t.Fatalf("admission = %v, state %+v", err, budget.Snapshot())
			}
		})
	}
}

func TestCallBudgetValidatesEveryLimit(t *testing.T) {
	valid := CallBudgetLimits{
		MaxCalls: 1, MaxInputBytes: 1, MaxOutputTokens: 1, MaxReportedTokens: 1, MaxInFlight: 1,
	}
	for _, mutate := range []func(*CallBudgetLimits){
		func(l *CallBudgetLimits) { l.MaxCalls = 0 },
		func(l *CallBudgetLimits) { l.MaxCalls = 257 },
		func(l *CallBudgetLimits) { l.MaxInputBytes = 0 },
		func(l *CallBudgetLimits) { l.MaxInputBytes = 2<<20 + 1 },
		func(l *CallBudgetLimits) { l.MaxOutputTokens = 0 },
		func(l *CallBudgetLimits) { l.MaxOutputTokens = 32769 },
		func(l *CallBudgetLimits) { l.MaxReportedTokens = 0 },
		func(l *CallBudgetLimits) { l.MaxReportedTokens = 2_000_001 },
		func(l *CallBudgetLimits) { l.MaxInFlight = 0 },
		func(l *CallBudgetLimits) { l.MaxInFlight = 3 },
	} {
		limits := valid
		mutate(&limits)
		if _, err := NewCallBudget(limits); !errors.Is(err, ErrCallBudget) {
			t.Fatalf("invalid limits admitted: %+v", limits)
		}
	}
	if _, err := NewCallBudget(valid); err != nil {
		t.Fatal(err)
	}
	maximum := CallBudgetLimits{
		MaxCalls: 256, MaxInputBytes: 2 << 20, MaxOutputTokens: 32768, MaxReportedTokens: 2_000_000, MaxInFlight: 2,
	}
	if _, err := NewCallBudget(maximum); err != nil {
		t.Fatal(err)
	}
}

func TestCallBudgetAnthropicMissingUsageIsNotKnownZero(t *testing.T) {
	for _, test := range []struct {
		name, usage string
		known       bool
	}{
		{"absent", "", false},
		{"ambiguous zero", `,"usage":{"input_tokens":0,"output_tokens":0}`, false},
		{"positive input and zero output", `,"usage":{"input_tokens":2,"output_tokens":0}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"id":"synthetic","type":"message","role":"assistant",` +
					`"model":"claude-fixture","content":[{"type":"text","text":"synthetic"}],` +
					`"stop_reason":"end_turn"` + test.usage + `}`))
			}))
			defer server.Close()
			delegate := NewClaudeProviderWithBaseURL("synthetic-fixture-key", server.URL)
			budget := testCallBudget(t, nil)
			provider, err := budget.Wrap(delegate)
			if err != nil {
				t.Fatal(err)
			}
			response, err := budgetChat(t.Context(), provider)
			state := budget.Seal()
			if test.known {
				if err != nil || response == nil || !state.UsageKnown || state.ReportedTokens != 2 {
					t.Fatalf("reported usage = %+v/%v, state %+v", response, err, state)
				}
			} else if response != nil || !errors.Is(err, ErrCallBudget) || state.UsageKnown || state.UnknownUsage != 1 {
				t.Fatalf("unavailable usage = %+v/%v, state %+v", response, err, state)
			}
		})
	}
}
