package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const maxGitOutputBytes = 256 << 10

type gitRunner func(context.Context, string, ...string) (gitOutput, error)

type gitOutput struct {
	stdout    string
	stderr    string
	truncated bool
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	if buffer.remaining > 0 {
		keep := min(len(data), buffer.remaining)
		_, _ = buffer.buffer.Write(data[:keep])
		buffer.remaining -= keep
		buffer.truncated = keep != len(data)
	} else if len(data) != 0 {
		buffer.truncated = true
	}
	return written, nil
}

func runGit(ctx context.Context, cwd string, args ...string) (gitOutput, error) {
	commandArgs := append([]string{"-C", cwd}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = sanitizedGitEnvironment()
	stdout := &limitedBuffer{remaining: maxGitOutputBytes}
	stderr := &limitedBuffer{remaining: maxGitOutputBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	result := gitOutput{
		stdout:    strings.TrimSpace(stdout.buffer.String()),
		stderr:    strings.TrimSpace(stderr.buffer.String()),
		truncated: stdout.truncated || stderr.truncated,
	}
	if err != nil {
		return result, &gitCommandError{args: append([]string(nil), args...), stderr: result.stderr, err: err}
	}
	return result, nil
}

func sanitizedGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upperKey := strings.ToUpper(key)
		if strings.HasPrefix(upperKey, "GIT_") || upperKey == "LC_ALL" {
			continue
		}
		environment = append(environment, entry)
	}
	return append(
		environment,
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_LITERAL_PATHSPECS=1",
	)
}

type gitCommandError struct {
	args   []string
	stderr string
	err    error
}

func (commandError *gitCommandError) Error() string {
	if commandError == nil {
		return "coding worktree: Git command failed"
	}
	message := fmt.Sprintf("coding worktree: git %s failed", strings.Join(commandError.args, " "))
	if commandError.stderr != "" {
		message += ": " + commandError.stderr
	}
	return message
}

func (commandError *gitCommandError) Unwrap() error {
	if commandError == nil {
		return nil
	}
	return commandError.err
}

func gitExitCode(err error) (int, bool) {
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		return 0, false
	}
	return exitError.ExitCode(), true
}
