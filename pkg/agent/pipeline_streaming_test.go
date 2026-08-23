package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type configuredStreamingProvider struct {
	chatCalls    int
	streamCalls  int
	eventCalls   int
	chatModels   []string
	streamModels []string

	chatResponse *providers.LLMResponse
	streamPlan   []configuredStreamingCall
	eventPlan    []configuredStreamingEventCall
	afterEvents  func()
}

type configuredStreamingCall struct {
	chunks   []string
	response *providers.LLMResponse
	err      error
}

type configuredStreamingEventCall struct {
	chunks   []providers.StreamChunk
	response *providers.LLMResponse
	err      error
}

func (p *configuredStreamingProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.chatCalls++
	p.chatModels = append(p.chatModels, model)
	if p.chatResponse != nil {
		return p.chatResponse, nil
	}
	return &providers.LLMResponse{Content: "chat response"}, nil
}

func (p *configuredStreamingProvider) ChatStream(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
	onChunk func(accumulated string),
) (*providers.LLMResponse, error) {
	p.streamCalls++
	p.streamModels = append(p.streamModels, model)
	var plan configuredStreamingCall
	if len(p.streamPlan) >= p.streamCalls {
		plan = p.streamPlan[p.streamCalls-1]
	}
	for _, chunk := range plan.chunks {
		onChunk(chunk)
	}
	if plan.err != nil {
		return nil, plan.err
	}
	if plan.response != nil {
		return plan.response, nil
	}
	return &providers.LLMResponse{Content: "stream response"}, nil
}

func (p *configuredStreamingProvider) ChatStreamEvents(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
	onChunk func(providers.StreamChunk),
) (*providers.LLMResponse, error) {
	p.eventCalls++
	p.streamCalls++
	p.streamModels = append(p.streamModels, model)
	var plan configuredStreamingEventCall
	if len(p.eventPlan) >= p.eventCalls {
		plan = p.eventPlan[p.eventCalls-1]
	} else if len(p.streamPlan) >= p.eventCalls {
		legacyPlan := p.streamPlan[p.eventCalls-1]
		plan.response = legacyPlan.response
		plan.err = legacyPlan.err
		for _, chunk := range legacyPlan.chunks {
			plan.chunks = append(plan.chunks, providers.StreamChunk{Content: chunk})
		}
	}
	for _, chunk := range plan.chunks {
		onChunk(chunk)
	}
	if p.afterEvents != nil {
		p.afterEvents()
	}
	if plan.err != nil {
		return nil, plan.err
	}
	if plan.response != nil {
		return plan.response, nil
	}
	return &providers.LLMResponse{Content: "stream response"}, nil
}

func (p *configuredStreamingProvider) GetDefaultModel() string {
	return "mock-model"
}

func (p *configuredStreamingProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{
		Streaming: providers.StreamingCapabilities{Supported: true, Events: true},
	}
}

type configuredStreamingChatOnlyProvider struct {
	chatCalls int
}

func (p *configuredStreamingChatOnlyProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.chatCalls++
	return &providers.LLMResponse{Content: "chat only"}, nil
}

func (p *configuredStreamingChatOnlyProvider) GetDefaultModel() string {
	return "mock-model"
}

type configuredStreamingDelegate struct {
	streamer   bus.Streamer
	traceScope *runtimeevents.TraceScope
}

func (d configuredStreamingDelegate) GetStreamer(
	ctx context.Context,
	channel, chatID, sessionKey, requestID string,
	traceScope runtimeevents.TraceScope,
) (bus.Streamer, bool) {
	if d.traceScope != nil {
		*d.traceScope = traceScope
	}
	if d.streamer == nil {
		return nil, false
	}
	return d.streamer, true
}

type recordingStreamer struct {
	updates            []string
	finalized          []string
	reasoningUpdates   []string
	reasoningFinalized []string
	events             []string
	canceled           int
}

func (s *recordingStreamer) Update(ctx context.Context, content string) error {
	s.updates = append(s.updates, content)
	s.events = append(s.events, "content:"+content)
	return nil
}

func (s *recordingStreamer) Finalize(ctx context.Context, content string) error {
	s.finalized = append(s.finalized, content)
	s.events = append(s.events, "final:"+content)
	return nil
}

func (s *recordingStreamer) UpdateReasoning(ctx context.Context, content string) error {
	s.reasoningUpdates = append(s.reasoningUpdates, content)
	s.events = append(s.events, "reasoning:"+content)
	return nil
}

func (s *recordingStreamer) FinalizeReasoning(ctx context.Context, content string) error {
	s.reasoningFinalized = append(s.reasoningFinalized, content)
	s.events = append(s.events, "reasoning-final:"+content)
	return nil
}

func (s *recordingStreamer) Cancel(context.Context) {
	s.canceled++
}

type cleanableRecordingStreamer struct {
	recordingStreamer
	clearMarkers int
}

func (s *cleanableRecordingStreamer) ClearFinalizedStreamMarker() {
	s.clearMarkers++
}

type turnUsageRecordingStreamer struct {
	recordingStreamer
	inputTokens  int
	outputTokens int
	usageCalls   int
}

func (s *turnUsageRecordingStreamer) SetTurnUsage(inputTokens, outputTokens int) {
	s.usageCalls++
	s.inputTokens = inputTokens
	s.outputTokens = outputTokens
}

type failingFinalizeStreamer struct {
	recordingStreamer
	err error
}

func (s *failingFinalizeStreamer) Finalize(ctx context.Context, content string) error {
	s.finalized = append(s.finalized, content)
	return s.err
}

type failingUpdateStreamer struct {
	recordingStreamer
	err error
}

func (s *failingUpdateStreamer) Update(ctx context.Context, content string) error {
	s.updates = append(s.updates, content)
	return s.err
}

type failNthUpdateStreamer struct {
	recordingStreamer
	failOn int
	err    error
}

func (s *failNthUpdateStreamer) Update(ctx context.Context, content string) error {
	s.updates = append(s.updates, content)
	if len(s.updates) == s.failOn {
		return s.err
	}
	return nil
}

type configuredStreamingAfterHook struct {
	content string
	action  HookAction
}

func (h configuredStreamingAfterHook) BeforeLLM(
	ctx context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	return req, HookDecision{Action: HookActionContinue}, nil
}

func (h configuredStreamingAfterHook) AfterLLM(
	ctx context.Context,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	if h.action == HookActionAbortTurn || h.action == HookActionHardAbort {
		return resp, HookDecision{Action: h.action}, nil
	}
	next := resp.Clone()
	next.Response.Content = h.content
	return next, HookDecision{Action: HookActionModify}, nil
}

type configuredStreamingBeforeModelHook struct {
	model string
}

func (h configuredStreamingBeforeModelHook) BeforeLLM(
	ctx context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	next := req.Clone()
	next.Model = h.model
	return next, HookDecision{Action: HookActionModify}, nil
}

func (h configuredStreamingBeforeModelHook) AfterLLM(
	ctx context.Context,
	resp *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	return resp, HookDecision{Action: HookActionContinue}, nil
}

func TestConfiguredStreamingEligibilityGates(t *testing.T) {
	tests := []struct {
		name              string
		channel           string
		channelStreaming  bool
		modelStreaming    bool
		fallbacks         []string
		streamingProvider bool
		streamDelegate    bool
		wantStreamCalls   int
		wantChatCalls     int
	}{
		{
			name:              "channel and model enabled streams",
			channel:           "mintclaw",
			channelStreaming:  true,
			modelStreaming:    true,
			streamingProvider: true,
			streamDelegate:    true,
			wantStreamCalls:   1,
		},
		{
			name:              "wecom channel and model enabled streams",
			channel:           "wecom",
			channelStreaming:  true,
			modelStreaming:    true,
			streamingProvider: true,
			streamDelegate:    true,
			wantStreamCalls:   1,
		},
		{
			name:              "channel disabled uses chat",
			channel:           "mintclaw",
			modelStreaming:    true,
			streamingProvider: true,
			streamDelegate:    true,
			wantChatCalls:     1,
		},
		{
			name:              "model disabled uses chat",
			channel:           "mintclaw",
			channelStreaming:  true,
			streamingProvider: true,
			streamDelegate:    true,
			wantChatCalls:     1,
		},
		{
			name:             "provider without streaming uses chat",
			channel:          "mintclaw",
			channelStreaming: true,
			modelStreaming:   true,
			streamDelegate:   true,
			wantChatCalls:    1,
		},
		{
			name:              "multi candidate fallback uses chat",
			channel:           "mintclaw",
			channelStreaming:  true,
			modelStreaming:    true,
			fallbacks:         []string{"fallback-model"},
			streamingProvider: true,
			streamDelegate:    true,
			wantChatCalls:     1,
		},
		{
			name:              "missing streamer uses chat",
			channel:           "mintclaw",
			channelStreaming:  true,
			modelStreaming:    true,
			streamingProvider: true,
			wantChatCalls:     1,
		},
		{
			name:              "omitted fields use chat",
			channel:           "mintclaw",
			streamingProvider: true,
			streamDelegate:    true,
			wantChatCalls:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newConfiguredStreamingTestConfig(
				t,
				tt.channelStreaming,
				tt.modelStreaming,
				tt.fallbacks,
			)
			msgBus := bus.NewMessageBus()
			if tt.streamDelegate {
				msgBus.SetStreamDelegate(
					configuredStreamingDelegate{streamer: &recordingStreamer{}},
				)
			}

			if tt.streamingProvider {
				provider := &configuredStreamingProvider{}
				al := NewAgentLoop(cfg, msgBus, provider)
				runConfiguredStreamingTurn(t, al, tt.channel)
				if provider.streamCalls != tt.wantStreamCalls {
					t.Fatalf(
						"ChatStream calls = %d, want %d",
						provider.streamCalls,
						tt.wantStreamCalls,
					)
				}
				if provider.chatCalls != tt.wantChatCalls {
					t.Fatalf("Chat calls = %d, want %d", provider.chatCalls, tt.wantChatCalls)
				}
				return
			}

			provider := &configuredStreamingChatOnlyProvider{}
			al := NewAgentLoop(cfg, msgBus, provider)
			runConfiguredStreamingTurn(t, al, tt.channel)
			if provider.chatCalls != tt.wantChatCalls {
				t.Fatalf("Chat calls = %d, want %d", provider.chatCalls, tt.wantChatCalls)
			}
		})
	}
}

func TestConfiguredStreamingUsesChatInsideDurableOutboundTransaction(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	streamer := &recordingStreamer{}
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	_, err := al.runAgentLoop(
		withOutboundTransaction(t.Context(), "spool-streaming"),
		al.GetRegistry().GetDefaultAgent(),
		configuredStreamingTurnSpec("mintclaw"),
	)
	if err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	if provider.streamCalls != 0 || provider.chatCalls != 1 {
		t.Fatalf("provider calls = stream:%d chat:%d, want stream:0 chat:1", provider.streamCalls, provider.chatCalls)
	}
	if len(streamer.updates) != 0 || len(streamer.finalized) != 0 {
		t.Fatalf("streamer activity = updates:%v finalized:%v", streamer.updates, streamer.finalized)
	}
}

func TestPipelineChannelStreamingConfigUsesConfig(t *testing.T) {
	pipeline := &Pipeline{
		Cfg: newConfiguredStreamingTestConfig(t, true, true, nil),
	}

	got, ok := pipeline.channelStreamingConfig("mintclaw")
	if !ok {
		t.Fatal("channelStreamingConfig() ok = false, want true")
	}
	if !got.Enabled {
		t.Fatal("channelStreamingConfig().Enabled = false, want true")
	}
}

func TestConfiguredStreamingPreChunkFailureFallsBackToChat(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: &recordingStreamer{}})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{{
			err: errors.New("stream setup failed"),
		}},
		chatResponse: &providers.LLMResponse{Content: "chat after stream failure"},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	got := runConfiguredStreamingTurn(t, al, "mintclaw")

	if got != "chat after stream failure" {
		t.Fatalf("response = %q, want chat fallback response", got)
	}
	if provider.streamCalls != 1 || provider.chatCalls != 1 {
		t.Fatalf(
			"calls = stream:%d chat:%d, want stream:1 chat:1",
			provider.streamCalls,
			provider.chatCalls,
		)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.Content != "chat after stream failure" {
			t.Fatalf(
				"fallback outbound content = %q, want chat after stream failure",
				outbound.Content,
			)
		}
	case <-time.After(time.Second):
		t.Fatal("expected fallback outbound after pre-chunk stream failure")
	}
}

func TestConfiguredStreamingDisabledForInternalTurnWithoutUserVisibleOutput(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	streamer := &recordingStreamer{}
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{{
			chunks:   []string{"internal stream"},
			response: &providers.LLMResponse{Content: "stream response"},
		}},
		chatResponse: &providers.LLMResponse{Content: "chat response"},
	}
	al := NewAgentLoop(cfg, msgBus, provider)
	opts := configuredStreamingTurnSpec("mintclaw")
	opts.SendResponse = false
	opts.AllowInterimMintClawPublish = false

	got, err := al.runAgentLoop(context.Background(), al.GetRegistry().GetDefaultAgent(), opts)
	if err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	if got != "chat response" {
		t.Fatalf("response = %q, want chat response", got)
	}
	if provider.streamCalls != 0 || provider.chatCalls != 1 {
		t.Fatalf(
			"calls = stream:%d chat:%d, want stream:0 chat:1",
			provider.streamCalls,
			provider.chatCalls,
		)
	}
	if len(streamer.updates) != 0 || len(streamer.finalized) != 0 {
		t.Fatalf(
			"streamer updates=%v finalized=%v, want no streaming output",
			streamer.updates,
			streamer.finalized,
		)
	}
}

func TestConfiguredStreamingVisibleSendResponseFalseRetainsFinalizedStreamMarker(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	streamer := &cleanableRecordingStreamer{}
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{{
			chunks:   []string{"visible stream"},
			response: &providers.LLMResponse{Content: "stream response"},
		}},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	got := runConfiguredStreamingTurn(t, al, "mintclaw")

	if got != "stream response" {
		t.Fatalf("response = %q, want stream response", got)
	}
	if streamer.clearMarkers != 0 {
		t.Fatalf("clear markers = %d, want 0", streamer.clearMarkers)
	}
}

func TestConfiguredStreamingStreamsMintClawReasoningBeforeAnswerContent(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	streamer := &recordingStreamer{}
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		eventPlan: []configuredStreamingEventCall{{
			chunks: []providers.StreamChunk{
				{ReasoningContent: "thinking"},
				{ReasoningContent: "thinking more"},
				{Content: "answer"},
			},
			response: &providers.LLMResponse{
				Content:          "answer",
				ReasoningContent: "thinking more",
			},
		}},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	got := runConfiguredStreamingTurn(t, al, "mintclaw")
	if got != "answer" {
		t.Fatalf("response = %q, want answer", got)
	}
	if provider.eventCalls != 1 {
		t.Fatalf("ChatStreamEvents calls = %d, want 1", provider.eventCalls)
	}
	if len(streamer.reasoningUpdates) != 2 {
		t.Fatalf("reasoning updates = %v, want two streamed updates", streamer.reasoningUpdates)
	}
	if len(streamer.updates) != 1 || streamer.updates[0] != "answer" {
		t.Fatalf("content updates = %v, want [answer]", streamer.updates)
	}
	if len(streamer.events) < 3 ||
		streamer.events[0] != "reasoning:thinking" ||
		streamer.events[1] != "reasoning:thinking more" ||
		streamer.events[2] != "content:answer" {
		t.Fatalf("stream event order = %v, want reasoning before answer content", streamer.events)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		t.Fatalf("expected streamed reasoning to avoid a later thought outbound, got %+v", outbound)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestConfiguredStreamingProjectsProviderAccumulatedEvents(t *testing.T) {
	const sessionKey = "agent:main:mintclaw:session-1"
	projector, err := frontend.NewProjector(sessionKey, frontend.ProjectionLimits{})
	if err != nil {
		t.Fatalf("NewProjector() error = %v", err)
	}
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(frontend.NewStreamDelegate(projector, sessionKey))
	provider := &configuredStreamingProvider{
		eventPlan: []configuredStreamingEventCall{{
			chunks: []providers.StreamChunk{
				{ReasoningContent: "thinking"},
				{ReasoningContent: "thinking 💡"},
				{Content: "answer"},
				{Content: "answer ✅"},
			},
			response: &providers.LLMResponse{
				Content:          "answer ✅",
				ReasoningContent: "thinking 💡",
			},
		}},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	if got := runConfiguredStreamingTurn(t, al, "mintclaw"); got != "answer ✅" {
		t.Fatalf("response = %q, want accumulated provider answer", got)
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot.Entries) != 2 {
		t.Fatalf("entries = %#v, want one reasoning and one assistant entry", snapshot.Entries)
	}
	if got := snapshot.Entries[0]; got.Kind != frontend.EntryReasoning || got.Text != "thinking 💡" || !got.Complete {
		t.Fatalf("reasoning entry = %#v, want complete accumulated reasoning", got)
	}
	if got := snapshot.Entries[1]; got.Kind != frontend.EntryAssistant || got.Text != "answer ✅" || !got.Complete {
		t.Fatalf("assistant entry = %#v, want complete accumulated answer", got)
	}
}

func TestConfiguredStreamingReasoningOnlyFailureDiscardsAttemptBeforeFallback(t *testing.T) {
	const sessionKey = "agent:main:mintclaw:session-1"
	projector, err := frontend.NewProjector(sessionKey, frontend.ProjectionLimits{})
	if err != nil {
		t.Fatalf("NewProjector() error = %v", err)
	}
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(frontend.NewStreamDelegate(projector, sessionKey))
	provider := &configuredStreamingProvider{
		eventPlan: []configuredStreamingEventCall{{
			chunks: []providers.StreamChunk{{ReasoningContent: "failed provider reasoning"}},
			err:    errors.New("stream failed after reasoning"),
		}},
		chatResponse: &providers.LLMResponse{Content: "fallback answer"},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	if got := runConfiguredStreamingTurn(t, al, "mintclaw"); got != "fallback answer" {
		t.Fatalf("response = %q, want fallback answer", got)
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot.Entries) != 0 {
		t.Fatalf(
			"failed provider stream entries = %#v, want none before fallback turn-end projection",
			snapshot.Entries,
		)
	}
}

func TestConfiguredStreamingLateSteeringDiscardsFinalizedReasoningAttempt(t *testing.T) {
	const sessionKey = "agent:main:mintclaw:session-1"
	projector, err := frontend.NewProjector(sessionKey, frontend.ProjectionLimits{})
	if err != nil {
		t.Fatalf("NewProjector() error = %v", err)
	}
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(frontend.NewStreamDelegate(projector, sessionKey))
	provider := &configuredStreamingProvider{
		eventPlan: []configuredStreamingEventCall{
			{
				chunks: []providers.StreamChunk{{ReasoningContent: "discarded reasoning"}},
				response: &providers.LLMResponse{
					Content: "discarded answer", ReasoningContent: "discarded reasoning",
				},
			},
			{
				chunks:   []providers.StreamChunk{{Content: "final answer"}},
				response: &providers.LLMResponse{Content: "final answer"},
			},
		},
	}
	var al *AgentLoop
	provider.afterEvents = func() {
		if provider.eventCalls != 1 {
			return
		}
		agent := al.GetRegistry().GetDefaultAgent()
		if steerErr := al.Steer(agent.Workspace, sessionKey, agent.ID, providers.Message{
			Role: "user", Content: "new direction",
		}); steerErr != nil {
			t.Errorf("Steer() error = %v", steerErr)
		}
	}
	al = NewAgentLoop(cfg, msgBus, provider)

	if got := runConfiguredStreamingTurn(t, al, "mintclaw"); got != "final answer" {
		t.Fatalf("response = %q, want steered final answer", got)
	}
	snapshot, err := projector.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Kind != frontend.EntryAssistant ||
		snapshot.Entries[0].Text != "final answer" || !snapshot.Entries[0].Complete {
		t.Fatalf("steered stream entries = %#v, want only final answer", snapshot.Entries)
	}
}

func TestConfiguredStreamingSuppressesMintClawReasoningWhenThinkingOff(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	cfg.ModelList[0].ThinkingLevel = "off"
	streamer := &recordingStreamer{}
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		eventPlan: []configuredStreamingEventCall{{
			chunks: []providers.StreamChunk{
				{ReasoningContent: "thinking"},
				{Content: "answer"},
			},
			response: &providers.LLMResponse{
				Content:          "answer",
				ReasoningContent: "thinking",
			},
		}},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	got := runConfiguredStreamingTurn(t, al, "mintclaw")
	if got != "answer" {
		t.Fatalf("response = %q, want answer", got)
	}
	if len(streamer.reasoningUpdates) != 0 {
		t.Fatalf(
			"reasoning updates = %v, want none when thinking is off",
			streamer.reasoningUpdates,
		)
	}
	if len(streamer.reasoningFinalized) != 0 {
		t.Fatalf(
			"reasoning finalized = %v, want none when thinking is off",
			streamer.reasoningFinalized,
		)
	}
	if len(streamer.updates) != 1 || streamer.updates[0] != "answer" {
		t.Fatalf("content updates = %v, want [answer]", streamer.updates)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		t.Fatalf("expected no reasoning outbound when thinking is off, got %+v", outbound)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestConfiguredStreamingFinalFlushFailureAfterVisibleOutputReturnsErrorWithoutFallbackOrCancel(
	t *testing.T,
) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	streamer := &failingFinalizeStreamer{err: errors.New("final failed")}
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{{
			chunks:   []string{"partial stream"},
			response: &providers.LLMResponse{Content: "stream response"},
		}},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	_, err := al.runAgentLoop(
		context.Background(),
		al.GetRegistry().GetDefaultAgent(),
		configuredStreamingTurnSpec("mintclaw"),
	)
	if err == nil {
		t.Fatal("expected final flush failure after visible output to return an error")
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		t.Fatalf("unexpected fallback outbound after visible final flush failure: %#v", outbound)
	default:
	}
	if streamer.canceled != 0 {
		t.Fatalf(
			"streamer canceled = %d, want 0 for already-visible final flush failure",
			streamer.canceled,
		)
	}
}

func TestConfiguredStreamingFinalFlushFailureBeforeVisibleOutputPublishesFallback(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	streamer := &failingFinalizeStreamer{err: errors.New("final failed")}
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{{
			response: &providers.LLMResponse{Content: "stream response"},
		}},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	got := runConfiguredStreamingTurn(t, al, "mintclaw")

	if got != "stream response" {
		t.Fatalf("response = %q, want stream response", got)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.Content != "stream response" {
			t.Fatalf("fallback outbound content = %q, want stream response", outbound.Content)
		}
		if got := outbound.Context.Raw["model_name"]; got != "test-model" {
			t.Fatalf("fallback outbound model_name = %q, want %q", got, "test-model")
		}
	case <-time.After(time.Second):
		t.Fatal("expected fallback outbound after invisible final stream flush failure")
	}
	if streamer.canceled != 1 {
		t.Fatalf("streamer canceled = %d, want 1", streamer.canceled)
	}
}

func TestConfiguredStreamingFinalFlushFailureBeforeVisibleOutputKeepsNormalOutbound(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	streamer := &failingFinalizeStreamer{err: errors.New("final failed")}
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{{
			response: &providers.LLMResponse{Content: "stream response"},
		}},
	}
	al := NewAgentLoop(cfg, msgBus, provider)
	opts := configuredStreamingTurnSpec("mintclaw")
	opts.SendResponse = true

	got, err := al.runAgentLoop(context.Background(), al.GetRegistry().GetDefaultAgent(), opts)
	if err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	if got != "stream response" {
		t.Fatalf("response = %q, want stream response", got)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.Content != "stream response" {
			t.Fatalf("normal outbound content = %q, want stream response", outbound.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("expected normal outbound after invisible final stream flush failure")
	}
	if streamer.canceled != 1 {
		t.Fatalf("streamer canceled = %d, want 1", streamer.canceled)
	}
}

func TestConfiguredStreamingUpdateFailureThenStreamErrorFallsBackToChat(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	msgBus := bus.NewMessageBus()
	streamer := &failingUpdateStreamer{err: errors.New("draft failed")}
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{{
			chunks: []string{"not visible"},
			err:    errors.New("stream failed after invisible update"),
		}},
		chatResponse: &providers.LLMResponse{Content: "chat fallback after invisible update"},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	got := runConfiguredStreamingTurn(t, al, "mintclaw")

	if got != "chat fallback after invisible update" {
		t.Fatalf("response = %q, want chat fallback", got)
	}
	if provider.streamCalls != 1 || provider.chatCalls != 1 {
		t.Fatalf(
			"calls = stream:%d chat:%d, want stream:1 chat:1",
			provider.streamCalls,
			provider.chatCalls,
		)
	}
	if streamer.canceled != 1 {
		t.Fatalf("streamer canceled = %d, want 1", streamer.canceled)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.Content != "chat fallback after invisible update" {
			t.Fatalf(
				"fallback outbound content = %q, want chat fallback after invisible update",
				outbound.Content,
			)
		}
	case <-time.After(time.Second):
		t.Fatal("expected fallback outbound after update failure and stream error")
	}
}

func TestConfiguredStreamingUpdateFailureThenStreamSuccessFallsBackToChat(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	msgBus := bus.NewMessageBus()
	streamer := &failingUpdateStreamer{err: errors.New("draft failed")}
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{{
			chunks:   []string{"not visible"},
			response: &providers.LLMResponse{Content: "stream response"},
		}},
		chatResponse: &providers.LLMResponse{Content: "chat fallback after invisible update"},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	got := runConfiguredStreamingTurn(t, al, "mintclaw")

	if got != "chat fallback after invisible update" {
		t.Fatalf("response = %q, want chat fallback", got)
	}
	if provider.streamCalls != 1 || provider.chatCalls != 1 {
		t.Fatalf(
			"calls = stream:%d chat:%d, want stream:1 chat:1",
			provider.streamCalls,
			provider.chatCalls,
		)
	}
	if len(streamer.finalized) != 0 {
		t.Fatalf("stream finalized = %v, want none", streamer.finalized)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.Content != "chat fallback after invisible update" {
			t.Fatalf(
				"fallback outbound content = %q, want chat fallback after invisible update",
				outbound.Content,
			)
		}
	case <-time.After(time.Second):
		t.Fatal("expected fallback outbound after update failure and stream success")
	}
}

func TestConfiguredStreamingLaterUpdateFailureThenStreamSuccessReturnsVisibleError(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	msgBus := bus.NewMessageBus()
	streamer := &failNthUpdateStreamer{failOn: 2, err: errors.New("draft failed")}
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{{
			chunks:   []string{"visible chunk", "failed later chunk"},
			response: &providers.LLMResponse{Content: "stream response"},
		}},
		chatResponse: &providers.LLMResponse{Content: "chat fallback after later update failure"},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	_, err := al.runAgentLoop(
		context.Background(),
		al.GetRegistry().GetDefaultAgent(),
		configuredStreamingTurnSpec("mintclaw"),
	)
	if err == nil {
		t.Fatal("expected post-visible update failure to return an error")
	}
	if provider.streamCalls != 1 || provider.chatCalls != 0 {
		t.Fatalf(
			"calls = stream:%d chat:%d, want stream:1 chat:0",
			provider.streamCalls,
			provider.chatCalls,
		)
	}
	if streamer.canceled != 0 {
		t.Fatalf("streamer canceled = %d, want 0", streamer.canceled)
	}
	if len(streamer.finalized) != 0 {
		t.Fatalf("stream finalized = %v, want none", streamer.finalized)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		t.Fatalf("unexpected fallback outbound after post-visible update failure: %#v", outbound)
	default:
	}
}

func TestConfiguredStreamingBeforeLLMModelRewriteReevaluatesModelStreaming(t *testing.T) {
	tests := []struct {
		name                   string
		initialModelStreaming  bool
		rewriteModel           string
		rewriteModelStreaming  bool
		fallbacks              []string
		wantStreamCalls        int
		wantChatCalls          int
		wantFinalizedResponses int
	}{
		{
			name:                  "rewrite to disabled model uses chat",
			initialModelStreaming: true,
			rewriteModel:          "hook-disabled-model",
			wantChatCalls:         1,
		},
		{
			name:                   "rewrite to enabled model streams",
			rewriteModel:           "hook-enabled-model",
			rewriteModelStreaming:  true,
			fallbacks:              []string{"fallback-model"},
			wantStreamCalls:        1,
			wantFinalizedResponses: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newConfiguredStreamingTestConfig(t, true, tt.initialModelStreaming, tt.fallbacks)
			cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
				ModelName: tt.rewriteModel,
				Provider:  "openai",
				Model:     "openai/" + tt.rewriteModel,
				Streaming: config.ModelStreamingConfig{Enabled: tt.rewriteModelStreaming},
				Enabled:   true,
			})
			streamer := &recordingStreamer{}
			msgBus := bus.NewMessageBus()
			msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
			provider := &configuredStreamingProvider{
				streamPlan: []configuredStreamingCall{{
					chunks:   []string{"streamed after hook model rewrite"},
					response: &providers.LLMResponse{Content: "stream response"},
				}},
			}
			al := NewAgentLoop(cfg, msgBus, provider)
			useTestSideQuestionProvider(al, provider)
			if err := al.MountHook(NamedHook("rewrite-model", configuredStreamingBeforeModelHook{
				model: tt.rewriteModel,
			})); err != nil {
				t.Fatalf("MountHook() error = %v", err)
			}

			got := runConfiguredStreamingTurn(t, al, "mintclaw")

			if provider.streamCalls != tt.wantStreamCalls {
				t.Fatalf("ChatStream calls = %d, want %d", provider.streamCalls, tt.wantStreamCalls)
			}
			if provider.chatCalls != tt.wantChatCalls {
				t.Fatalf("Chat calls = %d, want %d", provider.chatCalls, tt.wantChatCalls)
			}
			if len(streamer.finalized) != tt.wantFinalizedResponses {
				t.Fatalf(
					"stream finalized = %v, want %d responses",
					streamer.finalized,
					tt.wantFinalizedResponses,
				)
			}
			if tt.wantChatCalls == 1 && got != "chat response" {
				t.Fatalf("response = %q, want chat response", got)
			}
			if tt.wantStreamCalls == 1 && got != "stream response" {
				t.Fatalf("response = %q, want stream response", got)
			}
			wantResolvedModel := "openai/" + tt.rewriteModel
			if tt.wantStreamCalls == 1 &&
				(len(provider.streamModels) != 1 || provider.streamModels[0] != wantResolvedModel) {
				t.Fatalf("stream models = %v, want [%s]", provider.streamModels, wantResolvedModel)
			}
			if tt.wantChatCalls == 1 &&
				(len(provider.chatModels) != 1 || provider.chatModels[0] != wantResolvedModel) {
				t.Fatalf("chat models = %v, want [%s]", provider.chatModels, wantResolvedModel)
			}
		})
	}
}

func TestConfiguredStreamingPostChunkFailureDoesNotFallBackToChat(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	streamer := &recordingStreamer{}
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{{
			chunks: []string{"partial"},
			err:    errors.New("stream failed after chunk"),
		}},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	_, err := al.runAgentLoop(
		context.Background(),
		al.GetRegistry().GetDefaultAgent(),
		configuredStreamingTurnSpec("mintclaw"),
	)
	if err == nil {
		t.Fatal("expected post-chunk stream failure to return an error")
	}
	if provider.streamCalls != 1 || provider.chatCalls != 0 {
		t.Fatalf(
			"calls = stream:%d chat:%d, want stream:1 chat:0",
			provider.streamCalls,
			provider.chatCalls,
		)
	}
	if len(streamer.updates) != 1 || streamer.updates[0] != "partial" {
		t.Fatalf("stream updates = %v, want [partial]", streamer.updates)
	}
	if streamer.canceled != 0 {
		t.Fatalf(
			"streamer canceled = %d, want 0 for already-visible stream failure",
			streamer.canceled,
		)
	}
}

func TestConfiguredStreamingPostChunkEOFDoesNotRetryOrCancelVisibleOutput(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	streamer := &recordingStreamer{}
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{{
			chunks: []string{"partial"},
			err:    io.EOF,
		}},
		chatResponse: &providers.LLMResponse{Content: "chat retry"},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	_, err := al.runAgentLoop(
		context.Background(),
		al.GetRegistry().GetDefaultAgent(),
		configuredStreamingTurnSpec("mintclaw"),
	)
	if err == nil {
		t.Fatal("expected post-chunk EOF to return an error")
	}
	if provider.streamCalls != 1 || provider.chatCalls != 0 {
		t.Fatalf(
			"calls = stream:%d chat:%d, want stream:1 chat:0",
			provider.streamCalls,
			provider.chatCalls,
		)
	}
	if len(streamer.updates) != 1 || streamer.updates[0] != "partial" {
		t.Fatalf("stream updates = %v, want [partial]", streamer.updates)
	}
	if streamer.canceled != 0 {
		t.Fatalf("streamer canceled = %d, want 0 for already-visible stream EOF", streamer.canceled)
	}
}

func TestConfiguredStreamingFinalizesAfterAfterLLMHookMutation(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	streamer := &recordingStreamer{}
	var traceScope runtimeevents.TraceScope
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer, traceScope: &traceScope})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{{
			chunks:   []string{"partial"},
			response: &providers.LLMResponse{Content: "original streamed response"},
		}},
	}
	al := NewAgentLoop(cfg, msgBus, provider)
	if err := al.MountHook(NamedHook("rewrite-stream-response", configuredStreamingAfterHook{
		content: "hooked final response",
	})); err != nil {
		t.Fatalf("MountHook() error = %v", err)
	}

	got := runConfiguredStreamingTurn(t, al, "mintclaw")

	if got != "hooked final response" {
		t.Fatalf("response = %q, want hook-modified response", got)
	}
	if len(streamer.finalized) != 1 || streamer.finalized[0] != "hooked final response" {
		t.Fatalf("stream finalized = %v, want [hooked final response]", streamer.finalized)
	}
	if !traceScope.Complete() {
		t.Fatalf("stream trace scope = %+v, want complete turn identity", traceScope)
	}
}

func TestConfiguredStreamingAfterLLMAbortCancelsPublishedStream(t *testing.T) {
	tests := []struct {
		name   string
		action HookAction
	}{
		{name: "abort turn", action: HookActionAbortTurn},
		{name: "hard abort", action: HookActionHardAbort},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
			streamer := &recordingStreamer{}
			msgBus := bus.NewMessageBus()
			msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
			provider := &configuredStreamingProvider{
				streamPlan: []configuredStreamingCall{{
					chunks:   []string{"partial before abort"},
					response: &providers.LLMResponse{Content: "should not be visible"},
				}},
			}
			al := NewAgentLoop(cfg, msgBus, provider)
			if err := al.MountHook(NamedHook("abort-stream-response", configuredStreamingAfterHook{
				action: tt.action,
			})); err != nil {
				t.Fatalf("MountHook() error = %v", err)
			}

			_, _ = al.runAgentLoop(
				context.Background(),
				al.GetRegistry().GetDefaultAgent(),
				configuredStreamingTurnSpec("mintclaw"),
			)

			if streamer.canceled != 1 {
				t.Fatalf("streamer canceled = %d, want 1", streamer.canceled)
			}
			if len(streamer.finalized) != 0 {
				t.Fatalf("stream finalized = %v, want none", streamer.finalized)
			}
		})
	}
}

func TestConfiguredStreamingFinalizesWithDefaultResponseWhenContentEmpty(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	streamer := &recordingStreamer{}
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{{
			chunks:   []string{"partial response"},
			response: &providers.LLMResponse{},
		}},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	got := runConfiguredStreamingTurn(t, al, "mintclaw")

	if got != defaultResponse {
		t.Fatalf("response = %q, want default response", got)
	}
	if len(streamer.finalized) != 1 || streamer.finalized[0] != defaultResponse {
		t.Fatalf("stream finalized = %v, want [%q]", streamer.finalized, defaultResponse)
	}
}

func TestConfiguredStreamingToolCallsUseCompleteStreamResponse(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	streamer := &recordingStreamer{}
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{
			{
				chunks: []string{"partial tool-call response"},
				response: &providers.LLMResponse{
					Content: "need a tool",
					ToolCalls: []providers.ToolCall{{
						ID:        "call-1",
						Type:      "function",
						Name:      "tool_limit_test_tool",
						Arguments: map[string]any{"value": "x"},
					}},
				},
			},
			{
				response: &providers.LLMResponse{Content: "tool call handled"},
			},
		},
	}
	al := NewAgentLoop(cfg, msgBus, provider)
	agent := al.GetRegistry().GetDefaultAgent()
	agent.Tools.Register(&toolLimitTestTool{})

	got := runConfiguredStreamingTurn(t, al, "mintclaw")

	if got != "tool call handled" {
		t.Fatalf("response = %q, want tool call handled", got)
	}
	if provider.streamCalls != 2 {
		t.Fatalf("ChatStream calls = %d, want 2", provider.streamCalls)
	}
	if provider.chatCalls != 0 {
		t.Fatalf("Chat calls = %d, want 0", provider.chatCalls)
	}
	if streamer.canceled != 1 {
		t.Fatalf(
			"streamer canceled = %d, want 1 for non-final tool-call response",
			streamer.canceled,
		)
	}
	if len(streamer.finalized) != 1 || streamer.finalized[0] != "tool call handled" {
		t.Fatalf("stream finalized = %v, want [tool call handled]", streamer.finalized)
	}
}

func TestConfiguredStreamingFinalTurnUsesAccumulatedTurnUsage(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	streamer := &turnUsageRecordingStreamer{}
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{
			{
				chunks: []string{"partial tool-call response"},
				response: &providers.LLMResponse{
					Content: "need a tool",
					ToolCalls: []providers.ToolCall{{
						ID:        "call-1",
						Type:      "function",
						Name:      "tool_limit_test_tool",
						Arguments: map[string]any{"value": "x"},
					}},
					Usage: &providers.UsageInfo{
						PromptTokens:     123,
						CompletionTokens: 45,
						TotalTokens:      168,
					},
				},
			},
			{
				response: &providers.LLMResponse{
					Content: "final answer without usage",
					Usage:   nil,
				},
			},
		},
	}
	al := NewAgentLoop(cfg, msgBus, provider)
	agent := al.GetRegistry().GetDefaultAgent()
	agent.Tools.Register(&toolLimitTestTool{})

	got := runConfiguredStreamingTurn(t, al, "mintclaw")

	if got != "final answer without usage" {
		t.Fatalf("response = %q, want final answer without usage", got)
	}
	if streamer.usageCalls != 1 {
		t.Fatalf(
			"SetTurnUsage calls = %d, want 1 for accumulated turn usage",
			streamer.usageCalls,
		)
	}
	if streamer.inputTokens != 123 || streamer.outputTokens != 45 {
		t.Fatalf(
			"streamed usage = (%d, %d), want (123, 45)",
			streamer.inputTokens,
			streamer.outputTokens,
		)
	}
	if len(streamer.finalized) != 1 || streamer.finalized[0] != "final answer without usage" {
		t.Fatalf("stream finalized = %v, want [final answer without usage]", streamer.finalized)
	}
}

func TestPipelineFinalizeUsesAccumulatedTurnUsage(t *testing.T) {
	ts := &turnState{
		opts: turnSpec{NoHistory: true},
	}
	ts.RecordLLMUsage(&providers.UsageInfo{
		PromptTokens:     10000,
		CompletionTokens: 2000,
		TotalTokens:      12000,
	})
	ts.RecordLLMUsage(&providers.UsageInfo{
		PromptTokens:     252,
		CompletionTokens: 2500,
		TotalTokens:      2752,
	})

	result, err := (&Pipeline{}).finalizeTurn(
		context.Background(),
		ts,
		&turnExecution{
			model: turnExecutionModel{
				llmModelName:     "fallback-model",
				defaultModelName: "primary-model",
			},
		},
		newLLMIterationState(1),
		TurnEndStatusCompleted,
		"final answer",
	)
	if err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if result.usageInputTokens != 10252 ||
		result.usageOutputTokens != 4500 ||
		result.usageTotalTokens != 14752 {
		t.Fatalf(
			"usage = (%d, %d, %d), want (10252, 4500, 14752)",
			result.usageInputTokens,
			result.usageOutputTokens,
			result.usageTotalTokens,
		)
	}
}

func newConfiguredStreamingTestConfig(
	t *testing.T,
	channelStreaming bool,
	modelStreaming bool,
	fallbacks []string,
) *config.Config {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "configured-streaming-agent-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				ModelFallbacks:    append([]string(nil), fallbacks...),
				MaxTokens:         4096,
				MaxToolIterations: 3,
			},
		},
		Channels: config.ChannelsConfig{
			"mintclaw": newConfiguredStreamingMintClawChannel(t, channelStreaming),
			"wecom":    newConfiguredStreamingWeComChannel(t, channelStreaming),
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "test-model",
			Provider:  "openai",
			Model:     "openai/test-model",
			Streaming: config.ModelStreamingConfig{Enabled: modelStreaming},
		}},
	}
	if len(fallbacks) > 0 {
		cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
			ModelName: "fallback-model",
			Provider:  "openai",
			Model:     "openai/fallback-model",
			Streaming: config.ModelStreamingConfig{Enabled: true},
		})
	}
	if err := config.InitChannelList(cfg.Channels); err != nil {
		t.Fatalf("InitChannelList() error = %v", err)
	}
	return cfg
}

func newConfiguredStreamingWeComChannel(t *testing.T, enabled bool) *config.Channel {
	t.Helper()
	settings := config.WeComSettings{
		BotID: "bot-1",
		Streaming: config.StreamingConfig{
			Enabled:         enabled,
			ThrottleSeconds: 1,
			MinGrowthChars:  40,
		},
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("Marshal settings error = %v", err)
	}
	return &config.Channel{
		Type:     config.ChannelWeCom,
		Enabled:  true,
		Settings: config.RawNode(raw),
	}
}

func newConfiguredStreamingMintClawChannel(t *testing.T, enabled bool) *config.Channel {
	t.Helper()
	settings := config.MintClawSettings{
		Streaming: config.StreamingConfig{
			Enabled:         enabled,
			ThrottleSeconds: 1,
			MinGrowthChars:  40,
		},
	}
	settings.SetToken("test-token")
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("Marshal settings error = %v", err)
	}
	return &config.Channel{
		Type:     config.ChannelMintClaw,
		Enabled:  true,
		Settings: config.RawNode(raw),
	}
}

func configuredStreamingTurnSpec(channel string) turnSpec {
	return turnSpec{
		Dispatch: testDispatchRequest(
			"agent:main:"+channel+":session-1",
			channel,
			"session-1",
			"hello",
		),
		DefaultResponse:             defaultResponse,
		EnableSummary:               false,
		SendResponse:                false,
		AllowInterimMintClawPublish: true,
		NoHistory:                   true,
	}
}

func runConfiguredStreamingTurn(t *testing.T, al *AgentLoop, channel string) string {
	t.Helper()
	got, err := al.runAgentLoop(
		context.Background(),
		al.GetRegistry().GetDefaultAgent(),
		configuredStreamingTurnSpec(channel),
	)
	if err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	return got
}
