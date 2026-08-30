package tui

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNormalizeAttachmentPath(t *testing.T) {
	temp := t.TempDir()
	path := filepath.Join(temp, "trace output.log")
	fileURL := (&url.URL{Scheme: "file", Path: path}).String()
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "quoted", value: `"` + path + `"`, want: path},
		{name: "single quoted", value: `'` + path + `'`, want: path},
		{name: "shell escaped", value: strings.ReplaceAll(path, " ", `\ `), want: path},
		{name: "file URL", value: fileURL, want: path},
		{name: "Windows drive", value: `C:\Users\Alice\screen.png`, want: filepath.Clean(`C:\Users\Alice\screen.png`)},
		{name: "UNC", value: `\\server\share\screen.png`, want: filepath.Clean(`\\server\share\screen.png`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeAttachmentPath(test.value)
			if err != nil || got != test.want {
				t.Fatalf("normalizeAttachmentPath(%q) = %q, %v; want %q", test.value, got, err, test.want)
			}
		})
	}
	if _, err := normalizeAttachmentPath("one two"); err == nil {
		t.Fatal("unquoted multiple path words were accepted")
	}
	if _, err := normalizeAttachmentPath("file://remote/share/image.png"); err == nil {
		t.Fatal("remote file URL was accepted")
	}
}

func TestNormalizeAttachmentPathsKeepsQuotedWindowsBatchesSeparate(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{
			name:  "drives",
			value: `"C:\one.png" "D:\two.png"`,
			want:  []string{`C:\one.png`, `D:\two.png`},
		},
		{
			name:  "UNC shares",
			value: `"\\server\one\a.png" "\\server\two\b.png"`,
			want:  []string{`\\server\one\a.png`, `\\server\two\b.png`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeAttachmentPaths(test.value)
			if err != nil || !slices.Equal(got, test.want) {
				t.Fatalf("normalizeAttachmentPaths(%q) = %#v, %v; want %#v", test.value, got, err, test.want)
			}
		})
	}
}

func TestNormalizeAttachmentPathDecodesPOSIXMetacharacterEscapes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report(final).png")
	escaped := strings.NewReplacer("(", `\(`, ")", `\)`).Replace(path)
	got, err := normalizeAttachmentPath(escaped)
	if err != nil || got != path {
		t.Fatalf("normalizeAttachmentPath(%q) = %q, %v; want %q", escaped, got, err, path)
	}
}

func TestDuplicateComposerLabelsDoNotRetainRemovedPayload(t *testing.T) {
	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(t.TempDir(), "report.log")
	second := filepath.Join(t.TempDir(), "report.log")
	for _, path := range []string{first, second} {
		if err = os.WriteFile(path, []byte(path), 0o600); err != nil {
			t.Fatal(err)
		}
		if err = model.addComposerAttachment(path, "", "text/plain", false, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := model.ComposerValue(); got != "[File: report.log][File: report.log #2]" {
		t.Fatalf("duplicate labels = %q", got)
	}
	model.composer.SetValue("[File: report.log #2]")
	model.pruneDetachedAttachments()
	if len(model.composerAttachments) != 1 || model.composerAttachments[0].input.Path != second {
		t.Fatalf("retained attachments = %+v", model.composerAttachments)
	}
	submission := model.prepareSubmission(model.ComposerValue())
	if submission.input.Text != "" || len(submission.input.Attachments) != 1 ||
		submission.input.Attachments[0].Path != second {
		t.Fatalf("duplicate-label submission = %+v", submission.input)
	}
}

func TestCloseRichInputRemovesOnlyComposerOwnedFiles(t *testing.T) {
	model, err := newTestModel(newController(t))
	if err != nil {
		t.Fatal(err)
	}
	paste := strings.Repeat("x", largePasteRuneThreshold+1)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(paste), Paste: true})
	ownedPath := model.composerAttachments[0].input.Path
	directory := filepath.Dir(ownedPath)
	callerPath := filepath.Join(t.TempDir(), "caller.log")
	if err = os.WriteFile(callerPath, []byte("caller data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = model.addComposerAttachment(callerPath, "", "", false, false); err != nil {
		t.Fatal(err)
	}
	if err = model.closeRichInput(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(ownedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("composer-owned paste remains: %v", err)
	}
	if _, err = os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private paste directory remains: %v", err)
	}
	if _, err = os.Stat(callerPath); err != nil {
		t.Fatalf("caller-owned attachment was removed: %v", err)
	}
}
