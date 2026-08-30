package tui

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
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
