package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func TestConvertSessionsSeparatesAndValidatesCorpus(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	sessions := filepath.Join(workspace, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := writeTestConfig(t, root, workspace)

	currentKey := session.BuildOpaqueSessionKey("current-with-history")
	currentScope := testCurrentScope()
	currentMeta := testMetadata(t, currentKey, currentScope, 2, true)
	writeTestFile(t, filepath.Join(sessions, sanitizeSessionKey(currentKey)+".meta.json"), currentMeta)
	oldHistory := strings.Join([]string{
		`{"role":"user","content":"hello"}`,
		`{"role":"assistant","content":"","tool_calls":[` +
			`{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"README.md\"}"},` +
			`"extra_content":{"tool_feedback_explanation":"read"}},` +
			`{"id":"call_2","type":"function","function":{"name":"search_files","arguments":"{}",` +
			`"thought_signature":"signature"},"extra_content":{"google":{"thought_signature":"signature"},` +
			`"tool_feedback_explanation":"search"}},` +
			`{"id":"call_3","type":"function","function":{"name":"exec","arguments":"{\"cmd\":\"pwd\"}"}}]}`,
	}, "\n") + "\n"
	currentHistoryPath := filepath.Join(sessions, sanitizeSessionKey(currentKey)+".jsonl")
	writeTestFile(t, currentHistoryPath, []byte(oldHistory))

	emptyKey := session.BuildOpaqueSessionKey("current-without-history")
	emptyMeta := testMetadata(t, emptyKey, currentScope, 0, false)
	writeTestFile(t, filepath.Join(sessions, sanitizeSessionKey(emptyKey)+".meta.json"), emptyMeta)

	archivedKey := "agent:main:direct:old"
	archivedScope := json.RawMessage(`{"version":1,"agent_id":"main","channel":"telegram","account":"default",` +
		`"dimensions":[],"values":{}}`)
	archivedMeta := testMetadata(t, archivedKey, archivedScope, 1, true)
	archivedMetaPath := filepath.Join(sessions, sanitizeSessionKey(archivedKey)+".meta.json")
	archivedHistoryPath := filepath.Join(sessions, sanitizeSessionKey(archivedKey)+".jsonl")
	archivedHistory := []byte("not-current-json\n")
	writeTestFile(t, archivedMetaPath, archivedMeta)
	writeTestFile(t, archivedHistoryPath, archivedHistory)

	output := filepath.Join(root, "candidate")
	manifest, err := convertSessions(convertOptions{
		SourceRoot:  root,
		OutputRoot:  output,
		ConfigPaths: []string{configPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Totals.Retained.Metadata != 2 || manifest.Totals.Retained.Histories != 1 ||
		manifest.Totals.Archived.Metadata != 1 || manifest.Totals.Archived.Histories != 1 {
		t.Fatalf("cohort totals = %+v", manifest.Totals)
	}
	if manifest.Totals.AliasesRemoved != 1 || manifest.Totals.ToolCallsFlattened != 3 ||
		manifest.Totals.GoogleCasesFlattened != 1 || manifest.Totals.MessagesValidated != 2 {
		t.Fatalf("conversion totals = %+v", manifest.Totals)
	}

	relCurrentMeta, err := filepath.Rel(root, filepath.Join(sessions, sanitizeSessionKey(currentKey)+".meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	convertedMeta := readTestFile(t, filepath.Join(output, cohortRetained, relCurrentMeta))
	if bytes.Contains(convertedMeta, []byte(`"aliases"`)) {
		t.Fatalf("converted metadata retains aliases: %s", convertedMeta)
	}
	decodedMeta, err := memory.DecodeSessionMeta(convertedMeta)
	if err != nil {
		t.Fatalf("decode converted metadata: %v", err)
	}
	if decodedMeta.Key != currentKey {
		t.Fatalf("converted key = %q, want %q", decodedMeta.Key, currentKey)
	}

	relHistory, err := filepath.Rel(root, currentHistoryPath)
	if err != nil {
		t.Fatal(err)
	}
	convertedHistory := readTestFile(t, filepath.Join(output, cohortRetained, relHistory))
	lines := bytes.Split(bytes.TrimSuffix(convertedHistory, []byte{'\n'}), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("converted lines = %d, want 2", len(lines))
	}
	message, err := memory.DecodeJSONLMessage(lines[1])
	if err != nil {
		t.Fatalf("decode converted message: %v", err)
	}
	if len(message.ToolCalls) != 3 || message.ToolCalls[0].Name != "read_file" ||
		message.ToolCalls[1].ThoughtSignature != "signature" ||
		message.ToolCalls[1].ToolFeedbackExplanation != "search" {
		t.Fatalf("converted tool calls = %+v", message.ToolCalls)
	}

	relArchivedMeta, err := filepath.Rel(root, archivedMetaPath)
	if err != nil {
		t.Fatal(err)
	}
	relArchivedHistory, err := filepath.Rel(root, archivedHistoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, filepath.Join(output, cohortArchived, relArchivedMeta)); !bytes.Equal(got, archivedMeta) {
		t.Fatal("archived metadata is not an exact copy")
	}
	if got := readTestFile(
		t,
		filepath.Join(output, cohortArchived, relArchivedHistory),
	); !bytes.Equal(
		got,
		archivedHistory,
	) {
		t.Fatal("archived history is not an exact copy")
	}
	if got := readTestFile(t, currentHistoryPath); !bytes.Equal(got, []byte(oldHistory)) {
		t.Fatal("source history changed")
	}
	if _, err := os.Stat(filepath.Join(output, "manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
}

func TestConvertSessionsUsesRuntimeWorkspaceResolution(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	defaultWorkspace := filepath.Join(root, "workspace")
	namedWorkspace := filepath.Join(root, "workspace-coding")
	for _, path := range []string{defaultWorkspace, namedWorkspace} {
		if err := os.MkdirAll(filepath.Join(path, "sessions"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = "~/workspace"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "Coding"},
	}
	configPath := writeTestConfigValue(t, root, cfg)
	for index, workspace := range []string{defaultWorkspace, namedWorkspace} {
		key := session.BuildOpaqueSessionKey("workspace-resolution-" + string(rune('a'+index)))
		writeTestFile(
			t,
			filepath.Join(workspace, "sessions", sanitizeSessionKey(key)+".meta.json"),
			testMetadata(t, key, testCurrentScope(), 0, false),
		)
	}

	manifest, err := convertSessions(convertOptions{
		SourceRoot:  root,
		OutputRoot:  filepath.Join(root, "candidate"),
		ConfigPaths: []string{configPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"workspace-coding/sessions", "workspace/sessions"}
	if len(manifest.SessionDirs) != len(want) {
		t.Fatalf("session dirs = %v, want %v", manifest.SessionDirs, want)
	}
	for index := range want {
		if manifest.SessionDirs[index] != want[index] {
			t.Fatalf("session dirs = %v, want %v", manifest.SessionDirs, want)
		}
	}
	if manifest.Totals.Retained.Metadata != 2 {
		t.Fatalf("retained metadata = %d, want 2", manifest.Totals.Retained.Metadata)
	}
}

func TestConvertMessageRejectsAmbiguousOldToolCalls(t *testing.T) {
	current := []byte(
		`{"role":"assistant","content":"","tool_calls":[` +
			`{"id":"call_1","type":"function","name":"read_file","arguments":{}}]}`,
	)
	converted, toolCalls, googleCases, err := convertMessage(current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(converted, current) || toolCalls != 0 || googleCases != 0 {
		t.Fatalf("current message changed: %s, %d, %d", converted, toolCalls, googleCases)
	}

	tests := map[string]string{
		"mixed flat and nested fields": `{"role":"assistant","content":"","tool_calls":[{` +
			`"id":"call_1","name":"read_file","arguments":{},` +
			`"function":{"name":"read_file","arguments":"{}"}}]}`,
		"invalid arguments": `{"role":"assistant","content":"","tool_calls":[{` +
			`"id":"call_1","type":"function","function":{"name":"read_file","arguments":"[]"}}]}`,
		"conflicting signatures": `{"role":"assistant","content":"","tool_calls":[{` +
			`"id":"call_1","type":"function",` +
			`"function":{"name":"read_file","arguments":"{}","thought_signature":"one"},` +
			`"extra_content":{"google":{"thought_signature":"two"},` +
			`"tool_feedback_explanation":"feedback"}}]}`,
		"unknown old field": `{"role":"assistant","content":"","tool_calls":[{` +
			`"id":"call_1","type":"function",` +
			`"function":{"name":"read_file","arguments":"{}"},"legacy":true}]}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := convertMessage([]byte(input)); err == nil {
				t.Fatal("expected conversion error")
			}
		})
	}
}

func TestConvertSessionsRejectsRetainedCountMismatch(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	sessions := filepath.Join(workspace, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := writeTestConfig(t, root, workspace)
	key := session.BuildOpaqueSessionKey("count-mismatch")
	writeTestFile(
		t,
		filepath.Join(sessions, sanitizeSessionKey(key)+".meta.json"),
		testMetadata(t, key, testCurrentScope(), 2, false),
	)
	writeTestFile(
		t,
		filepath.Join(sessions, sanitizeSessionKey(key)+".jsonl"),
		[]byte("{\"role\":\"user\",\"content\":\"one\"}\n"),
	)
	_, err := convertSessions(convertOptions{
		SourceRoot:  root,
		OutputRoot:  filepath.Join(root, "candidate"),
		ConfigPaths: []string{configPath},
	})
	if err == nil || !strings.Contains(err.Error(), "metadata records 2") {
		t.Fatalf("count mismatch error = %v", err)
	}
}

func TestConvertSessionsRecordsArchivedCountMismatch(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	sessions := filepath.Join(workspace, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := writeTestConfig(t, root, workspace)
	key := "agent:main:old"
	metaPath := filepath.Join(sessions, sanitizeSessionKey(key)+".meta.json")
	historyPath := filepath.Join(sessions, sanitizeSessionKey(key)+".jsonl")
	writeTestFile(t, metaPath, testMetadata(t, key, json.RawMessage(`{"version":1}`), 1, false))
	options := convertOptions{
		SourceRoot:  root,
		OutputRoot:  filepath.Join(root, "candidate"),
		ConfigPaths: []string{configPath},
	}
	if _, err := convertSessions(options); err == nil ||
		!strings.Contains(err.Error(), "archived metadata") || !strings.Contains(err.Error(), "has no history") {
		t.Fatalf("missing archived history error = %v", err)
	}
	history := []byte("not-json\nalso-not-json\n")
	writeTestFile(t, historyPath, history)
	manifest, err := convertSessions(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.ArchivedHistoryCountMismatches) != 1 {
		t.Fatalf("archived count mismatches = %+v", manifest.ArchivedHistoryCountMismatches)
	}
	mismatch := manifest.ArchivedHistoryCountMismatches[0]
	relHistory, err := filepath.Rel(root, historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if mismatch.Path != filepath.ToSlash(relHistory) || mismatch.MetadataCount != 1 || mismatch.FramedMessages != 2 {
		t.Fatalf("archived count mismatch = %+v", mismatch)
	}
	var persisted cutoverManifest
	if err := json.Unmarshal(
		readTestFile(t, filepath.Join(options.OutputRoot, "manifest.json")),
		&persisted,
	); err != nil {
		t.Fatal(err)
	}
	if len(persisted.ArchivedHistoryCountMismatches) != 1 ||
		persisted.ArchivedHistoryCountMismatches[0] != mismatch {
		t.Fatalf("persisted archived count mismatches = %+v", persisted.ArchivedHistoryCountMismatches)
	}
	archived := readTestFile(t, filepath.Join(options.OutputRoot, cohortArchived, relHistory))
	if !bytes.Equal(archived, history) {
		t.Fatal("archived count-mismatched history is not an exact copy")
	}
}

func TestConvertHistoryRejectsNonCanonicalFraming(t *testing.T) {
	if _, err := convertHistory([]byte("{\"role\":\"user\",\"content\":\"hello\"}")); err == nil {
		t.Fatal("expected missing final newline error")
	}
	if _, err := convertHistory([]byte("{\"role\":\"user\",\"content\":\"hello\"}\n\n")); err == nil {
		t.Fatal("expected blank line error")
	}
}

func TestInspectMetadataRejectsUnknownAndDuplicateFields(t *testing.T) {
	key := session.BuildOpaqueSessionKey("strict-metadata")
	valid := testMetadata(t, key, testCurrentScope(), 0, false)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(valid, &object); err != nil {
		t.Fatal(err)
	}
	object["legacy"] = json.RawMessage("true")
	unknown, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inspectMetadata(unknown, sanitizeSessionKey(key)+".meta.json"); err == nil {
		t.Fatal("expected unknown metadata field error")
	}

	duplicate := bytes.Replace(valid, []byte(`"key":`), []byte(`"key":"duplicate","key":`), 1)
	if _, err := inspectMetadata(duplicate, sanitizeSessionKey(key)+".meta.json"); err == nil {
		t.Fatal("expected duplicate metadata field error")
	}

	delete(object, "legacy")
	object["history_dirty"] = json.RawMessage("true")
	dirty, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inspectMetadata(dirty, sanitizeSessionKey(key)+".meta.json"); err == nil ||
		!strings.Contains(err.Error(), "unfinished history mutation") {
		t.Fatalf("dirty metadata error = %v", err)
	}

	archivedKey := "agent:main:old"
	archived := testMetadata(t, archivedKey, json.RawMessage(`{"version":1}`), 0, false)
	if err := json.Unmarshal(archived, &object); err != nil {
		t.Fatal(err)
	}
	object["history_dirty"] = json.RawMessage("true")
	archivedDirty, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inspectMetadata(
		archivedDirty,
		sanitizeSessionKey(archivedKey)+".meta.json",
	); err == nil || !strings.Contains(err.Error(), "unfinished history mutation") {
		t.Fatalf("archived dirty metadata error = %v", err)
	}
}

func TestConvertSessionsRejectsOrphanHistoryAndExistingOutput(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	sessions := filepath.Join(workspace, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := writeTestConfig(t, root, workspace)
	writeTestFile(t, filepath.Join(sessions, "orphan.jsonl"), []byte("{}\n"))

	options := convertOptions{
		SourceRoot:  root,
		OutputRoot:  filepath.Join(root, "candidate"),
		ConfigPaths: []string{configPath},
	}
	overlap := options
	overlap.OutputRoot = filepath.Join(workspace, "candidate")
	if _, err := convertSessions(overlap); err == nil ||
		!strings.Contains(err.Error(), "overlaps configured workspace") {
		t.Fatalf("workspace overlap error = %v", err)
	}
	if _, err := convertSessions(options); err == nil || !strings.Contains(err.Error(), "orphan history") {
		t.Fatalf("orphan conversion error = %v", err)
	}
	if _, err := os.Stat(options.OutputRoot); !os.IsNotExist(err) {
		t.Fatalf("failed conversion published output: %v", err)
	}
	partials, err := filepath.Glob(filepath.Join(root, ".candidate.partial-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(partials) != 0 {
		t.Fatalf("failed conversion retained partial outputs: %v", partials)
	}
	if err := os.Mkdir(options.OutputRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := convertSessions(options); err == nil || !strings.Contains(err.Error(), "output already exists") {
		t.Fatalf("existing output error = %v", err)
	}
}

func writeTestConfig(t *testing.T, root, workspace string) string {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	return writeTestConfigValue(t, root, cfg)
}

func writeTestConfigValue(t *testing.T, root string, cfg *config.Config) string {
	t.Helper()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	writeTestFile(t, path, data)
	return path
}

func testMetadata(
	t *testing.T,
	key string,
	scope json.RawMessage,
	count int,
	aliases bool,
) []byte {
	t.Helper()
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	meta := memory.SessionMeta{
		Key:       key,
		Summary:   "",
		Skip:      0,
		Count:     count,
		CreatedAt: now,
		UpdatedAt: now,
		Scope:     scope,
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if !aliases {
		return data
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	object["aliases"] = json.RawMessage(`["old-key"]`)
	data, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testCurrentScope() json.RawMessage {
	data, err := json.Marshal(session.SessionScope{
		Version:    session.ScopeVersion,
		AgentID:    "main",
		Channel:    "telegram",
		Account:    "default",
		Dimensions: []string{},
		Values:     map[string]string{},
	})
	if err != nil {
		panic(err)
	}
	return data
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
