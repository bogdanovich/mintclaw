//go:build linux || darwin

package provisioning

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fixtureRoot(t *testing.T) (*os.File, string, uint32) {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file, path, uint32(os.Getuid())
}

func fakeCiphertext() []byte {
	return []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xa5}, 128)) + "\n")
}

func credentialPath(path string) string {
	return filepath.Join(path, "etc", "credstore.encrypted", "mintclaw-credential", credentialName)
}

func TestProvisionStoresOnlyBoundedCiphertextAndClearsInput(t *testing.T) {
	root, path, owner := fixtureRoot(t)
	token := []byte("SYNTHETIC_PRIVATE_TOKEN_CANARY")
	ciphertext := fakeCiphertext()
	want := bytes.Clone(ciphertext)
	err := provisionAt(context.Background(), root, owner,
		func(ctx context.Context) ([]byte, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 5*time.Minute {
				t.Fatal("private input has no bounded deadline")
			}
			return token, nil
		},
		func(ctx context.Context, input []byte) ([]byte, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 30*time.Second || !bytes.Equal(input, token) {
				t.Fatal("encryption input or deadline is incorrect")
			}
			return ciphertext, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(credentialPath(path))
	if err != nil || !bytes.Equal(data, want) || bytes.Contains(data, []byte("SYNTHETIC_PRIVATE_TOKEN_CANARY")) {
		t.Fatal("storage did not contain only the expected ciphertext")
	}
	info, err := os.Stat(credentialPath(path))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("encrypted credential is not owner-only")
	}
	entries, err := os.ReadDir(filepath.Dir(credentialPath(path)))
	if err != nil || len(entries) != 1 || entries[0].Name() != credentialName {
		t.Fatal("temporary encrypted credential was not cleaned up")
	}
	if !allZero(token) || !allZero(ciphertext) {
		t.Fatal("owned private buffers were retained")
	}
}

func TestUnsafeStorageFailsBeforePrivateInput(t *testing.T) {
	for _, scenario := range []string{"owner", "etc_write", "parent_link", "private_link", "private_public", "parent_file"} {
		t.Run(scenario, func(t *testing.T) {
			root, path, owner := fixtureRoot(t)
			parent := filepath.Join(path, "etc", "credstore.encrypted")
			private := filepath.Join(parent, "mintclaw-credential")
			switch scenario {
			case "owner":
				owner++
			case "etc_write":
				if err := os.Chmod(filepath.Join(path, "etc"), 0o777); err != nil {
					t.Fatal(err)
				}
			case "parent_link":
				if err := os.Symlink(t.TempDir(), parent); err != nil {
					t.Fatal(err)
				}
			case "private_link", "private_public":
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatal(err)
				}
				if scenario == "private_link" {
					if err := os.Symlink(t.TempDir(), private); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Mkdir(private, 0o755); err != nil {
					t.Fatal(err)
				}
			case "parent_file":
				if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			err := provisionAt(context.Background(), root, owner,
				func(context.Context) ([]byte, error) { called = true; return nil, nil },
				func(context.Context, []byte) ([]byte, error) { called = true; return nil, nil })
			if !errors.Is(err, ErrStorage) || called {
				t.Fatal("unsafe storage admitted private input")
			}
		})
	}
}

func TestExistingCredentialNeverPromptsOrOverwrites(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		root, path, owner := fixtureRoot(t)
		dir, err := openCredentialDirectory(root, owner)
		if err != nil {
			t.Fatal(err)
		}
		_ = dir.Close()
		if symlink {
			if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), credentialPath(path)); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(credentialPath(path), []byte("existing encrypted fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		called := false
		err = provisionAt(context.Background(), root, owner,
			func(context.Context) ([]byte, error) { called = true; return nil, nil }, nil)
		if !errors.Is(err, ErrExists) || called {
			t.Fatal("existing credential admitted private input")
		}
	}
}

func TestPrivateFailuresClearBuffersAndDoNotCreateCredential(t *testing.T) {
	for _, scenario := range []string{"input", "invalid", "input_oversized", "encryption", "malformed", "oversized", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			root, path, owner := fixtureRoot(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			token := []byte("SYNTHETIC_FAILURE_TOKEN_CANARY")
			switch scenario {
			case "invalid":
				token = append(token, '\n')
			case "input_oversized":
				token = bytes.Repeat([]byte{'x'}, maxTokenBytes+1)
			}
			ciphertext := fakeCiphertext()
			switch scenario {
			case "malformed":
				ciphertext = []byte("raw synthetic provider diagnostic")
			case "oversized":
				ciphertext = bytes.Repeat([]byte{'A'}, maxCiphertextBytes+1)
			}
			err := provisionAt(ctx, root, owner,
				func(context.Context) ([]byte, error) {
					if scenario == "input" {
						return token, errors.New(string(token))
					}
					return token, nil
				},
				func(context.Context, []byte) ([]byte, error) {
					if scenario == "encryption" {
						return ciphertext, errors.New(string(token))
					}
					if scenario == "canceled" {
						cancel()
					}
					return ciphertext, nil
				})
			if err == nil || strings.Contains(err.Error(), "SYNTHETIC") || !allZero(token) {
				t.Fatal("private failure was accepted, echoed, or retained input")
			}
			if scenario != "input" && scenario != "invalid" && scenario != "input_oversized" && !allZero(ciphertext) {
				t.Fatal("failed helper output was retained")
			}
			if _, err := os.Lstat(credentialPath(path)); !os.IsNotExist(err) {
				t.Fatal("failed setup created a credential")
			}
		})
	}
}

func TestConcurrentSetupFailsBeforeSecondPrivateInput(t *testing.T) {
	root, _, owner := fixtureRoot(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	defer func() {
		close(release)
		select {
		case err := <-done:
			if !errors.Is(err, ErrInput) {
				t.Error("first fixture input did not terminate safely")
			}
		case <-time.After(2 * time.Second):
			t.Error("first fixture setup did not terminate")
		}
	}()
	go func() {
		done <- provisionAt(context.Background(), root, owner,
			func(context.Context) ([]byte, error) { close(entered); <-release; return nil, ErrInput }, nil)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first setup did not acquire the private lock")
	}
	called := false
	err := provisionAt(context.Background(), root, owner,
		func(context.Context) ([]byte, error) { called = true; return nil, nil }, nil)
	if !errors.Is(err, ErrBusy) || called {
		t.Fatal("concurrent setup admitted another token input")
	}
}

func TestCommitRefusesAConcurrentTarget(t *testing.T) {
	root, path, owner := fixtureRoot(t)
	err := provisionAt(context.Background(), root, owner,
		func(context.Context) ([]byte, error) { return []byte("synthetic-token"), nil },
		func(context.Context, []byte) ([]byte, error) {
			if err := os.WriteFile(credentialPath(path), []byte("competing encrypted fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			return fakeCiphertext(), nil
		})
	if !errors.Is(err, ErrExists) {
		t.Fatal("concurrent target was replaced")
	}
	data, err := os.ReadFile(credentialPath(path))
	if err != nil || string(data) != "competing encrypted fixture" {
		t.Fatal("competing target changed")
	}
}

func TestPublicProvisionRejectsBeforeFilesystemAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Provision(ctx); !errors.Is(err, ErrInput) {
		t.Fatal("canceled setup proceeded")
	}
	if runtime.GOOS == "darwin" {
		if err := Provision(context.Background()); !errors.Is(err, ErrUnsupported) {
			t.Fatal("unsupported setup proceeded")
		}
	} else if os.Getuid() != 0 || os.Geteuid() != 0 {
		if err := Provision(context.Background()); !errors.Is(err, ErrIdentity) {
			t.Fatal("non-operator setup proceeded")
		}
	}
}

func allZero(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}
