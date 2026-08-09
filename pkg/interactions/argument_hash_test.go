package interactions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashArgumentsCanonicalRestartStableAndSecretSafe(t *testing.T) {
	workspace := t.TempDir()
	first, err := HashArguments(workspace, map[string]any{
		"z": []any{map[string]any{"secret": "low-entropy-token", "n": 1}},
		"a": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashArguments(workspace, map[string]any{
		"a": true,
		"z": []any{map[string]any{"n": 1, "secret": "low-entropy-token"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 || strings.Contains(first, "low-entropy-token") {
		t.Fatalf("argument hashes = %q, %q", first, second)
	}
	info, err := os.Stat(argumentHashKeyPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("argument hash key mode = %o", info.Mode().Perm())
	}
}

func TestHashArgumentsAtPathUsesExactRuntimeKey(t *testing.T) {
	root := t.TempDir()
	keyPath := filepath.Join(root, "runtime", "interaction_hmac.key")
	if _, err := HashArgumentsAtPath(keyPath, map[string]any{"approved": true}); err != nil {
		t.Fatalf("HashArgumentsAtPath() error = %v", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("exact argument hash key missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatalf("HashArgumentsAtPath() created legacy state directory: %v", err)
	}
}

func TestHashArgumentsUsesWorkspaceScopedKey(t *testing.T) {
	args := map[string]any{"token": "same"}
	first, err := HashArguments(t.TempDir(), args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashArguments(t.TempDir(), args)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("separate workspaces reused an approval hash key")
	}
}
