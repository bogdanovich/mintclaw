package eval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/events"
)

type fakeConversationTransport struct {
	events  chan protocol.MintClawMessage
	sent    []protocol.MintClawMessage
	onSend  func(protocol.MintClawMessage, int)
	failure error
	closed  int
}

func newFakeConversation() *fakeConversationTransport {
	return &fakeConversationTransport{events: make(chan protocol.MintClawMessage, 512)}
}

func (transport *fakeConversationTransport) Send(ctx context.Context, request protocol.MintClawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	transport.sent = append(transport.sent, request)
	if transport.onSend != nil {
		transport.onSend(request, len(transport.sent))
	}
	return nil
}

func (transport *fakeConversationTransport) Receive(ctx context.Context) (protocol.MintClawMessage, error) {
	select {
	case response := <-transport.events:
		return response, nil
	case <-ctx.Done():
		return protocol.MintClawMessage{}, ctx.Err()
	}
}

func (transport *fakeConversationTransport) Failure() error { return transport.failure }
func (transport *fakeConversationTransport) Close()         { transport.closed++ }

func testResponse(request protocol.MintClawMessage, number int) protocol.MintClawMessage {
	return protocol.MintClawMessage{
		Type: protocol.TypeMessageUpdate, SessionID: request.SessionID,
		Payload: map[string]any{
			protocol.PayloadKeyRequestID: request.ID, protocol.PayloadKeyMessageID: "prompt-" + strconv.Itoa(number),
			protocol.PayloadKeyContent: "synthetic-marker", protocol.PayloadKeyFinal: true,
			protocol.PayloadKeyAgentID: "main", protocol.PayloadKeySessionKey: "eval-session-key",
			protocol.PayloadKeyTraceScopes: []events.TraceScope{{Workspace: "/synthetic/eval", TurnID: request.ID}},
		},
	}
}

func runFakeConversation(t *testing.T, transport *fakeConversationTransport, item Case) Trial {
	t.Helper()
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	return runScriptedConversation(
		t.Context(),
		transport,
		LoadedCase{Case: item, Digest: digest(data)},
		"eval-test-session",
		"main",
		"/synthetic/eval",
	)
}

func TestScriptedConversationConsumesRevisionsAndOnlyNewFollowup(t *testing.T) {
	transport := newFakeConversation()
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		foreign := testResponse(request, number)
		foreign.SessionID = "foreign-session"
		transport.events <- foreign
		stale := testResponse(request, number)
		stale.Payload[protocol.PayloadKeyRequestID] = "stale-request"
		transport.events <- stale
		stream := testResponse(request, number)
		stream.Payload[protocol.PayloadKeyFinal] = false
		stream.Payload[protocol.PayloadKeyContent] = "partial"
		transport.events <- stream
		transport.events <- testResponse(request, number)
	}
	item := testCase()
	item.Checks = append(item.Checks, Check{ID: "marker", Kind: "response_contains", Expected: "synthetic-marker"})
	trial := runFakeConversation(t, transport, item)
	if trial.Verdict != Pass || len(transport.sent) != 2 || transport.closed != 1 ||
		transport.sent[0].ID == transport.sent[1].ID || transport.sent[0].SessionID != transport.sent[1].SessionID ||
		transport.sent[1].Payload[protocol.PayloadKeyContent] != item.Driver.Steps[0].Action.Text ||
		!trial.TransportClosed || trial.RemoteState != "unknown" || len(trial.Events) != 6 {
		t.Fatal("conversation lifecycle was not preserved", trial.Verdict, trial.Code)
	}
	if trial.Checks[1].EvidenceIDs[0] != trial.Events[len(trial.Events)-1].ID {
		t.Fatal("marker check did not use the last terminal revision")
	}
}

func TestScriptedConversationDoesNotPassFabricatedDoneOrMissingIdentity(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(map[string]any)
		verdict Verdict
	}{
		{"fabricated done", func(p map[string]any) { p[protocol.PayloadKeyContent] = "Done, everything succeeded." }, Fail},
		{"missing agent", func(p map[string]any) { delete(p, protocol.PayloadKeyAgentID) }, Inconclusive},
		{"missing scope", func(p map[string]any) { delete(p, protocol.PayloadKeyTraceScopes) }, Inconclusive},
		{"wrong agent", func(p map[string]any) { p[protocol.PayloadKeyAgentID] = "other-agent" }, Fail},
		{"wrong workspace", func(p map[string]any) {
			p[protocol.PayloadKeyTraceScopes] = []events.TraceScope{{Workspace: "/production", TurnID: "foreign-turn"}}
		}, Fail},
		{"nonstring content", func(p map[string]any) { p[protocol.PayloadKeyContent] = true }, Inconclusive},
		{"nonboolean final", func(p map[string]any) {
			p[protocol.PayloadKeyFinal] = "true"
			p[protocol.PayloadKeyKind] = protocol.MessageKindFinalReply
		}, Inconclusive},
		{"nonboolean placeholder", func(p map[string]any) { p[protocol.PayloadKeyPlaceholder] = 1 }, Inconclusive},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := newFakeConversation()
			transport.onSend = func(request protocol.MintClawMessage, number int) {
				response := testResponse(request, number)
				test.mutate(response.Payload)
				transport.events <- response
			}
			item := testCase()
			item.Checks = append(
				item.Checks,
				Check{ID: "marker", Kind: "response_contains", Expected: "synthetic-marker"},
			)
			trial := runFakeConversation(t, transport, item)
			if trial.Verdict != test.verdict {
				t.Fatal("unsafe or fabricated result was admitted", trial.Verdict, trial.Code)
			}
		})
	}
}

func TestScriptedNavigationBindsLatestPrompt(t *testing.T) {
	transport := newFakeConversation()
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		response := testResponse(request, number)
		if number < 3 {
			response.Payload[protocol.PayloadKeyFinal] = false
			response.Payload[protocol.PayloadKeyControls] = "prompt"
			response.Payload[protocol.PayloadKeyInteraction] = "question"
			response.Payload[protocol.PayloadKeyInteractionID] = "interaction-" + strconv.Itoa(number)
			response.Payload[protocol.PayloadKeyInteractionShortID] = "short-" + strconv.Itoa(number)
		}
		transport.events <- response
	}
	item := testCase()
	item.Driver.Steps = []Step{
		{WaitFor: Expectation{Kind: "question"}, Action: Action{Kind: "clarify"}},
		{WaitFor: Expectation{Kind: "question"}, Action: Action{Kind: "cancel"}},
	}
	item.Checks = append(item.Checks, Check{ID: "question", Kind: "interaction_observed"})
	trial := runFakeConversation(t, transport, item)
	if trial.Verdict != Pass || len(transport.sent) != 3 ||
		transport.sent[1].Payload[protocol.PayloadKeyInteractionShortID] != "short-1" ||
		transport.sent[1].Payload[protocol.PayloadKeyInteractionPrompt] != "prompt-1" ||
		transport.sent[2].Payload[protocol.PayloadKeyInteractionShortID] != "short-2" ||
		transport.sent[2].Payload[protocol.PayloadKeyInteractionPrompt] != "prompt-2" ||
		transport.sent[2].Payload[protocol.PayloadKeyInteractionChoice] != "cancel" {
		t.Fatal("navigation was not bound to the actual latest prompt", trial.Verdict, trial.Code)
	}
}

func TestScriptedConversationTimeoutAndBoundsCannotPass(t *testing.T) {
	for _, mode := range []string{"timeout", "event-limit", "queue-limit", "stop", "judge-required", "protocol-error"} {
		t.Run(mode, func(t *testing.T) {
			item := testCase()
			item.Limits.TurnTimeout, item.Limits.CaseTimeout = "50ms", "1s"
			transport := newFakeConversation()
			transport.onSend = func(request protocol.MintClawMessage, number int) {
				if mode == "timeout" && number == 2 {
					return
				}
				if mode == "protocol-error" {
					transport.events <- protocol.MintClawMessage{Type: protocol.TypeError, Payload: map[string]any{"request_id": request.ID, "message": "must-not-echo"}}
					return
				}
				if mode == "event-limit" {
					for range 257 {
						response := testResponse(request, number)
						response.Payload[protocol.PayloadKeyFinal] = false
						transport.events <- response
					}
					return
				}
				transport.events <- testResponse(request, number)
			}
			if mode == "queue-limit" {
				transport.failure = gatewayclient.ErrQueueLimit
			}
			if mode == "stop" {
				item.Driver.Steps[0].Action = Action{Kind: "stop"}
			}
			if mode == "judge-required" {
				item.Rubric = []Criterion{{ID: "coherence", Description: "Be coherent.", Required: true}}
			}
			trial := runFakeConversation(t, transport, item)
			data, _ := json.Marshal(trial)
			if trial.Verdict == Pass || trial.RemoteState != "unknown" || !trial.TransportClosed ||
				len(transport.sent) > 2 || strings.Contains(string(data), "must-not-echo") {
				t.Fatal("incomplete work was reported as passing or replayed", trial.Verdict, trial.Code)
			}
		})
	}
}

func TestScriptedConversationRejectsApprovalAndUnavailableProtectedAnswer(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run(map[bool]string{false: "approval", true: "protected"}[protected], func(t *testing.T) {
			item := testCase()
			transport := newFakeConversation()
			transport.onSend = func(request protocol.MintClawMessage, number int) {
				response := testResponse(request, number)
				if !protected {
					response.Payload[protocol.PayloadKeyFinal] = false
					response.Payload[protocol.PayloadKeyControls] = "prompt"
					response.Payload[protocol.PayloadKeyInteraction] = "approval"
				}
				transport.events <- response
			}
			if protected {
				item.Driver.Facts = map[string]string{"private-answer": "private-answer-canary"}
				item.Driver.Steps[0].Action = Action{Kind: "protected_answer", Fact: "private-answer"}
			} else {
				item.Driver.Steps[0].WaitFor.Kind = "approval"
			}
			trial := runFakeConversation(t, transport, item)
			data, _ := json.Marshal(trial)
			if trial.Verdict == Pass || len(transport.sent) != 1 ||
				strings.Contains(string(data), "private-answer-canary") {
				t.Fatal("runner granted authority or exposed unavailable protected ingress", trial.Verdict, trial.Code)
			}
		})
	}
}

func TestVerdictsAndChecksFailClosed(t *testing.T) {
	if combineVerdicts(Pass, Error, Fail) != Fail || combineVerdicts(Pass, Inconclusive) != Inconclusive ||
		combineVerdicts(Pass, Verdict("invented")) != Error || combineVerdicts(Fail, Verdict("invented")) != Fail {
		t.Fatal("verdict precedence hid a hard failure or unknown")
	}
	first := Event{ID: "first", RequestID: "same", Kind: "final", SessionKey: "session", Content: "forbidden"}
	last := Event{ID: "last", RequestID: "same", Kind: "final", SessionKey: "session", Content: "clean"}
	checks := gradeConversation([]Check{
		{ID: "session", Kind: "same_session"}, {ID: "privacy", Kind: "response_absent", Expected: "forbidden"},
	}, []Event{first, last}, last)
	if checks[0].Verdict == Pass || checks[1].Verdict != Fail || checks[1].EvidenceIDs[0] != first.ID {
		t.Fatal("duplicates or a later clean response hid a failure")
	}
}

func TestScriptedConversationUsesSharedGatewayConnection(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		for number := 1; ; number++ {
			var request protocol.MintClawMessage
			if connection.ReadJSON(&request) != nil {
				return
			}
			if request.SessionID != r.URL.Query().Get("session_id") {
				return
			}
			if connection.WriteJSON(testResponse(request, number)) != nil {
				return
			}
		}
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatal(err)
	}
	settings, err := json.Marshal(map[string]any{"token": "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Gateway.Host, cfg.Gateway.Port = endpoint.Hostname(), port
	cfg.Channels = config.ChannelsConfig{"mintclaw": {Type: config.ChannelMintClaw, Enabled: true, Settings: settings}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	const session = "eval-integration-session"
	client, err := gatewayclient.Connect(ctx, cfg, session, gatewayclient.Options{})
	if err != nil {
		t.Fatal(err)
	}
	item := testCase()
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	trial := runScriptedConversation(
		ctx,
		client,
		LoadedCase{Case: item, Digest: digest(data)},
		session,
		"main",
		"/synthetic/eval",
	)
	if trial.Verdict != Pass || !trial.TransportClosed || len(trial.Events) != 4 {
		t.Fatal("eval engine did not use the real shared connection", trial.Verdict, trial.Code)
	}
}
