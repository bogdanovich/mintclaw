//go:build linux

package isolation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestBuildLinuxBwrapArgs_IncludesNamespaceFlagsAndExec(t *testing.T) {
	root := t.TempDir()
	binaryDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binaryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(binaryDir, "tool")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := BuildLinuxMountPlan(root, []config.ExposePath{{Source: binaryDir, Target: binaryDir, Mode: "ro"}})
	args, err := buildLinuxBwrapArgs(binaryPath, binaryPath, []string{binaryPath, "--flag"}, root, plan)
	if err != nil {
		t.Fatalf("buildLinuxBwrapArgs() error = %v", err)
	}
	hasNet := false
	hasIPC := false
	hasExec := false
	for i := range args {
		switch args[i] {
		case "--unshare-net":
			hasNet = true
		case "--unshare-ipc":
			hasIPC = true
		case "--":
			if i+1 < len(args) && args[i+1] == binaryPath {
				hasExec = true
			}
		}
	}
	if hasNet {
		t.Fatalf("bwrap args should not unshare net by default: %v", args)
	}
	if !hasIPC || !hasExec {
		t.Fatalf("bwrap args missing required items: %v", args)
	}
}

func TestBuildDocumentMountPlanHasOneWritablePrivateRoot(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "worker")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	immutable := filepath.Join(root, "backend")
	if err := os.WriteFile(immutable, []byte("backend"), 0o500); err != nil {
		t.Fatal(err)
	}
	plan, resolvedExecutable, workingDirectory, err := buildDocumentMountPlan(
		executable,
		scratch,
		[]string{immutable},
	)
	if err != nil {
		t.Fatalf("buildDocumentMountPlan() error = %v", err)
	}
	if resolvedExecutable != executable || workingDirectory != scratch {
		t.Fatalf(
			"buildDocumentMountPlan() paths = (%q, %q), want (%q, %q)",
			resolvedExecutable,
			workingDirectory,
			executable,
			scratch,
		)
	}
	foundExecutable := false
	foundScratch := false
	foundImmutable := false
	for _, rule := range plan {
		if rule.Target == "/" || rule.Target == "/usr" || rule.Target == root {
			t.Fatalf("document policy exposes a broad host root: %+v", rule)
		}
		if rule.Mode == "rw" && rule.Target != scratch {
			t.Fatalf("document policy has unexpected writable mount: %+v", rule)
		}
		if rule.Target == executable && rule.Mode == "ro" {
			foundExecutable = true
		}
		if rule.Target == scratch && rule.Mode == "rw" {
			foundScratch = true
		}
		if rule.Target == immutable && rule.Mode == "ro" {
			foundImmutable = true
		}
	}
	if !foundExecutable || !foundScratch || !foundImmutable {
		t.Fatalf("document policy plan = %+v", plan)
	}
}

func TestBuildDocumentBwrapArgsConfinesNamespacesAndDoesNotMountArguments(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "worker")
	scratch := filepath.Join(root, "scratch")
	secret := filepath.Join(root, "gateway-secret")
	for path, mode := range map[string]os.FileMode{executable: 0o755, secret: 0o600} {
		if err := os.WriteFile(path, []byte("test"), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := []MountRule{
		{Source: executable, Target: executable, Mode: "ro"},
		{Source: scratch, Target: scratch, Mode: "rw"},
	}
	arguments, err := buildDocumentBwrapArgs(
		"/usr/bin/bwrap",
		executable,
		[]string{"document", "_worker", secret},
		scratch,
		plan,
	)
	if err != nil {
		t.Fatalf("buildDocumentBwrapArgs() error = %v", err)
	}
	joined := strings.Join(arguments, " ")
	for _, required := range []string{
		"--unshare-net", "--unshare-ipc", "--unshare-pid", "--unshare-uts", "--cap-drop ALL",
		"--proc /proc", "--dev /dev", "--tmpfs /tmp", "--chdir " + scratch,
		"--setenv " + documentPolicyEnvironment + " " + DocumentPolicyMode,
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("document bwrap args lack %q: %v", required, arguments)
		}
	}
	secretOccurrences := 0
	for _, argument := range arguments {
		if argument == secret {
			secretOccurrences++
		}
	}
	if secretOccurrences != 1 {
		t.Fatalf("absolute command argument was mounted into policy: %v", arguments)
	}
}

func TestPrepareDocumentCommandWithBwrapPreservesCommandLifecycle(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "worker")
	bwrap := filepath.Join(root, "bwrap")
	for _, path := range []string{executable, bwrap} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.CommandContext(context.Background(), executable, "document", "_worker")
	command.Dir = scratch
	command.Env = []string{"HOME=" + scratch, documentPolicyEnvironment + "=stale"}
	if err := prepareDocumentCommandWithBwrap(command, scratch, bwrap, nil); err != nil {
		t.Fatalf("prepareDocumentCommandWithBwrap() error = %v", err)
	}
	if command.Path != bwrap || command.Dir != "" || !documentPolicyEnvironmentPresent(command.Env) {
		t.Fatalf("prepared document command = %#v", command)
	}
}

func TestDocumentPolicyRejectsUnqualifiedExecutableAndIgnoresAmbientPath(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "ambient-bwrap-ran")
	fake := filepath.Join(root, "bwrap")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf ran > "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	_ = documentPolicyStatus()
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("document policy executed ambient bwrap: %v", err)
	}
	if err := verifyDocumentPolicyExecutable(fake, strings.Repeat("0", 64)); err == nil {
		t.Fatal("document policy admitted unexpected executable bytes")
	}
	if err := verifyDocumentPolicyExecutable(filepath.Join(root, "missing"), strings.Repeat("0", 64)); err == nil {
		t.Fatal("document policy admitted a missing executable")
	}
}

func documentPolicyEnvironmentPresent(environment []string) bool {
	want := documentPolicyEnvironment + "=" + DocumentPolicyMode
	count := 0
	for _, item := range environment {
		if item == want {
			count++
		}
	}
	return count == 1
}

func TestResolveLinuxWorkingDir_ResolvesRelativeDir(t *testing.T) {
	cwd := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if chdirErr := os.Chdir(previous); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	}()
	if chdirErr := os.Chdir(cwd); chdirErr != nil {
		t.Fatal(chdirErr)
	}

	resolvedDir, execDir, err := resolveLinuxWorkingDir("./hooks", "./hook.sh")
	if err != nil {
		t.Fatalf("resolveLinuxWorkingDir() error = %v", err)
	}
	want := filepath.Join(cwd, "hooks")
	if resolvedDir != want || execDir != want {
		t.Fatalf("resolveLinuxWorkingDir() = (%q, %q), want (%q, %q)", resolvedDir, execDir, want, want)
	}
}

func TestResolveLinuxCommandPath_UsesExecDirForRelativeCommand(t *testing.T) {
	execDir := filepath.Join(t.TempDir(), "hooks")
	got, err := resolveLinuxCommandPath("./hook.sh", execDir)
	if err != nil {
		t.Fatalf("resolveLinuxCommandPath() error = %v", err)
	}
	want := filepath.Join(execDir, "hook.sh")
	if got != want {
		t.Fatalf("resolveLinuxCommandPath() = %q, want %q", got, want)
	}
}

func TestBuildLinuxBwrapArgs_UsesResolvedPathForRelativeCommand(t *testing.T) {
	root := t.TempDir()
	execDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(execDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedPath := filepath.Join(execDir, "hook.sh")
	if err := os.WriteFile(resolvedPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := []MountRule{
		{Source: execDir, Target: execDir, Mode: "rw"},
		{Source: resolvedPath, Target: resolvedPath, Mode: "ro"},
	}
	args, err := buildLinuxBwrapArgs("./hook.sh", resolvedPath, []string{"./hook.sh"}, execDir, plan)
	if err != nil {
		t.Fatalf("buildLinuxBwrapArgs() error = %v", err)
	}
	hasExecDir := false
	for _, arg := range args {
		if arg == execDir {
			hasExecDir = true
			break
		}
	}
	if !hasExecDir {
		t.Fatalf("buildLinuxBwrapArgs() missing resolved chdir: %v", args)
	}
	for i := range args {
		if args[i] == "--" {
			if i+1 >= len(args) || args[i+1] != resolvedPath {
				t.Fatalf("buildLinuxBwrapArgs() exec path = %v, want %q after --", args, resolvedPath)
			}
			return
		}
	}
	t.Fatalf("buildLinuxBwrapArgs() missing exec delimiter: %v", args)
}

func TestAppendLinuxArgumentMounts_AddsAbsoluteArgumentPaths(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input.txt")
	if err := os.WriteFile(input, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "out", "result.txt")
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		t.Fatal(err)
	}

	plan := appendLinuxArgumentMounts(nil, []string{input, "--output=" + output})
	if len(plan) != 2 {
		t.Fatalf("appendLinuxArgumentMounts() len = %d, want 2", len(plan))
	}
	if plan[0].Source != input || plan[0].Mode != "ro" {
		t.Fatalf("appendLinuxArgumentMounts()[0] = %+v, want source=%q mode=ro", plan[0], input)
	}
	if plan[1].Source != filepath.Dir(output) || plan[1].Mode != "rw" {
		t.Fatalf("appendLinuxArgumentMounts()[1] = %+v, want source=%q mode=rw", plan[1], filepath.Dir(output))
	}
}
