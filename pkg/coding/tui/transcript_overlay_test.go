package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

func TestTranscriptOverlaySearchesCompleteCommandErrorAndDiffEvidence(t *testing.T) {
	projector, err := frontend.NewProjector("thread-search", frontend.ProjectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	projector.TurnStarted("turn-1", "Inspect CAFÉ")
	projector.Error("turn-1", "error-1", "diagnostic Ошибка")
	projector.ToolStarted("turn-1", "command-1", "exec", "fields: command")
	exitCode := 7
	projector.ToolCommandOutput("turn-1", "command-1", frontend.CommandState{
		Command: "go test ./...", Status: frontend.CommandFailed, ExitCode: &exitCode,
		Transcript: []frontend.CommandTranscriptEntry{{Sequence: 1, Stream: "stderr", Text: "hidden failure\n"}},
	})
	projector.ToolCompleted("turn-1", "command-1", "exec", "tests failed", time.Second, true, nil)
	projector.ToolStarted("turn-1", "diff-1", "repository_diff", "{}")
	projector.ToolRepositoryDiff("turn-1", "diff-1", repositoryDiffRenderFixture())
	projector.ToolCompleted("turn-1", "diff-1", "repository_diff", "", 0, false, nil)
	model, err := newTestModel(&fakeController{Projector: projector})
	if err != nil {
		t.Fatal(err)
	}
	model.resize(72, 15)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlT})
	lines := model.transcriptOverlayLines()

	for _, query := range []string{"café", "ОШИБКА", "hidden failure", "+func NewType"} {
		model.transcriptOverlay.queryInput.SetValue(query)
		model.transcriptOverlay.applySearch(lines)
		if len(model.transcriptOverlay.matches) == 0 {
			t.Fatalf("search %q found no match in %q", query, overlayPlainText(lines))
		}
		selected := lines[model.transcriptOverlay.selected].text
		if !strings.Contains(strings.ToLower(selected), strings.ToLower(query)) {
			t.Fatalf("search %q selected %q", query, selected)
		}
		if model.transcriptOverlay.followBottom {
			t.Fatalf("search %q kept live-tail following enabled", query)
		}
	}
}

func TestTranscriptOverlayMatchNavigationIsRelativeAndWraps(t *testing.T) {
	lines := []transcriptOverlayLine{
		{key: "0", text: "match zero"},
		{key: "1", text: "unrelated"},
		{key: "2", text: "MATCH two"},
		{key: "3", text: "unrelated"},
		{key: "4", text: "match four"},
	}
	state := newTranscriptOverlayState()
	state.queryInput.SetValue("match")
	state.selected = 1
	state.applySearch(lines)
	if state.selected != 2 {
		t.Fatalf("initial search selected line %d, want 2", state.selected)
	}
	state.selectLine(lines, 3)
	state.moveMatch(lines, 1)
	if state.selected != 4 {
		t.Fatalf("next relative match selected line %d, want 4", state.selected)
	}
	state.moveMatch(lines, 1)
	if state.selected != 0 {
		t.Fatalf("wrapped next match selected line %d, want 0", state.selected)
	}
	state.selectLine(lines, 1)
	state.moveMatch(lines, -1)
	if state.selected != 0 {
		t.Fatalf("previous relative match selected line %d, want 0", state.selected)
	}
	state.moveMatch(lines, -1)
	if state.selected != 4 {
		t.Fatalf("wrapped previous match selected line %d, want 4", state.selected)
	}
}

func TestTranscriptOverlayAcceptsMultiRuneUnicodeSearchInput(t *testing.T) {
	controller := newController(t)
	controller.Error("turn-1", "error-1", "Ошибка 界")
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.resize(64, 10)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlT})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ОШИБКА 界")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.transcriptOverlay.query != "ОШИБКА 界" || len(model.transcriptOverlay.matches) != 1 {
		t.Fatalf(
			"Unicode search query=%q matches=%v",
			model.transcriptOverlay.query,
			model.transcriptOverlay.matches,
		)
	}
}

func TestTranscriptOverlayRestoresPanelFocusToolSelectionAndSemanticScroll(t *testing.T) {
	controller := newController(t)
	controller.TurnStarted("turn-1", "inspect")
	controller.ToolStarted("turn-1", "call-1", "exec", "{}")
	controller.ToolOutput("turn-1", "call-1", "tool output")
	controller.ToolCompleted("turn-1", "call-1", "exec", "", 0, false, nil)
	for index := range 20 {
		controller.Warning("turn-1", "warning-"+string(rune('a'+index)), "retained warning line")
	}
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.resize(60, 10)
	model.focused = true
	model.composer.SetValue("unsubmitted draft")
	model.composer.Focus()
	model.commandPanel = commandPanelDiff
	model.commandPanelOffset = 2
	model.selectedToolID = toolViewID(model.snapshot.Tools[0])
	model.toolSelectionActive = true
	model.viewport.SetYOffset(4)
	wantPosition := model.captureViewportPosition()

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlT})
	if !model.transcriptOverlay.active || model.composer.Focused() ||
		strings.Contains(model.View(), "unsubmitted draft") {
		t.Fatalf("overlay did not exclusively own focus/view: %q", model.View())
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyUp})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.transcriptOverlay.active || !model.composer.Focused() || model.commandPanel != commandPanelDiff ||
		model.commandPanelOffset != 2 || !model.toolSelectionActive {
		t.Fatalf(
			"restored state active=%t focus=%t panel=%d offset=%d tool=%t",
			model.transcriptOverlay.active,
			model.composer.Focused(),
			model.commandPanel,
			model.commandPanelOffset,
			model.toolSelectionActive,
		)
	}
	if got := model.captureViewportPosition(); got != wantPosition {
		t.Fatalf("restored viewport = %+v, want %+v", got, wantPosition)
	}
}

func TestTranscriptOverlayCopyIsPlainAndIgnoresStaleResults(t *testing.T) {
	controller := newController(t)
	controller.TurnStarted(
		"turn-1",
		"safe\x1b]8;;https://example.invalid\a link\x1b]8;;\a \u202eevil שלום 👩🏽‍💻",
	)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.resize(80, 12)
	var copied string
	model.writeClipboardText = func(_ context.Context, text string) error {
		copied = text
		return nil
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlT})
	lines := model.transcriptOverlayLines()
	plain := overlayPlainText(lines)
	if strings.ContainsAny(plain, "\x1b\a") || strings.ContainsRune(plain, '\u202e') ||
		strings.Contains(plain, "https://example.invalid") {
		t.Fatalf("overlay retained unsafe terminal/link data: %q", plain)
	}
	for _, line := range lines {
		if ansi.StringWidth(line.text) > model.width-2 {
			t.Fatalf("overlay line exceeds content width: %q", line.text)
		}
	}
	selected := findOverlayLine(t, lines, "safe")
	model.transcriptOverlay.selectLine(lines, selected)
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	model = updated.(*Model)
	if command == nil {
		t.Fatal("copy key returned no command")
	}
	rawMessage := command()
	message, ok := rawMessage.(TranscriptCopyMsg)
	if !ok {
		t.Fatalf("copy command returned %T", rawMessage)
	}
	if strings.ContainsAny(copied, "\x1b\a") || strings.ContainsRune(copied, '\u202e') ||
		!strings.Contains(copied, "link") || !strings.Contains(copied, "שלום") || !strings.Contains(copied, "👩🏽‍💻") {
		t.Fatalf("copied unsafe or incomplete line %q", copied)
	}
	model = updateModel(t, model, message)
	if model.transcriptOverlay.notice != "Copied line" {
		t.Fatalf("copy notice = %q", model.transcriptOverlay.notice)
	}
	copied = ""
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}})
	model = updated.(*Model)
	message = command().(TranscriptCopyMsg)
	if !strings.Contains(copied, "safe") || !strings.Contains(copied, "👩🏽‍💻") ||
		strings.ContainsRune(copied, '\u202e') {
		t.Fatalf("copied full transcript = %q", copied)
	}
	model = updateModel(t, model, message)
	if model.transcriptOverlay.notice != "Copied full transcript" {
		t.Fatalf("full copy notice = %q", model.transcriptOverlay.notice)
	}

	model.writeClipboardText = func(context.Context, string) error { return errors.New("denied") }
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	model = updated.(*Model)
	model = updateModel(t, model, command())
	if model.transcriptOverlay.notice != "Copy failed: denied" {
		t.Fatalf("failed copy notice = %q", model.transcriptOverlay.notice)
	}

	requestID := model.transcriptOverlay.copyRequestID
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	model = updateModel(t, model, TranscriptCopyMsg{RequestID: requestID, Scope: "line", Err: errors.New("late")})
	if model.transcriptOverlay.notice == "Copy failed: late" {
		t.Fatal("stale copy result mutated the closed overlay")
	}
}

func TestTranscriptOverlayPagesOlderHistoryAndReturnsToLatest(t *testing.T) {
	controller := &pagedController{
		fakeController: newController(t),
		pages: map[int]frontend.TranscriptPage{
			10: {
				Entries: []frontend.TranscriptEntry{{
					ID: "older", Kind: frontend.EntryWarning, Text: "older retained evidence",
				}},
				Start: 0, End: 10, Total: 11, HasOlder: false, HasNewer: true,
			},
			-1: {
				Entries: []frontend.TranscriptEntry{{
					ID: "latest", Kind: frontend.EntryError, Text: "latest retained evidence",
				}},
				Start: 10, End: 11, Total: 11, HasOlder: true,
			},
		},
	}
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.resize(64, 10)
	model = updateModel(t, model, TranscriptPageMsg{Page: frontend.TranscriptPage{
		Entries: []frontend.TranscriptEntry{{
			ID: "latest", Kind: frontend.EntryError, Text: "latest retained evidence",
		}},
		Start: 10, End: 11, Total: 11, HasOlder: true,
	}, Mode: transcriptPageInitial})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlT})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyHome})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	model = updated.(*Model)
	if command == nil || !model.transcript.loading {
		t.Fatalf("older page did not start: command=%v loading=%t", command, model.transcript.loading)
	}
	model = updateModel(t, model, command())
	if len(controller.requests) != 1 || controller.requests[0].Before != 10 ||
		!strings.Contains(overlayPlainText(model.transcriptOverlayLines()), "older retained evidence") {
		t.Fatalf(
			"older requests=%+v transcript=%q",
			controller.requests,
			overlayPlainText(model.transcriptOverlayLines()),
		)
	}

	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnd})
	model = updated.(*Model)
	if command == nil || !model.transcript.loading {
		t.Fatalf("latest page did not start: command=%v loading=%t", command, model.transcript.loading)
	}
	model = updateModel(t, model, command())
	if len(controller.requests) != 2 || controller.requests[1].Before != -1 || model.transcript.hasNewer {
		t.Fatalf("latest requests=%+v state=%+v", controller.requests, model.transcript)
	}
}

func TestTranscriptOverlaySelectionSurvivesHistoryPrependAndResize(t *testing.T) {
	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}
	model.resize(64, 12)
	model = updateModel(t, model, TranscriptPageMsg{Page: frontend.TranscriptPage{
		Entries: []frontend.TranscriptEntry{{
			ID: "current", Kind: frontend.EntryError, Text: "current selected evidence",
		}},
		Start: 10, End: 11, Total: 11, HasOlder: true,
	}, Mode: transcriptPageInitial})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlT})
	lines := model.transcriptOverlayLines()
	model.transcriptOverlay.selectLine(lines, findOverlayLine(t, lines, "current selected"))
	wantKey := model.transcriptOverlay.selectedKey

	model = updateModel(t, model, TranscriptPageMsg{Page: frontend.TranscriptPage{
		Entries: []frontend.TranscriptEntry{{ID: "older", Kind: frontend.EntryWarning, Text: "older evidence"}},
		Start:   0, End: 10, Total: 11, HasOlder: false, HasNewer: true,
	}, Mode: transcriptPageOlder})
	model.resize(41, 9)
	if model.transcriptOverlay.selectedKey != wantKey {
		t.Fatalf("selection key after prepend/resize = %q, want %q", model.transcriptOverlay.selectedKey, wantKey)
	}
	lines = model.transcriptOverlayLines()
	if !strings.Contains(lines[model.transcriptOverlay.selected].text, "current selected") {
		t.Fatalf("selection after prepend/resize = %+v", lines[model.transcriptOverlay.selected])
	}
}

func TestTranscriptOverlayHelpAndTinyViewsRemainKeyboardDiscoverableAndBounded(t *testing.T) {
	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyCtrlT})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	help := model.View()
	for _, want := range []string{"no mouse or color required", "PgUp/PgDown", "n/N", "Ctrl+L", "c", "Esc"} {
		if !strings.Contains(help, want) {
			t.Fatalf("transcript help omits %q: %q", want, help)
		}
	}

	for _, size := range []struct{ width, height int }{{1, 1}, {12, 1}, {18, 2}, {24, 3}, {40, 8}} {
		model.resize(size.width, size.height)
		view := model.View()
		viewLines := strings.Split(view, "\n")
		if len(viewLines) > size.height {
			t.Fatalf("view %dx%d emitted %d rows: %q", size.width, size.height, len(viewLines), view)
		}
		for _, line := range viewLines {
			if ansi.StringWidth(line) > size.width {
				t.Fatalf("view %dx%d emitted wide line %q", size.width, size.height, line)
			}
		}
	}
	model.resize(40, 1)
	if !strings.Contains(model.View(), "Esc close") {
		t.Fatalf("one-row overlay lacks exit hint: %q", model.View())
	}
}

func TestSlashTranscriptOpensOverlayWithoutSubmitting(t *testing.T) {
	controller := newController(t)
	model, err := newTestModel(controller)
	if err != nil {
		t.Fatal(err)
	}
	model.composer.SetValue("/transcript")
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !model.transcriptOverlay.active || model.ComposerValue() != "" || controller.submits.Load() != 0 {
		t.Fatalf(
			"slash transcript active=%t draft=%q submits=%d",
			model.transcriptOverlay.active,
			model.ComposerValue(),
			controller.submits.Load(),
		)
	}
}

func findOverlayLine(t *testing.T, lines []transcriptOverlayLine, text string) int {
	t.Helper()
	for index, line := range lines {
		if strings.Contains(line.text, text) {
			return index
		}
	}
	t.Fatalf("overlay omits %q: %q", text, overlayPlainText(lines))
	return -1
}

func overlayPlainText(lines []transcriptOverlayLine) string {
	plain := make([]string, len(lines))
	for index, line := range lines {
		plain[index] = line.text
	}
	return strings.Join(plain, "\n")
}
