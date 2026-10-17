package eval

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestPrepareCLIRequiresExplicitSeedAndPrivateDestination(t *testing.T) {
	home, parent := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvHome, home)
	for _, args := range [][]string{
		{"prepare"},
		{"prepare", "--preset", "../../../../evals/presets/local.template.json"},
		{
			"prepare", "--preset", "../../../../evals/presets/local.template.json",
			"--out", parent, "--json",
		},
	} {
		command := NewEvalCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs(args)
		if err := command.Execute(); err == nil || strings.Contains(output.String(), `"outcome":"prepared"`) ||
			strings.Contains(output.String(), "MC_EVAL_CONTEXT") {
			t.Fatal("unprepared CLI source ran or claimed preparation")
		}
	}
	if _, err := os.Stat(filepath.Join(home, "config.json")); !os.IsNotExist(err) {
		t.Fatal("CLI created a default/production configuration", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("invalid CLI allocated a profile", err)
	}
}
