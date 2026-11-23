package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func fixtureComputerKey(
	session computer.Session,
	observation computer.Observation,
	id string,
) tools.ComputerActRequest {
	return tools.ComputerActRequest{
		SessionID: session.Binding.SessionID, ObservationID: observation.ID, RequestID: id,
		Action: computer.InputAction{Kind: computer.InputKey, Key: &computer.InputKeySpec{Name: "end"}},
	}
}

func TestComputerActToolUsesProtectedGatewayDispatch(t *testing.T) {
	client, handler, cfg := computerClientFixtureWithInput(t, 0, true, true)
	principal := computerClientPrincipal()
	session := openComputerClientFixture(t, client)
	observation, err := client.Observe(t.Context(), principal, session.Binding.SessionID, "observe")
	if err != nil {
		t.Fatal(err)
	}
	running := &services{Computer: &computerRuntime{
		client: client, revision: client.policy.PolicyRevision(client.execution), workspaceID: client.workspaceID,
	}}
	source := &gatewayComputerToolSource{
		services: running, revision: running.Computer.revision, workspaceID: running.Computer.workspaceID,
	}
	tool := tools.NewComputerActTool(tools.NewComputerToolOptions(cfg.Tools.Computer), source)
	registry := tools.NewToolRegistry()
	registry.Register(tool)
	const text = "private exact café Привет e\u0301 👩‍💻"
	args := map[string]any{
		"computer_session_id": session.Binding.SessionID,
		"observation_id":      observation.ID,
		"action": map[string]any{
			"kind": "insert_text",
			"text": map[string]any{"element_ref": "original-caret"},
		},
		"text": text,
	}
	durable, protected, err := registry.DurableArguments(tool.Name(), args)
	if err != nil || !protected || durable["text"] != "*" || args["text"] != text ||
		!registry.ProtectedDurableResult(tool.Name(), args) {
		t.Fatal("tool failed to protect the argument or result", err)
	}
	logged := tools.ToolLogArguments(tool.Name(), args)
	loggedBytes, err := json.Marshal(logged)
	if err != nil || logged["redacted"] != true || strings.Contains(string(loggedBytes), text) {
		t.Fatal("runtime logging would expose protected text")
	}
	ctx := toolshared.WithRuntimeCapabilities(t.Context(), runtimecap.NewContext(runtimecap.Inputs{
		Kind: principal.Runtime,
	}).BindPrincipal(principal))
	ctx = toolshared.WithToolCallID(ctx, "input-call")
	output := registry.Execute(ctx, tool.Name(), args)
	if output.IsError || output.ContextText != text || strings.Contains(output.ForLLM, text) ||
		handler.actionText != text {
		t.Fatal("protected text did not reach only the live route")
	}
	var projected struct {
		ObservationID string `json:"observation_id"`
	}
	if json.Unmarshal([]byte(output.ForLLM), &projected) != nil ||
		projected.ObservationID == "" || projected.ObservationID == observation.ID {
		t.Fatal("post-action result omitted fresh evidence")
	}
	// RequestID is the existing tool-call hash. The gateway's real file store
	// must retain only the commitment and command metadata.
	actions := slices.DeleteFunc(
		slices.Clone(handler.commands),
		func(name string) bool { return name != nodes.ComputerCommandAct },
	)
	if len(actions) != 1 {
		t.Fatal("expected just one forwarded action")
	}
	storeRecords := client.source.store
	sum := sha256.Sum256([]byte("input-call"))
	requestID := hex.EncodeToString(sum[:])
	invocationID := client.invocationID(principal, "desktop", nodes.ComputerCommandAct, requestID)
	retained, found, err := storeRecords.Lookup(client.invocationPrincipal(principal), invocationID)
	if err != nil || !found || retained.State != nodes.GatewayInvocationDispatched {
		t.Fatal("action lacked a durable dispatch record", err)
	}
	raw, err := json.Marshal(retained)
	if err != nil || strings.Contains(string(raw), text) ||
		!strings.Contains(string(retained.Plan.Input), "text_digest") {
		t.Fatal("gateway ledger retained protected bytes or omitted the commitment")
	}
	if duplicate := registry.Execute(ctx, tool.Name(), args); !duplicate.IsError || handler.observations != 2 {
		t.Fatal("old observation dispatched another action")
	}
	// The same invocation ID is still fenced if it is reissued using newly returned evidence.
	args["observation_id"] = projected.ObservationID
	if duplicate := registry.Execute(ctx, tool.Name(), args); !duplicate.IsError || handler.observations != 2 {
		t.Fatal("a different observation replayed an old invocation ID")
	}
}

func TestComputerNodeActRequiresSeparateGrantAndNodeApproval(t *testing.T) {
	for _, settings := range []struct {
		name                  string
		advertise, allowInput bool
	}{{"read-only grant", true, false}, {"read-only node", false, true}} {
		t.Run(settings.name, func(t *testing.T) {
			client, handler, cfg := computerClientFixtureWithInput(t, 0, settings.advertise, settings.allowInput)
			tool := tools.NewComputerActTool(
				tools.NewComputerToolOptions(cfg.Tools.Computer),
				&gatewayComputerToolSource{},
			)
			if tool.ToolEnabledForAgent("browser") != settings.allowInput || tool.ToolEnabledForAgent("reader") {
				t.Fatal("input visibility ignored the separate gateway grant")
			}
			session := openComputerClientFixture(t, client)
			principal := computerClientPrincipal()
			observation, err := client.Observe(t.Context(), principal, session.Binding.SessionID, "observe")
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Act(t.Context(), principal, fixtureComputerKey(session, observation, "act"))
			if !errors.Is(err, computer.ErrDenied) || slices.Contains(handler.commands, nodes.ComputerCommandAct) {
				t.Fatal("unapproved input reached the node", err)
			}
		})
	}
}

func TestComputerNodeActFencesControlAndUnknownOutcome(t *testing.T) {
	client, handler, _ := computerClientFixtureWithInput(t, 0, true, true)
	principal := computerClientPrincipal()
	session := openComputerClientFixture(t, client)
	id := session.Binding.SessionID
	observation, err := client.Observe(t.Context(), principal, id, "observe")
	if err != nil {
		t.Fatal(err)
	}
	action := fixtureComputerKey(session, observation, "act")
	other := principal
	other.ExecutionID = "foreign"
	if _, err = client.Act(t.Context(), other, action); !errors.Is(err, computer.ErrDenied) {
		t.Fatal("foreign execution reached input", err)
	}
	for _, event := range []computer.ControlEvent{computer.EventHandoff, computer.EventRequestResume, computer.EventResume} {
		if _, err = client.Control(t.Context(), principal, id, string(event), event); err != nil {
			t.Fatal(err)
		}
		_, err = client.Act(t.Context(), principal, action)
		want := computer.ErrDenied
		if event == computer.EventResume {
			want = computer.ErrStale
		}
		if !errors.Is(err, want) || slices.Contains(handler.commands, nodes.ComputerCommandAct) {
			t.Fatal("input during human control or from stale evidence", event, err)
		}
	}
	fresh, err := client.Observe(t.Context(), principal, id, "resume-observe")
	if err != nil {
		t.Fatal(err)
	}
	handler.loseNextAction = true
	action.ObservationID = fresh.ID
	if _, err = client.Act(t.Context(), principal, action); !errors.Is(err, errComputerOutcomeUnknown) {
		t.Fatal("lost input response did not fence mutations", err)
	}
	if _, err = client.Status(t.Context(), principal, id, "status"); err != nil {
		t.Fatal(err)
	}
	newer, err := client.Observe(t.Context(), principal, id, "reconcile-observe")
	if err != nil {
		t.Fatal("lost input blocked independent observation", err)
	}
	action.ObservationID, action.RequestID = newer.ID, "other-act"
	if _, err = client.Act(t.Context(), principal, action); !errors.Is(err, errComputerOutcomeUnknown) {
		t.Fatal("unchanged status or fresh observation lifted uncertainty", err)
	}
	if _, err = client.Control(
		t.Context(),
		principal,
		id,
		"handoff-after-unknown",
		computer.EventHandoff,
	); !errors.Is(
		err,
		errComputerOutcomeUnknown,
	) {
		t.Fatal("unconfirmed stop allowed a handoff", err)
	}
	released, err := client.Control(t.Context(), principal, id, "release", computer.EventRelease)
	if err != nil || released.Control != computer.ControlReleased {
		t.Fatal("explicit release could not retire the original input session", err)
	}
	actions := slices.DeleteFunc(
		slices.Clone(handler.commands),
		func(name string) bool { return name != nodes.ComputerCommandAct },
	)
	if !reflect.DeepEqual(actions, []string{nodes.ComputerCommandAct}) {
		t.Fatal("uncertain outcome led to another dispatch")
	}
}

func TestComputerNodeActRequestsRemoteStopAfterLocalCancellation(t *testing.T) {
	client, handler, _ := computerClientFixtureWithInput(t, 0, true, true)
	principal := computerClientPrincipal()
	session := openComputerClientFixture(t, client)
	observation, err := client.Observe(t.Context(), principal, session.Binding.SessionID, "observe")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	handler.actionStarted, handler.delay = started, 20*time.Second
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, callErr := client.Act(ctx, principal, fixtureComputerKey(session, observation, "stopped-act"))
		done <- callErr
	}()
	select {
	case <-started:
		cancel()
	case err := <-done:
		t.Fatal("action did not reach the dispatch boundary", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for action dispatch")
	}
	if err = <-done; !errors.Is(err, errComputerOutcomeUnknown) || handler.cancelCalls != 1 {
		t.Fatal("dispatched action cancellation was omitted or falsely confirmed", err)
	}
}
