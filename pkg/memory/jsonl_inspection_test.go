package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func inspectionFixture(t *testing.T) JSONLSnapshot {
	t.Helper()
	snapshot, err := BuildJSONLSnapshot("inspection-session", []providers.Message{
		{Role: "user", Content: "Ask one synthetic question."},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "question-call", Name: "request_user_input"}}},
		{
			Role: "tool", ToolCallID: "question-call", Content: "synthetic-answer",
			ToolResultStatus: providers.ToolResultStatusSuccess,
		},
		{Role: "assistant", Content: "The synthetic answer was received."},
	}, time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func inspectionMetadata(t *testing.T, metadata SessionMeta) []byte {
	t.Helper()
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestJSONLInspectionMatchesCanonicalSnapshotWithoutMutatingInputs(t *testing.T) {
	snapshot := inspectionFixture(t)
	meta, err := DecodeSessionMeta(snapshot.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	meta.Skip, meta.Summary = 1, "synthetic-summary"
	snapshot.Metadata = inspectionMetadata(t, meta)
	metadataBefore, historyBefore := bytes.Clone(snapshot.Metadata), bytes.Clone(snapshot.JSONL)
	result, err := InspectJSONLSnapshot(t.Context(), meta.Key, snapshot.Metadata, snapshot.JSONL)
	if err != nil || result.Metadata.Key != meta.Key || result.Metadata.Skip != 1 || len(result.History) != 3 ||
		result.History[0].Role != "assistant" || len(result.History[0].ToolCalls) != 1 ||
		result.History[0].ToolCalls[0].ID != "question-call" || result.History[1].ToolCallID != "question-call" {
		t.Fatal("clean inspection lost committed history/order", err)
	}
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, snapshot.JSONLFile), snapshot.JSONL, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, snapshot.MetadataFile), snapshot.Metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	canonical, err := store.GetSnapshot(t.Context(), meta.Key)
	if err != nil || !reflect.DeepEqual(canonical.History, result.History) ||
		canonical.Summary != result.Metadata.Summary {
		t.Fatal("inspection diverged from native canonical decoding", err)
	}
	if !bytes.Equal(snapshot.Metadata, metadataBefore) || !bytes.Equal(snapshot.JSONL, historyBefore) {
		t.Fatal("inspection rewrote caller-owned snapshot bytes")
	}
}

func TestJSONLInspectionRejectsDirtyPartialForeignAndOversizedSnapshots(t *testing.T) {
	for _, mode := range []string{
		"dirty", "foreign", "count-low", "count-high", "negative-count", "skip", "too-many-records",
		"missing-history", "partial-history", "unknown-message-field", "history-bytes", "combined-bytes",
	} {
		t.Run(mode, func(t *testing.T) {
			snapshot := inspectionFixture(t)
			meta, err := DecodeSessionMeta(snapshot.Metadata)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "dirty":
				meta.HistoryDirty = true
			case "foreign":
				meta.Key = "foreign-session"
			case "count-low":
				meta.Count--
			case "count-high":
				meta.Count++
			case "negative-count":
				meta.Count = -1
			case "skip":
				meta.Skip = meta.Count + 1
			case "too-many-records":
				meta.Count = MaxJSONLInspectionRecords + 1
			case "missing-history":
				snapshot.JSONL = nil
			case "partial-history":
				snapshot.JSONL = snapshot.JSONL[:len(snapshot.JSONL)-10]
			case "unknown-message-field":
				snapshot.JSONL = bytes.Replace(snapshot.JSONL, []byte(`"role":"user"`),
					[]byte(`"unexpected":true,"role":"user"`), 1)
			case "history-bytes":
				snapshot.JSONL = bytes.Repeat([]byte{'x'}, MaxJSONLInspectionBytes+1)
			case "combined-bytes":
				meta.Summary = strings.Repeat("s", MaxJSONLInspectionBytes)
			}
			snapshot.Metadata = inspectionMetadata(t, meta)
			metadataBefore, historyBefore := bytes.Clone(snapshot.Metadata), bytes.Clone(snapshot.JSONL)
			result, err := InspectJSONLSnapshot(t.Context(), "inspection-session", snapshot.Metadata, snapshot.JSONL)
			if err == nil || len(result.History) != 0 || result.Metadata.Key != "" {
				t.Fatal("unavailable snapshot produced partial/successful evidence")
			}
			if !bytes.Equal(snapshot.Metadata, metadataBefore) || !bytes.Equal(snapshot.JSONL, historyBefore) {
				t.Fatal("refused inspection attempted recovery or repair")
			}
		})
	}
}

func TestJSONLInspectionHonorsCancellationAndByteBoundary(t *testing.T) {
	snapshot := inspectionFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := InspectJSONLSnapshot(
		ctx,
		"inspection-session",
		snapshot.Metadata,
		snapshot.JSONL,
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal("canceled inspection was not stopped", err)
	}
	meta, err := DecodeSessionMeta(snapshot.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	meta.Summary = strings.Repeat("s", MaxJSONLInspectionBytes-len(snapshot.Metadata)-len(snapshot.JSONL))
	snapshot.Metadata = inspectionMetadata(t, meta)
	// The builder uses indented metadata; size the final compact encoding exactly.
	meta.Summary += strings.Repeat("s", MaxJSONLInspectionBytes-len(snapshot.Metadata)-len(snapshot.JSONL))
	snapshot.Metadata = inspectionMetadata(t, meta)
	if len(snapshot.Metadata)+len(snapshot.JSONL) != MaxJSONLInspectionBytes {
		t.Fatal("invalid byte-boundary fixture")
	}
	if _, err = InspectJSONLSnapshot(t.Context(), meta.Key, snapshot.Metadata, snapshot.JSONL); err != nil {
		t.Fatal("exact byte boundary rejected", err)
	}
}

func TestJSONLInspectionNamesMatchNativeWriterAndRejectInvalidKeys(t *testing.T) {
	store := &JSONLStore{dir: "not-opened"}
	for _, key := range []string{"opaque-session", "agent:main:chat/123", `agent\legacy`, "session with spaces"} {
		history, metadata, err := JSONLSessionFiles(key)
		if err != nil || history != filepath.Base(store.jsonlPath(key)) ||
			metadata != filepath.Base(store.metaPath(key)) ||
			strings.ContainsAny(history, `/\`) ||
			strings.ContainsAny(metadata, `/\`) {
			t.Fatal("inspection filename mapping diverged from native writer", err)
		}
		snapshot, err := BuildJSONLSnapshot(key, nil, time.Now())
		if err != nil || history != snapshot.JSONLFile || metadata != snapshot.MetadataFile {
			t.Fatal("snapshot filename mapping diverged", err)
		}
		if _, err = InspectJSONLSnapshot(t.Context(), key, snapshot.Metadata, snapshot.JSONL); err != nil {
			t.Fatal("clean empty snapshot rejected", err)
		}
	}
	for _, key := range []string{"", " \n", "nul\x00key", string([]byte{0xff})} {
		if history, metadata, err := JSONLSessionFiles(key); err == nil || history != "" || metadata != "" {
			t.Fatal("invalid inspection key produced filenames")
		}
	}
}
