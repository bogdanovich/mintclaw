package diagnostics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

func TestQueryFindsExactTaskAndThreadWithoutExportingRoots(t *testing.T) {
	base := t.TempDir()
	profileRoot := filepath.Join(base, "profile-traces")
	threadRoot := filepath.Join(base, "thread-traces")
	created := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	writeQueryTrace(t, profileRoot, queryTrace("trace-task", created, "main-turn-1", "task-safe-1"))
	writeQueryTrace(t, threadRoot, queryTrace("trace-thread", created.Add(time.Minute), "coding-turn-1", ""))
	if err := os.WriteFile(filepath.Join(profileRoot, "trace-broken.json"), []byte("{secret"), 0o600); err != nil {
		t.Fatalf("WriteFile(corrupt): %v", err)
	}
	if err := os.WriteFile(filepath.Join(profileRoot, "_unsafe-name.json"), []byte("private"), 0o600); err != nil {
		t.Fatalf("WriteFile(unsafe name): %v", err)
	}

	stores := []StoreSpec{
		{
			ID: "profile-main", Kind: "profile", Alias: "main", Root: profileRoot,
			Capture:   CaptureSettings{Enabled: true, ContentMode: "redacted_content", MaxTraces: 100},
			Retention: 24 * time.Hour,
		},
		{
			ID: "coding-thread-safe", Kind: "coding_thread", Alias: "thread-safe-1", Root: threadRoot,
			Capture:   CaptureSettings{Enabled: true, ContentMode: "redacted_content", MaxTraces: 100},
			Retention: 24 * time.Hour,
		},
	}
	result, err := Query(QueryOptions{
		Stores: stores, Selector: Selector{TaskID: "task-safe-1"},
		Now: func() time.Time { return created.Add(2 * time.Hour) },
	})
	if err != nil {
		t.Fatalf("Query(task): %v", err)
	}
	if result.TotalMatches != 1 || len(result.Matches) != 1 || result.Matches[0].Trace.TraceID != "trace-task" {
		t.Fatalf("task matches = %#v", result.Matches)
	}
	if len(result.Problems) != 2 || !hasProblemCode(result.Problems, "corrupt_trace") ||
		!hasProblemCode(result.Problems, "invalid_trace_filename") {
		t.Fatalf("problems = %#v", result.Problems)
	}
	if result.Stores[0].ProblemFiles != 2 || result.Stores[0].CorruptTraces != 1 {
		t.Fatalf("profile health = %#v", result.Stores[0])
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, forbidden := range []string{base, profileRoot, threadRoot, "secret"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("query result leaked %q: %s", forbidden, encoded)
		}
	}

	threadResult, err := Query(QueryOptions{
		Stores: stores, Selector: Selector{ThreadID: "thread-safe-1"},
	})
	if err != nil {
		t.Fatalf("Query(thread): %v", err)
	}
	if threadResult.TotalMatches != 1 || threadResult.Matches[0].Trace.TraceID != "trace-thread" {
		t.Fatalf("thread matches = %#v", threadResult.Matches)
	}
}

func hasProblemCode(problems []Problem, code string) bool {
	for _, problem := range problems {
		if problem.Code == code {
			return true
		}
	}
	return false
}

func TestQueryMatchesCorrelationAndReportsLimits(t *testing.T) {
	root := filepath.Join(t.TempDir(), "traces")
	created := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for index := range 3 {
		trace := queryTrace(
			"trace-"+string(rune('a'+index)),
			created.Add(time.Duration(index)*time.Minute),
			"turn-1",
			"",
		)
		trace.Records[0].Correlation.InvocationID = "invocation-safe"
		writeQueryTrace(t, root, trace)
	}
	result, err := Query(QueryOptions{
		Stores:   []StoreSpec{{ID: "profile-main", Kind: "profile", Root: root, MaxTraces: 10}},
		Selector: Selector{InvocationID: "invocation-safe"},
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if result.TotalMatches != 3 || len(result.Matches) != 2 || !result.MatchTruncated {
		t.Fatalf("limited result = %#v", result)
	}
	if result.Matches[0].Trace.TraceID != "trace-c" || result.Matches[1].Trace.TraceID != "trace-b" {
		t.Fatalf("match order = %#v", result.Matches)
	}
}

func TestQueryEnforcesStoreSessionBoundary(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shared-traces")
	created := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	selectedHash := strings.Repeat("a", 64)
	selected := queryTrace("trace-selected", created, "turn-selected", "")
	selected.Metadata.SessionHash = selectedHash
	writeQueryTrace(t, root, selected)
	unrelated := queryTrace("trace-unrelated", created.Add(time.Minute), "turn-unrelated", "")
	unrelated.Metadata.SessionHash = strings.Repeat("b", 64)
	writeQueryTrace(t, root, unrelated)
	if err := os.WriteFile(filepath.Join(root, "trace-unrelated-corrupt.json"), []byte("{private"), 0o600); err != nil {
		t.Fatalf("WriteFile(corrupt): %v", err)
	}

	result, err := Query(QueryOptions{Stores: []StoreSpec{{
		ID: "coding-thread", Kind: "coding_thread", Alias: "thread-safe", Root: root,
		RequiredSessionHash: selectedHash,
	}}, Selector: Selector{ThreadID: "thread-safe"}})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if result.TotalMatches != 1 || result.Matches[0].Trace.TraceID != "trace-selected" {
		t.Fatalf("session-bounded matches = %#v", result.Matches)
	}
	if len(result.Problems) != 0 || result.Stores[0].ProblemFiles != 1 || result.Stores[0].CorruptTraces != 1 {
		t.Fatalf("session-bounded problems = %#v, health = %#v", result.Problems, result.Stores[0])
	}
}

func TestQuerySanitizesAndSaturatesDroppedRecordTotals(t *testing.T) {
	root := filepath.Join(t.TempDir(), "traces")
	created := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for index, dropped := range []int{-4, int(^uint(0) >> 1), 1} {
		trace := queryTrace(fmt.Sprintf("trace-%d", index), created.Add(time.Duration(index)*time.Minute), "turn", "")
		trace.Truncation.DroppedRecords = dropped
		writeQueryTrace(t, root, trace)
	}

	result, err := Query(QueryOptions{
		Stores: []StoreSpec{{ID: "profile-main", Kind: "profile", Root: root}},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got, want := result.Stores[0].DroppedRecords, int(^uint(0)>>1); got != want {
		t.Fatalf("DroppedRecords = %d, want %d", got, want)
	}
}

func TestQueryExactTraceDoesNotInspectUnrelatedFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "traces")
	created := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	writeQueryTrace(t, root, queryTrace("trace-selected", created, "turn-selected", ""))
	if err := os.WriteFile(filepath.Join(root, "trace-unrelated-corrupt.json"), []byte("{private"), 0o600); err != nil {
		t.Fatalf("WriteFile(corrupt): %v", err)
	}

	result, err := Query(QueryOptions{
		Stores:   []StoreSpec{{ID: "profile-main", Kind: "profile", Root: root}},
		Selector: Selector{TraceIDs: []string{"trace-selected"}},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Matches) != 1 || len(result.Problems) != 0 ||
		result.Stores[0].ScanMode != "exact_trace_ids" || result.Stores[0].CandidateFiles != 1 {
		t.Fatalf("exact query = %#v", result)
	}
}

func TestQueryRejectsUnsafeSelectorsAndStoreMetadata(t *testing.T) {
	validStore := StoreSpec{ID: "store-1", Kind: "profile", Root: t.TempDir()}
	tooManyTraces := make([]string, HardTraceSelectors+1)
	for index := range tooManyTraces {
		tooManyTraces[index] = fmt.Sprintf("trace-%d", index)
	}
	for name, options := range map[string]QueryOptions{
		"trace":   {Stores: []StoreSpec{validStore}, Selector: Selector{TraceIDs: []string{"../trace"}}},
		"task":    {Stores: []StoreSpec{validStore}, Selector: Selector{TaskID: "/private/task"}},
		"session": {Stores: []StoreSpec{validStore}, Selector: Selector{SessionHash: "short"}},
		"store":   {Stores: []StoreSpec{{ID: "bad/store", Kind: "profile", Root: t.TempDir()}}},
		"duplicate store": {
			Stores: []StoreSpec{validStore, {ID: validStore.ID, Kind: "profile", Root: t.TempDir()}},
		},
		"trace count": {
			Stores:   []StoreSpec{validStore},
			Selector: Selector{TraceIDs: tooManyTraces},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Query(options); err == nil {
				t.Fatal("Query() accepted unsafe input")
			}
		})
	}
}

func queryTrace(id string, created time.Time, turnID, taskID string) diagnostictrace.Trace {
	return diagnostictrace.Trace{
		SchemaVersion: diagnostictrace.SchemaVersionV1,
		TraceID:       id,
		CreatedAt:     created,
		Policy:        diagnostictrace.CapturePolicy{ContentMode: diagnostictrace.ContentMetadataOnly},
		Limits:        diagnostictrace.DefaultLimits(),
		Metadata:      diagnostictrace.Metadata{RootTurnID: turnID},
		Records: []diagnostictrace.Record{{
			Sequence: 1,
			Kind:     diagnostictrace.RecordTurnEnd,
			Origin:   diagnostictrace.Origin{Kind: "runtime_event", ID: "evt-" + id},
			Scope:    diagnostictrace.Scope{TurnID: turnID, TaskID: taskID},
			Data:     json.RawMessage(`{"status":"completed"}`),
		}},
		Outcome: &diagnostictrace.Outcome{Status: "completed"},
	}
}

func writeQueryTrace(t *testing.T, root string, trace diagnostictrace.Trace) {
	t.Helper()
	finalized, err := diagnostictrace.Finalize(trace)
	if err != nil {
		t.Fatalf("Finalize(%s): %v", trace.TraceID, err)
	}
	if _, err := (diagnostictrace.Store{Root: root}).Save(finalized); err != nil {
		t.Fatalf("Save(%s): %v", trace.TraceID, err)
	}
}
