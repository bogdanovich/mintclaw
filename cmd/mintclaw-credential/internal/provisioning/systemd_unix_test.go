//go:build linux || darwin

package provisioning

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEncryptionCommandHasNoInheritedEnvironmentOrTokenArguments(t *testing.T) {
	t.Setenv("MINTCLAW_SETUP_UNTRUSTED", "SYNTHETIC_INHERITED_CANARY")
	token := []byte("SYNTHETIC_STDIN_TOKEN_CANARY")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := encryptionCommand(ctx, token)
	metadata := strings.Join(append(command.Args, command.Env...), "\n")
	if strings.Contains(metadata, string(token)) || strings.Contains(metadata, "SYNTHETIC_INHERITED_CANARY") ||
		command.Path != encryptionTool || command.Stderr != io.Discard || command.WaitDelay != time.Second {
		t.Fatal("private encryption command leaked authority or lost its bounds")
	}
	if !strings.Contains(metadata, "--with-key=auto") || !strings.Contains(metadata, "--name="+credentialName) {
		t.Fatal("OS encryption mode or credential name changed")
	}
	input, err := io.ReadAll(command.Stdin)
	if err != nil || !bytes.Equal(input, token) {
		t.Fatal("private pipe lost the token input")
	}
}

func TestEncryptionFailuresHaveFixedOutputAndBoundedLifetime(t *testing.T) {
	for _, mode := range []string{"success", "fail", "overflow", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if mode == "timeout" {
				var shortCancel context.CancelFunc
				ctx, shortCancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer shortCancel()
			}
			command := encryptionCommand(ctx, []byte("SYNTHETIC_CHILD_STDIN_CANARY"))
			var err error
			command.Path, err = os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command.Args = []string{command.Path, "-test.run=^TestEncryptionSubprocessFixture$"}
			command.Env = append(command.Env, "MINTCLAW_SETUP_TEST_MODE="+mode)
			started := time.Now()
			output, err := runEncryption(command)
			if mode == "success" {
				if err != nil || !bytes.Equal(output, fakeCiphertext()) {
					t.Fatal("bounded fake encryption output was lost")
				}
			} else if !errors.Is(err, ErrEncryption) || output != nil || strings.Contains(err.Error(), "CANARY") {
				t.Fatal("failed helper output or private diagnostic escaped")
			}
			if time.Since(started) > 6*time.Second {
				t.Fatal("private encryption helper exceeded its bounded lifetime")
			}
		})
	}
}

func TestEncryptionSubprocessFixture(t *testing.T) {
	mode := os.Getenv("MINTCLAW_SETUP_TEST_MODE")
	if mode == "" {
		return
	}
	input, _ := io.ReadAll(os.Stdin)
	// Every fixture intentionally emits synthetic private text on stderr. The
	// parent must discard it, even when the helper exits unsuccessfully.
	_, _ = os.Stderr.Write(input)
	switch mode {
	case "success":
		_, _ = os.Stdout.Write(fakeCiphertext())
	case "overflow":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{'A'}, maxCiphertextBytes+1))
	case "timeout":
		time.Sleep(time.Minute)
	case "fail":
		os.Exit(1)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func TestUntrustedEncryptionExecutableIsRejected(t *testing.T) {
	for _, mode := range []string{"safe", "symlink", "writable", "setuid", "parent_writable"} {
		t.Run(mode, func(t *testing.T) {
			root, path, owner := fixtureRoot(t)
			bin := filepath.Join(path, "usr", "bin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			tool := filepath.Join(bin, "systemd-creds")
			if mode == "symlink" {
				if err := os.Symlink(filepath.Join(t.TempDir(), "tool"), tool); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(tool, []byte("synthetic executable fixture"), 0o755); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "writable":
				if err := os.Chmod(tool, 0o777); err != nil {
					t.Fatal(err)
				}
			case "setuid":
				if err := os.Chmod(tool, 0o755|os.ModeSetuid); err != nil {
					t.Fatal(err)
				}
			case "parent_writable":
				if err := os.Chmod(bin, 0o777); err != nil {
					t.Fatal(err)
				}
			}
			err := checkEncryptionTool(root, owner)
			if (mode == "safe" && err != nil) || (mode != "safe" && !errors.Is(err, ErrEncryption)) {
				t.Fatal("untrusted encryption executable admission was incorrect")
			}
		})
	}
}
