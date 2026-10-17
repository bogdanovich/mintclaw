//go:build darwin

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopPackagerNeverReplacesAnApplication(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "operator config.json")
	if err := os.WriteFile(config, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(dir, "MintClaw Companion.app")
	script := filepath.Join("..", "..", "scripts", "build-macos-companion.sh")
	args := []string{script, "--binary", binary, "--config", config, "--output", app, "--development-ad-hoc"}
	if output, buildErr := exec.CommandContext(t.Context(), "/bin/bash", args...).CombinedOutput(); buildErr != nil {
		t.Fatalf("build private test bundle: %v: %s", buildErr, output)
	}
	plist := filepath.Join(app, "Contents", "Info.plist")
	before, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	if output, replaceErr := exec.CommandContext(t.Context(), "/bin/bash", args...).
		CombinedOutput(); replaceErr == nil ||
		!strings.Contains(string(output), "existing applications are never replaced") {
		t.Fatalf("existing output accepted: %v: %s", replaceErr, output)
	}
	after, err := os.ReadFile(plist)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("existing application was modified", err)
	}
	if output, getErr := exec.CommandContext(t.Context(), "/usr/bin/plutil", "-extract", "MintClawNodeConfigPath",
		"raw", "-o", "-", plist).CombinedOutput(); getErr != nil || strings.TrimSpace(string(output)) != config {
		t.Fatalf("config reference not preserved: %v: %s", getErr, output)
	}
}

func TestDesktopPackagerRequiresExplicitSigningChoice(t *testing.T) {
	script := filepath.Join("..", "..", "scripts", "build-macos-companion.sh")
	for _, args := range [][]string{
		{script},
		{script, "--sign-identity", "-"},
		{script, "--sign-identity", "not-used", "--development-ad-hoc"},
		{script, "--development-ad-hoc", "--sign-identity", "not-used"},
	} {
		if output, err := exec.CommandContext(t.Context(), "/bin/bash", args...).CombinedOutput(); err == nil {
			t.Fatalf("ambiguous signing accepted: %q: %s", args, output)
		}
	}
}
