package interactions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type privateArgumentMarshalFailure struct{}

func (privateArgumentMarshalFailure) MarshalJSON() ([]byte, error) {
	return nil, errors.New("PRIVATE_ARGUMENT_ERROR_CANARY")
}

func TestVerifyArgumentHashUsesNativeFingerprintWithoutFileMutation(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "interaction_hmac.key")
	arguments := map[string]any{"z": []any{1, "private-argument"}, "a": true}
	expected, err := HashArgumentsAtPath(keyPath, arguments)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(key)
	if err := os.Chmod(keyPath, 0o400); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := VerifyArgumentHash(key, map[string]any{"a": true, "z": []any{1, "private-argument"}}, expected)
	if err != nil || !matched || !bytes.Equal(key, before) {
		t.Fatal("read-only verification differs from native hashing or changes the caller key")
	}
	matched, err = VerifyArgumentHash(key, map[string]any{"a": true, "z": []any{1, "changed"}}, expected)
	if err != nil || matched {
		t.Fatal("changed arguments matched the native binding")
	}
	after, err := os.Stat(keyPath)
	if err != nil || after.Mode() != info.Mode() || !after.ModTime().Equal(info.ModTime()) {
		t.Fatal("verification repaired or changed key file metadata")
	}
	data, err := os.ReadFile(keyPath)
	if err != nil || !bytes.Equal(data, before) {
		t.Fatal("verification changed key file bytes")
	}
}

func TestVerifyArgumentHashRefusesInvalidMaterialAndPrivateErrors(t *testing.T) {
	key := bytes.Repeat([]byte("k"), argumentHashKeySize)
	for name, test := range map[string]struct {
		key       []byte
		arguments map[string]any
		expected  string
	}{
		"missing key":  {expected: strings.Repeat("a", 64)},
		"short key":    {key: key[:len(key)-1], expected: strings.Repeat("a", 64)},
		"invalid hash": {key: key, expected: "private-invalid-hash"},
		"private marshal error": {
			key: key, expected: strings.Repeat("a", 64),
			arguments: map[string]any{"value": privateArgumentMarshalFailure{}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			matched, err := VerifyArgumentHash(test.key, test.arguments, test.expected)
			if matched || !errors.Is(err, ErrInvalidInteraction) ||
				strings.Contains(err.Error(), "PRIVATE_ARGUMENT_ERROR_CANARY") ||
				strings.Contains(err.Error(), "private-invalid-hash") {
				t.Fatal("invalid input authorized a match or exposed private error data")
			}
		})
	}
	expected, err := hashArguments(key, map[string]any{"allow": true})
	if err != nil {
		t.Fatal(err)
	}
	matched, err := VerifyArgumentHash(key, map[string]any{"allow": true}, strings.ToUpper(expected))
	if err != nil || matched {
		t.Fatal("verification disagrees with the native exact digest comparison")
	}
}

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
