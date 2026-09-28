//go:build darwin

package companion

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type darwinAuthorityBrokerProcessRunner struct{}

func (*darwinAuthorityBrokerProcessRunner) Execute(
	ctx context.Context,
	prepared preparedAuthorityBrokerExecution,
	request ShellBrokerRequest,
) (ShellBrokerResult, error) {
	executeContext, cancel := context.WithTimeout(ctx, time.Duration(request.TimeoutSeconds)*time.Second)
	defer cancel()
	output := newLocalUserShellCapture(request.OutputBytesMax)
	command := exec.Command(prepared.shellPath, prepared.shellArguments...)
	command.Dir = prepared.workingDirectory
	command.Env = append([]string(nil), prepared.environment...)
	command.Stdout = output
	command.Stderr = output.stderrWriter()
	command.WaitDelay = 250 * time.Millisecond
	prepareJobProcess(command)
	if os.Geteuid() == 0 {
		command.SysProcAttr.Credential = &syscall.Credential{
			Uid:    prepared.profile.UID,
			Gid:    prepared.profile.GID,
			Groups: append([]uint32(nil), prepared.profile.SupplementaryGroups...),
		}
	} else if prepared.profile.UID != uint32(os.Geteuid()) ||
		prepared.profile.GID != uint32(os.Getegid()) ||
		len(prepared.profile.SupplementaryGroups) != 0 {
		return ShellBrokerResult{}, errors.New("unprivileged broker fixture cannot change identity")
	}
	startedAt := time.Now()
	if err := command.Start(); err != nil {
		return ShellBrokerResult{}, fmt.Errorf("start macOS authority shell: %w", err)
	}
	reason := observeShellProcess(executeContext, command.Process.Pid)
	drain := drainJobProcessGroup(command, reason != "completed")
	waitErr := command.Wait()
	completedAt := time.Now()
	if reason != "completed" || drain.observationUnknown || drain.hadDescendants {
		return ShellBrokerResult{}, fmt.Errorf(
			"%w: macOS authority shell termination is not proven",
			ErrShellBrokerOutcomeUnknown,
		)
	}
	exitCode, signal, err := localUserShellExit(waitErr)
	if err != nil {
		return ShellBrokerResult{}, err
	}
	stdout, stderr, truncated := output.result()
	return ShellBrokerResult{
		ExitCode: exitCode, Stdout: stdout, Stderr: stderr,
		Signal: signal, Truncated: truncated,
		StartedAt: startedAt.UnixMilli(), CompletedAt: completedAt.UnixMilli(),
	}, nil
}

func RunAuthorityBrokerWorker(context.Context, bool) error {
	return errors.New("authority broker worker mode requires Linux")
}
