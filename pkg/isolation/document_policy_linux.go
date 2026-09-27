//go:build linux

package isolation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const documentPolicyProbeTimeout = 2 * time.Second

var documentPolicyReadOnlyPaths = []string{
	"/lib",
	"/lib64",
	"/usr/lib",
	"/etc/ld.so.cache",
	"/etc/ld.so.conf",
	"/etc/ld.so.conf.d",
	"/etc/fonts",
	"/usr/share/fonts",
	"/usr/local/share/fonts",
	"/var/cache/fontconfig",
	"/usr/share/poppler",
	"/usr/share/ghostscript",
	"/usr/share/color",
	"/usr/lib/locale",
	"/usr/share/locale",
	"/etc/localtime",
	"/usr/share/zoneinfo",
}

func documentPolicyStatus() error {
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return fmt.Errorf("document isolation requires bubblewrap: %w", err)
	}
	scratch, err := os.MkdirTemp("", "mintclaw-document-isolation-probe-")
	if err != nil {
		return fmt.Errorf("create document isolation probe scratch: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	if err = os.Chmod(scratch, 0o700); err != nil {
		return fmt.Errorf("protect document isolation probe scratch: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), documentPolicyProbeTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/true")
	command.Env = []string{"HOME=" + scratch, "TMPDIR=" + scratch, "LANG=C", "LC_ALL=C", "PATH="}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err = prepareDocumentCommandWithBwrap(command, scratch, bwrapPath, nil); err != nil {
		return err
	}
	if err = command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("document isolation probe exceeded its runtime limit")
		}
		return fmt.Errorf("document isolation probe failed: %w", err)
	}
	return nil
}

func prepareDocumentCommand(
	ctx context.Context,
	command *exec.Cmd,
	scratch string,
	immutableReadOnlyPaths []string,
) error {
	if command == nil {
		return errors.New("document isolation command is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := documentPolicyStatus(); err != nil {
		return err
	}
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return fmt.Errorf("document isolation requires bubblewrap: %w", err)
	}
	return prepareDocumentCommandWithBwrap(command, scratch, bwrapPath, immutableReadOnlyPaths)
}

func prepareDocumentCommandWithBwrap(
	command *exec.Cmd,
	scratch string,
	bwrapPath string,
	immutableReadOnlyPaths []string,
) error {
	if command == nil || command.Path == "" || len(command.Args) == 0 {
		return errors.New("document isolation command is invalid")
	}
	plan, executable, workingDirectory, err := buildDocumentMountPlan(
		command.Path,
		scratch,
		immutableReadOnlyPaths,
	)
	if err != nil {
		return err
	}
	arguments, err := buildDocumentBwrapArgs(
		bwrapPath,
		executable,
		command.Args[1:],
		workingDirectory,
		plan,
	)
	if err != nil {
		return err
	}
	markDocumentPolicyEnvironment(command)
	command.Path = bwrapPath
	command.Args = arguments
	command.Dir = ""
	return nil
}

func markDocumentPolicyEnvironment(command *exec.Cmd) {
	prefix := documentPolicyEnvironment + "="
	environment := make([]string, 0, len(command.Env)+1)
	for _, item := range command.Env {
		if len(item) >= len(prefix) && item[:len(prefix)] == prefix {
			continue
		}
		environment = append(environment, item)
	}
	command.Env = append(environment, prefix+DocumentPolicyMode)
}

func buildDocumentMountPlan(
	executable string,
	scratch string,
	immutableReadOnlyPaths []string,
) ([]MountRule, string, string, error) {
	executable = filepath.Clean(executable)
	scratch = filepath.Clean(scratch)
	if !filepath.IsAbs(executable) || !filepath.IsAbs(scratch) || scratch == string(filepath.Separator) {
		return nil, "", "", errors.New("document isolation paths must be absolute and private")
	}
	scratchInfo, err := os.Lstat(scratch)
	if err != nil || !scratchInfo.IsDir() || scratchInfo.Mode()&os.ModeSymlink != 0 ||
		scratchInfo.Mode().Perm()&0o077 != 0 {
		return nil, "", "", errors.New("document isolation scratch must be a private directory")
	}
	resolvedScratch, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		return nil, "", "", fmt.Errorf("resolve document isolation scratch: %w", err)
	}
	resolvedExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, "", "", fmt.Errorf("resolve document worker executable: %w", err)
	}
	executableInfo, err := os.Stat(resolvedExecutable)
	if err != nil || !executableInfo.Mode().IsRegular() || executableInfo.Mode().Perm()&0o111 == 0 {
		return nil, "", "", errors.New("document worker executable is unavailable")
	}
	if pathWithin(resolvedExecutable, resolvedScratch) {
		return nil, "", "", errors.New("document worker executable must be outside writable scratch")
	}

	plan := make([]MountRule, 0, len(documentPolicyReadOnlyPaths)+len(immutableReadOnlyPaths)+2)
	for _, path := range documentPolicyReadOnlyPaths {
		if _, statErr := os.Stat(path); statErr == nil {
			plan = ensureLinuxMountRule(plan, path, path, "ro")
		}
	}
	for _, path := range immutableReadOnlyPaths {
		path = filepath.Clean(path)
		if !filepath.IsAbs(path) || path == string(filepath.Separator) || pathWithin(path, resolvedScratch) {
			return nil, "", "", fmt.Errorf("invalid immutable document input %q", path)
		}
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return nil, "", "", fmt.Errorf("resolve immutable document input %s: %w", path, resolveErr)
		}
		info, statErr := os.Stat(resolved)
		if statErr != nil || !info.Mode().IsRegular() {
			return nil, "", "", fmt.Errorf("immutable document input %s is unavailable", path)
		}
		plan = ensureLinuxMountRule(plan, resolved, path, "ro")
	}
	plan = ensureLinuxMountRule(plan, resolvedExecutable, executable, "ro")
	plan = ensureLinuxMountRule(plan, resolvedScratch, scratch, "rw")
	return plan, executable, scratch, nil
}

func buildDocumentBwrapArgs(
	bwrapPath string,
	executable string,
	commandArgs []string,
	workingDirectory string,
	plan []MountRule,
) ([]string, error) {
	arguments := []string{
		bwrapPath,
		"--die-with-parent",
		"--unshare-net",
		"--unshare-ipc",
		"--unshare-pid",
		"--unshare-uts",
		"--cap-drop", "ALL",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
	}
	for _, rule := range plan {
		flag, err := linuxBindFlag(rule)
		if err != nil {
			return nil, err
		}
		arguments = append(arguments, flag, rule.Source, rule.Target)
	}
	arguments = append(
		arguments,
		"--chdir", workingDirectory,
		"--setenv", documentPolicyEnvironment, DocumentPolicyMode,
		"--", executable,
	)
	arguments = append(arguments, commandArgs...)
	return arguments, nil
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && relative != "." &&
		!filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
