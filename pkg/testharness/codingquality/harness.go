// Package codingquality exercises MintClaw's real coding tools through the
// provider-neutral tool-call shapes used by deterministic quality scenarios.
package codingquality

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	fstools "github.com/bogdanovich/mintclaw/pkg/tools/fs"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const SchemaVersion = "mintclaw.coding-quality.v1"

type ToolCallFamily string

type FixtureScale string

const (
	ObjectArguments ToolCallFamily = "object_arguments"
	FunctionJSON    ToolCallFamily = "function_json"
	SmallFixture    FixtureScale   = "small"
	LargeFixture    FixtureScale   = "large"
)

type Report struct {
	SchemaVersion            string         `json:"schema_version"`
	Family                   ToolCallFamily `json:"family"`
	Fixture                  FixtureScale   `json:"fixture"`
	FixtureFiles             int            `json:"fixture_files"`
	FirstAttemptEditCorrect  bool           `json:"first_attempt_edit_correct"`
	PatchWriteAuditVerified  bool           `json:"patch_write_audit_verified"`
	StalePatchRejected       bool           `json:"stale_patch_rejected"`
	SearchExpectedFiles      int            `json:"search_expected_files"`
	SearchUnexpectedFiles    int            `json:"search_unexpected_files"`
	SearchExactFiles         bool           `json:"search_exact_files"`
	SearchOutputBytes        int            `json:"search_output_bytes"`
	SearchEstimatedTokens    int            `json:"search_estimated_tokens"`
	LongReadBounded          bool           `json:"long_read_bounded"`
	UnicodeRoundTrip         bool           `json:"unicode_round_trip"`
	AwkwardPathRoundTrip     bool           `json:"awkward_path_round_trip"`
	IgnoredGeneratedExcluded bool           `json:"ignored_generated_excluded"`
	BinaryContentExcluded    bool           `json:"binary_content_excluded"`
	RenameVisible            bool           `json:"rename_visible"`
	DeletedReadActionable    bool           `json:"deleted_read_actionable"`
	CommandOutputBytes       int            `json:"command_output_bytes"`
	CommandEstimatedTokens   int            `json:"command_estimated_tokens"`
	CommandArtifactRetained  bool           `json:"command_artifact_retained"`
	CancellationClassified   bool           `json:"cancellation_classified"`
	RecoverySucceeded        bool           `json:"recovery_succeeded"`
}

type toolSet struct {
	items map[string]toolshared.Tool
	exec  *tools.ExecTool
}

// Evaluate builds a representative repository fixture beneath root and
// returns bounded, deterministic quality measurements for one tool-call family.
func Evaluate(
	ctx context.Context,
	root string,
	scratch string,
	family ToolCallFamily,
	scale FixtureScale,
) (Report, error) {
	fixtureFiles, err := buildFixture(root, scale)
	if err != nil {
		return Report{}, err
	}
	set, err := newToolSet(root, scratch)
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = set.exec.Close() }()

	report := Report{SchemaVersion: SchemaVersion, Family: family, Fixture: scale, FixtureFiles: fixtureFiles}
	execute := func(name string, args map[string]any) (*toolshared.ToolResult, error) {
		call, encodeErr := encodeCall(family, name, args)
		if encodeErr != nil {
			return nil, encodeErr
		}
		decodedName, decodedArgs, decodeErr := decodeCall(call)
		if decodeErr != nil {
			return nil, decodeErr
		}
		tool := set.items[decodedName]
		if tool == nil {
			return nil, fmt.Errorf("coding quality: tool %q is not registered", decodedName)
		}
		return tool.Execute(toolshared.WithToolContext(ctx, "coding", "quality-fixture"), decodedArgs), nil
	}

	patchResult, err := execute("apply_patch", map[string]any{"input": firstAttemptPatch})
	if err != nil {
		return Report{}, err
	}
	edited, readErr := os.ReadFile(filepath.Join(root, "edit.go"))
	report.FirstAttemptEditCorrect = !patchResult.IsError && readErr == nil && string(edited) == editedFileContent
	report.PatchWriteAuditVerified = len(patchResult.WriteAudit) == 1 &&
		patchResult.WriteAudit[0].Target == "edit.go" && patchResult.WriteAudit[0].Action == "update" &&
		patchResult.WriteAudit[0].Tool == "apply_patch" && patchResult.WriteAudit[0].Success

	if _, readToolErr := execute("read_file", map[string]any{"path": "stale.txt"}); readToolErr != nil {
		return Report{}, readToolErr
	}
	if writeErr := os.WriteFile(
		filepath.Join(root, "stale.txt"),
		[]byte("state=external\n"),
		0o600,
	); writeErr != nil {
		return Report{}, writeErr
	}
	staleResult, err := execute("apply_patch", map[string]any{"input": stalePatch})
	if err != nil {
		return Report{}, err
	}
	staleContent, readErr := os.ReadFile(filepath.Join(root, "stale.txt"))
	report.StalePatchRejected = staleResult.IsError && readErr == nil && string(staleContent) == "state=external\n"

	searchResult, err := execute("search_files", map[string]any{
		"pattern": "QUALITY_NEEDLE", "path": ".", "output_mode": "files_only",
	})
	if err != nil {
		return Report{}, err
	}
	normalizedSearch := normalizeOutput(searchResult.ForLLM, root, scratch)
	searchPaths, parseErr := parseFilesOnlyPaths(normalizedSearch)
	if parseErr != nil {
		return Report{}, parseErr
	}
	report.SearchExpectedFiles, report.SearchUnexpectedFiles = classifySearchPaths(
		searchPaths,
		[]string{"src/main.go", "unicode.txt"},
	)
	report.SearchExactFiles = equalStrings(searchPaths, []string{"src/main.go", "unicode.txt"})
	report.SearchOutputBytes = len(normalizedSearch)
	report.SearchEstimatedTokens = estimateTokens(normalizedSearch)
	report.IgnoredGeneratedExcluded = !strings.Contains(normalizedSearch, "generated/noisy.go")
	report.BinaryContentExcluded = !strings.Contains(normalizedSearch, "binary.dat")

	longRead, err := execute("read_file", map[string]any{"path": "long.txt", "length": 512})
	if err != nil {
		return Report{}, err
	}
	report.LongReadBounded = !longRead.IsError && len(longRead.ForLLM) < 1024 &&
		strings.Contains(longRead.ForLLM, "TRUNCATED")
	unicodeWrite, err := execute("write_file", map[string]any{
		"path": "written-unicode.txt", "content": "Привет, 世界 🦀\n",
	})
	if err != nil {
		return Report{}, err
	}
	writtenUnicode, readErr := os.ReadFile(filepath.Join(root, "written-unicode.txt"))
	report.UnicodeRoundTrip = !unicodeWrite.IsError && readErr == nil && string(writtenUnicode) == "Привет, 世界 🦀\n"
	awkwardRead, err := execute("read_file", map[string]any{"path": awkwardPath})
	if err != nil {
		return Report{}, err
	}
	awkwardWrite, err := execute("write_file", map[string]any{
		"path": awkwardPath, "content": awkwardUpdatedContent, "overwrite": true,
	})
	if err != nil {
		return Report{}, err
	}
	awkwardContent, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(awkwardPath)))
	report.AwkwardPathRoundTrip = !awkwardRead.IsError &&
		readFilePayload(awkwardRead.ForLLM) == awkwardInitialContent &&
		!awkwardWrite.IsError &&
		readErr == nil &&
		string(awkwardContent) == awkwardUpdatedContent

	if renameErr := os.Rename(
		filepath.Join(root, "rename-me.txt"),
		filepath.Join(root, "renamed.txt"),
	); renameErr != nil {
		return Report{}, renameErr
	}
	if removeErr := os.Remove(filepath.Join(root, "delete-me.txt")); removeErr != nil {
		return Report{}, removeErr
	}
	renameResult, err := execute("search_files", map[string]any{
		"pattern": "renamed.txt", "target": "files", "path": ".",
	})
	if err != nil {
		return Report{}, err
	}
	report.RenameVisible = !renameResult.IsError && strings.Contains(renameResult.ForLLM, "renamed.txt") &&
		!strings.Contains(renameResult.ForLLM, "rename-me.txt")
	deletedResult, err := execute("read_file", map[string]any{"path": "delete-me.txt"})
	if err != nil {
		return Report{}, err
	}
	report.DeletedReadActionable = deletedResult.IsError && strings.TrimSpace(deletedResult.ForLLM) != ""

	largeCommand := largeOutputCommand()
	commandResult, err := execute("exec", map[string]any{"action": "run", "command": largeCommand})
	if err != nil {
		return Report{}, err
	}
	normalizedCommand := normalizeOutput(commandResult.ContentForLLM(), root, scratch)
	report.CommandOutputBytes = len(normalizedCommand)
	report.CommandEstimatedTokens = estimateTokens(normalizedCommand)
	report.CommandArtifactRetained = commandArtifactIsComplete(commandResult)

	cancelCtx, cancel := context.WithCancel(ctx)
	started := filepath.Join(root, "command-started")
	cancelDone := make(chan *toolshared.ToolResult, 1)
	go func() {
		cancelDone <- set.exec.Execute(
			toolshared.WithToolContext(cancelCtx, "coding", "quality-fixture"),
			map[string]any{"action": "run", "command": cancellationCommand()},
		)
	}()
	if waitErr := waitForPath(ctx, started); waitErr != nil {
		cancel()
		return Report{}, waitErr
	}
	cancel()
	select {
	case canceled := <-cancelDone:
		report.CancellationClassified = canceled.IsError && strings.Contains(canceled.ForLLM, "interrupted")
	case <-time.After(5 * time.Second):
		return Report{}, fmt.Errorf("coding quality: canceled command did not settle")
	}
	recovery, recoveryErr := execute("exec", map[string]any{"action": "run", "command": recoveryCommand()})
	if recoveryErr != nil {
		return Report{}, recoveryErr
	}
	report.RecoverySucceeded = !recovery.IsError && strings.TrimSpace(recovery.ForLLM) == "recovered"

	return report, nil
}

func newToolSet(root, scratch string) (toolSet, error) {
	execTool, err := tools.NewCodingExecToolWithRuntimeConfig(root, scratch, config.DefaultConfig())
	if err != nil {
		return toolSet{}, err
	}
	items := []toolshared.Tool{
		fstools.NewReadFileBytesTool(root, false, fstools.MaxReadFileSize),
		fstools.NewSearchFilesTool(root, false, fstools.MaxReadFileSize),
		fstools.NewApplyPatchTool(root, false),
		fstools.NewWriteFileTool(root, false),
		execTool,
	}
	set := toolSet{items: make(map[string]toolshared.Tool, len(items)), exec: execTool}
	for _, tool := range items {
		set.items[tool.Name()] = tool
	}
	return set, nil
}

func encodeCall(family ToolCallFamily, name string, args map[string]any) (providers.ToolCall, error) {
	switch family {
	case ObjectArguments:
		return providers.ToolCall{Name: name, Arguments: cloneArgs(args)}, nil
	case FunctionJSON:
		encoded, err := json.Marshal(args)
		if err != nil {
			return providers.ToolCall{}, err
		}
		return providers.ToolCall{Function: &providers.FunctionCall{Name: name, Arguments: string(encoded)}}, nil
	default:
		return providers.ToolCall{}, fmt.Errorf("coding quality: unsupported tool-call family %q", family)
	}
}

func decodeCall(call providers.ToolCall) (string, map[string]any, error) {
	normalized := providers.NormalizeToolCall(call)
	if strings.TrimSpace(normalized.Name) == "" {
		return "", nil, fmt.Errorf("coding quality: function call name is required")
	}
	return normalized.Name, cloneArgs(normalized.Arguments), nil
}

func buildFixture(root string, scale FixtureScale) (int, error) {
	if scale != SmallFixture && scale != LargeFixture {
		return 0, fmt.Errorf("coding quality: unsupported fixture scale %q", scale)
	}
	files := map[string][]byte{
		".gitignore":         []byte("generated/\n"),
		"src/main.go":        []byte("package fixture\n\nconst marker = \"QUALITY_NEEDLE\"\n"),
		"edit.go":            []byte("package fixture\n\nfunc Add(left, right int) int { return left - right }\n"),
		"stale.txt":          []byte("state=old\n"),
		"unicode.txt":        []byte("Привет QUALITY_NEEDLE 世界 🦀\n"),
		"long.txt":           []byte(strings.Repeat("long-line-", 12000) + "\n"),
		"generated/noisy.go": []byte("package generated // QUALITY_NEEDLE\n"),
		"binary.dat":         append([]byte{0, 1, 2}, []byte("QUALITY_NEEDLE")...),
		awkwardPath:          []byte(awkwardInitialContent),
		"rename-me.txt":      []byte("rename fixture\n"),
		"delete-me.txt":      []byte("delete fixture\n"),
	}
	if scale == LargeFixture {
		for index := 0; index < 400; index++ {
			name := filepath.Join("src", "bulk", fmt.Sprintf("fixture_%03d.go", index))
			files[name] = []byte(fmt.Sprintf("package bulk\n\nconst fixture%d = %d\n", index, index))
		}
	}
	for name, data := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return 0, err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return 0, err
		}
	}
	return len(files), nil
}

func commandArtifactIsComplete(result *toolshared.ToolResult) bool {
	if result == nil || result.Deliverable == nil || len(result.Deliverable.Artifacts) != 1 ||
		!strings.Contains(result.ForLLM, "truncated") {
		return false
	}
	path := result.Deliverable.Artifacts[0].LocalPath
	data, err := os.ReadFile(path)
	return err == nil && len(data) > maxExpectedCommandContext &&
		strings.Contains(string(data), "QUALITY_HEAD") && strings.Contains(string(data), "QUALITY_TAIL")
}

func waitForPath(ctx context.Context, path string) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("coding quality: command did not start")
		case <-ticker.C:
		}
	}
}

func largeOutputCommand() string {
	if runtime.GOOS == "windows" {
		return `[Console]::Write('QUALITY_HEAD' + ('x' * 20000) + 'QUALITY_TAIL')`
	}
	return `printf 'QUALITY_HEAD'; yes x | head -c 20000; printf 'QUALITY_TAIL'`
}

func cancellationCommand() string {
	if runtime.GOOS == "windows" {
		return `Set-Content -NoNewline -Path 'command-started' -Value started; Start-Sleep -Seconds 30`
	}
	return `touch command-started; while :; do sleep 1; done`
}

func recoveryCommand() string {
	if runtime.GOOS == "windows" {
		return `[Console]::Write('recovered')`
	}
	return `printf recovered`
}

func normalizeOutput(value, root, scratch string) string {
	value = strings.ReplaceAll(value, filepath.Clean(root), "<root>")
	return strings.ReplaceAll(value, filepath.Clean(scratch), "<scratch>")
}

func estimateTokens(value string) int {
	return (utf8.RuneCountInString(value) + 3) / 4
}

func parseFilesOnlyPaths(output string) ([]string, error) {
	parts := strings.SplitN(output, "\n\n", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "Matched files:") {
		return nil, fmt.Errorf("coding quality: unexpected files-only search output %q", output)
	}
	paths := strings.Split(strings.TrimSpace(parts[1]), "\n")
	if len(paths) == 1 && paths[0] == "" {
		return nil, nil
	}
	return paths, nil
}

func readFilePayload(output string) string {
	parts := strings.SplitN(output, "\n\n", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

func classifySearchPaths(actual, expected []string) (int, int) {
	expectedSet := make(map[string]bool, len(expected))
	for _, path := range expected {
		expectedSet[path] = true
	}
	matched := 0
	unexpected := 0
	for _, path := range actual {
		if expectedSet[path] {
			matched++
		} else {
			unexpected++
		}
	}
	return matched, unexpected
}

func equalStrings(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range actual {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}

func cloneArgs(args map[string]any) map[string]any {
	cloned := make(map[string]any, len(args))
	for key, value := range args {
		cloned[key] = value
	}
	return cloned
}

const (
	maxExpectedCommandContext = 12000
	awkwardPath               = "space dir/данные 🦀.txt"
	awkwardInitialContent     = "awkward path: исходный 🦀\n"
	awkwardUpdatedContent     = "awkward path: updated 世界\n"
	editedFileContent         = "package fixture\n\nfunc Add(left, right int) int { return left + right }\n"
	firstAttemptPatch         = `*** Begin Patch
*** Update File: edit.go
@@
-func Add(left, right int) int { return left - right }
+func Add(left, right int) int { return left + right }
*** End Patch`
	stalePatch = `*** Begin Patch
*** Update File: stale.txt
@@
-state=old
+state=patched
*** End Patch`
)
