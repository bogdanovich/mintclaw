package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/session"
	workspaceutil "github.com/bogdanovich/mintclaw/pkg/workspace"
)

const cutoverManifestVersion = 1

const (
	cohortRetained = "retained"
	cohortArchived = "archived"
	fileMetadata   = "metadata"
	fileHistory    = "history"
)

var oldToolCallFields = map[string]struct{}{
	"id":            {},
	"type":          {},
	"function":      {},
	"extra_content": {},
}

var oldFunctionFields = map[string]struct{}{
	"name":              {},
	"arguments":         {},
	"thought_signature": {},
}

var oldExtraContentFields = map[string]struct{}{
	"google":                    {},
	"tool_feedback_explanation": {},
}

var oldGoogleFields = map[string]struct{}{
	"thought_signature": {},
}

type convertOptions struct {
	SourceRoot  string
	OutputRoot  string
	ConfigPaths []string
}

type fileDigest struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type manifestFile struct {
	Path         string `json:"path"`
	Cohort       string `json:"cohort"`
	Kind         string `json:"kind"`
	SourceBytes  int64  `json:"source_bytes"`
	OutputBytes  int64  `json:"output_bytes"`
	SourceSHA256 string `json:"source_sha256"`
	OutputSHA256 string `json:"output_sha256"`
}

type cohortTotals struct {
	Metadata      int   `json:"metadata"`
	Histories     int   `json:"histories"`
	MetadataBytes int64 `json:"metadata_bytes"`
	HistoryBytes  int64 `json:"history_bytes"`
}

type cutoverTotals struct {
	Retained             cohortTotals `json:"retained"`
	Archived             cohortTotals `json:"archived"`
	AliasesRemoved       int          `json:"aliases_removed"`
	ToolCallsFlattened   int          `json:"tool_calls_flattened"`
	GoogleCasesFlattened int          `json:"google_cases_flattened"`
	MessagesValidated    int          `json:"messages_validated"`
}

type archivedHistoryCountMismatch struct {
	Path           string `json:"path"`
	MetadataCount  int    `json:"metadata_count"`
	FramedMessages int    `json:"framed_messages"`
}

type cutoverManifest struct {
	Version                        int                            `json:"version"`
	Configs                        []fileDigest                   `json:"configs"`
	SessionDirs                    []string                       `json:"session_dirs"`
	Totals                         cutoverTotals                  `json:"totals"`
	ArchivedHistoryCountMismatches []archivedHistoryCountMismatch `json:"archived_history_count_mismatches,omitempty"`
	Files                          []manifestFile                 `json:"files"`
}

type metadataInspection struct {
	meta           memory.SessionMeta
	retained       bool
	aliasesRemoved bool
	output         []byte
}

type historyConversion struct {
	output      []byte
	messages    int
	toolCalls   int
	googleCases int
}

func convertSessions(options convertOptions) (cutoverManifest, error) {
	prepared, prepareErr := prepareOptions(options)
	if prepareErr != nil {
		return cutoverManifest{}, prepareErr
	}
	manifest, sessionDirs, inventoryErr := inventoryConfigs(prepared)
	if inventoryErr != nil {
		return cutoverManifest{}, inventoryErr
	}
	if overlapErr := rejectOutputOverlap(prepared.OutputRoot, sessionDirs); overlapErr != nil {
		return cutoverManifest{}, overlapErr
	}

	parent := filepath.Dir(prepared.OutputRoot)
	stage, stageErr := os.MkdirTemp(parent, "."+filepath.Base(prepared.OutputRoot)+".partial-")
	if stageErr != nil {
		return cutoverManifest{}, fmt.Errorf("create output staging directory: %w", stageErr)
	}
	if populateErr := populateStage(prepared, stage, sessionDirs, &manifest); populateErr != nil {
		return cutoverManifest{}, removePartialStage(stage, populateErr)
	}
	if renameErr := os.Rename(stage, prepared.OutputRoot); renameErr != nil {
		return cutoverManifest{}, removePartialStage(stage, fmt.Errorf("publish output: %w", renameErr))
	}
	return manifest, nil
}

func populateStage(
	options convertOptions,
	stage string,
	sessionDirs []string,
	manifest *cutoverManifest,
) error {
	for _, sessionDir := range sessionDirs {
		if convertErr := convertSessionDir(options.SourceRoot, stage, sessionDir, manifest); convertErr != nil {
			return convertErr
		}
	}
	if validateErr := validateManifest(options.SourceRoot, stage, sessionDirs, *manifest); validateErr != nil {
		return validateErr
	}
	encoded, encodeErr := json.MarshalIndent(*manifest, "", "  ")
	if encodeErr != nil {
		return fmt.Errorf("encode manifest: %w", encodeErr)
	}
	encoded = append(encoded, '\n')
	if writeErr := writeNewFile(filepath.Join(stage, "manifest.json"), encoded); writeErr != nil {
		return fmt.Errorf("write manifest: %w", writeErr)
	}
	return nil
}

func removePartialStage(stage string, cause error) error {
	if cleanupErr := os.RemoveAll(stage); cleanupErr != nil {
		return errors.Join(cause, fmt.Errorf("remove partial output %q: %w", stage, cleanupErr))
	}
	return cause
}

func prepareOptions(options convertOptions) (convertOptions, error) {
	if strings.TrimSpace(options.SourceRoot) == "" {
		return convertOptions{}, errors.New("--source-root is required")
	}
	if strings.TrimSpace(options.OutputRoot) == "" {
		return convertOptions{}, errors.New("--output is required")
	}
	if len(options.ConfigPaths) == 0 {
		return convertOptions{}, errors.New("at least one --config is required")
	}
	if !filepath.IsAbs(options.SourceRoot) {
		return convertOptions{}, errors.New("--source-root must be absolute")
	}
	if !filepath.IsAbs(options.OutputRoot) {
		return convertOptions{}, errors.New("--output must be absolute")
	}
	root, rootErr := exactAbsolutePath(options.SourceRoot)
	if rootErr != nil {
		return convertOptions{}, fmt.Errorf("source root: %w", rootErr)
	}
	info, statErr := os.Stat(root)
	if statErr != nil {
		return convertOptions{}, fmt.Errorf("stat source root: %w", statErr)
	}
	if !info.IsDir() {
		return convertOptions{}, errors.New("source root is not a directory")
	}
	output, outputErr := filepath.Abs(options.OutputRoot)
	if outputErr != nil {
		return convertOptions{}, fmt.Errorf("resolve output: %w", outputErr)
	}
	output = filepath.Clean(output)
	_, outputStatErr := os.Lstat(output)
	if outputStatErr == nil {
		return convertOptions{}, errors.New("output already exists")
	}
	if !errors.Is(outputStatErr, os.ErrNotExist) {
		return convertOptions{}, fmt.Errorf("inspect output: %w", outputStatErr)
	}
	parent, parentErr := exactAbsolutePath(filepath.Dir(output))
	if parentErr != nil {
		return convertOptions{}, fmt.Errorf("output parent: %w", parentErr)
	}
	output = filepath.Join(parent, filepath.Base(output))

	configs := append([]string(nil), options.ConfigPaths...)
	for index, path := range configs {
		if !filepath.IsAbs(path) {
			return convertOptions{}, fmt.Errorf("config %q path must be absolute", path)
		}
		resolved, resolveErr := exactAbsolutePath(path)
		if resolveErr != nil {
			return convertOptions{}, fmt.Errorf("config %q: %w", path, resolveErr)
		}
		if _, relErr := relativeWithin(root, resolved); relErr != nil {
			return convertOptions{}, fmt.Errorf("config %q: %w", path, relErr)
		}
		configs[index] = resolved
	}
	slices.Sort(configs)
	for index := 1; index < len(configs); index++ {
		if configs[index] == configs[index-1] {
			return convertOptions{}, fmt.Errorf("duplicate config %q", configs[index])
		}
	}
	return convertOptions{SourceRoot: root, OutputRoot: output, ConfigPaths: configs}, nil
}

func inventoryConfigs(options convertOptions) (cutoverManifest, []string, error) {
	manifest := cutoverManifest{Version: cutoverManifestVersion}
	workspaces := make(map[string]struct{})
	for _, configPath := range options.ConfigPaths {
		data, readErr := readRegularFile(configPath)
		if readErr != nil {
			return cutoverManifest{}, nil, fmt.Errorf("read config %q: %w", configPath, readErr)
		}
		var cfg config.Config
		if decodeErr := config.DecodeCurrentConfig(data, &cfg); decodeErr != nil {
			return cutoverManifest{}, nil, fmt.Errorf("decode config %q: %w", configPath, decodeErr)
		}
		relConfig, relativeErr := relativeWithin(options.SourceRoot, configPath)
		if relativeErr != nil {
			return cutoverManifest{}, nil, fmt.Errorf("config %q: %w", configPath, relativeErr)
		}
		manifest.Configs = append(manifest.Configs, digestRecord(relConfig, data))
		for index, agent := range cfg.Agents.List {
			workspace := workspaceutil.ResolveAgentPath(&agent, &cfg.Agents.Defaults)
			if !filepath.IsAbs(workspace) {
				return cutoverManifest{}, nil, fmt.Errorf(
					"config %q agent %d workspace %q is not absolute",
					configPath,
					index,
					workspace,
				)
			}
			resolved, resolveErr := exactAbsolutePath(workspace)
			if resolveErr != nil {
				return cutoverManifest{}, nil, fmt.Errorf(
					"config %q agent %d workspace: %w",
					configPath,
					index,
					resolveErr,
				)
			}
			if _, relativeErr := relativeWithin(options.SourceRoot, resolved); relativeErr != nil {
				return cutoverManifest{}, nil, fmt.Errorf(
					"config %q agent %d workspace: %w",
					configPath,
					index,
					relativeErr,
				)
			}
			workspaces[resolved] = struct{}{}
		}
	}

	sessionDirs := make([]string, 0, len(workspaces))
	for workspace := range workspaces {
		sessionDir, resolveErr := exactAbsolutePath(filepath.Join(workspace, "sessions"))
		if resolveErr != nil {
			return cutoverManifest{}, nil, fmt.Errorf("workspace %q sessions: %w", workspace, resolveErr)
		}
		info, statErr := os.Stat(sessionDir)
		if statErr != nil {
			return cutoverManifest{}, nil, fmt.Errorf("stat session directory %q: %w", sessionDir, statErr)
		}
		if !info.IsDir() {
			return cutoverManifest{}, nil, fmt.Errorf("session path %q is not a directory", sessionDir)
		}
		sessionDirs = append(sessionDirs, sessionDir)
	}
	slices.Sort(sessionDirs)
	for _, sessionDir := range sessionDirs {
		rel, relativeErr := relativeWithin(options.SourceRoot, sessionDir)
		if relativeErr != nil {
			return cutoverManifest{}, nil, relativeErr
		}
		manifest.SessionDirs = append(manifest.SessionDirs, filepath.ToSlash(rel))
	}
	return manifest, sessionDirs, nil
}

func convertSessionDir(sourceRoot, stageRoot, sessionDir string, manifest *cutoverManifest) error {
	entries, readDirErr := os.ReadDir(sessionDir)
	if readDirErr != nil {
		return fmt.Errorf("read session directory %q: %w", sessionDir, readDirErr)
	}
	metadata := make(map[string]string)
	histories := make(map[string]string)
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, ".meta.json"):
			if !entry.Type().IsRegular() {
				return fmt.Errorf("metadata entry %q is not a regular file", filepath.Join(sessionDir, name))
			}
			metadata[strings.TrimSuffix(name, ".meta.json")] = filepath.Join(sessionDir, name)
		case strings.HasSuffix(name, ".jsonl"):
			if !entry.Type().IsRegular() {
				return fmt.Errorf("history entry %q is not a regular file", filepath.Join(sessionDir, name))
			}
			histories[strings.TrimSuffix(name, ".jsonl")] = filepath.Join(sessionDir, name)
		}
	}
	for stem := range histories {
		if _, ok := metadata[stem]; !ok {
			return fmt.Errorf("orphan history %q has no metadata", histories[stem])
		}
	}

	stems := make([]string, 0, len(metadata))
	for stem := range metadata {
		stems = append(stems, stem)
	}
	slices.Sort(stems)
	for _, stem := range stems {
		metaPath := metadata[stem]
		metaData, readMetaErr := readRegularFile(metaPath)
		if readMetaErr != nil {
			return fmt.Errorf("read metadata %q: %w", metaPath, readMetaErr)
		}
		inspection, inspectErr := inspectMetadata(metaData, filepath.Base(metaPath))
		if inspectErr != nil {
			return fmt.Errorf("inspect metadata %q: %w", metaPath, inspectErr)
		}
		cohort := cohortArchived
		metaOutput := metaData
		if inspection.retained {
			cohort = cohortRetained
			metaOutput = inspection.output
			manifest.Totals.Retained.Metadata++
			manifest.Totals.Retained.MetadataBytes += int64(len(metaData))
			if inspection.aliasesRemoved {
				manifest.Totals.AliasesRemoved++
			}
		} else {
			manifest.Totals.Archived.Metadata++
			manifest.Totals.Archived.MetadataBytes += int64(len(metaData))
		}
		if emitErr := emitFile(
			sourceRoot,
			stageRoot,
			metaPath,
			cohort,
			fileMetadata,
			metaData,
			metaOutput,
			manifest,
		); emitErr != nil {
			return emitErr
		}

		historyPath, hasHistory := histories[stem]
		if !hasHistory {
			if inspection.meta.Count != 0 {
				return fmt.Errorf(
					"%s metadata %q counts %d messages but has no history",
					cohort,
					metaPath,
					inspection.meta.Count,
				)
			}
			continue
		}
		historyData, readHistoryErr := readRegularFile(historyPath)
		if readHistoryErr != nil {
			return fmt.Errorf("read history %q: %w", historyPath, readHistoryErr)
		}
		historyOutput := historyData
		var historyMessages int
		if inspection.retained {
			converted, convertErr := convertHistory(historyData)
			if convertErr != nil {
				return fmt.Errorf("convert history %q: %w", historyPath, convertErr)
			}
			historyOutput = converted.output
			historyMessages = converted.messages
			manifest.Totals.Retained.Histories++
			manifest.Totals.Retained.HistoryBytes += int64(len(historyData))
			manifest.Totals.ToolCallsFlattened += converted.toolCalls
			manifest.Totals.GoogleCasesFlattened += converted.googleCases
			manifest.Totals.MessagesValidated += converted.messages
		} else {
			framedMessages, framingErr := scanHistoryRecords(historyData, nil)
			if framingErr != nil {
				return fmt.Errorf("inspect archived history %q: %w", historyPath, framingErr)
			}
			historyMessages = framedMessages
			manifest.Totals.Archived.Histories++
			manifest.Totals.Archived.HistoryBytes += int64(len(historyData))
		}
		if historyMessages != inspection.meta.Count && inspection.retained {
			return fmt.Errorf(
				"%s history %q has %d messages; metadata records %d",
				cohort,
				historyPath,
				historyMessages,
				inspection.meta.Count,
			)
		}
		if historyMessages != inspection.meta.Count {
			rel, relativeErr := relativeWithin(sourceRoot, historyPath)
			if relativeErr != nil {
				return relativeErr
			}
			manifest.ArchivedHistoryCountMismatches = append(
				manifest.ArchivedHistoryCountMismatches,
				archivedHistoryCountMismatch{
					Path:           filepath.ToSlash(rel),
					MetadataCount:  inspection.meta.Count,
					FramedMessages: historyMessages,
				},
			)
		}
		if emitErr := emitFile(
			sourceRoot,
			stageRoot,
			historyPath,
			cohort,
			fileHistory,
			historyData,
			historyOutput,
			manifest,
		); emitErr != nil {
			return emitErr
		}
	}
	return nil
}

func inspectMetadata(data []byte, filename string) (metadataInspection, error) {
	object, decodeObjectErr := decodeJSONObject(data, "metadata")
	if decodeObjectErr != nil {
		return metadataInspection{}, decodeObjectErr
	}
	aliasesRemoved := false
	if aliases, ok := object["aliases"]; ok {
		var values []string
		if decodeAliasesErr := json.Unmarshal(aliases, &values); decodeAliasesErr != nil || values == nil {
			return metadataInspection{}, errors.New("metadata.aliases must be an array of strings")
		}
		delete(object, "aliases")
		aliasesRemoved = true
	}
	withoutAliases, encodeErr := json.Marshal(object)
	if encodeErr != nil {
		return metadataInspection{}, fmt.Errorf("encode metadata without aliases: %w", encodeErr)
	}
	meta, decodeMetaErr := memory.DecodeSessionMeta(withoutAliases)
	if decodeMetaErr != nil {
		return metadataInspection{}, fmt.Errorf("decode metadata without aliases: %w", decodeMetaErr)
	}
	if filename != sanitizeSessionKey(meta.Key)+".meta.json" {
		return metadataInspection{}, fmt.Errorf("metadata.key %q does not match its filename", meta.Key)
	}
	if meta.HistoryDirty || meta.HistoryHasPrevious || meta.HistoryPreviousCount != 0 ||
		meta.HistoryPreviousSkip != 0 || meta.HistoryTargetDigest != "" {
		return metadataInspection{}, errors.New("metadata contains unfinished history mutation state")
	}

	scopeVersion, hasScopeVersion, inspectScopeErr := inspectScopeVersion(meta.Scope)
	if inspectScopeErr != nil {
		return metadataInspection{}, inspectScopeErr
	}
	if hasScopeVersion && scopeVersion == session.ScopeVersion {
		if _, decodeScopeErr := session.DecodeCurrentSessionScope(meta.Scope); decodeScopeErr != nil {
			return metadataInspection{}, fmt.Errorf("decode current scope: %w", decodeScopeErr)
		}
	}
	retained := session.IsOpaqueSessionKey(meta.Key) && hasScopeVersion && scopeVersion == session.ScopeVersion
	if !retained {
		return metadataInspection{meta: meta}, nil
	}

	output := data
	if aliasesRemoved {
		output, encodeErr = json.MarshalIndent(meta, "", "  ")
		if encodeErr != nil {
			return metadataInspection{}, fmt.Errorf("encode current metadata: %w", encodeErr)
		}
		if _, validateErr := memory.DecodeSessionMeta(output); validateErr != nil {
			return metadataInspection{}, fmt.Errorf("validate encoded current metadata: %w", validateErr)
		}
	}
	return metadataInspection{
		meta:           meta,
		retained:       true,
		aliasesRemoved: aliasesRemoved,
		output:         output,
	}, nil
}

func inspectScopeVersion(raw json.RawMessage) (int, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, false, nil
	}
	object, decodeErr := decodeJSONObject(raw, "metadata.scope")
	if decodeErr != nil {
		return 0, false, decodeErr
	}
	versionRaw, ok := object["version"]
	if !ok {
		return 0, false, nil
	}
	var version int
	if decodeVersionErr := json.Unmarshal(versionRaw, &version); decodeVersionErr != nil {
		return 0, false, errors.New("metadata.scope.version must be an integer")
	}
	return version, true, nil
}

func convertHistory(data []byte) (historyConversion, error) {
	conversion := historyConversion{}
	messages, scanErr := scanHistoryRecords(data, func(_ int, line []byte) error {
		converted, toolCalls, googleCases, convertErr := convertMessage(line)
		if convertErr != nil {
			return convertErr
		}
		if len(converted) > memory.MaxJSONLRecordBytes {
			return fmt.Errorf("converted record exceeds %d-byte limit", memory.MaxJSONLRecordBytes)
		}
		conversion.output = append(conversion.output, converted...)
		conversion.output = append(conversion.output, '\n')
		conversion.toolCalls += toolCalls
		conversion.googleCases += googleCases
		return nil
	})
	if scanErr != nil {
		return historyConversion{}, scanErr
	}
	conversion.messages = messages
	return conversion, nil
}

func scanHistoryRecords(data []byte, visit func(index int, line []byte) error) (int, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), memory.MaxJSONLRecordBytes)
	messages := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			return 0, fmt.Errorf("message %d is blank", messages+1)
		}
		if visit != nil {
			if visitErr := visit(messages+1, line); visitErr != nil {
				return 0, fmt.Errorf("message %d: %w", messages+1, visitErr)
			}
		}
		messages++
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return 0, fmt.Errorf("scan JSONL: %w", scanErr)
	}
	if len(data) > 0 && !bytes.HasSuffix(data, []byte{'\n'}) {
		return 0, errors.New("history is missing its final newline")
	}
	return messages, nil
}

func convertMessage(line []byte) ([]byte, int, int, error) {
	object, decodeObjectErr := decodeJSONObject(line, "message")
	if decodeObjectErr != nil {
		return nil, 0, 0, decodeObjectErr
	}
	if _, decodeCurrentErr := memory.DecodeJSONLMessage(line); decodeCurrentErr == nil {
		return line, 0, 0, nil
	}
	toolCallsRaw, ok := object["tool_calls"]
	if !ok {
		return nil, 0, 0, errors.New("record is neither current nor an old nested-tool-call message")
	}
	var toolCalls []json.RawMessage
	if decodeCallsErr := json.Unmarshal(
		toolCallsRaw,
		&toolCalls,
	); decodeCallsErr != nil || toolCalls == nil ||
		len(toolCalls) == 0 {
		return nil, 0, 0, errors.New("old message tool_calls must be a non-empty array")
	}
	convertedCalls := make([]json.RawMessage, 0, len(toolCalls))
	googleCases := 0
	for index, raw := range toolCalls {
		converted, googleCase, convertErr := convertOldToolCall(raw)
		if convertErr != nil {
			return nil, 0, 0, fmt.Errorf("tool_calls[%d]: %w", index, convertErr)
		}
		convertedCalls = append(convertedCalls, converted)
		if googleCase {
			googleCases++
		}
	}
	encodedCalls, encodeCallsErr := json.Marshal(convertedCalls)
	if encodeCallsErr != nil {
		return nil, 0, 0, fmt.Errorf("encode tool calls: %w", encodeCallsErr)
	}
	object["tool_calls"] = encodedCalls
	converted, encodeMessageErr := json.Marshal(object)
	if encodeMessageErr != nil {
		return nil, 0, 0, fmt.Errorf("encode message: %w", encodeMessageErr)
	}
	if _, validateErr := memory.DecodeJSONLMessage(converted); validateErr != nil {
		return nil, 0, 0, fmt.Errorf("validate converted message: %w", validateErr)
	}
	return converted, len(toolCalls), googleCases, nil
}

func convertOldToolCall(data []byte) ([]byte, bool, error) {
	object, decodeObjectErr := decodeJSONObject(data, "tool call")
	if decodeObjectErr != nil {
		return nil, false, decodeObjectErr
	}
	if fieldsErr := rejectUnknownFields(object, oldToolCallFields, "old tool call"); fieldsErr != nil {
		return nil, false, fieldsErr
	}
	id, idErr := requiredJSONString(object, "id", "old tool call")
	if idErr != nil {
		return nil, false, idErr
	}
	if id == "" {
		return nil, false, errors.New("old tool call id is empty")
	}
	toolType, typeErr := requiredJSONString(object, "type", "old tool call")
	if typeErr != nil {
		return nil, false, typeErr
	}
	if toolType != "function" {
		return nil, false, fmt.Errorf("old tool call type %q is not the audited function shape", toolType)
	}
	functionRaw, ok := object["function"]
	if !ok {
		return nil, false, errors.New("old tool call is missing function")
	}
	function, decodeFunctionErr := decodeJSONObject(functionRaw, "old tool call.function")
	if decodeFunctionErr != nil {
		return nil, false, decodeFunctionErr
	}
	if fieldsErr := rejectUnknownFields(function, oldFunctionFields, "old tool call.function"); fieldsErr != nil {
		return nil, false, fieldsErr
	}
	name, nameErr := requiredJSONString(function, "name", "old tool call.function")
	if nameErr != nil {
		return nil, false, nameErr
	}
	if name == "" {
		return nil, false, errors.New("old tool call function name is empty")
	}
	argumentsText, argumentsErr := requiredJSONString(function, "arguments", "old tool call.function")
	if argumentsErr != nil {
		return nil, false, argumentsErr
	}
	arguments := []byte(argumentsText)
	if len(bytes.TrimSpace(arguments)) == 0 {
		return nil, false, errors.New("old tool call arguments are empty")
	}
	if _, decodeArgumentsErr := decodeJSONObject(
		arguments,
		"old tool call.function.arguments",
	); decodeArgumentsErr != nil {
		return nil, false, fmt.Errorf("decode arguments: %w", decodeArgumentsErr)
	}
	functionSignature, signatureErr := optionalJSONString(function, "thought_signature", "old tool call.function")
	if signatureErr != nil {
		return nil, false, signatureErr
	}

	feedback := ""
	googleSignature := ""
	googleCase := false
	if extraRaw, ok := object["extra_content"]; ok {
		extra, decodeExtraErr := decodeJSONObject(extraRaw, "old tool call.extra_content")
		if decodeExtraErr != nil {
			return nil, false, decodeExtraErr
		}
		if fieldsErr := rejectUnknownFields(
			extra,
			oldExtraContentFields,
			"old tool call.extra_content",
		); fieldsErr != nil {
			return nil, false, fieldsErr
		}
		decodedFeedback, feedbackErr := requiredJSONString(
			extra,
			"tool_feedback_explanation",
			"old tool call.extra_content",
		)
		if feedbackErr != nil {
			return nil, false, feedbackErr
		}
		feedback = decodedFeedback
		if feedback == "" {
			return nil, false, errors.New("old tool call feedback explanation is empty")
		}
		if googleRaw, ok := extra["google"]; ok {
			google, decodeGoogleErr := decodeJSONObject(googleRaw, "old tool call.extra_content.google")
			if decodeGoogleErr != nil {
				return nil, false, decodeGoogleErr
			}
			if fieldsErr := rejectUnknownFields(
				google,
				oldGoogleFields,
				"old tool call.extra_content.google",
			); fieldsErr != nil {
				return nil, false, fieldsErr
			}
			decodedSignature, googleSignatureErr := requiredJSONString(
				google,
				"thought_signature",
				"old tool call.extra_content.google",
			)
			if googleSignatureErr != nil {
				return nil, false, googleSignatureErr
			}
			googleSignature = decodedSignature
			if googleSignature == "" {
				return nil, false, errors.New("old Google thought signature is empty")
			}
			googleCase = true
		}
	}
	if googleCase && functionSignature == "" {
		return nil, false, errors.New("old Google signature has no matching function signature")
	}
	if googleCase && functionSignature != googleSignature {
		return nil, false, errors.New("old tool call has conflicting thought signatures")
	}
	if functionSignature != "" && !googleCase {
		return nil, false, errors.New("old function signature is outside the audited Google shape")
	}

	current := map[string]json.RawMessage{
		"id":        object["id"],
		"type":      object["type"],
		"name":      mustMarshalJSON(name),
		"arguments": append(json.RawMessage(nil), arguments...),
	}
	if functionSignature != "" {
		current["thought_signature"] = mustMarshalJSON(functionSignature)
	}
	if feedback != "" {
		current["tool_feedback_explanation"] = mustMarshalJSON(feedback)
	}
	encoded, encodeErr := json.Marshal(current)
	if encodeErr != nil {
		return nil, false, fmt.Errorf("encode current tool call: %w", encodeErr)
	}
	return encoded, googleCase, nil
}

func decodeJSONObject(data []byte, label string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, tokenErr := decoder.Token()
	if tokenErr != nil {
		return nil, fmt.Errorf("decode %s: %w", label, tokenErr)
	}
	if token != json.Delim('{') {
		return nil, fmt.Errorf("%s must be a JSON object", label)
	}
	object := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		if keyErr != nil {
			return nil, fmt.Errorf("decode %s field: %w", label, keyErr)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("%s contains a non-string field name", label)
		}
		if _, duplicate := object[key]; duplicate {
			return nil, fmt.Errorf("%s contains duplicate field %q", label, key)
		}
		var value json.RawMessage
		if decodeValueErr := decoder.Decode(&value); decodeValueErr != nil {
			return nil, fmt.Errorf("decode %s.%s: %w", label, key, decodeValueErr)
		}
		object[key] = value
	}
	if _, closeErr := decoder.Token(); closeErr != nil {
		return nil, fmt.Errorf("close %s object: %w", label, closeErr)
	}
	if trailingErr := decoder.Decode(new(any)); !errors.Is(trailingErr, io.EOF) {
		if trailingErr == nil {
			return nil, fmt.Errorf("%s contains a trailing JSON value", label)
		}
		return nil, fmt.Errorf("%s contains trailing data: %w", label, trailingErr)
	}
	return object, nil
}

func requiredJSONString(object map[string]json.RawMessage, field, label string) (string, error) {
	raw, ok := object[field]
	if !ok {
		return "", fmt.Errorf("%s is missing %s", label, field)
	}
	return decodeJSONString(raw, label+"."+field)
}

func optionalJSONString(object map[string]json.RawMessage, field, label string) (string, error) {
	raw, ok := object[field]
	if !ok {
		return "", nil
	}
	return decodeJSONString(raw, label+"."+field)
}

func decodeJSONString(raw json.RawMessage, label string) (string, error) {
	var value string
	if decodeErr := json.Unmarshal(raw, &value); decodeErr != nil {
		return "", fmt.Errorf("%s must be a string", label)
	}
	return value, nil
}

func rejectUnknownFields(object map[string]json.RawMessage, allowed map[string]struct{}, label string) error {
	for field := range object {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("%s contains unknown field %q", label, field)
		}
	}
	return nil
}

func emitFile(
	sourceRoot string,
	stageRoot string,
	sourcePath string,
	cohort string,
	kind string,
	source []byte,
	output []byte,
	manifest *cutoverManifest,
) error {
	rel, relativeErr := relativeWithin(sourceRoot, sourcePath)
	if relativeErr != nil {
		return relativeErr
	}
	destination := filepath.Join(stageRoot, cohort, rel)
	if writeErr := writeNewFile(destination, output); writeErr != nil {
		return fmt.Errorf("write %s output %q: %w", cohort, destination, writeErr)
	}
	manifest.Files = append(manifest.Files, manifestFile{
		Path:         filepath.ToSlash(rel),
		Cohort:       cohort,
		Kind:         kind,
		SourceBytes:  int64(len(source)),
		OutputBytes:  int64(len(output)),
		SourceSHA256: digestHex(source),
		OutputSHA256: digestHex(output),
	})
	return nil
}

func validateManifest(sourceRoot, stageRoot string, sessionDirs []string, manifest cutoverManifest) error {
	for _, configFile := range manifest.Configs {
		data, readConfigErr := readRegularFile(filepath.Join(sourceRoot, filepath.FromSlash(configFile.Path)))
		if readConfigErr != nil {
			return fmt.Errorf("re-read config %q: %w", configFile.Path, readConfigErr)
		}
		if int64(len(data)) != configFile.Bytes || digestHex(data) != configFile.SHA256 {
			return fmt.Errorf("config %q changed while the copy was prepared", configFile.Path)
		}
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	archivedHistories := make(map[string]struct{}, manifest.Totals.Archived.Histories)
	for _, file := range manifest.Files {
		identity := file.Cohort + "/" + file.Path
		if _, duplicate := seen[file.Path]; duplicate {
			return fmt.Errorf("manifest contains duplicate source path %q", file.Path)
		}
		seen[file.Path] = struct{}{}
		source, readSourceErr := readRegularFile(filepath.Join(sourceRoot, filepath.FromSlash(file.Path)))
		if readSourceErr != nil {
			return fmt.Errorf("re-read source %q: %w", file.Path, readSourceErr)
		}
		if int64(len(source)) != file.SourceBytes || digestHex(source) != file.SourceSHA256 {
			return fmt.Errorf("source %q changed while the copy was prepared", file.Path)
		}
		data, readOutputErr := readRegularFile(filepath.Join(stageRoot, filepath.FromSlash(identity)))
		if readOutputErr != nil {
			return fmt.Errorf("validate output %q: %w", identity, readOutputErr)
		}
		if int64(len(data)) != file.OutputBytes || digestHex(data) != file.OutputSHA256 {
			return fmt.Errorf("output %q does not match its manifest digest", identity)
		}
		if file.Cohort == cohortArchived && file.SourceSHA256 != file.OutputSHA256 {
			return fmt.Errorf("archive output %q is not an exact copy", identity)
		}
		if file.Cohort == cohortArchived && file.Kind == fileHistory {
			archivedHistories[file.Path] = struct{}{}
		}
	}
	mismatchPaths := make(map[string]struct{}, len(manifest.ArchivedHistoryCountMismatches))
	for _, mismatch := range manifest.ArchivedHistoryCountMismatches {
		if mismatch.MetadataCount == mismatch.FramedMessages {
			return fmt.Errorf("archived count mismatch %q records equal counts", mismatch.Path)
		}
		if _, ok := archivedHistories[mismatch.Path]; !ok {
			return fmt.Errorf("archived count mismatch %q does not name an archived history", mismatch.Path)
		}
		if _, duplicate := mismatchPaths[mismatch.Path]; duplicate {
			return fmt.Errorf("manifest contains duplicate archived count mismatch %q", mismatch.Path)
		}
		mismatchPaths[mismatch.Path] = struct{}{}
	}
	metadataTotal := manifest.Totals.Retained.Metadata + manifest.Totals.Archived.Metadata
	historyTotal := manifest.Totals.Retained.Histories + manifest.Totals.Archived.Histories
	if len(manifest.Files) != metadataTotal+historyTotal {
		return errors.New("manifest totals do not cover every emitted session file")
	}
	for _, sessionDir := range sessionDirs {
		entries, readDirErr := os.ReadDir(sessionDir)
		if readDirErr != nil {
			return fmt.Errorf("re-read session directory %q: %w", sessionDir, readDirErr)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".meta.json") && !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			path := filepath.Join(sessionDir, entry.Name())
			rel, relativeErr := relativeWithin(sourceRoot, path)
			if relativeErr != nil {
				return relativeErr
			}
			if _, ok := seen[filepath.ToSlash(rel)]; !ok {
				return fmt.Errorf("session file %q appeared while the copy was prepared", path)
			}
		}
	}
	return nil
}

func rejectOutputOverlap(output string, sessionDirs []string) error {
	for _, sessionDir := range sessionDirs {
		workspace := filepath.Dir(sessionDir)
		if pathsOverlap(output, workspace) {
			return fmt.Errorf("output %q overlaps configured workspace %q", output, workspace)
		}
	}
	return nil
}

func pathsOverlap(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	return first == second || strings.HasPrefix(first, second+string(filepath.Separator)) ||
		strings.HasPrefix(second, first+string(filepath.Separator))
}

func exactAbsolutePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path is empty")
	}
	abs, absoluteErr := filepath.Abs(path)
	if absoluteErr != nil {
		return "", absoluteErr
	}
	abs = filepath.Clean(abs)
	resolved, resolveErr := filepath.EvalSymlinks(abs)
	if resolveErr != nil {
		return "", resolveErr
	}
	if resolved != abs {
		return "", fmt.Errorf("path contains a symbolic-link component: %q resolves to %q", abs, resolved)
	}
	return abs, nil
}

func relativeWithin(root, path string) (string, error) {
	rel, relativeErr := filepath.Rel(root, path)
	if relativeErr != nil {
		return "", relativeErr
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q is not a child of source root %q", path, root)
	}
	return rel, nil
}

func readRegularFile(path string) ([]byte, error) {
	info, statErr := os.Lstat(path)
	if statErr != nil {
		return nil, statErr
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return os.ReadFile(path)
}

func writeNewFile(path string, data []byte) error {
	if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o700); mkdirErr != nil {
		return mkdirErr
	}
	file, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if openErr != nil {
		return openErr
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	return writeErr
}

func digestRecord(path string, data []byte) fileDigest {
	return fileDigest{Path: filepath.ToSlash(path), Bytes: int64(len(data)), SHA256: digestHex(data)}
}

func digestHex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func sanitizeSessionKey(key string) string {
	key = strings.ReplaceAll(key, ":", "_")
	key = strings.ReplaceAll(key, "/", "_")
	return strings.ReplaceAll(key, "\\", "_")
}

func mustMarshalJSON(value string) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func manifestPath(outputRoot string) string {
	resolved, err := filepath.Abs(outputRoot)
	if err != nil {
		return filepath.Join(outputRoot, "manifest.json")
	}
	return filepath.Join(resolved, "manifest.json")
}
