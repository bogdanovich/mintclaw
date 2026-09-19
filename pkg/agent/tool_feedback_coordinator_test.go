package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestToolFeedbackPublisherProjectsLocalPDFSelector(t *testing.T) {
	path := "/home/server/private/forms/tax-return.pdf"
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ToolFeedback.Enabled = true
	msgBus := bus.NewMessageBus()
	_, agent, cleanup := newTurnCoordTestLoop(t, &mockProvider{})
	defer cleanup()

	spec := makeTestTurnSpec("tool-feedback-publisher")
	spec.Dispatch = testDispatchRequest(
		"tool-feedback-publisher",
		"telegram",
		"chat-1",
		"Read "+path+".",
	)
	ts := newTurnState(agent, spec, turnEventScope{
		turnID: "tool-feedback-publisher-turn", context: newTurnContext(nil, nil, nil),
	})
	publisher := &toolFeedbackPublisher{bus: msgBus, cfg: cfg}
	publisher.publishToolFeedbackForCall(
		t.Context(),
		ts,
		&providers.LLMResponse{},
		providers.ToolCall{ID: "search-call", Name: "tool_search"},
		"tool_search",
		map[string]any{"query": "document tools"},
		[]providers.Message{{Role: "user", Content: "Read " + path + "."}},
	)

	select {
	case outbound := <-msgBus.OutboundChan():
		if strings.Contains(outbound.Content, path) ||
			!strings.Contains(outbound.Content, protectedLocalPDFSelectorReceipt) {
			t.Fatalf("outbound tool feedback = %q", outbound.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for projected tool feedback")
	}
}
