package gateway

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestRunWithProviderCallBudgetRejectsNilBeforeStartup(t *testing.T) {
	home := t.TempDir()
	err := RunWithProviderCallBudget(false, home, filepath.Join(home, "absent.json"), false, nil)
	if !errors.Is(err, providers.ErrCallBudget) {
		t.Fatalf("nil budget = %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("invalid budget created runtime state before admission")
	}
}
