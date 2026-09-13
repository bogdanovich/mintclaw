package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

func TestCollectLiveExecutionEvidenceFollowsDelegatedTrace(t *testing.T) {
	rootWorkspace := t.TempDir()
	childWorkspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Diagnostics.TraceCapture.Enabled = true
	cfg.Diagnostics.TraceCapture.ContentMode = "redacted_content"
	cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{
		ID: "browser", Workspace: childWorkspace,
	})
	created := time.Now().UTC().Add(-time.Second)
	sessionKey := "sk_v1_live_evidence_test"
	sessionHash := fmt.Sprintf("%x", sha256.Sum256([]byte(sessionKey)))
	root := finalizedLiveEvidenceTrace(t, diagnostictrace.Trace{
		SchemaVersion: diagnostictrace.SchemaVersionV1,
		TraceID:       "trace-live-parent",
		CreatedAt:     created,
		Policy: diagnostictrace.CapturePolicy{
			ContentMode: diagnostictrace.ContentRedacted, Redactor: "test",
		},
		Limits: diagnostictrace.DefaultLimits(),
		Metadata: diagnostictrace.Metadata{
			RootTurnID: "main-turn-1", AgentID: "main", SessionHash: sessionHash,
		},
		Records: []diagnostictrace.Record{
			liveEvidenceRecord(
				t,
				1,
				0,
				diagnostictrace.RecordToolCall,
				"root-call",
				"delegate-1",
				diagnostictrace.ToolPayload{
					Tool: "delegate", Status: "started", Executed: true,
				},
			),
			liveEvidenceRecord(
				t,
				2,
				500*time.Millisecond,
				diagnostictrace.RecordSubTurnAdmission,
				"admission",
				"",
				diagnostictrace.SubTurnAdmissionPayload{
					State: "admitted", Stage: "target_agent", AgentID: "browser", ChildTurnID: "subturn-1",
				},
			),
			liveEvidenceRecord(
				t,
				3,
				time.Second,
				diagnostictrace.RecordToolResult,
				"root-result",
				"delegate-1",
				diagnostictrace.ToolPayload{
					Tool: "delegate", Status: "completed", Executed: true,
				},
			),
		},
		Outcome: &diagnostictrace.Outcome{Status: "completed"},
	})
	child := finalizedLiveEvidenceTrace(t, diagnostictrace.Trace{
		SchemaVersion: diagnostictrace.SchemaVersionV1,
		TraceID:       "trace-live-child",
		CreatedAt:     created.Add(100 * time.Millisecond),
		Policy: diagnostictrace.CapturePolicy{
			ContentMode: diagnostictrace.ContentRedacted, Redactor: "test",
		},
		Limits: diagnostictrace.DefaultLimits(),
		Metadata: diagnostictrace.Metadata{
			RootTurnID: "browser-turn-2", ParentTurnID: "main-turn-1", AgentID: "browser",
		},
		Records: []diagnostictrace.Record{
			liveEvidenceRecord(
				t,
				1,
				0,
				diagnostictrace.RecordToolCall,
				"child-call",
				"session-1",
				diagnostictrace.ToolPayload{
					Tool: "browser_session", Status: "started", Executed: true,
					ArgumentsPreview: `{"operation":"open","target":"gateway","profile":"managed"}`,
				},
			),
			liveEvidenceRecord(
				t,
				2,
				time.Millisecond,
				diagnostictrace.RecordToolResult,
				"child-result",
				"session-1",
				diagnostictrace.ToolPayload{
					Tool: "browser_session", Status: "completed", Executed: true,
				},
			),
		},
		Outcome: &diagnostictrace.Outcome{Status: "completed"},
	})
	for workspace, trace := range map[string]diagnostictrace.Trace{
		rootWorkspace:  root,
		childWorkspace: child,
	} {
		store := diagnostictrace.Store{
			Root: diagnostictrace.ResolveStoreRoot(cfg.Diagnostics.TraceCapture.StateDir, workspace),
		}
		if _, err := store.Save(trace); err != nil {
			t.Fatalf("Save(%s): %v", trace.TraceID, err)
		}
	}

	evidence, err := collectLiveExecutionEvidence(
		t.Context(), cfg, runtimeevents.NewTraceScope(rootWorkspace, "main-turn-1"),
		sessionKey, "browser",
	)
	if err != nil {
		t.Fatalf("collectLiveExecutionEvidence() error = %v", err)
	}
	if evidence.Status != "verified" || evidence.SafeError != nil ||
		evidence.Delegation.Admitted != 1 || evidence.Parent.ToolCalls["delegate"] != 1 ||
		evidence.Child.ToolCalls["browser_session"] != 1 || len(evidence.Child.BrowserSessions) != 1 {
		t.Fatalf("evidence = %#v", evidence)
	}
	selection := evidence.Child.BrowserSessions[0]
	if selection.Operation != "open" || selection.Target != "gateway" || selection.Profile != "managed" {
		t.Fatalf("browser session evidence = %#v", selection)
	}
}

func TestCollectLiveExecutionEvidenceFailsClosedWithoutTrace(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Diagnostics.TraceCapture.Enabled = true
	cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{
		ID: "browser", Workspace: t.TempDir(),
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	evidence, err := collectLiveExecutionEvidence(
		ctx, cfg, runtimeevents.NewTraceScope(t.TempDir(), "main-turn-missing"),
		"sk_v1_missing", "browser",
	)
	if err == nil || evidence.Status != "unavailable" || evidence.SafeError == nil ||
		evidence.SafeError.Code != "trace_unavailable" {
		t.Fatalf("evidence = %#v, error = %v", evidence, err)
	}
}

func TestSummarizeLiveTraceRejectsUnexecutedToolEvidence(t *testing.T) {
	tests := []struct {
		name           string
		callExecuted   bool
		resultExecuted bool
	}{
		{name: "call", callExecuted: false, resultExecuted: true},
		{name: "result", callExecuted: true, resultExecuted: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trace := finalizedLiveEvidenceTrace(t, diagnostictrace.Trace{
				SchemaVersion: diagnostictrace.SchemaVersionV1,
				TraceID:       "trace-unexecuted-" + tt.name,
				CreatedAt:     time.Now().UTC(),
				Policy: diagnostictrace.CapturePolicy{
					ContentMode: diagnostictrace.ContentRedacted, Redactor: "test",
				},
				Limits: diagnostictrace.DefaultLimits(),
				Metadata: diagnostictrace.Metadata{
					RootTurnID: "browser-turn-1", AgentID: "browser",
				},
				Records: []diagnostictrace.Record{
					liveEvidenceRecord(
						t,
						1,
						0,
						diagnostictrace.RecordToolCall,
						"call",
						"session-1",
						diagnostictrace.ToolPayload{
							Tool: "browser_session", Status: "started", Executed: tt.callExecuted,
							ArgumentsPreview: `{"operation":"open","target":"gateway","profile":"managed"}`,
						},
					),
					liveEvidenceRecord(
						t,
						2,
						time.Millisecond,
						diagnostictrace.RecordToolResult,
						"result",
						"session-1",
						diagnostictrace.ToolPayload{
							Tool: "browser_session", Status: "completed", Executed: tt.resultExecuted,
						},
					),
				},
				Outcome: &diagnostictrace.Outcome{Status: "completed"},
			})

			evidence := summarizeLiveTrace(trace)
			if !evidence.Incomplete {
				t.Fatalf("evidence accepted unexecuted %s record: %#v", tt.name, evidence)
			}
		})
	}
}

func finalizedLiveEvidenceTrace(t *testing.T, trace diagnostictrace.Trace) diagnostictrace.Trace {
	t.Helper()
	finalized, err := diagnostictrace.Finalize(trace)
	if err != nil {
		t.Fatalf("Finalize(%s): %v", trace.TraceID, err)
	}
	return finalized
}

func liveEvidenceRecord(
	t *testing.T,
	sequence uint64,
	offset time.Duration,
	kind diagnostictrace.RecordKind,
	origin string,
	toolCallID string,
	payload any,
) diagnostictrace.Record {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return diagnostictrace.Record{
		Sequence: sequence, OffsetNanos: int64(offset), Kind: kind,
		Origin:      diagnostictrace.Origin{Kind: "runtime_event", ID: origin},
		Correlation: diagnostictrace.Correlation{ToolCallID: toolCallID},
		Data:        data,
	}
}
