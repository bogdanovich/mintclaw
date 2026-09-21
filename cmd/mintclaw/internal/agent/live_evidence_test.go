package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
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
			ChildTurnID: "subturn-1", SessionHash: "durable-child-session-hash",
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
	sibling := finalizedLiveEvidenceTrace(t, diagnostictrace.Trace{
		SchemaVersion: diagnostictrace.SchemaVersionV1,
		TraceID:       "trace-live-child-sibling",
		CreatedAt:     created.Add(200 * time.Millisecond),
		Policy: diagnostictrace.CapturePolicy{
			ContentMode: diagnostictrace.ContentRedacted, Redactor: "test",
		},
		Limits: diagnostictrace.DefaultLimits(),
		Metadata: diagnostictrace.Metadata{
			RootTurnID: "browser-turn-3", ParentTurnID: "main-turn-1", AgentID: "browser",
			ChildTurnID: "subturn-sibling", SessionHash: "newer-durable-session-hash",
		},
		Records: []diagnostictrace.Record{
			liveEvidenceRecord(
				t,
				1,
				0,
				diagnostictrace.RecordToolCall,
				"sibling-call",
				"session-sibling",
				diagnostictrace.ToolPayload{
					Tool: "browser_session", Status: "started", Executed: true,
					ArgumentsPreview: `{"operation":"open","target":"companion","profile":"managed"}`,
				},
			),
			liveEvidenceRecord(
				t,
				2,
				time.Millisecond,
				diagnostictrace.RecordToolResult,
				"sibling-result",
				"session-sibling",
				diagnostictrace.ToolPayload{
					Tool: "browser_session", Status: "completed", Executed: true,
				},
			),
		},
		Outcome: &diagnostictrace.Outcome{Status: "completed"},
	})
	childStore := diagnostictrace.Store{
		Root: diagnostictrace.ResolveStoreRoot(cfg.Diagnostics.TraceCapture.StateDir, childWorkspace),
	}
	if _, err := childStore.Save(sibling); err != nil {
		t.Fatalf("Save(%s): %v", sibling.TraceID, err)
	}

	evidence, err := collectLiveExecutionEvidence(
		t.Context(), cfg, runtimeevents.NewTraceScope(rootWorkspace, "main-turn-1"),
		sessionKey, "browser", created.Add(-time.Second),
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

func TestCollectLiveExecutionEvidenceUsesRootScopeForDelegatedSessionFinal(t *testing.T) {
	rootWorkspace := t.TempDir()
	childWorkspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Diagnostics.TraceCapture.Enabled = true
	cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{
		ID: "browser", Workspace: childWorkspace,
	})
	created := time.Now().UTC()
	rootSessionKey := "sk_v1_live_evidence_root"
	rootSessionHash := fmt.Sprintf("%x", sha256.Sum256([]byte(rootSessionKey)))
	root := finalizedLiveEvidenceTrace(t, diagnostictrace.Trace{
		SchemaVersion: diagnostictrace.SchemaVersionV1,
		TraceID:       "trace-live-parent-delegated-final",
		CreatedAt:     created,
		Policy: diagnostictrace.CapturePolicy{
			ContentMode: diagnostictrace.ContentRedacted, Redactor: "test",
		},
		Limits: diagnostictrace.DefaultLimits(),
		Metadata: diagnostictrace.Metadata{
			RootTurnID: "main-turn-delegated-final", AgentID: "main", SessionHash: rootSessionHash,
		},
		Records: []diagnostictrace.Record{
			liveEvidenceRecord(
				t, 1, 0, diagnostictrace.RecordToolCall, "root-call", "delegate-final",
				diagnostictrace.ToolPayload{Tool: "delegate", Status: "started", Executed: true},
			),
			liveEvidenceRecord(
				t, 2, time.Millisecond, diagnostictrace.RecordSubTurnAdmission, "admission", "",
				diagnostictrace.SubTurnAdmissionPayload{
					State: "admitted", Stage: "target_agent", AgentID: "browser",
					ChildTurnID: "subturn-delegated-final",
				},
			),
			liveEvidenceRecord(
				t, 3, 2*time.Millisecond, diagnostictrace.RecordToolResult, "root-result", "delegate-final",
				diagnostictrace.ToolPayload{Tool: "delegate", Status: "completed", Executed: true},
			),
		},
		Outcome: &diagnostictrace.Outcome{Status: "completed"},
	})
	child := finalizedLiveEvidenceTrace(t, diagnostictrace.Trace{
		SchemaVersion: diagnostictrace.SchemaVersionV1,
		TraceID:       "trace-live-child-delegated-final",
		CreatedAt:     created.Add(time.Millisecond),
		Policy: diagnostictrace.CapturePolicy{
			ContentMode: diagnostictrace.ContentRedacted, Redactor: "test",
		},
		Limits: diagnostictrace.DefaultLimits(),
		Metadata: diagnostictrace.Metadata{
			RootTurnID: "browser-turn-delegated-final", ParentTurnID: "main-turn-delegated-final",
			ChildTurnID: "subturn-delegated-final", AgentID: "browser",
			SessionHash: "delegated-child-session-hash",
		},
		Records: []diagnostictrace.Record{
			liveEvidenceRecord(
				t, 1, 0, diagnostictrace.RecordToolCall, "child-call", "session-open",
				diagnostictrace.ToolPayload{
					Tool: "browser_session", Status: "started", Executed: true,
					ArgumentsPreview: `{"operation":"open","target":"gateway","profile":"managed"}`,
				},
			),
			liveEvidenceRecord(
				t, 2, time.Millisecond, diagnostictrace.RecordToolResult, "child-result", "session-open",
				diagnostictrace.ToolPayload{Tool: "browser_session", Status: "completed", Executed: true},
			),
		},
		Outcome: &diagnostictrace.Outcome{Status: "completed"},
	})
	for workspace, trace := range map[string]diagnostictrace.Trace{
		rootWorkspace: root, childWorkspace: child,
	} {
		store := diagnostictrace.Store{
			Root: diagnostictrace.ResolveStoreRoot(cfg.Diagnostics.TraceCapture.StateDir, workspace),
		}
		if _, err := store.Save(trace); err != nil {
			t.Fatalf("Save(%s): %v", trace.TraceID, err)
		}
	}

	evidence, err := collectLiveExecutionEvidence(
		t.Context(), cfg,
		runtimeevents.NewTraceScope(rootWorkspace, "main-turn-delegated-final"),
		"sk_v1_delegated_child_final", "browser", created.Add(-time.Second),
	)
	if err != nil || evidence.Status != "verified" || evidence.Parent.Outcome != "completed" ||
		evidence.Delegation.Admitted != 1 || evidence.Child.Outcome != "completed" {
		t.Fatalf("evidence = %#v, error = %v", evidence, err)
	}
}

func TestCollectLiveExecutionEvidenceStitchesUserOnlyHandoffContinuation(t *testing.T) {
	rootWorkspace := t.TempDir()
	childWorkspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Diagnostics.TraceCapture.Enabled = true
	cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{
		ID: "browser", Workspace: childWorkspace,
	})
	created := time.Now().UTC().Add(-5 * time.Second)
	sessionKey := "sk_v1_live_evidence_handoff"
	sessionHash := fmt.Sprintf("%x", sha256.Sum256([]byte(sessionKey)))
	childSessionHash := "durable-handoff-child-session-hash"
	root := finalizedLiveEvidenceTrace(t, diagnostictrace.Trace{
		SchemaVersion: diagnostictrace.SchemaVersionV1,
		TraceID:       "trace-live-parent-handoff",
		CreatedAt:     created,
		Policy: diagnostictrace.CapturePolicy{
			ContentMode: diagnostictrace.ContentRedacted, Redactor: "test",
		},
		Limits: diagnostictrace.DefaultLimits(),
		Metadata: diagnostictrace.Metadata{
			RootTurnID: "main-turn-handoff", AgentID: "main", SessionHash: sessionHash,
		},
		Records: []diagnostictrace.Record{
			liveEvidenceRecord(
				t, 1, 0, diagnostictrace.RecordToolCall, "root-call", "delegate-handoff",
				diagnostictrace.ToolPayload{
					Tool: "delegate", Status: "started", Executed: true,
					ArgumentsPreview: `{"agent_id":"browser","delivery_mode":"user_only"}`,
				},
			),
			liveEvidenceRecord(
				t, 2, time.Millisecond, diagnostictrace.RecordSubTurnAdmission, "admission", "",
				diagnostictrace.SubTurnAdmissionPayload{
					State: "admitted", Stage: "target_agent", AgentID: "browser",
					ChildTurnID: "subturn-handoff",
				},
			),
		},
		Outcome: &diagnostictrace.Outcome{Status: "suspended"},
	})
	initial := finalizedLiveEvidenceTrace(t, diagnostictrace.Trace{
		SchemaVersion: diagnostictrace.SchemaVersionV1,
		TraceID:       "trace-live-child-handoff-initial",
		CreatedAt:     created.Add(100 * time.Millisecond),
		Policy: diagnostictrace.CapturePolicy{
			ContentMode: diagnostictrace.ContentRedacted, Redactor: "test",
		},
		Limits: diagnostictrace.DefaultLimits(),
		Metadata: diagnostictrace.Metadata{
			RootTurnID: "browser-turn-handoff", ParentTurnID: "main-turn-handoff",
			ChildTurnID: "subturn-handoff", AgentID: "browser", SessionHash: childSessionHash,
		},
		Records: []diagnostictrace.Record{
			liveEvidenceRecord(
				t, 1, 0, diagnostictrace.RecordToolCall, "open-call", "session-open",
				diagnostictrace.ToolPayload{
					Tool: "browser_session", Status: "started", Executed: true,
					ArgumentsPreview: `{"operation":"open","target":"cloud","profile":"personal"}`,
				},
			),
			liveEvidenceRecord(
				t, 2, time.Millisecond, diagnostictrace.RecordToolResult, "open-result", "session-open",
				diagnostictrace.ToolPayload{Tool: "browser_session", Status: "completed", Executed: true},
			),
			liveEvidenceRecord(
				t, 3, 2*time.Millisecond, diagnostictrace.RecordToolCall, "handoff-call", "session-handoff",
				diagnostictrace.ToolPayload{
					Tool: "browser_session", Status: "started", Executed: true,
					ArgumentsPreview: `{"operation":"handoff"}`,
				},
			),
			liveEvidenceRecord(
				t, 4, 3*time.Millisecond, diagnostictrace.RecordToolResult, "handoff-result", "session-handoff",
				diagnostictrace.ToolPayload{Tool: "browser_session", Status: "completed", Executed: true},
			),
		},
		Outcome: &diagnostictrace.Outcome{Status: "suspended"},
	})
	continuation := finalizedLiveEvidenceTrace(t, diagnostictrace.Trace{
		SchemaVersion: diagnostictrace.SchemaVersionV1,
		TraceID:       "trace-live-child-handoff-continuation",
		CreatedAt:     created.Add(2 * time.Second),
		Policy: diagnostictrace.CapturePolicy{
			ContentMode: diagnostictrace.ContentRedacted, Redactor: "test",
		},
		Limits: diagnostictrace.DefaultLimits(),
		Metadata: diagnostictrace.Metadata{
			RootTurnID: "browser-turn-handoff-continuation", AgentID: "browser",
			SessionHash: childSessionHash,
		},
		Records: []diagnostictrace.Record{
			liveEvidenceRecord(
				t, 1, 0, diagnostictrace.RecordToolCall, "resume-call", "session-resume",
				diagnostictrace.ToolPayload{
					Tool: "browser_session", Status: "started", Executed: true,
					ArgumentsPreview: `{"operation":"resume"}`,
				},
			),
			liveEvidenceRecord(
				t, 2, time.Millisecond, diagnostictrace.RecordToolResult, "resume-result", "session-resume",
				diagnostictrace.ToolPayload{Tool: "browser_session", Status: "completed", Executed: true},
			),
			liveEvidenceRecord(
				t, 3, 2*time.Millisecond, diagnostictrace.RecordToolCall, "close-call", "session-close",
				diagnostictrace.ToolPayload{
					Tool: "browser_session", Status: "started", Executed: true,
					ArgumentsPreview: `{"operation":"close"}`,
				},
			),
			liveEvidenceRecord(
				t, 4, 3*time.Millisecond, diagnostictrace.RecordToolResult, "close-result", "session-close",
				diagnostictrace.ToolPayload{Tool: "browser_session", Status: "completed", Executed: true},
			),
		},
		Outcome: &diagnostictrace.Outcome{Status: "completed"},
	})
	rootStore := diagnostictrace.Store{
		Root: diagnostictrace.ResolveStoreRoot(cfg.Diagnostics.TraceCapture.StateDir, rootWorkspace),
	}
	childStore := diagnostictrace.Store{
		Root: diagnostictrace.ResolveStoreRoot(cfg.Diagnostics.TraceCapture.StateDir, childWorkspace),
	}
	if _, err := rootStore.Save(root); err != nil {
		t.Fatal(err)
	}
	for _, trace := range []diagnostictrace.Trace{initial, continuation} {
		if _, err := childStore.Save(trace); err != nil {
			t.Fatal(err)
		}
	}

	evidence, err := collectLiveExecutionEvidence(
		t.Context(), cfg, runtimeevents.NewTraceScope(rootWorkspace, "main-turn-handoff"),
		sessionKey, "browser", created.Add(-time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Parent.Outcome != "completed" || len(evidence.Parent.UnpairedCalls) != 0 ||
		evidence.Child.Outcome != "completed" || evidence.Child.ToolCalls["browser_session"] != 4 ||
		len(evidence.Child.BrowserSessions) != 4 {
		t.Fatalf("evidence = %#v", evidence)
	}
	wantOperations := []string{"open", "handoff", "resume", "close"}
	for index, want := range wantOperations {
		if got := evidence.Child.BrowserSessions[index].Operation; got != want {
			t.Fatalf("browser session operation %d = %q, want %q", index, got, want)
		}
	}
}

func TestLiveEvidenceUserOnlyDelegationRequiresExactTarget(t *testing.T) {
	tests := []struct {
		name     string
		previews []string
		want     bool
	}{
		{name: "exact", previews: []string{`{"agent_id":"browser","delivery_mode":"user_only"}`}, want: true},
		{name: "parent only", previews: []string{`{"agent_id":"browser","delivery_mode":"parent_only"}`}},
		{name: "wrong agent", previews: []string{`{"agent_id":"coding","delivery_mode":"user_only"}`}},
		{name: "malformed", previews: []string{`not-json`}},
		{
			name: "ambiguous",
			previews: []string{
				`{"agent_id":"browser","delivery_mode":"user_only"}`,
				`{"agent_id":"browser","delivery_mode":"user_only"}`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records := make([]diagnostictrace.Record, 0, len(tt.previews))
			for index, preview := range tt.previews {
				records = append(records, liveEvidenceRecord(
					t, uint64(index+1), time.Duration(index)*time.Millisecond,
					diagnostictrace.RecordToolCall, "delegate-call", fmt.Sprintf("delegate-%d", index),
					diagnostictrace.ToolPayload{
						Tool: "delegate", Status: "started", Executed: true, ArgumentsPreview: preview,
					},
				))
			}
			trace := diagnostictrace.Trace{Records: records}
			if got := liveEvidenceUserOnlyDelegation(trace, "browser"); got != tt.want {
				t.Fatalf("liveEvidenceUserOnlyDelegation() = %v, want %v", got, tt.want)
			}
		})
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
		"sk_v1_missing", "browser", time.Now().UTC(),
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

func TestSummarizeLiveTraceAdmitsOnlyReadOnlyBrowserContexts(t *testing.T) {
	tests := []struct {
		operation string
		wantTool  string
	}{
		{operation: "list", wantTool: "browser_contexts"},
		{operation: "open", wantTool: "other"},
		{operation: "select", wantTool: "other"},
		{operation: "close", wantTool: "other"},
		{operation: "", wantTool: "other"},
	}
	for _, tt := range tests {
		name := tt.operation
		if name == "" {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			trace := finalizedLiveEvidenceTrace(t, diagnostictrace.Trace{
				SchemaVersion: diagnostictrace.SchemaVersionV1,
				TraceID:       "trace-browser-contexts-" + name,
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
						t, 1, 0, diagnostictrace.RecordToolCall, "call", "contexts-1",
						diagnostictrace.ToolPayload{
							Tool: "browser_contexts", Action: tt.operation,
							Status: "started", Executed: true,
						},
					),
					liveEvidenceRecord(
						t, 2, time.Millisecond, diagnostictrace.RecordToolResult,
						"result", "contexts-1", diagnostictrace.ToolPayload{
							Tool: "browser_contexts", Status: "completed", Executed: true,
						},
					),
				},
				Outcome: &diagnostictrace.Outcome{Status: "completed"},
			})

			evidence := summarizeLiveTrace(trace)
			if evidence.ToolCalls[tt.wantTool] != 1 || len(evidence.ToolCalls) != 1 ||
				len(evidence.ToolFailures) != 0 || len(evidence.UnpairedCalls) != 0 {
				t.Fatalf("operation %q evidence = %#v", tt.operation, evidence)
			}
		})
	}
}

func TestSummarizeLiveTraceBindsBrowserStatusState(t *testing.T) {
	tests := []struct {
		name           string
		resultPreview  string
		wantState      string
		wantIncomplete bool
	}{
		{
			name: "lost", resultPreview: `{"browser_session_id":"private-session","state":"lost"}`,
			wantState: "lost",
		},
		{
			name: "ready", resultPreview: `{"browser_session_id":"private-session","state":"ready"}`,
			wantState: "ready",
		},
		{name: "missing state", resultPreview: `{}`, wantIncomplete: true},
		{name: "malformed", resultPreview: `not-json`, wantIncomplete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trace := finalizedLiveEvidenceTrace(t, diagnostictrace.Trace{
				SchemaVersion: diagnostictrace.SchemaVersionV1,
				TraceID:       "trace-browser-status-" + strings.ReplaceAll(tt.name, " ", "-"),
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
						t, 1, 0, diagnostictrace.RecordToolCall, "call", "session-status",
						diagnostictrace.ToolPayload{
							Tool: "browser_session", Status: "started", Executed: true,
							ArgumentsPreview: `{"operation":"status","browser_session_id":"private-session"}`,
						},
					),
					liveEvidenceRecord(
						t, 2, time.Millisecond, diagnostictrace.RecordToolResult,
						"result", "session-status", diagnostictrace.ToolPayload{
							Tool: "browser_session", Status: "completed", Executed: true,
							ResultPreview: tt.resultPreview,
						},
					),
				},
				Outcome: &diagnostictrace.Outcome{Status: "completed"},
			})

			evidence := summarizeLiveTrace(trace)
			if evidence.Incomplete != tt.wantIncomplete ||
				len(evidence.BrowserSessions) != 1 ||
				evidence.BrowserSessions[0].State != tt.wantState {
				t.Fatalf("status evidence = %#v", evidence)
			}
			encoded, err := json.Marshal(evidence)
			if err != nil {
				t.Fatalf("Marshal(evidence): %v", err)
			}
			if strings.Contains(string(encoded), "private-session") {
				t.Fatalf("status evidence leaked browser session ID: %s", encoded)
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
