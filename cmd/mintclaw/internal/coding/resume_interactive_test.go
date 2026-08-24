package coding

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingpicker "github.com/bogdanovich/mintclaw/pkg/coding/picker"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/coding/tui"
)

type staticPickerSource struct {
	page codingpicker.Page
}

func (s staticPickerSource) Page(
	context.Context,
	codingpicker.Query,
) (codingpicker.Page, error) {
	return s.page, nil
}

func TestResumePickerAndExplicitSelectionOpenResumedInteractiveController(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 16, 10, 0, 0, 0, time.UTC)
	baseDeps := testDependencies(home, project, &now)
	var created commandResult
	if err := json.Unmarshal(
		executeCommand(t, newCodeCommand(baseDeps), "fix parser", "--json"),
		&created,
	); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name          string
		args          []string
		pickerRuns    int
		initialPrompt string
	}{
		{name: "picker", pickerRuns: 1},
		{name: "explicit with prompt", args: []string{created.ThreadID, "--prompt", "continue work"}, initialPrompt: "continue work"},
		{name: "last", args: []string{"--last"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			deps := testDependencies(home, project, &now)
			deps.terminal = func(io.Reader, io.Writer, bool) tui.TerminalCapabilities {
				return tui.TerminalCapabilities{Interactive: true, Color: true}
			}
			deps.newPickerSource = func(
				*thread.Store,
				thread.ProjectIdentity,
			) (codingpicker.Source, error) {
				return staticPickerSource{}, nil
			}
			pickerRuns := 0
			deps.runPicker = func(
				_ context.Context,
				_ codingpicker.Source,
				options tui.PickerOptions,
			) (tui.PickerSelection, error) {
				pickerRuns++
				if !options.AlternateScreen || options.AllProjects {
					t.Fatalf("picker options = %+v", options)
				}
				return tui.PickerSelection{ThreadID: created.ThreadID}, nil
			}
			controllerRuns := 0
			deps.newController = func(request codingTurnRequest, resumed bool) (frontend.Controller, error) {
				controllerRuns++
				if !resumed || request.Metadata.ThreadID != created.ThreadID {
					t.Fatalf("resumed controller request = %+v resumed=%t", request, resumed)
				}
				if err := request.Store.ValidateLease(request.Lease, created.ThreadID); err != nil {
					t.Fatalf("controller lease = %v", err)
				}
				projector, err := frontend.NewProjector(created.ThreadID, frontend.ProjectionLimits{})
				if err != nil {
					return nil, err
				}
				projector.Open(true)
				return &interactiveLeaseController{Projector: projector, lease: request.Lease}, nil
			}
			tuiRuns := 0
			deps.runTUI = func(ctx context.Context, controller frontend.Controller, options tui.Options) error {
				tuiRuns++
				if options.InitialPrompt != testCase.initialPrompt || !options.AlternateScreen || !options.ReportFocus {
					t.Fatalf("resume TUI options = %+v", options)
				}
				return controller.Close(ctx)
			}

			executeCommand(t, newResumeCommand(deps), testCase.args...)
			if pickerRuns != testCase.pickerRuns || controllerRuns != 1 || tuiRuns != 1 {
				t.Fatalf("runs picker=%d controller=%d TUI=%d", pickerRuns, controllerRuns, tuiRuns)
			}
			store, err := thread.NewStore(filepath.Join(home, "coding"))
			if err != nil {
				t.Fatal(err)
			}
			lease, err := store.AcquireLease(created.ThreadID)
			if err != nil {
				t.Fatalf("interactive resume did not release lease: %v", err)
			}
			if err := lease.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInteractiveResumeRechecksLiveLeaseAndProjectAfterPicker(t *testing.T) {
	home := t.TempDir()
	currentProject := t.TempDir()
	foreignProject := t.TempDir()
	now := time.Date(2026, time.August, 16, 11, 0, 0, 0, time.UTC)
	currentDeps := testDependencies(home, currentProject, &now)
	foreignDeps := testDependencies(home, foreignProject, &now)
	var current commandResult
	if err := json.Unmarshal(
		executeCommand(t, newCodeCommand(currentDeps), "current", "--json"),
		&current,
	); err != nil {
		t.Fatal(err)
	}
	var foreign commandResult
	if err := json.Unmarshal(
		executeCommand(t, newCodeCommand(foreignDeps), "foreign", "--json"),
		&foreign,
	); err != nil {
		t.Fatal(err)
	}
	interactive := func(deps dependencies, selected string) dependencies {
		deps.terminal = func(io.Reader, io.Writer, bool) tui.TerminalCapabilities {
			return tui.TerminalCapabilities{Interactive: true}
		}
		deps.newPickerSource = func(
			*thread.Store,
			thread.ProjectIdentity,
		) (codingpicker.Source, error) {
			return staticPickerSource{}, nil
		}
		deps.runPicker = func(
			context.Context,
			codingpicker.Source,
			tui.PickerOptions,
		) (tui.PickerSelection, error) {
			return tui.PickerSelection{ThreadID: selected}, nil
		}
		deps.newController = func(codingTurnRequest, bool) (frontend.Controller, error) {
			t.Fatal("invalid picker selection reached controller construction")
			return nil, nil
		}
		return deps
	}

	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.AcquireLease(current.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	_, busyErr := executeCommandError(newResumeCommand(interactive(currentDeps, current.ThreadID)))
	if !errors.Is(busyErr, thread.ErrLeaseBusy) {
		t.Fatalf("picker bypassed live lease: %v", busyErr)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}

	_, mismatchErr := executeCommandError(
		newResumeCommand(interactive(currentDeps, foreign.ThreadID)),
		foreign.ThreadID,
	)
	if mismatchErr == nil || !stringsContainAll(
		mismatchErr.Error(),
		"belongs to",
		"change directory before resuming",
	) {
		t.Fatalf("picker bypassed project mismatch: %v", mismatchErr)
	}
	lease, err := store.AcquireLease(foreign.ThreadID)
	if err != nil {
		t.Fatalf("mismatch path leaked lease: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledResumePickerDoesNotAcquireOrCreateController(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	deps.terminal = func(io.Reader, io.Writer, bool) tui.TerminalCapabilities {
		return tui.TerminalCapabilities{Interactive: true}
	}
	deps.newPickerSource = func(*thread.Store, thread.ProjectIdentity) (codingpicker.Source, error) {
		return staticPickerSource{}, nil
	}
	deps.runPicker = func(
		context.Context,
		codingpicker.Source,
		tui.PickerOptions,
	) (tui.PickerSelection, error) {
		return tui.PickerSelection{Canceled: true}, nil
	}
	deps.newController = func(codingTurnRequest, bool) (frontend.Controller, error) {
		t.Fatal("canceled picker created a controller")
		return nil, nil
	}
	executeCommand(t, newResumeCommand(deps))
}

func TestInteractiveResumePersistsProjectIdentityResolvedUnderLease(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "resume@example.invalid"},
		{"config", "user.name", "Resume Test"},
		{"add", "."},
		{"commit", "-m", "initial"},
	} {
		runPickerGit(t, project, args...)
	}
	now := time.Date(2026, time.August, 16, 13, 0, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	var created commandResult
	if err := json.Unmarshal(executeCommand(t, newCodeCommand(deps), "follow branch", "--json"), &created); err != nil {
		t.Fatal(err)
	}

	deps = testDependencies(home, project, &now)
	deps.terminal = func(io.Reader, io.Writer, bool) tui.TerminalCapabilities {
		return tui.TerminalCapabilities{Interactive: true}
	}
	deps.newPickerSource = func(*thread.Store, thread.ProjectIdentity) (codingpicker.Source, error) {
		return staticPickerSource{}, nil
	}
	deps.runPicker = func(context.Context, codingpicker.Source, tui.PickerOptions) (tui.PickerSelection, error) {
		runPickerGit(t, project, "switch", "-c", "feature/resumed")
		return tui.PickerSelection{ThreadID: created.ThreadID}, nil
	}
	var admitted thread.ProjectIdentity
	deps.newController = func(request codingTurnRequest, resumed bool) (frontend.Controller, error) {
		if !resumed {
			t.Fatal("resume controller was not marked resumed")
		}
		admitted = request.Metadata.Project
		projector, err := frontend.NewProjector(created.ThreadID, frontend.ProjectionLimits{})
		if err != nil {
			return nil, err
		}
		projector.Open(true)
		return &interactiveLeaseController{Projector: projector, lease: request.Lease}, nil
	}
	deps.runTUI = func(ctx context.Context, controller frontend.Controller, _ tui.Options) error {
		return controller.Close(ctx)
	}
	executeCommand(t, newResumeCommand(deps))

	current, err := thread.ResolveProject(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.GitBranch != "feature/resumed" || admitted != current {
		t.Fatalf("admitted project = %+v, want current %+v", admitted, current)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Load(created.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Project != current {
		t.Fatalf("persisted project = %+v, want current %+v", metadata.Project, current)
	}
}

func stringsContainAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}
