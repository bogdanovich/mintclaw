package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestNodeFileDiagnosticTraceRetainsLifecycleWithoutSensitivePreviews(t *testing.T) {
	settings := traceCaptureSettings{contentMode: diagnostictrace.ContentRedacted}
	started := time.Now()
	trace := &activeTraceCapture{startedAt: started, turnID: "turn-file"}
	secretPath := "/private/operator/config.json"
	fullDigest := strings.Repeat("a", 64)

	call := traceNodeFileRecord(t, settings, trace, runtimeevents.Event{
		Kind: runtimeevents.KindAgentToolExecStart,
		Time: started.Add(time.Millisecond),
		Payload: ToolExecStartPayload{
			ToolCallID: "call-file",
			Tool:       "nodes_upload",
			Arguments: map[string]any{
				"destination":  secretPath,
				"sha256":       fullDigest,
				"artifact_ref": "media://private-source",
			},
		},
	})
	if call.ArgumentsPreview != "" || call.ArgsHash == "" {
		t.Fatalf("file call trace payload = %#v", call)
	}

	result := traceNodeFileRecord(t, settings, trace, runtimeevents.Event{
		Kind: runtimeevents.KindAgentToolExecEnd,
		Time: started.Add(2 * time.Millisecond),
		Payload: ToolExecEndPayload{
			ToolCallID:       "call-status",
			Tool:             "nodes_status",
			ResultHash:       "safe-result-hash",
			DiagnosticResult: `{"path":"` + secretPath + `","sha256":"` + fullDigest + `"}`,
		},
	})
	if result.ResultPreview != "" || result.ResultHash != "safe-result-hash" {
		t.Fatalf("file status trace payload = %#v", result)
	}

	observed := traceNodeFileRecord(t, settings, trace, runtimeevents.Event{
		Kind: runtimeevents.KindNodeInvocationObserved,
		Time: started.Add(3 * time.Millisecond),
		Source: runtimeevents.Source{
			Component: "nodes",
			Name:      "nodes_upload",
		},
		Correlation: runtimeevents.Correlation{RequestID: "call-file"},
		Payload: tools.NodeInvocationEventPayload{
			Observation:  tools.NodeInvocationObservationCompleted,
			InvocationID: "private-transfer-id",
			Target:       "personal-vpn",
			Command:      "file.upload.v1",
			Risk:         nodes.RiskWrite,
			GatewayState: nodes.GatewayInvocationDispatched,
			State:        "committed",
		},
	})
	if observed.Tool != "nodes_upload" || observed.Action != "completed" ||
		observed.Status != "committed" || observed.ResultHash == "" ||
		observed.ResultHash == "private-transfer-id" {
		t.Fatalf("file lifecycle trace payload = %#v", observed)
	}
	encoded, err := json.Marshal([]diagnostictrace.ToolPayload{call, result, observed})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		secretPath,
		fullDigest,
		"media://private-source",
		"private-transfer-id",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("file diagnostic trace leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestNodeInvocationDiagnosticTraceRetainsExactAuthorityAndTerminalTruth(t *testing.T) {
	settings := traceCaptureSettings{contentMode: diagnostictrace.ContentRedacted}
	started := time.Now()
	trace := &activeTraceCapture{startedAt: started, turnID: "turn-node"}
	tests := []struct {
		name        string
		observation string
		state       string
		errorCode   string
	}{
		{name: "healthy", observation: tools.NodeInvocationObservationCompleted, state: "succeeded"},
		{
			name:        "failed",
			observation: tools.NodeInvocationObservationStatus,
			state:       "failed",
			errorCode:   "REMOTE_FAILED",
		},
		{
			name:        "canceled",
			observation: tools.NodeInvocationObservationStatus,
			state:       "canceled",
			errorCode:   "CANCELED",
		},
		{
			name:        "timed out",
			observation: tools.NodeInvocationObservationCompleted,
			state:       "failed",
			errorCode:   "COMMAND_TIMEOUT",
		},
		{
			name: "disconnected uncertain", observation: tools.NodeInvocationObservationUncertain,
			state: "unknown", errorCode: "DISPATCH_UNCERTAIN",
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			safeName := strings.ReplaceAll(test.name, " ", "-")
			invocationID := "inv-node-" + safeName
			record, _, ok := runtimeEventRecord(
				settings,
				trace,
				runtimeevents.Event{
					ID:   "event-node-" + safeName,
					Kind: runtimeevents.KindNodeInvocationObserved,
					Time: started.Add(time.Duration(index+1) * time.Millisecond),
					Source: runtimeevents.Source{
						Component: "nodes",
						Name:      "nodes_invoke",
					},
					Scope: runtimeevents.Scope{
						TraceScope: runtimeevents.NewTraceScope("/private/project", "turn-node"),
						AgentID:    "main",
					},
					Correlation: runtimeevents.Correlation{RequestID: "tool-call-node"},
					Payload: tools.NodeInvocationEventPayload{
						Observation: test.observation, InvocationID: invocationID,
						Target: "workstation", ScopeAlias: "project", ProfileAlias: "owner",
						PolicyRevision: "policy-v7", Command: "shell.exec.v1",
						Risk: nodes.RiskPrivileged, GatewayState: nodes.GatewayInvocationDispatched,
						State: test.state, ErrorCode: test.errorCode,
					},
				},
			)
			if !ok || record.Kind != diagnostictrace.RecordToolResult {
				t.Fatalf("node event produced kind %q, ok=%v", record.Kind, ok)
			}
			if record.Scope.TargetAlias != "workstation" || record.Scope.ScopeAlias != "project" ||
				record.Scope.ProfileAlias != "owner" || record.Correlation.InvocationID != invocationID ||
				record.Correlation.PolicyRevision != "policy-v7" ||
				record.Correlation.ToolCallID != "tool-call-node" {
				t.Fatalf("node correlation = scope %#v correlation %#v", record.Scope, record.Correlation)
			}
			var payload diagnostictrace.ToolPayload
			if err := json.Unmarshal(record.Data, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Status != test.state || payload.Action != test.observation ||
				payload.DecisionCode != strings.ToLower(test.errorCode) {
				t.Fatalf("node terminal payload = %#v", payload)
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"/private/project", "command body", "command output"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("node trace leaked %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestBrowserContextsDiagnosticTraceRetainsOnlySafeOperation(t *testing.T) {
	settings := traceCaptureSettings{contentMode: diagnostictrace.ContentRedacted}
	started := time.Now()
	record, _, ok := runtimeEventRecord(
		settings,
		&activeTraceCapture{startedAt: started, turnID: "turn-browser"},
		runtimeevents.Event{
			Kind: runtimeevents.KindAgentToolExecStart,
			Time: started.Add(time.Millisecond),
			Payload: ToolExecStartPayload{
				ToolCallID: "call-contexts",
				Tool:       "browser_contexts",
				Arguments: map[string]any{
					"operation":          "list",
					"browser_session_id": "private-session-id",
				},
			},
		},
	)
	if !ok || record.Kind != diagnostictrace.RecordToolCall {
		t.Fatalf("browser context event produced kind %q, ok=%v", record.Kind, ok)
	}
	var payload diagnostictrace.ToolPayload
	if err := json.Unmarshal(record.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Tool != "browser_contexts" || payload.Action != "list" ||
		payload.ArgumentsPreview != "" || payload.ArgsHash == "" {
		t.Fatalf("browser context trace payload = %#v", payload)
	}
	if strings.Contains(string(record.Data), "private-session-id") {
		t.Fatalf("browser context trace leaked session authority: %s", record.Data)
	}
}

func TestSubTurnAdmissionDiagnosticTraceRetainsWaitLifecycle(t *testing.T) {
	started := time.Now()
	record, _, ok := runtimeEventRecord(
		traceCaptureSettings{contentMode: diagnostictrace.ContentMetadataOnly},
		&activeTraceCapture{startedAt: started, turnID: "turn-parent"},
		runtimeevents.Event{
			Kind: runtimeevents.KindAgentSubTurnAdmission,
			Time: started.Add(1250 * time.Millisecond),
			Payload: SubTurnAdmissionPayload{
				AgentID:      "browser",
				ChildTurnID:  "child-browser-2",
				Stage:        "target_agent",
				State:        "queued",
				Active:       1,
				Limit:        1,
				WaitDuration: 1250 * time.Millisecond,
				WaitTimeout:  30 * time.Second,
			},
		},
	)
	if !ok || record.Kind != diagnostictrace.RecordSubTurnAdmission {
		t.Fatalf("admission event produced kind %q, ok=%v", record.Kind, ok)
	}
	var payload diagnostictrace.SubTurnAdmissionPayload
	if err := json.Unmarshal(record.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.State != "queued" || payload.Stage != "target_agent" || payload.AgentID != "browser" ||
		payload.ChildTurnID != "child-browser-2" ||
		payload.Active != 1 || payload.Limit != 1 || payload.WaitMS != 1250 ||
		payload.TimeoutMS != 30000 {
		t.Fatalf("admission trace payload = %#v", payload)
	}
}

func traceNodeFileRecord(
	t *testing.T,
	settings traceCaptureSettings,
	trace *activeTraceCapture,
	event runtimeevents.Event,
) diagnostictrace.ToolPayload {
	t.Helper()
	record, _, ok := runtimeEventRecord(settings, trace, event)
	if !ok {
		t.Fatalf("event %q did not produce a diagnostic record", event.Kind)
	}
	var payload diagnostictrace.ToolPayload
	if err := json.Unmarshal(record.Data, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}
