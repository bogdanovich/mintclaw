package diagnostics

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/config"
	diagnosticspkg "github.com/bogdanovich/mintclaw/pkg/diagnostics"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/workspace"
)

const (
	listSchemaV1      = "mintclaw.diagnostics.list.v1"
	showSchemaV1      = "mintclaw.diagnostics.show.v1"
	correlateSchemaV1 = "mintclaw.diagnostics.correlate.v1"
)

type dependencies struct {
	home       func() string
	configPath func() string
	loadConfig func(string) (*config.Config, error)
	now        func() time.Time
}

func defaultDependencies() dependencies {
	return dependencies{
		home:       config.GetHome,
		configPath: internal.GetConfigPath,
		loadConfig: internal.LoadConfigAt,
		now:        time.Now,
	}
}

type commonOptions struct {
	configPath     string
	traceRoots     []string
	traceIDs       []string
	threadID       string
	turnID         string
	taskID         string
	sessionHash    string
	modelAttemptID string
	toolCallID     string
	invocationID   string
	interactionID  string
	completionID   string
	deliveryID     string
	since          time.Duration
	after          string
	before         string
	limit          int
	jsonOutput     bool
}

type traceEnvelope struct {
	StoreID       string                         `json:"store_id"`
	SchemaVersion string                         `json:"schema_version"`
	TraceID       string                         `json:"trace_id"`
	CreatedAt     time.Time                      `json:"created_at"`
	Source        diagnostictrace.Source         `json:"source"`
	Policy        diagnostictrace.CapturePolicy  `json:"policy"`
	Limits        diagnostictrace.AppliedLimits  `json:"limits"`
	Metadata      diagnostictrace.Metadata       `json:"metadata,omitempty"`
	Outcome       *diagnostictrace.Outcome       `json:"outcome,omitempty"`
	Truncation    diagnostictrace.Truncation     `json:"truncation,omitempty"`
	Projection    diagnostictrace.SafeProjection `json:"projection"`
	RecordCount   int                            `json:"record_count"`
	RecordKinds   map[string]int                 `json:"record_kinds"`
}

type listReport struct {
	SchemaVersion  string                       `json:"schema_version"`
	GeneratedAt    time.Time                    `json:"generated_at"`
	Selector       diagnosticspkg.Selector      `json:"selector"`
	Stores         []diagnosticspkg.StoreHealth `json:"stores"`
	Problems       []diagnosticspkg.Problem     `json:"problems,omitempty"`
	Traces         []traceEnvelope              `json:"traces"`
	TotalMatches   int                          `json:"total_matches"`
	MatchTruncated bool                         `json:"match_truncated,omitempty"`
	ScanTruncated  bool                         `json:"scan_truncated,omitempty"`
}

type shownTrace struct {
	StoreID string                    `json:"store_id"`
	Trace   diagnostictrace.SafeTrace `json:"trace"`
}

type showReport struct {
	SchemaVersion string                       `json:"schema_version"`
	GeneratedAt   time.Time                    `json:"generated_at"`
	Selector      diagnosticspkg.Selector      `json:"selector"`
	Stores        []diagnosticspkg.StoreHealth `json:"stores"`
	Problems      []diagnosticspkg.Problem     `json:"problems,omitempty"`
	Traces        []shownTrace                 `json:"traces"`
	ScanTruncated bool                         `json:"scan_truncated,omitempty"`
}

type timelineRecord struct {
	At      time.Time                  `json:"at"`
	StoreID string                     `json:"store_id"`
	TraceID string                     `json:"trace_id"`
	Record  diagnostictrace.SafeRecord `json:"record"`
}

type correlateReport struct {
	SchemaVersion  string                       `json:"schema_version"`
	GeneratedAt    time.Time                    `json:"generated_at"`
	Selector       diagnosticspkg.Selector      `json:"selector"`
	Stores         []diagnosticspkg.StoreHealth `json:"stores"`
	Problems       []diagnosticspkg.Problem     `json:"problems,omitempty"`
	Traces         []traceEnvelope              `json:"traces"`
	Timeline       []timelineRecord             `json:"timeline"`
	TotalMatches   int                          `json:"total_matches"`
	MatchTruncated bool                         `json:"match_truncated,omitempty"`
	ScanTruncated  bool                         `json:"scan_truncated,omitempty"`
}

// NewDiagnosticsCommand creates the read-only passive-diagnostics command.
func NewDiagnosticsCommand() *cobra.Command {
	return newDiagnosticsCommand(defaultDependencies())
}

func newDiagnosticsCommand(deps dependencies) *cobra.Command {
	deps = completeDependencies(deps)
	options := &commonOptions{}
	command := &cobra.Command{
		Use:   "diagnostics",
		Short: "Inspect bounded passive diagnostic evidence",
		Long: `Inspect passive MintClaw traces without replaying runtime work.

Default output excludes prompts, responses, previews, source and diff content,
tool arguments/results, commands/output, environment values, credentials,
absolute paths, channel destinations, and unrelated trace content.`,
		Args: cobra.NoArgs,
	}
	bindCommonFlags(command, options)
	command.AddCommand(
		newListCommand(deps, options),
		newShowCommand(deps, options),
		newCorrelateCommand(deps, options),
		newExportCommand(deps, options),
	)
	return command
}

func bindCommonFlags(command *cobra.Command, options *commonOptions) {
	flags := command.PersistentFlags()
	flags.StringVar(&options.configPath, "config", "", "Path to config.json (default: active MintClaw config)")
	flags.StringArrayVar(
		&options.traceRoots,
		"trace-root",
		nil,
		"Explicit trace-store root; repeatable and mutually exclusive with --thread",
	)
	flags.StringArrayVar(&options.traceIDs, "trace", nil, "Match one exact trace ID; repeatable")
	flags.StringVar(&options.threadID, "thread", "", "Select one local coding thread")
	flags.StringVar(&options.turnID, "turn", "", "Match one exact turn ID")
	flags.StringVar(&options.taskID, "task", "", "Match one exact task ID")
	flags.StringVar(&options.sessionHash, "session-hash", "", "Match one exact SHA-256 session hash")
	flags.StringVar(&options.modelAttemptID, "model-attempt", "", "Match one exact model-attempt ID")
	flags.StringVar(&options.toolCallID, "tool-call", "", "Match one exact tool-call ID")
	flags.StringVar(&options.invocationID, "invocation", "", "Match one exact node invocation ID")
	flags.StringVar(&options.interactionID, "interaction", "", "Match one exact interaction ID")
	flags.StringVar(&options.completionID, "completion", "", "Match one exact completion ID")
	flags.StringVar(&options.deliveryID, "delivery", "", "Match one exact delivery ID")
	flags.DurationVar(&options.since, "since", 0, "Only traces created within this duration")
	flags.StringVar(&options.after, "after", "", "Only traces at or after this RFC3339 timestamp")
	flags.StringVar(&options.before, "before", "", "Only traces at or before this RFC3339 timestamp")
	flags.IntVar(&options.limit, "limit", diagnosticspkg.DefaultResultLimit, "Maximum matching traces to return")
	flags.BoolVar(&options.jsonOutput, "json", false, "Emit stable JSON")
}

func newListCommand(deps dependencies, options *commonOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List bounded trace envelopes and capture health",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := runQuery(deps, *options, nil, "")
			if err != nil {
				return err
			}
			report, err := makeListReport(result)
			if err != nil {
				return err
			}
			if options.jsonOutput {
				return writeJSON(command.OutOrStdout(), report)
			}
			renderList(command.OutOrStdout(), report)
			return nil
		},
	}
}

func newShowCommand(deps dependencies, options *commonOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "show <trace-id>",
		Short: "Show one metadata-only trace timeline",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if len(options.traceIDs) > 0 {
				return fmt.Errorf("diagnostics show: --trace cannot be combined with the trace argument")
			}
			result, err := runQuery(deps, *options, []string{args[0]}, "")
			if err != nil {
				return err
			}
			if result.TotalMatches == 0 {
				return fmt.Errorf("diagnostics show: trace not found")
			}
			report, err := makeShowReport(result)
			if err != nil {
				return err
			}
			if options.jsonOutput {
				return writeJSON(command.OutOrStdout(), report)
			}
			renderShow(command.OutOrStdout(), report)
			return nil
		},
	}
}

func newCorrelateCommand(deps dependencies, options *commonOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "correlate",
		Short: "Build a safe ordered timeline for exact identifiers",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := runQuery(deps, *options, nil, "correlate")
			if err != nil {
				return err
			}
			report, err := makeCorrelateReport(result)
			if err != nil {
				return err
			}
			if options.jsonOutput {
				return writeJSON(command.OutOrStdout(), report)
			}
			renderCorrelate(command.OutOrStdout(), report)
			return nil
		},
	}
}

func newExportCommand(deps dependencies, options *commonOptions) *cobra.Command {
	var output string
	var includeRedactedContent bool
	command := &cobra.Command{
		Use:   "export",
		Short: "Write a checksum-verifiable support directory",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := runQuery(deps, *options, nil, "export")
			if err != nil {
				return err
			}
			version, commit := config.RuntimeBuildIdentity()
			receipt, err := diagnosticspkg.WriteBundle(diagnosticspkg.BundleOptions{
				OutputDir: output, Query: result,
				Source:                 diagnosticspkg.DefaultBundleSource(version, commit),
				IncludeRedactedContent: includeRedactedContent,
			})
			if err != nil {
				return err
			}
			if options.jsonOutput {
				return writeJSON(command.OutOrStdout(), receipt)
			}
			fmt.Fprintf(
				command.OutOrStdout(),
				"Support bundle written: %s\nManifest: %s\nManifest SHA-256: %s\nFiles: %d  Traces: %d  Privacy: %s\n",
				receipt.OutputDir,
				receipt.ManifestPath,
				receipt.ManifestSHA256,
				receipt.Files,
				receipt.Traces,
				receipt.PrivacyMode,
			)
			return nil
		},
	}
	command.Flags().StringVar(&output, "output", "", "New output directory (required; existing paths are refused)")
	command.Flags().BoolVar(
		&includeRedactedContent,
		"include-redacted-content",
		false,
		"Include one exact trace's already-redacted private content; never upload automatically",
	)
	_ = command.MarkFlagRequired("output")
	return command
}

func runQuery(
	deps dependencies,
	options commonOptions,
	traceIDs []string,
	requireIdentityFor string,
) (diagnosticspkg.QueryResult, error) {
	if traceIDs == nil {
		traceIDs = append([]string(nil), options.traceIDs...)
	}
	selector, err := selectorFromOptions(options, traceIDs, deps.now())
	if err != nil {
		return diagnosticspkg.QueryResult{}, err
	}
	if requireIdentityFor != "" && !selector.HasIdentity() {
		return diagnosticspkg.QueryResult{}, fmt.Errorf(
			"diagnostics %s: an exact identity selector is required",
			requireIdentityFor,
		)
	}
	stores, err := resolveStores(deps, options)
	if err != nil {
		return diagnosticspkg.QueryResult{}, err
	}
	return diagnosticspkg.Query(diagnosticspkg.QueryOptions{
		Stores: stores, Selector: selector, Limit: options.limit, Now: deps.now,
	})
}

func selectorFromOptions(options commonOptions, traceIDs []string, now time.Time) (diagnosticspkg.Selector, error) {
	if options.since < 0 {
		return diagnosticspkg.Selector{}, fmt.Errorf("diagnostics: --since must not be negative")
	}
	if options.since > 0 && strings.TrimSpace(options.after) != "" {
		return diagnosticspkg.Selector{}, fmt.Errorf("diagnostics: --since and --after are mutually exclusive")
	}
	selector := diagnosticspkg.Selector{
		TraceIDs: traceIDs, ThreadID: strings.TrimSpace(options.threadID), TurnID: strings.TrimSpace(options.turnID),
		TaskID: strings.TrimSpace(options.taskID), SessionHash: strings.TrimSpace(options.sessionHash),
		ModelAttemptID: strings.TrimSpace(options.modelAttemptID), ToolCallID: strings.TrimSpace(options.toolCallID),
		InvocationID: strings.TrimSpace(options.invocationID), InteractionID: strings.TrimSpace(options.interactionID),
		CompletionID: strings.TrimSpace(options.completionID), DeliveryID: strings.TrimSpace(options.deliveryID),
	}
	if options.since > 0 {
		selector.NotBefore = now.UTC().Add(-options.since)
	}
	if strings.TrimSpace(options.after) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(options.after))
		if err != nil {
			return diagnosticspkg.Selector{}, fmt.Errorf("diagnostics: parse --after: %w", err)
		}
		selector.NotBefore = parsed
	}
	if strings.TrimSpace(options.before) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(options.before))
		if err != nil {
			return diagnosticspkg.Selector{}, fmt.Errorf("diagnostics: parse --before: %w", err)
		}
		selector.NotAfter = parsed
	}
	return selector, nil
}

func resolveStores(deps dependencies, options commonOptions) ([]diagnosticspkg.StoreSpec, error) {
	if len(options.traceRoots) > 0 && strings.TrimSpace(options.threadID) != "" {
		return nil, fmt.Errorf("diagnostics: --trace-root and --thread are mutually exclusive")
	}
	if len(options.traceRoots) > 0 {
		stores := make([]diagnosticspkg.StoreSpec, 0, len(options.traceRoots))
		for index, root := range options.traceRoots {
			if strings.TrimSpace(root) == "" {
				return nil, fmt.Errorf("diagnostics: --trace-root must not be empty")
			}
			stores = append(stores, diagnosticspkg.StoreSpec{
				ID: fmt.Sprintf("explicit-%d", index+1), Kind: "explicit", Root: root,
				Capture: diagnosticspkg.CaptureSettings{ContentMode: "unknown"},
			})
		}
		return stores, nil
	}
	configPath := strings.TrimSpace(options.configPath)
	if configPath == "" {
		configPath = deps.configPath()
	}
	cfg, err := deps.loadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("diagnostics: load config: %w", err)
	}
	capture := captureSettings(cfg.Diagnostics.TraceCapture)
	retention := time.Duration(capture.RetentionHours) * time.Hour
	if strings.TrimSpace(options.threadID) != "" {
		root, rootErr := resolveThreadTraceRoot(deps.home(), options.threadID, cfg.Diagnostics.TraceCapture.StateDir)
		if rootErr != nil {
			return nil, rootErr
		}
		return []diagnosticspkg.StoreSpec{{
			ID: "coding-thread", Kind: "coding_thread", Alias: options.threadID,
			Root: root, RequiredSessionHash: diagnosticSessionHash(cfg, options.threadID),
			Capture: capture, Retention: retention, MaxTraces: capture.MaxTraces,
		}}, nil
	}

	workspaces := []struct {
		alias string
		path  string
	}{{alias: routing.DefaultAgentID, path: cfg.WorkspacePath()}}
	for index := range cfg.Agents.List {
		agent := &cfg.Agents.List[index]
		alias := routing.NormalizeAgentID(agent.ID)
		if alias == "" || alias == routing.DefaultAgentID {
			continue
		}
		workspaces = append(workspaces, struct {
			alias string
			path  string
		}{alias: alias, path: workspace.ResolveAgentPath(agent, &cfg.Agents.Defaults)})
	}
	stores := make([]diagnosticspkg.StoreSpec, 0, len(workspaces))
	seenRoots := make(map[string]int, len(workspaces))
	for _, candidate := range workspaces {
		if strings.TrimSpace(candidate.path) == "" {
			continue
		}
		root := diagnostictrace.ResolveStoreRoot(cfg.Diagnostics.TraceCapture.StateDir, candidate.path)
		absolute, absErr := filepath.Abs(root)
		if absErr != nil {
			return nil, fmt.Errorf("diagnostics: resolve profile trace root: %w", absErr)
		}
		absolute = filepath.Clean(absolute)
		if existing, duplicate := seenRoots[absolute]; duplicate {
			stores[existing].ID = fmt.Sprintf("profile-shared-%d", existing+1)
			stores[existing].Kind = "profile_shared"
			stores[existing].Alias = ""
			continue
		}
		seenRoots[absolute] = len(stores)
		stores = append(stores, diagnosticspkg.StoreSpec{
			ID: "profile-" + candidate.alias, Kind: "profile", Alias: candidate.alias,
			Root: absolute, Capture: capture, Retention: retention, MaxTraces: capture.MaxTraces,
		})
	}
	if len(stores) == 0 {
		return nil, fmt.Errorf("diagnostics: config resolved no trace stores")
	}
	return stores, nil
}

func diagnosticSessionHash(cfg *config.Config, threadID string) string {
	value := thread.SessionKey(strings.TrimSpace(threadID))
	if cfg != nil {
		value = cfg.SensitiveDataReplacer().Replace(value)
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func resolveThreadTraceRoot(home, threadID, stateDir string) (string, error) {
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		return "", fmt.Errorf("diagnostics: open coding store: %w", err)
	}
	threadRoot, err := store.ThreadRoot(strings.TrimSpace(threadID))
	if err != nil {
		return "", fmt.Errorf("diagnostics: resolve coding thread: %w", err)
	}
	stateDir = strings.TrimSpace(stateDir)
	switch {
	case stateDir == "":
		return filepath.Join(threadRoot, "diagnostics", "traces"), nil
	case filepath.IsAbs(stateDir):
		return filepath.Join(filepath.Clean(stateDir), "traces"), nil
	default:
		clean := filepath.Clean(stateDir)
		if !filepath.IsLocal(clean) {
			return "", fmt.Errorf("diagnostics: relative trace state escapes coding thread")
		}
		return filepath.Join(threadRoot, clean, "traces"), nil
	}
}

func captureSettings(value config.DiagnosticTraceCaptureConfig) diagnosticspkg.CaptureSettings {
	maximumTraceBytes := value.MaxTraceBytes
	if maximumTraceBytes <= 0 {
		maximumTraceBytes = config.DefaultDiagnosticTraceMaxBytes
	}
	maximumRecords := value.MaxRecords
	if maximumRecords <= 0 {
		maximumRecords = config.DefaultDiagnosticTraceMaxRecords
	}
	maximumRecordBytes := value.MaxRecordBytes
	if maximumRecordBytes <= 0 {
		maximumRecordBytes = config.DefaultDiagnosticTraceMaxRecordBytes
	}
	retentionHours := value.RetentionHours
	if retentionHours <= 0 {
		retentionHours = config.DefaultDiagnosticTraceRetentionHours
	}
	maximumTraces := value.MaxTraces
	if maximumTraces <= 0 {
		maximumTraces = config.DefaultDiagnosticTraceMaxTraces
	}
	return diagnosticspkg.CaptureSettings{
		Enabled: value.Enabled, ContentMode: value.EffectiveContentMode(),
		MaxTraceBytes: maximumTraceBytes, MaxRecords: maximumRecords,
		MaxRecordBytes: maximumRecordBytes, RetentionHours: retentionHours, MaxTraces: maximumTraces,
	}
}

func makeListReport(result diagnosticspkg.QueryResult) (listReport, error) {
	traces := make([]traceEnvelope, 0, len(result.Matches))
	for _, match := range result.Matches {
		summary, err := diagnostictrace.Summarize(match.Trace)
		if err != nil {
			return listReport{}, err
		}
		traces = append(traces, envelopeFromSummary(match.StoreID, summary))
	}
	return listReport{
		SchemaVersion: listSchemaV1, GeneratedAt: result.GeneratedAt, Selector: result.Selector,
		Stores: result.Stores, Problems: result.Problems, Traces: traces,
		TotalMatches: result.TotalMatches, MatchTruncated: result.MatchTruncated,
		ScanTruncated: result.ScanTruncated,
	}, nil
}

func makeShowReport(result diagnosticspkg.QueryResult) (showReport, error) {
	traces := make([]shownTrace, 0, len(result.Matches))
	for _, match := range result.Matches {
		summary, err := diagnostictrace.Summarize(match.Trace)
		if err != nil {
			return showReport{}, err
		}
		traces = append(traces, shownTrace{StoreID: match.StoreID, Trace: summary})
	}
	return showReport{
		SchemaVersion: showSchemaV1, GeneratedAt: result.GeneratedAt, Selector: result.Selector,
		Stores: result.Stores, Problems: result.Problems, Traces: traces, ScanTruncated: result.ScanTruncated,
	}, nil
}

func makeCorrelateReport(result diagnosticspkg.QueryResult) (correlateReport, error) {
	traces := make([]traceEnvelope, 0, len(result.Matches))
	timeline := make([]timelineRecord, 0)
	for _, match := range result.Matches {
		summary, err := diagnostictrace.Summarize(match.Trace)
		if err != nil {
			return correlateReport{}, err
		}
		traces = append(traces, envelopeFromSummary(match.StoreID, summary))
		for _, record := range summary.Records {
			timeline = append(timeline, timelineRecord{
				At:      match.Trace.CreatedAt.Add(time.Duration(record.OffsetNanos)),
				StoreID: match.StoreID, TraceID: match.Trace.TraceID, Record: record,
			})
		}
	}
	slices.SortFunc(timeline, func(a, b timelineRecord) int {
		if compared := a.At.Compare(b.At); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.TraceID, b.TraceID); compared != 0 {
			return compared
		}
		return cmp.Compare(a.Record.Sequence, b.Record.Sequence)
	})
	return correlateReport{
		SchemaVersion: correlateSchemaV1, GeneratedAt: result.GeneratedAt, Selector: result.Selector,
		Stores: result.Stores, Problems: result.Problems, Traces: traces, Timeline: timeline,
		TotalMatches: result.TotalMatches, MatchTruncated: result.MatchTruncated,
		ScanTruncated: result.ScanTruncated,
	}, nil
}

func envelopeFromSummary(storeID string, summary diagnostictrace.SafeTrace) traceEnvelope {
	return traceEnvelope{
		StoreID: storeID, SchemaVersion: summary.SchemaVersion, TraceID: summary.TraceID,
		CreatedAt: summary.CreatedAt, Source: summary.Source, Policy: summary.Policy,
		Limits: summary.Limits, Metadata: summary.Metadata, Outcome: summary.Outcome,
		Truncation: summary.Truncation, Projection: summary.Projection,
		RecordCount: len(summary.Records), RecordKinds: summary.RecordKinds,
	}
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func renderList(writer io.Writer, report listReport) {
	fmt.Fprintf(
		writer,
		"Diagnostic traces: matches=%d shown=%d match_truncated=%t scan_truncated=%t\n",
		report.TotalMatches,
		len(report.Traces),
		report.MatchTruncated,
		report.ScanTruncated,
	)
	for _, store := range report.Stores {
		fmt.Fprintf(
			writer,
			"Store %s (%s): status=%s mode=%s valid=%d problems=%d corrupt=%d incomplete=%d dropped=%d scanned=%d/%d entries=%d\n",
			store.ID,
			store.Kind,
			store.Status,
			store.ScanMode,
			store.ValidTraces,
			store.ProblemFiles,
			store.CorruptTraces,
			store.IncompleteTraces,
			store.DroppedRecords,
			store.ScannedFiles,
			store.CandidateFiles,
			store.ExaminedEntries,
		)
	}
	for _, trace := range report.Traces {
		status := "unknown"
		if trace.Outcome != nil && trace.Outcome.Status != "" {
			status = trace.Outcome.Status
		}
		fmt.Fprintf(
			writer,
			"%s  %s  %s  records=%d  mode=%s  incomplete=%t\n",
			trace.CreatedAt.Format(time.RFC3339),
			trace.TraceID,
			status,
			trace.RecordCount,
			trace.Policy.ContentMode,
			trace.Truncation.Incomplete,
		)
	}
	for _, problem := range report.Problems {
		identity := problem.TraceID
		if identity == "" {
			identity = "name_hash:" + problem.NameHash
		}
		fmt.Fprintf(writer, "Problem %s %s: %s\n", problem.StoreID, identity, problem.Code)
	}
}

func renderShow(writer io.Writer, report showReport) {
	for _, shown := range report.Traces {
		trace := shown.Trace
		status := "unknown"
		if trace.Outcome != nil && trace.Outcome.Status != "" {
			status = trace.Outcome.Status
		}
		fmt.Fprintf(
			writer,
			"Trace %s (%s): created=%s status=%s records=%d mode=%s incomplete=%t dropped=%d\n",
			trace.TraceID,
			shown.StoreID,
			trace.CreatedAt.Format(time.RFC3339Nano),
			status,
			len(trace.Records),
			trace.Policy.ContentMode,
			trace.Truncation.Incomplete,
			trace.Truncation.DroppedRecords,
		)
		for _, record := range trace.Records {
			facts, _ := json.Marshal(record.Facts)
			fmt.Fprintf(
				writer,
				"  %d +%s %s turn=%s task=%s call=%s event=%s facts=%s\n",
				record.Sequence,
				time.Duration(record.OffsetNanos),
				record.Kind,
				record.Scope.TurnID,
				record.Scope.TaskID,
				record.Correlation.ToolCallID,
				record.Correlation.EventID,
				facts,
			)
		}
	}
}

func renderCorrelate(writer io.Writer, report correlateReport) {
	fmt.Fprintf(
		writer,
		"Correlated traces: matches=%d shown=%d records=%d match_truncated=%t scan_truncated=%t\n",
		report.TotalMatches,
		len(report.Traces),
		len(report.Timeline),
		report.MatchTruncated,
		report.ScanTruncated,
	)
	for _, item := range report.Timeline {
		facts, _ := json.Marshal(item.Record.Facts)
		fmt.Fprintf(
			writer,
			"%s %s #%d %s turn=%s task=%s model_attempt=%s tool_call=%s invocation=%s interaction=%s facts=%s\n",
			item.At.Format(time.RFC3339Nano),
			item.TraceID,
			item.Record.Sequence,
			item.Record.Kind,
			item.Record.Scope.TurnID,
			item.Record.Scope.TaskID,
			item.Record.Correlation.ModelAttemptID,
			item.Record.Correlation.ToolCallID,
			item.Record.Correlation.InvocationID,
			item.Record.Correlation.InteractionID,
			facts,
		)
	}
}

func completeDependencies(deps dependencies) dependencies {
	defaults := defaultDependencies()
	if deps.home == nil {
		deps.home = defaults.home
	}
	if deps.configPath == nil {
		deps.configPath = defaults.configPath
	}
	if deps.loadConfig == nil {
		deps.loadConfig = defaults.loadConfig
	}
	if deps.now == nil {
		deps.now = defaults.now
	}
	return deps
}
