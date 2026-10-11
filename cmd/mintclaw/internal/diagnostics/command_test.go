package diagnostics

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

func TestDiagnosticsListShowAndCorrelateStayMetadataOnlyAndReadOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "traces")
	created := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	trace := diagnostictrace.Trace{
		SchemaVersion: diagnostictrace.SchemaVersionV1,
		TraceID:       "trace-safe-command",
		CreatedAt:     created,
		Source: diagnostictrace.Source{
			MintClawVersion: "v0.1.0-test",
			Commit:          strings.Repeat("a", 40),
		},
		Policy: diagnostictrace.CapturePolicy{
			ContentMode: diagnostictrace.ContentRedacted,
			Redactor:    "mintclaw.config_filter.v1",
		},
		Limits:   diagnostictrace.DefaultLimits(),
		Metadata: diagnostictrace.Metadata{RootTurnID: "main-turn-1", SessionHash: strings.Repeat("b", 64)},
		Records: []diagnostictrace.Record{{
			Sequence: 1,
			Kind:     diagnostictrace.RecordToolResult,
			Origin:   diagnostictrace.Origin{Kind: "runtime_event", ID: "evt-safe"},
			Scope:    diagnostictrace.Scope{TurnID: "main-turn-1", TaskID: "task-safe"},
			Correlation: diagnostictrace.Correlation{
				ModelAttemptID: "model-attempt-safe", ToolCallID: "call-safe",
			},
			Data: json.RawMessage(`{
                "tool":"read_file",
                "status":"succeeded",
                "result_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
                "result_preview":"private result",
                "unknown":"private unknown"
            }`),
		}},
		Outcome: &diagnostictrace.Outcome{Status: "completed"},
	}
	finalized, err := diagnostictrace.Finalize(trace)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	path, err := (diagnostictrace.Store{Root: root}).Save(finalized)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(before): %v", err)
	}
	infoBefore, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(before): %v", err)
	}

	list := executeDiagnostics(t, []string{"--trace-root", root, "--json", "list"})
	show := executeDiagnostics(t, []string{"--trace-root", root, "--json", "show", trace.TraceID})
	correlate := executeDiagnostics(t, []string{
		"--trace-root", root, "--turn", "main-turn-1", "--json", "correlate",
	})
	bundleOutput := filepath.Join(t.TempDir(), "bundle")
	exportReceipt := executeDiagnostics(t, []string{
		"--trace-root", root,
		"--trace", trace.TraceID,
		"--json",
		"export",
		"--output", bundleOutput,
	})
	for name, output := range map[string]string{"list": list, "show": show, "correlate": correlate} {
		if !json.Valid([]byte(output)) {
			t.Fatalf("%s output is not JSON: %s", name, output)
		}
		for _, forbidden := range []string{root, "private result", "private unknown", "result_preview"} {
			if strings.Contains(output, forbidden) {
				t.Fatalf("%s leaked %q: %s", name, forbidden, output)
			}
		}
	}
	for _, expected := range []string{trace.TraceID, "read_file", "succeeded", "call-safe"} {
		if !strings.Contains(show, expected) || !strings.Contains(correlate, expected) {
			t.Fatalf("safe evidence lacks %q\nshow=%s\ncorrelate=%s", expected, show, correlate)
		}
	}
	if strings.Contains(list, "call-safe") {
		t.Fatalf("list unexpectedly included record content: %s", list)
	}
	if !json.Valid([]byte(exportReceipt)) || !strings.Contains(exportReceipt, "metadata_only") {
		t.Fatalf("export receipt = %s", exportReceipt)
	}
	bundledTrace, err := os.ReadFile(filepath.Join(bundleOutput, "traces", trace.TraceID+".json"))
	if err != nil {
		t.Fatalf("ReadFile(bundle trace): %v", err)
	}
	for _, forbidden := range []string{"private result", "private unknown", "result_preview", root} {
		if strings.Contains(string(bundledTrace), forbidden) {
			t.Fatalf("support bundle leaked %q: %s", forbidden, bundledTrace)
		}
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(after): %v", err)
	}
	infoAfter, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(after): %v", err)
	}
	if !bytes.Equal(before, after) || !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Fatal("read-only diagnostics changed the stored trace")
	}
}

func TestDiagnosticsCorrelateRequiresExactIdentity(t *testing.T) {
	command := newDiagnosticsCommand(dependencies{})
	command.SetArgs([]string{"--trace-root", t.TempDir(), "correlate"})
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "exact identity selector") {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestResolveThreadTraceRootIsThreadOwnedOrExplicitAbsolute(t *testing.T) {
	home := t.TempDir()
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("EvalSymlinks(home): %v", err)
	}
	threadID := "123e4567-e89b-12d3-a456-426614174000"
	threadRoot := filepath.Join(resolvedHome, "coding", "threads", threadID)
	tests := []struct {
		name     string
		stateDir string
		want     string
		wantErr  bool
	}{
		{name: "default", want: filepath.Join(threadRoot, "diagnostics", "traces")},
		{
			name:     "relative",
			stateDir: filepath.Join("private", "diagnostics"),
			want:     filepath.Join(threadRoot, "private", "diagnostics", "traces"),
		},
		{name: "absolute", stateDir: filepath.Join(home, "external"), want: filepath.Join(home, "external", "traces")},
		{name: "escape", stateDir: filepath.Join("..", "escape"), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveThreadTraceRoot(home, threadID, test.stateDir)
			if test.wantErr {
				if err == nil {
					t.Fatalf("resolveThreadTraceRoot() = %q, want error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("resolveThreadTraceRoot() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestDiagnosticsThreadSelectionIsolatesSharedAbsoluteStore(t *testing.T) {
	home := t.TempDir()
	sharedState := filepath.Join(t.TempDir(), "shared-diagnostics")
	root := filepath.Join(sharedState, "traces")
	selectedThread := "123e4567-e89b-12d3-a456-426614174000"
	otherThread := "123e4567-e89b-12d3-a456-426614174001"
	created := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	cfg := config.DefaultConfig()
	cfg.Diagnostics.TraceCapture.Enabled = true
	cfg.Diagnostics.TraceCapture.StateDir = sharedState
	selected := diagnostictrace.Trace{
		SchemaVersion: diagnostictrace.SchemaVersionV1,
		TraceID:       "trace-selected-thread",
		CreatedAt:     created,
		Policy:        diagnostictrace.CapturePolicy{ContentMode: diagnostictrace.ContentMetadataOnly},
		Limits:        diagnostictrace.DefaultLimits(),
		Metadata: diagnostictrace.Metadata{
			RootTurnID: "turn-selected", SessionHash: diagnosticSessionHash(cfg, selectedThread),
		},
		Records: []diagnostictrace.Record{{
			Sequence: 1, Kind: diagnostictrace.RecordTurnEnd,
			Origin: diagnostictrace.Origin{Kind: "runtime_event", ID: "evt-selected"},
			Data:   json.RawMessage(`{"status":"completed"}`),
		}},
		Outcome: &diagnostictrace.Outcome{Status: "completed"},
	}
	other := selected
	other.TraceID = "trace-other-thread"
	other.CreatedAt = created.Add(time.Minute)
	other.Metadata.RootTurnID = "turn-other"
	other.Metadata.SessionHash = diagnosticSessionHash(cfg, otherThread)
	other.Records = append([]diagnostictrace.Record(nil), selected.Records...)
	other.Records[0].Origin.ID = "evt-other"
	for _, trace := range []diagnostictrace.Trace{selected, other} {
		finalized, err := diagnostictrace.Finalize(trace)
		if err != nil {
			t.Fatalf("Finalize(%s): %v", trace.TraceID, err)
		}
		if _, err := (diagnostictrace.Store{Root: root}).Save(finalized); err != nil {
			t.Fatalf("Save(%s): %v", trace.TraceID, err)
		}
	}

	command := newDiagnosticsCommand(dependencies{
		home:       func() string { return home },
		configPath: func() string { return filepath.Join(home, "config.json") },
		loadConfig: func(string) (*config.Config, error) { return cfg, nil },
		now:        func() time.Time { return created.Add(2 * time.Minute) },
	})
	output := &bytes.Buffer{}
	command.SetOut(output)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"--thread", selectedThread, "--json", "list"})
	if err := command.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(output.String(), selected.TraceID) || strings.Contains(output.String(), other.TraceID) {
		t.Fatalf("thread-isolated output = %s", output)
	}
}

func TestResolveStoresLabelsSharedProfileRootWithoutFalseAlias(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Diagnostics.TraceCapture.StateDir = filepath.Join(t.TempDir(), "shared-diagnostics")
	cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{ID: "family"})
	stores, err := resolveStores(dependencies{
		configPath: func() string { return filepath.Join(t.TempDir(), "config.json") },
		loadConfig: func(string) (*config.Config, error) { return cfg, nil },
	}, commonOptions{})
	if err != nil {
		t.Fatalf("resolveStores: %v", err)
	}
	if len(stores) != 1 || stores[0].Kind != "profile_shared" || stores[0].Alias != "" ||
		stores[0].ID != "profile-shared-1" {
		t.Fatalf("shared stores = %#v", stores)
	}
}

func executeDiagnostics(t *testing.T, args []string) string {
	t.Helper()
	command := newDiagnosticsCommand(dependencies{now: func() time.Time {
		return time.Date(2026, 10, 11, 13, 0, 0, 0, time.UTC)
	}})
	output := &bytes.Buffer{}
	command.SetOut(output)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs(args)
	if err := command.Execute(); err != nil {
		t.Fatalf("Execute(%v): %v", args, err)
	}
	return output.String()
}
