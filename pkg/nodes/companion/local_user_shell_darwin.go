//go:build darwin

package companion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type localUserShellBroker struct {
	revision string
	profile  normalizedAuthorityBrokerProfile
	active   chan struct{}
}

func normalizeLocalUserShellConfig(
	config LocalUserShellConfig,
	baseDir string,
) (LocalUserShellConfig, error) {
	config.Revision = strings.TrimSpace(config.Revision)
	config.Profile = strings.TrimSpace(config.Profile)
	if !validShellBrokerRevision(config.Revision) {
		return LocalUserShellConfig{}, errors.New("revision is invalid")
	}
	profile, err := normalizeAuthorityBrokerProfile(
		config.Profile,
		AuthorityBrokerProfile{
			Revision:                  config.Revision,
			ShellPath:                 config.ShellPath,
			Login:                     config.Login,
			UID:                       uint32(os.Geteuid()),
			GID:                       uint32(os.Getegid()),
			WorkingScopes:             config.WorkingScopes,
			FixedEnvironment:          config.FixedEnvironment,
			PermittedEnvironmentNames: config.PermittedEnvironmentNames,
			Network:                   "inherit",
			TimeoutSecondsMax:         config.TimeoutSecondsMax,
			OutputBytesMax:            config.OutputBytesMax,
			ConcurrentCommands:        config.ConcurrentCommands,
		},
		baseDir,
	)
	if err != nil {
		return LocalUserShellConfig{}, err
	}
	config.Profile = profile.alias
	config.ShellPath = profile.ShellPath
	config.WorkingScopes = profile.WorkingScopes
	config.FixedEnvironment = profile.FixedEnvironment
	config.PermittedEnvironmentNames = profile.PermittedEnvironmentNames
	config.ready = &profile
	return config, nil
}

// NewLocalUserShellBroker constructs the macOS same-account shell executor and
// its model-safe projection from an already normalized node configuration.
func NewLocalUserShellBroker(
	config LocalUserShellConfig,
) (ShellBrokerSnapshot, ShellBroker, error) {
	if config.ready == nil || config.ready.alias != config.Profile {
		return ShellBrokerSnapshot{}, nil, errors.New("local-user shell config is not normalized")
	}
	profile := *config.ready
	snapshot, err := normalizeShellBrokerSnapshot(ShellBrokerSnapshot{
		Revision: config.Revision,
		Profiles: []ShellBrokerProfile{
			{
				Alias: profile.alias, Revision: profile.Revision,
				UID: profile.UID, GID: profile.GID,
				WorkingScopes:      sortedAuthorityBrokerMapKeys(profile.WorkingScopes),
				EnvironmentNames:   append([]string(nil), profile.PermittedEnvironmentNames...),
				TimeoutSecondsMax:  profile.TimeoutSecondsMax,
				OutputBytesMax:     profile.OutputBytesMax,
				ConcurrentCommands: profile.ConcurrentCommands,
			},
		},
	})
	if err != nil {
		return ShellBrokerSnapshot{}, nil, err
	}
	return snapshot, &localUserShellBroker{
		revision: config.Revision,
		profile:  profile,
		active:   make(chan struct{}, profile.ConcurrentCommands),
	}, nil
}

func (*localUserShellBroker) SupportsConfirmedCancellation() bool {
	return false
}

func (broker *localUserShellBroker) Execute(
	ctx context.Context,
	request ShellBrokerRequest,
) (ShellBrokerResult, error) {
	prepared, err := broker.prepare(request)
	if err != nil {
		return ShellBrokerResult{}, err
	}
	executeCtx, cancel := context.WithTimeout(ctx, time.Duration(request.TimeoutSeconds)*time.Second)
	defer cancel()
	select {
	case broker.active <- struct{}{}:
		defer func() { <-broker.active }()
	case <-executeCtx.Done():
		return ShellBrokerResult{}, executeCtx.Err()
	}
	if contextErr := executeCtx.Err(); contextErr != nil {
		return ShellBrokerResult{}, contextErr
	}
	output := newLocalUserShellCapture(request.OutputBytesMax)
	command := exec.Command(prepared.shellPath, prepared.shellArguments...)
	command.Dir = prepared.workingDirectory
	command.Env = append([]string(nil), prepared.environment...)
	command.Stdout = output
	command.Stderr = output.stderrWriter()
	command.WaitDelay = 250 * time.Millisecond
	prepareJobProcess(command)
	startedAt := time.Now()
	if startErr := command.Start(); startErr != nil {
		return ShellBrokerResult{}, fmt.Errorf("start local-user shell: %w", startErr)
	}
	reason := broker.observe(executeCtx, command.Process.Pid)
	drain := drainJobProcessGroup(command, reason != "completed")
	waitErr := command.Wait()
	completedAt := time.Now()
	if reason != "completed" || drain.observationUnknown {
		return ShellBrokerResult{}, fmt.Errorf(
			"%w: local-user shell termination cannot prove that detached descendants stopped",
			ErrShellBrokerOutcomeUnknown,
		)
	}
	if drain.hadDescendants {
		return ShellBrokerResult{}, fmt.Errorf(
			"%w: local-user shell process group outlived its leader",
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

func (broker *localUserShellBroker) prepare(
	request ShellBrokerRequest,
) (preparedAuthorityBrokerExecution, error) {
	config := AuthorityBrokerConfig{
		Revision: broker.revision,
		normalizedProfile: map[string]normalizedAuthorityBrokerProfile{
			broker.profile.alias: broker.profile,
		},
	}
	return config.prepareExecution(request)
}

func (*localUserShellBroker) observe(ctx context.Context, processGroup int) string {
	observer := time.NewTicker(jobProcessObservationInterval)
	defer observer.Stop()
	for {
		select {
		case <-ctx.Done():
			return "canceled"
		case <-observer.C:
			exited, err := jobProcessLeaderExited(processGroup)
			if err == nil && exited {
				return "completed"
			}
		}
	}
}

func localUserShellExit(waitErr error) (int, string, error) {
	if waitErr == nil {
		return 0, "", nil
	}
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		return 0, "", fmt.Errorf(
			"%w: a detached local-user shell process retained output",
			ErrShellBrokerOutcomeUnknown,
		)
	}
	var exitError *exec.ExitError
	if !errors.As(waitErr, &exitError) {
		return 0, "", fmt.Errorf("wait local-user shell: %w", waitErr)
	}
	signal := ""
	if status, ok := exitError.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		signal = status.Signal().String()
	}
	return exitError.ExitCode(), signal, nil
}

type localUserShellCapture struct {
	mu        sync.Mutex
	remaining int
	stdout    []byte
	stderr    []byte
	truncated bool
	stderrOut localUserShellCaptureWriter
}

type localUserShellCaptureWriter struct {
	capture *localUserShellCapture
	stderr  bool
}

func newLocalUserShellCapture(limit int) *localUserShellCapture {
	capture := &localUserShellCapture{remaining: limit}
	capture.stderrOut = localUserShellCaptureWriter{capture: capture, stderr: true}
	return capture
}

func (capture *localUserShellCapture) Write(data []byte) (int, error) {
	return capture.write(data, false)
}

func (capture *localUserShellCapture) stderrWriter() io.Writer {
	return &capture.stderrOut
}

func (writer *localUserShellCaptureWriter) Write(data []byte) (int, error) {
	return writer.capture.write(data, writer.stderr)
}

func (capture *localUserShellCapture) write(data []byte, stderr bool) (int, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	accepted := min(len(data), capture.remaining)
	if stderr {
		capture.stderr = append(capture.stderr, data[:accepted]...)
	} else {
		capture.stdout = append(capture.stdout, data[:accepted]...)
	}
	capture.remaining -= accepted
	if accepted < len(data) {
		capture.truncated = true
	}
	return len(data), nil
}

func (capture *localUserShellCapture) result() (string, string, bool) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return string(capture.stdout), string(capture.stderr), capture.truncated
}
