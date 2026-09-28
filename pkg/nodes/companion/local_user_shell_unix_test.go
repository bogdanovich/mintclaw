//go:build linux || darwin

package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestConfigNormalizesLocalUserOwnerShell(t *testing.T) {
	workingDirectory := t.TempDir()
	cfg, err := (Config{
		GatewayURL: "wss://gateway.example",
		OwnerShell: &OwnerShellConfig{
			Enabled: true,
			LocalUser: &LocalUserShellConfig{
				Revision: "local-owner-v1", Profile: "owner-user",
				ShellPath:                 "/bin/sh",
				WorkingScopes:             map[string]string{"home": workingDirectory},
				FixedEnvironment:          map[string]string{"OWNER_FIXED": "fixed"},
				PermittedEnvironmentNames: []string{"OWNER_SUPPLIED"},
				TimeoutSecondsMax:         30, OutputBytesMax: 8192, ConcurrentCommands: 1,
			},
		},
	}).Normalize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OwnerShell == nil || cfg.OwnerShell.LocalUser == nil ||
		cfg.OwnerShell.LocalUser.Profile != "owner-user" {
		t.Fatalf("normalized local-user shell = %#v", cfg.OwnerShell)
	}
	snapshot, _, err := NewLocalUserShellBroker(*cfg.OwnerShell.LocalUser)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Profiles[0].UID != uint32(os.Geteuid()) ||
		snapshot.Profiles[0].GID != uint32(os.Getegid()) {
		t.Fatalf("local-user identity = %#v", snapshot.Profiles[0])
	}
	resolved, err := filepath.EvalSymlinks(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OwnerShell.LocalUser.WorkingScopes["home"] != resolved {
		t.Fatalf("working scope = %q, want %q", cfg.OwnerShell.LocalUser.WorkingScopes["home"], resolved)
	}
}

func TestConfigRejectsAmbiguousOwnerShellExecutor(t *testing.T) {
	_, err := (Config{
		GatewayURL: "wss://gateway.example",
		OwnerShell: &OwnerShellConfig{
			Enabled:      true,
			BrokerSocket: "/tmp/authority.sock",
			LocalUser:    &LocalUserShellConfig{},
		},
	}).Normalize(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("Normalize() error = %v", err)
	}
}

func TestLocalUserShellExecRunsAsCompanionAccount(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Setenv("OWNER_SECRET", "must-not-be-inherited")
	config, err := normalizeLocalUserShellConfig(LocalUserShellConfig{
		Revision: "local-owner-v1", Profile: "owner-user", ShellPath: "/bin/sh",
		WorkingScopes:             map[string]string{"home": workingDirectory},
		FixedEnvironment:          map[string]string{"OWNER_FIXED": "fixed"},
		PermittedEnvironmentNames: []string{"OWNER_SUPPLIED"},
		TimeoutSecondsMax:         30, OutputBytesMax: 8192, ConcurrentCommands: 1,
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, broker, err := NewLocalUserShellBroker(config)
	if err != nil {
		t.Fatal(err)
	}
	policy := nodes.LocalCommandPolicy{
		Revision:          "local-owner-policy-v1",
		AllowedCommands:   []string{"shell.exec.v1"},
		MaximumRisk:       nodes.RiskPrivileged,
		MaxTimeoutSeconds: 30,
		MaxOutputBytes:    8192,
	}
	runtime, err := NewRuntime(
		nodes.ID("node_test"),
		"test",
		policy,
		newMemoryInvocationLedger(),
		WithShellBroker(snapshot, broker),
	)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := shellRuntimeDescriptor(t, runtime)
	if descriptor.SupportsCancel || descriptor.SupportsTerminal {
		t.Fatalf("local-user shell advertised unsupported controls: %#v", descriptor)
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), workingDirectory) || strings.Contains(string(encoded), "/bin/sh") {
		t.Fatalf("descriptor leaked local paths: %s", encoded)
	}
	input := json.RawMessage(
		`{"profile":"owner-user","script":"printf 'cwd=%s fixed=%s supplied=%s uid=%s secret=%s' \"$PWD\" \"$OWNER_FIXED\" \"$OWNER_SUPPLIED\" \"$(/usr/bin/id -u)\" \"${OWNER_SECRET-unset}\"; printf err >&2; exit 7","cwd":"home","env":{"OWNER_SUPPLIED":"supplied"},"timeout_seconds":5}`,
	)
	plan := testRuntimePlan(t, runtime, "shell.exec.v1", input)
	result, err := runtime.Invoke(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	var output ShellBrokerResult
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	want := "cwd=" + resolved + " fixed=fixed supplied=supplied uid=" + osUIDString() + " secret=unset"
	if output.ExitCode != 7 || output.Stdout != want || output.Stderr != "err" || output.Truncated {
		t.Fatalf("shell output = %#v, want stdout %q", output, want)
	}
}

func TestLocalUserShellCancellationIsUnknownAfterStart(t *testing.T) {
	workingDirectory := t.TempDir()
	readyPath := filepath.Join(workingDirectory, "ready")
	config, err := normalizeLocalUserShellConfig(LocalUserShellConfig{
		Revision: "local-owner-v1", Profile: "owner-user", ShellPath: "/bin/sh",
		WorkingScopes:     map[string]string{"home": workingDirectory},
		TimeoutSecondsMax: 30, OutputBytesMax: 8192, ConcurrentCommands: 1,
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, rawBroker, err := NewLocalUserShellBroker(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, executeErr := rawBroker.Execute(ctx, ShellBrokerRequest{
			InvocationID: "inv_local_cancel", PlanHash: strings.Repeat("a", 64),
			Profile: "owner-user", ProfileRevision: "local-owner-v1",
			Script:       "printf ready > " + shellSingleQuote(readyPath) + "; sleep 30",
			WorkingScope: "home", Environment: map[string]string{},
			TimeoutSeconds: 30, OutputBytesMax: 8192,
		})
		done <- executeErr
	}()
	waitForLocalUserShellFile(t, readyPath)
	cancel()
	if err := <-done; !errors.Is(err, ErrShellBrokerOutcomeUnknown) {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestLocalUserShellBoundsOutput(t *testing.T) {
	workingDirectory := t.TempDir()
	config, err := normalizeLocalUserShellConfig(LocalUserShellConfig{
		Revision: "local-owner-v1", Profile: "owner-user", ShellPath: "/bin/sh",
		WorkingScopes:     map[string]string{"home": workingDirectory},
		TimeoutSecondsMax: 30, OutputBytesMax: 8192, ConcurrentCommands: 1,
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, broker, err := NewLocalUserShellBroker(config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := broker.Execute(t.Context(), ShellBrokerRequest{
		InvocationID: "inv_local_output", PlanHash: strings.Repeat("b", 64),
		Profile: "owner-user", ProfileRevision: "local-owner-v1",
		Script: "printf 123456", WorkingScope: "home", Environment: map[string]string{},
		TimeoutSeconds: 5, OutputBytesMax: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "1234" || result.Stderr != "" || !result.Truncated {
		t.Fatalf("bounded shell output = %#v", result)
	}
}

func TestLocalUserShellBackgroundChildIsUnknown(t *testing.T) {
	workingDirectory := t.TempDir()
	config, err := normalizeLocalUserShellConfig(LocalUserShellConfig{
		Revision: "local-owner-v1", Profile: "owner-user", ShellPath: "/bin/sh",
		WorkingScopes:     map[string]string{"home": workingDirectory},
		TimeoutSecondsMax: 30, OutputBytesMax: 8192, ConcurrentCommands: 1,
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, broker, err := NewLocalUserShellBroker(config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = broker.Execute(t.Context(), ShellBrokerRequest{
		InvocationID: "inv_local_background", PlanHash: strings.Repeat("c", 64),
		Profile: "owner-user", ProfileRevision: "local-owner-v1",
		Script: "sleep 30 &", WorkingScope: "home", Environment: map[string]string{},
		TimeoutSeconds: 5, OutputBytesMax: 4096,
	})
	if !errors.Is(err, ErrShellBrokerOutcomeUnknown) {
		t.Fatalf("Execute() error = %v", err)
	}
}

func osUIDString() string {
	return fmt.Sprintf("%d", os.Geteuid())
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func waitForLocalUserShellFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
