package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPresetValidationIsOffline(t *testing.T) {
	root := t.TempDir()
	preset := Preset{
		Version: Version, ID: "local-eval", ConfigPath: "profile/does-not-exist.json",
		ExpectedAgent: "main", ExpectedWorkspace: "profile/workspace", IsolationRoot: "profile",
		Capabilities: []string{"conversation"},
	}
	data, err := json.Marshal(preset)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, "preset.json")
	if err = os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPreset(name)
	if err != nil || loaded.ConfigPath != filepath.Join(root, preset.ConfigPath) ||
		loaded.ExpectedWorkspace != filepath.Join(root, preset.ExpectedWorkspace) {
		t.Fatal("preset did not resolve paths without runtime access", err)
	}
	for _, mutate := range []func(*Preset){
		func(p *Preset) { p.Version++ }, func(p *Preset) { p.ID = "../secret" },
		func(p *Preset) { p.ConfigPath = "https://secret.invalid/config" },
		func(p *Preset) { p.ExpectedAgent = "Wrong Agent" }, func(p *Preset) { p.IsolationRoot = "/" },
		func(p *Preset) { p.UserModel = "../provider" },
		func(p *Preset) { p.Capabilities = []string{"conversation", "conversation"} },
		func(p *Preset) { p.WorkspaceFiles = []WorkspaceFile{{Path: "sessions/old.json", SHA256: digest(nil)}} },
		func(p *Preset) {
			p.WorkspaceFiles = []WorkspaceFile{{Path: "skills/../AGENTS.md", SHA256: digest(nil)}}
		},
		func(p *Preset) { p.WorkspaceFiles = []WorkspaceFile{{Path: "AGENTS.md", SHA256: "bad"}} },
		func(p *Preset) {
			p.WorkspaceFiles = []WorkspaceFile{{Path: "AGENTS.md", SHA256: digest(nil)}, {Path: "AGENTS.md", SHA256: digest(nil)}}
		},
		func(p *Preset) {
			p.WorkspaceFiles = []WorkspaceFile{{Path: "skills/a", SHA256: digest(nil)}, {Path: "skills/a/b", SHA256: digest(nil)}}
		},
		func(p *Preset) {
			p.WorkspaceFiles = []WorkspaceFile{{Path: "skills/A.md", SHA256: digest(nil)}, {Path: "skills/a.md", SHA256: digest(nil)}}
		},
	} {
		invalid := preset
		mutate(&invalid)
		if !errors.Is(invalid.Validate(), ErrInvalidPreset) {
			t.Fatal("invalid preset accepted")
		}
	}
}

func TestPresetRejectsPathsOutsideIsolation(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name      string
		config    string
		workspace string
		isolation string
	}{
		{"config outside", "elsewhere/config.json", "profile/workspace", "profile"},
		{"workspace outside", "profile/config.json", "../workspace", "profile"},
		{"prefix collision", "profile-extra/config.json", "profile/workspace", "profile"},
		{"config equals root", "profile", "profile/workspace", "profile"},
		{"filesystem root", "profile/config.json", "profile/workspace", "profile/../../../.."},
	} {
		t.Run(test.name, func(t *testing.T) {
			preset := Preset{
				Version: Version, ID: "local-eval", ConfigPath: test.config, ExpectedAgent: "main",
				ExpectedWorkspace: test.workspace, IsolationRoot: test.isolation,
				Capabilities: []string{"conversation"},
			}
			if test.name == "filesystem root" {
				preset.IsolationRoot = filepath.VolumeName(root) + string(filepath.Separator)
			}
			data, err := json.Marshal(preset)
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(root, "preset.json")
			if err = os.WriteFile(name, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err = LoadPreset(name); !errors.Is(err, ErrInvalidPreset) {
				t.Fatal("preset admitted an invalid isolation boundary")
			}
		})
	}
}
