package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPresetRejectsWindowsVolumeRoots(t *testing.T) {
	for _, root := range []string{`C:\`, `C:/`, `\\server\share`, `\\server\share\`} {
		t.Run(root, func(t *testing.T) {
			preset := Preset{
				Version: Version, ID: "volume-root", ConfigPath: filepath.Join(root, "profile", "config.json"),
				ExpectedAgent: "main", ExpectedWorkspace: filepath.Join(root, "profile", "workspace"),
				IsolationRoot: root, Capabilities: []string{"conversation"},
			}
			data, err := json.Marshal(preset)
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(t.TempDir(), "preset.json")
			if err = os.WriteFile(name, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err = LoadPreset(name); !errors.Is(err, ErrInvalidPreset) {
				t.Fatal("filesystem-wide Windows boundary admitted")
			}
		})
	}
}
