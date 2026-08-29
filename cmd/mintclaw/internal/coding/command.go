package coding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal"
	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingpicker "github.com/bogdanovich/mintclaw/pkg/coding/picker"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/coding/tui"
)

type dependencies struct {
	home            func() string
	cwd             func() (string, error)
	now             func() time.Time
	newThreadID     func() string
	turnRunner      codingTurnRunner
	resolveModel    func(string) (string, string, error)
	terminal        func(io.Reader, io.Writer, bool) tui.TerminalCapabilities
	newController   func(codingTurnRequest, bool) (frontend.Controller, error)
	runTUI          func(context.Context, frontend.Controller, tui.Options) error
	newPickerSource func(*thread.Store, thread.ProjectIdentity) (codingpicker.Source, error)
	runPicker       func(context.Context, codingpicker.Source, tui.PickerOptions) (tui.PickerSelection, error)
}

func defaultDependencies() dependencies {
	return dependencies{
		home:            internal.GetMintClawHome,
		cwd:             os.Getwd,
		now:             time.Now,
		newThreadID:     thread.NewThreadID,
		turnRunner:      newNativeCodingTurnRunner(),
		resolveModel:    resolveNativeCodingModel,
		terminal:        detectCodingTerminal,
		newController:   newNativeCodingController,
		runTUI:          tui.Run,
		newPickerSource: newPickerCatalogSource,
		runPicker:       tui.RunPicker,
	}
}

func detectCodingTerminal(input io.Reader, output io.Writer, noColor bool) tui.TerminalCapabilities {
	inputFile, inputOK := input.(*os.File)
	outputFile, outputOK := output.(*os.File)
	if !inputOK || !outputOK {
		return tui.TerminalCapabilities{Reason: "coding command streams are not terminal files"}
	}
	return tui.DetectTerminalCapabilities(inputFile, outputFile, noColor)
}

type commandResult struct {
	Action        string `json:"action"`
	ThreadID      string `json:"thread_id"`
	SessionKey    string `json:"session_key"`
	ProjectRoot   string `json:"project_root"`
	InvocationCWD string `json:"invocation_cwd"`
	StateRoot     string `json:"state_root"`
	Model         string `json:"model,omitempty"`
	Provider      string `json:"provider,omitempty"`
	PromptStored  bool   `json:"prompt_stored"`
	Response      string `json:"response,omitempty"`
}

type listResult struct {
	ProjectRoot           string                         `json:"project_root,omitempty"`
	AllProjects           bool                           `json:"all_projects"`
	Threads               []thread.Metadata              `json:"threads"`
	Skipped               []thread.SkippedEntry          `json:"skipped,omitempty"`
	SkippedTotal          int                            `json:"skipped_total,omitempty"`
	Scanned               int                            `json:"scanned"`
	Matched               int                            `json:"matched"`
	Truncated             bool                           `json:"scan_truncated"`
	HasMore               bool                           `json:"has_more"`
	NextOffset            int                            `json:"next_offset,omitempty"`
	Search                string                         `json:"search,omitempty"`
	SearchMatches         []thread.HistoricalSearchMatch `json:"search_matches,omitempty"`
	ContentThreadsScanned int                            `json:"content_threads_scanned,omitempty"`
	ContentBytesScanned   int64                          `json:"content_bytes_scanned,omitempty"`
	ContentScanTruncated  bool                           `json:"content_scan_truncated,omitempty"`
}

// NewCodeCommand creates a durable coding thread from the first prompt.
func NewCodeCommand() *cobra.Command {
	return newCodeCommand(defaultDependencies())
}

func newCodeCommand(deps dependencies) *cobra.Command {
	deps = completeDependencies(deps)
	var model string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "code <prompt>",
		Short: "Create an interactive project coding thread",
		Long: "Create a durable coding thread for the current project and run its first prompt in the MintClaw " +
			"terminal UI. Redirected and JSON output use the plain renderer.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt := strings.Join(args, " ")
			noColor, _ := cmd.Flags().GetBool("no-color")
			capabilities := deps.terminal(cmd.InOrStdin(), cmd.OutOrStdout(), noColor)
			if !jsonOutput && capabilities.Interactive {
				return runNewInteractive(
					cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), deps, prompt, model, noColor,
				)
			}
			return runNew(cmd.Context(), cmd.OutOrStdout(), deps, prompt, model, jsonOutput)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "Persist a model override for this thread")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit machine-readable JSON")
	return cmd
}

func completeDependencies(deps dependencies) dependencies {
	if deps.terminal == nil {
		deps.terminal = func(io.Reader, io.Writer, bool) tui.TerminalCapabilities {
			return tui.TerminalCapabilities{Reason: "interactive terminal detection is unavailable"}
		}
	}
	if deps.newController == nil {
		deps.newController = newNativeCodingController
	}
	if deps.runTUI == nil {
		deps.runTUI = tui.Run
	}
	if deps.newPickerSource == nil {
		deps.newPickerSource = newPickerCatalogSource
	}
	if deps.runPicker == nil {
		deps.runPicker = tui.RunPicker
	}
	return deps
}

func runNewInteractive(
	ctx context.Context,
	in io.Reader,
	out io.Writer,
	deps dependencies,
	prompt string,
	model string,
	noColor bool,
) error {
	_, store, metadata, lease, err := prepareNewThread(ctx, deps, prompt, model)
	if err != nil {
		return err
	}
	frontendController, err := deps.newController(codingTurnRequest{
		Store: store, Lease: lease, Metadata: metadata,
	}, false)
	if err != nil {
		return errors.Join(err, lease.Release())
	}
	return deps.runTUI(ctx, frontendController, tui.Options{
		Input:           in,
		Output:          out,
		InitialPrompt:   prompt,
		AlternateScreen: true,
		ReportFocus:     true,
		NoColor:         noColor,
		Environment:     os.Environ(),
	})
}

func prepareNewThread(
	ctx context.Context,
	deps dependencies,
	prompt string,
	model string,
) (thread.ProjectIdentity, *thread.Store, thread.Metadata, *thread.Lease, error) {
	project, store, resolveErr := resolveEnvironment(ctx, deps)
	if resolveErr != nil {
		return thread.ProjectIdentity{}, nil, thread.Metadata{}, nil, resolveErr
	}
	if err := thread.ValidatePrompt(prompt); err != nil {
		return thread.ProjectIdentity{}, nil, thread.Metadata{}, nil, err
	}
	metadata, metadataErr := thread.NewMetadata(deps.newThreadID(), project, prompt, deps.now())
	if metadataErr != nil {
		return thread.ProjectIdentity{}, nil, thread.Metadata{}, nil, metadataErr
	}
	resolvedModel, resolvedProvider, err := deps.resolveModel(model)
	if err != nil {
		return thread.ProjectIdentity{}, nil, thread.Metadata{}, nil, err
	}
	metadata.Model = resolvedModel
	metadata.Provider = resolvedProvider
	if err := metadata.Validate(); err != nil {
		return thread.ProjectIdentity{}, nil, thread.Metadata{}, nil, err
	}
	if _, err := runtimeLayoutFor(store, metadata); err != nil {
		return thread.ProjectIdentity{}, nil, thread.Metadata{}, nil, err
	}
	if err := store.ProvisionThread(metadata.ThreadID); err != nil {
		return thread.ProjectIdentity{}, nil, thread.Metadata{}, nil, err
	}
	lease, leaseErr := store.AcquireLease(metadata.ThreadID)
	if leaseErr != nil {
		return thread.ProjectIdentity{}, nil, thread.Metadata{}, nil, leaseErr
	}
	if err := store.Save(metadata); err != nil {
		return thread.ProjectIdentity{}, nil, thread.Metadata{}, nil, errors.Join(err, lease.Release())
	}
	return project, store, metadata, lease, nil
}

// NewResumeCommand creates the top-level coding-thread discovery and resume command.
func NewResumeCommand() *cobra.Command {
	return newResumeCommand(defaultDependencies())
}

func newResumeCommand(deps dependencies) *cobra.Command {
	deps = completeDependencies(deps)
	var all bool
	var archived bool
	var last bool
	var model string
	var prompt string
	var jsonOutput bool
	var offset int
	var limit int
	var search string
	cmd := &cobra.Command{
		Use:   "resume [thread-id]",
		Short: "List or resume project coding threads",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			threadID := ""
			if len(args) == 1 {
				threadID = args[0]
			}
			options := resumeOptions{
				threadID:  threadID,
				all:       all,
				archived:  archived,
				last:      last,
				model:     model,
				prompt:    prompt,
				promptSet: cmd.Flags().Changed("prompt"),
				json:      jsonOutput,
				offset:    offset,
				limit:     limit,
				search:    search,
				searchSet: cmd.Flags().Changed("search"),
			}
			noColor, _ := cmd.Flags().GetBool("no-color")
			capabilities := deps.terminal(cmd.InOrStdin(), cmd.OutOrStdout(), noColor)
			if !jsonOutput && capabilities.Interactive && offset == 0 && limit == 0 {
				return runResumeInteractive(
					cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), deps, options, noColor,
				)
			}
			return runResume(cmd.Context(), cmd.OutOrStdout(), deps, options)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "List threads from every project")
	cmd.Flags().BoolVar(&archived, "archived", false, "List or select archived threads instead of active threads")
	cmd.Flags().BoolVar(&last, "last", false, "Resume the most recently updated matching thread")
	cmd.Flags().StringVar(&model, "model", "", "Replace the persisted model override")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Append a prompt while resuming the selected thread")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit machine-readable JSON")
	cmd.Flags().IntVar(&offset, "offset", 0, "List offset within the bounded result set")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum threads to list on this page")
	cmd.Flags().StringVar(&search, "search", "", "Search title, preview, and bounded transcript history")
	return cmd
}

func runNew(
	ctx context.Context,
	out io.Writer,
	deps dependencies,
	prompt string,
	model string,
	jsonOutput bool,
) error {
	_, store, metadata, lease, err := prepareNewThread(ctx, deps, prompt, model)
	if err != nil {
		return err
	}
	outcome, turnErr := deps.turnRunner.Run(ctx, codingTurnRequest{
		Store: store, Lease: lease, Metadata: metadata, Prompt: prompt,
	})
	if outcome.Model != "" {
		metadata.Model = outcome.Model
	}
	if outcome.Provider != "" {
		metadata.Provider = outcome.Provider
	}
	saveErr := store.Save(metadata)
	releaseErr := lease.Release()
	if turnErr != nil {
		return inspectableTurnError(
			metadata.ThreadID,
			outcome.PromptStored,
			errors.Join(turnErr, saveErr, releaseErr),
		)
	}
	if saveErr != nil || releaseErr != nil {
		return committedPromptOperationError(metadata.ThreadID, errors.Join(saveErr, releaseErr))
	}
	result, resultErr := resultFor("created", store, metadata, outcome.PromptStored, outcome.Response)
	if resultErr != nil {
		return preserveCommittedPromptState(metadata.ThreadID, true, resultErr)
	}
	return preserveCommittedPromptState(
		metadata.ThreadID,
		true,
		renderResult(out, result, jsonOutput),
	)
}

type resumeOptions struct {
	threadID  string
	all       bool
	archived  bool
	last      bool
	model     string
	prompt    string
	promptSet bool
	json      bool
	offset    int
	limit     int
	search    string
	searchSet bool
}

func validateResumeOptions(options resumeOptions) error {
	if options.threadID != "" && options.last {
		return fmt.Errorf("resume: thread ID and --last are mutually exclusive")
	}
	if options.searchSet && strings.TrimSpace(options.search) == "" {
		return fmt.Errorf("resume: --search requires a non-empty query")
	}
	if options.searchSet && (options.threadID != "" || options.last) {
		return fmt.Errorf("resume: --search cannot be combined with a thread ID or --last")
	}
	if options.searchSet && (options.model != "" || options.promptSet) {
		return fmt.Errorf("resume: --search is discovery-only and cannot append a prompt or replace the model")
	}
	if options.threadID != "" && (options.all || options.archived) {
		return fmt.Errorf("resume: explicit thread ID cannot be combined with --all or --archived")
	}
	if options.threadID == "" && !options.last && (options.model != "" || options.promptSet) {
		return fmt.Errorf("resume: --model and --prompt require a thread ID or --last")
	}
	if (options.threadID != "" || options.last) && (options.offset != 0 || options.limit != 0) {
		return fmt.Errorf("resume: --offset and --limit are list-only options")
	}
	return nil
}

func runResumeInteractive(
	ctx context.Context,
	in io.Reader,
	out io.Writer,
	deps dependencies,
	options resumeOptions,
	noColor bool,
) error {
	if err := validateResumeOptions(options); err != nil {
		return err
	}
	if options.promptSet {
		if err := thread.ValidatePrompt(options.prompt); err != nil {
			return err
		}
	}
	project, store, err := resolveEnvironment(ctx, deps)
	if err != nil {
		return err
	}
	threadID := options.threadID
	if threadID == "" && options.last {
		threadID, err = selectLastResumeThread(ctx, store, project, options.all, options.archived)
		if err != nil {
			return err
		}
	}
	if threadID == "" {
		source, sourceErr := deps.newPickerSource(store, project)
		if sourceErr != nil {
			return sourceErr
		}
		selection, pickerErr := deps.runPicker(ctx, source, tui.PickerOptions{
			Input:           in,
			Output:          out,
			AlternateScreen: true,
			AllProjects:     options.all,
			Archived:        options.archived,
			Search:          options.search,
			Environment:     os.Environ(),
			NoColor:         noColor,
			Now:             deps.now,
		})
		if pickerErr != nil {
			return pickerErr
		}
		if selection.Canceled {
			return nil
		}
		threadID = selection.ThreadID
		if strings.TrimSpace(threadID) == "" {
			return fmt.Errorf("resume picker returned no thread selection")
		}
	}
	metadata, lease, err := prepareResumedThread(ctx, store, project, deps, threadID, options, true)
	if err != nil {
		return err
	}
	frontendController, err := deps.newController(codingTurnRequest{
		Store: store, Lease: lease, Metadata: metadata,
	}, true)
	if err != nil {
		return errors.Join(err, lease.Release())
	}
	initialPrompt := ""
	if options.promptSet {
		initialPrompt = options.prompt
	}
	return deps.runTUI(ctx, frontendController, tui.Options{
		Input:           in,
		Output:          out,
		InitialPrompt:   initialPrompt,
		AlternateScreen: true,
		ReportFocus:     true,
		NoColor:         noColor,
		Environment:     os.Environ(),
	})
}

func selectLastResumeThread(
	ctx context.Context,
	store *thread.Store,
	project thread.ProjectIdentity,
	all bool,
	archived bool,
) (string, error) {
	catalog, err := thread.NewCatalog(store, thread.CatalogOptions{})
	if err != nil {
		return "", err
	}
	query := thread.CatalogQuery{All: all, Archived: archived, Last: true}
	if !all {
		query.ProjectKey = project.ProjectKey
	}
	page, err := catalog.Query(ctx, query)
	if err != nil {
		return "", err
	}
	if len(page.Threads) == 0 {
		scope := "the current project"
		if all {
			scope = "any project"
		}
		return "", fmt.Errorf("resume: no coding threads found for %s", scope)
	}
	return page.Threads[0].ThreadID, nil
}

func runResume(
	ctx context.Context,
	out io.Writer,
	deps dependencies,
	options resumeOptions,
) error {
	if err := validateResumeOptions(options); err != nil {
		return err
	}
	project, store, resolveErr := resolveEnvironment(ctx, deps)
	if resolveErr != nil {
		return resolveErr
	}
	if options.searchSet {
		searcher, searchErr := thread.NewHistoricalSearcher(store, thread.HistoricalSearchOptions{})
		if searchErr != nil {
			return searchErr
		}
		query := thread.HistoricalSearchQuery{
			All: options.all, Archived: options.archived, Text: strings.TrimSpace(options.search),
			Offset: options.offset, Limit: options.limit,
		}
		if !options.all {
			query.ProjectKey = project.ProjectKey
		}
		page, searchErr := searcher.Query(ctx, query)
		if searchErr != nil {
			return searchErr
		}
		return renderSearchList(out, project, page, options)
	}
	catalog, catalogErr := thread.NewCatalog(store, thread.CatalogOptions{})
	if catalogErr != nil {
		return catalogErr
	}
	query := thread.CatalogQuery{
		ThreadID: options.threadID,
		All:      options.all,
		Archived: options.archived,
		Last:     options.last,
		Offset:   options.offset,
		Limit:    options.limit,
	}
	if options.threadID == "" && !options.all {
		query.ProjectKey = project.ProjectKey
	}
	if options.threadID != "" || options.last {
		query.Offset = 0
		query.Limit = 0
	}
	page, queryErr := catalog.Query(ctx, query)
	if queryErr != nil {
		if options.threadID != "" && errors.Is(queryErr, fs.ErrNotExist) {
			return resumeThreadNotFoundError(options.threadID)
		}
		return queryErr
	}
	if options.threadID == "" && !options.last {
		return renderList(out, project, page, options)
	}
	if len(page.Threads) == 0 {
		scope := "the current project"
		if options.all {
			scope = "any project"
		}
		return fmt.Errorf("resume: no coding threads found for %s", scope)
	}
	result, resumeErr := resumeSelectedThread(ctx, store, project, deps, page.Threads[0].ThreadID, options)
	if resumeErr != nil {
		return resumeErr
	}
	return preserveCommittedPromptState(
		result.ThreadID,
		result.PromptStored,
		renderResult(out, result, options.json),
	)
}

func resumeSelectedThread(
	ctx context.Context,
	store *thread.Store,
	project thread.ProjectIdentity,
	deps dependencies,
	threadID string,
	options resumeOptions,
) (result commandResult, resultErr error) {
	updatedPreview := ""
	if options.promptSet {
		if err := thread.ValidatePrompt(options.prompt); err != nil {
			return commandResult{}, err
		}
		_, preview, displayErr := thread.DisplayFromRequest(options.prompt)
		if displayErr != nil {
			return commandResult{}, displayErr
		}
		updatedPreview = preview
	}
	metadata, lease, err := prepareResumedThread(ctx, store, project, deps, threadID, options, options.promptSet)
	if err != nil {
		return commandResult{}, err
	}
	promptStored := false
	defer func() {
		resultErr = errors.Join(resultErr, lease.Release())
		resultErr = preserveCommittedPromptState(threadID, promptStored, resultErr)
	}()
	var outcome codingTurnOutcome
	var turnErr error
	if options.promptSet {
		outcome, turnErr = deps.turnRunner.Run(ctx, codingTurnRequest{
			Store: store, Lease: lease, Metadata: metadata, Prompt: options.prompt,
		})
		promptStored = outcome.PromptStored
		if promptStored {
			metadata.Preview = updatedPreview
		}
		if outcome.Model != "" {
			metadata.Model = outcome.Model
		}
		if outcome.Provider != "" {
			metadata.Provider = outcome.Provider
		}
	}
	if err := store.Save(metadata); err != nil {
		if promptStored {
			return commandResult{}, errors.Join(
				turnErr,
				committedPromptOperationError(metadata.ThreadID, err),
			)
		}
		return commandResult{}, err
	}
	if turnErr != nil {
		return commandResult{}, inspectableTurnError(metadata.ThreadID, promptStored, turnErr)
	}
	result, buildErr := resultFor("resumed", store, metadata, promptStored, outcome.Response)
	if buildErr != nil {
		return commandResult{}, buildErr
	}
	return result, nil
}

func prepareResumedThread(
	ctx context.Context,
	store *thread.Store,
	project thread.ProjectIdentity,
	deps dependencies,
	threadID string,
	options resumeOptions,
	requireModel bool,
) (metadata thread.Metadata, lease *thread.Lease, resultErr error) {
	lease, err := store.AcquireLease(threadID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return thread.Metadata{}, nil, resumeThreadNotFoundError(threadID)
		}
		return thread.Metadata{}, nil, err
	}
	admitted := false
	defer func() {
		if !admitted {
			resultErr = errors.Join(resultErr, lease.Release())
			lease = nil
		}
	}()
	metadata, err = store.Load(threadID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return thread.Metadata{}, lease, resumeThreadNotFoundError(threadID)
		}
		return thread.Metadata{}, lease, err
	}
	inspection, err := thread.InspectLocation(ctx, metadata.Project, project.InvocationCWD)
	if err != nil {
		return thread.Metadata{}, lease, err
	}
	var admittedProject thread.ProjectIdentity
	switch inspection.State {
	case thread.LocationAvailable:
		if inspection.Current == nil {
			return thread.Metadata{}, lease, fmt.Errorf(
				"resume: thread %q project identity is unavailable after inspection",
				metadata.ThreadID,
			)
		}
		admittedProject = *inspection.Current
	case thread.LocationMismatch:
		return thread.Metadata{}, lease, fmt.Errorf(
			"resume: thread %q belongs to %q, not current project %q; change directory before resuming",
			metadata.ThreadID,
			metadata.Project.ProjectRoot,
			project.ProjectRoot,
		)
	case thread.LocationMissing, thread.LocationMoved:
		return thread.Metadata{}, lease, fmt.Errorf(
			"resume: thread %q project location %q is %s; explicit relocation is required",
			metadata.ThreadID,
			metadata.Project.ProjectRoot,
			inspection.State,
		)
	default:
		return thread.Metadata{}, lease, fmt.Errorf(
			"resume: thread %q has unknown project location state",
			metadata.ThreadID,
		)
	}
	if options.model != "" {
		resolvedModel, resolvedProvider, resolveErr := deps.resolveModel(options.model)
		if resolveErr != nil {
			return thread.Metadata{}, lease, resolveErr
		}
		metadata.Model = resolvedModel
		metadata.Provider = resolvedProvider
	} else if requireModel &&
		(strings.TrimSpace(metadata.Model) == "" || strings.TrimSpace(metadata.Provider) == "") {
		resolvedModel, resolvedProvider, resolveErr := deps.resolveModel(metadata.Model)
		if resolveErr != nil {
			return thread.Metadata{}, lease, resolveErr
		}
		metadata.Model = resolvedModel
		metadata.Provider = resolvedProvider
	}
	metadata.Project = admittedProject
	metadata.UpdatedAt = deps.now().UTC()
	if err := metadata.Validate(); err != nil {
		return thread.Metadata{}, lease, err
	}
	if _, err := runtimeLayoutFor(store, metadata); err != nil {
		return thread.Metadata{}, lease, err
	}
	if err := store.Save(metadata); err != nil {
		return thread.Metadata{}, lease, err
	}
	admitted = true
	return metadata, lease, nil
}

func resumeThreadNotFoundError(threadID string) error {
	return fmt.Errorf(
		"resume: coding thread %q was not found; run `mintclaw resume` or `mintclaw resume --all`",
		threadID,
	)
}

func committedPromptOperationError(threadID string, err error) error {
	return &thread.CommittedPromptError{ThreadID: threadID, Err: err}
}

func inspectableTurnError(threadID string, promptStored bool, err error) error {
	if err == nil {
		return nil
	}
	inspectable := fmt.Errorf(
		"coding thread %s remains inspectable with `mintclaw resume %s`: %w",
		threadID,
		threadID,
		err,
	)
	return preserveCommittedPromptState(threadID, promptStored, inspectable)
}

func appendOutcomeAllowsMetadataSave(err error) bool {
	return err == nil || thread.IsCommittedPromptError(err)
}

func preserveCommittedPromptState(threadID string, promptStored bool, err error) error {
	if err == nil || !promptStored || thread.IsCommittedPromptError(err) ||
		thread.IsIndeterminatePromptError(err) {
		return err
	}
	return committedPromptOperationError(threadID, err)
}

func resolveEnvironment(ctx context.Context, deps dependencies) (thread.ProjectIdentity, *thread.Store, error) {
	cwd, err := deps.cwd()
	if err != nil {
		return thread.ProjectIdentity{}, nil, fmt.Errorf("coding command: get current directory: %w", err)
	}
	project, err := thread.ResolveProject(ctx, cwd)
	if err != nil {
		return thread.ProjectIdentity{}, nil, err
	}
	home := strings.TrimSpace(deps.home())
	if home == "" {
		return thread.ProjectIdentity{}, nil, fmt.Errorf("coding command: MintClaw home is required")
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		return thread.ProjectIdentity{}, nil, err
	}
	return project, store, nil
}

func resultFor(
	action string,
	store *thread.Store,
	metadata thread.Metadata,
	stored bool,
	response string,
) (commandResult, error) {
	layout, err := runtimeLayoutFor(store, metadata)
	if err != nil {
		return commandResult{}, err
	}
	return commandResult{
		Action:        action,
		ThreadID:      metadata.ThreadID,
		SessionKey:    metadata.SessionKey,
		ProjectRoot:   metadata.Project.ProjectRoot,
		InvocationCWD: metadata.Project.InvocationCWD,
		StateRoot:     layout.StateRoot(),
		Model:         metadata.Model,
		Provider:      metadata.Provider,
		PromptStored:  stored,
		Response:      response,
	}, nil
}

func runtimeLayoutFor(store *thread.Store, metadata thread.Metadata) (agent.CodingRuntimeLayout, error) {
	stateRoot, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		return agent.CodingRuntimeLayout{}, err
	}
	layout, err := agent.NewCodingRuntimeLayout(
		metadata.ThreadID,
		metadata.Project.ProjectRoot,
		stateRoot,
		codingInstructionRoots(store, metadata),
	)
	if err != nil {
		return agent.CodingRuntimeLayout{}, fmt.Errorf("coding command: validate external runtime state: %w", err)
	}
	return layout, nil
}

func codingInstructionRoots(store *thread.Store, metadata thread.Metadata) []string {
	roots := []string{
		filepath.Join(store.Root(), "config"),
		metadata.Project.ProjectRoot,
	}
	if metadata.Project.InvocationCWD != metadata.Project.ProjectRoot {
		roots = append(roots, metadata.Project.InvocationCWD)
	}
	return roots
}

func renderResult(out io.Writer, result commandResult, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(out, result)
	}
	_, err := fmt.Fprintf(
		out,
		"Coding thread %s: %s\nProject: %q\nWorking directory: %q\nState: %q\n"+
			"Model: %q\nProvider: %q\nPrompt stored: %t\n",
		result.Action,
		result.ThreadID,
		result.ProjectRoot,
		result.InvocationCWD,
		result.StateRoot,
		result.Model,
		result.Provider,
		result.PromptStored,
	)
	if err == nil && result.Response != "" {
		_, err = fmt.Fprintf(out, "\n%s\n", result.Response)
	}
	return err
}

func renderList(
	out io.Writer,
	project thread.ProjectIdentity,
	page thread.CatalogPage,
	options resumeOptions,
) error {
	result := listResult{
		AllProjects:  options.all,
		Threads:      append([]thread.Metadata{}, page.Threads...),
		Skipped:      page.Skipped,
		SkippedTotal: page.SkippedTotal,
		Scanned:      page.Scanned,
		Matched:      page.Matched,
		Truncated:    page.ScanTruncated,
		HasMore:      page.HasMore,
		NextOffset:   page.NextOffset,
	}
	if !options.all {
		result.ProjectRoot = project.ProjectRoot
	}
	if options.json {
		return writeJSON(out, result)
	}
	if len(page.Threads) == 0 {
		if _, err := fmt.Fprintln(
			out,
			"No coding threads found. Start one with `mintclaw code <prompt>`.",
		); err != nil {
			return err
		}
	} else {
		for _, metadata := range page.Threads {
			if _, err := fmt.Fprintf(
				out,
				"%s\t%s\t%q\t%q\n",
				metadata.ThreadID,
				metadata.UpdatedAt.Format(time.RFC3339),
				metadata.Project.ProjectRoot,
				metadata.Title,
			); err != nil {
				return err
			}
		}
	}
	if page.HasMore {
		if _, err := fmt.Fprintf(
			out,
			"More threads available; continue with --offset %d.\n",
			page.NextOffset,
		); err != nil {
			return err
		}
	}
	if page.ScanTruncated {
		if _, err := fmt.Fprintln(
			out,
			"Thread scan was truncated; narrow the project or use an exact ID.",
		); err != nil {
			return err
		}
	}
	return nil
}

func renderSearchList(
	out io.Writer,
	project thread.ProjectIdentity,
	page thread.HistoricalSearchPage,
	options resumeOptions,
) error {
	result := listResult{
		AllProjects: options.all, Threads: []thread.Metadata{}, Search: strings.TrimSpace(options.search),
		SearchMatches: append([]thread.HistoricalSearchMatch(nil), page.Matches...),
		Skipped:       page.Skipped, SkippedTotal: page.SkippedTotal,
		Scanned: page.Scanned, Matched: page.Matched, Truncated: page.ScanTruncated,
		HasMore: page.HasMore, NextOffset: page.NextOffset,
		ContentThreadsScanned: page.ContentThreadsScanned,
		ContentBytesScanned:   page.ContentBytesScanned,
		ContentScanTruncated:  page.ContentScanTruncated,
	}
	if !options.all {
		result.ProjectRoot = project.ProjectRoot
	}
	if options.json {
		return writeJSON(out, result)
	}
	if len(page.Matches) == 0 {
		if _, err := fmt.Fprintf(out, "No coding threads match %q.\n", strings.TrimSpace(options.search)); err != nil {
			return err
		}
	} else {
		for _, match := range page.Matches {
			source := "metadata:" + string(match.Kind)
			if match.Kind == thread.HistoricalMatchTranscript {
				source = fmt.Sprintf("transcript:message-%d", match.Message)
			}
			if _, err := fmt.Fprintf(
				out,
				"%s\t%s\t%q\t%s\t%q\n",
				match.Metadata.ThreadID,
				match.MatchedAt.Format(time.RFC3339Nano),
				match.Metadata.Project.ProjectRoot,
				source,
				match.Snippet,
			); err != nil {
				return err
			}
		}
	}
	if page.HasMore {
		if _, err := fmt.Fprintf(
			out,
			"More matches available; continue with --offset %d.\n",
			page.NextOffset,
		); err != nil {
			return err
		}
	}
	if page.ScanTruncated || page.ContentScanTruncated || page.SkippedTotal > 0 {
		_, err := fmt.Fprintln(
			out,
			"Search coverage was incomplete or bounded; narrow the query and inspect JSON diagnostics.",
		)
		return err
	}
	return nil
}

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
