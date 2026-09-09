// Package workerprocess owns the parent side of one task-scoped coding worker
// process. It deliberately does not persist tasks, discover workers, or expose
// a listener; a higher-level supervisor remains responsible for durable task
// acceptance and successor-generation policy.
package workerprocess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
)

const (
	DefaultInitializeTimeout = 30 * time.Second
	DefaultStopTimeout       = 5 * time.Second
	DefaultDiagnosticsDrain  = 2 * time.Second
	MaxProcessStderrBytes    = 64 << 10
	maxLifecycleTimeout      = 5 * time.Minute
)

var (
	ErrExecutableMismatch      = errors.New("coding worker executable does not match the bound build")
	ErrDiagnosticsDrainTimeout = errors.New("coding worker diagnostics did not close before the deadline")
	ErrProcessNotRunning       = errors.New("coding worker process is not running")
)

// LauncherConfig fixes the executable and parent identity used for every
// worker generation launched by one supervisor instance.
type LauncherConfig struct {
	ExecutablePath    string
	ParentBuildID     string
	Environment       []string
	InitializeTimeout time.Duration
	StopTimeout       time.Duration
}

// Launcher starts the private worker command. The command arguments are not
// configurable so this boundary cannot become a general process runner.
type Launcher struct {
	executablePath    string
	parentBuildID     string
	environment       []string
	initializeTimeout time.Duration
	stopTimeout       time.Duration

	commandArgs []string
	buildID     func(string) (string, error)
}

func NewLauncher(config LauncherConfig) (*Launcher, error) {
	executablePath, err := canonicalExecutablePath(config.ExecutablePath)
	if err != nil {
		return nil, err
	}
	initializeTimeout, err := lifecycleTimeout(
		config.InitializeTimeout,
		DefaultInitializeTimeout,
		"initialize",
	)
	if err != nil {
		return nil, err
	}
	stopTimeout, err := lifecycleTimeout(config.StopTimeout, DefaultStopTimeout, "stop")
	if err != nil {
		return nil, err
	}
	launcher := &Launcher{
		executablePath:    executablePath,
		parentBuildID:     config.ParentBuildID,
		initializeTimeout: initializeTimeout,
		stopTimeout:       stopTimeout,
		commandArgs:       []string{"code", "_worker"},
		buildID:           worker.ExecutableBuildID,
	}
	if config.Environment != nil {
		launcher.environment = append([]string{}, config.Environment...)
	}
	return launcher, nil
}

// Launch starts and initializes exactly one worker generation. Cancellation
// before initialization completes terminates that child; after Launch returns,
// the Process owns a lifecycle independent of the launch context.
func (launcher *Launcher) Launch(ctx context.Context, binding worker.Binding) (*Process, error) {
	if launcher == nil || launcher.buildID == nil {
		return nil, errors.New("coding worker launcher is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	buildID, err := launcher.buildID(launcher.executablePath)
	if err != nil {
		return nil, err
	}
	if buildID != binding.ExpectedWorkerBuildID {
		return nil, ErrExecutableMismatch
	}
	params := worker.InitializeParams{
		MinProtocolVersion: worker.ProtocolV1,
		MaxProtocolVersion: worker.ProtocolV1,
		ParentBuildID:      launcher.parentBuildID,
		Binding:            binding,
	}
	if err = params.Validate(); err != nil {
		return nil, err
	}

	command := exec.Command(launcher.executablePath, launcher.commandArgs...)
	command.Dir = binding.ExecutionRoot
	if launcher.environment == nil {
		command.Env = os.Environ()
	} else {
		command.Env = append([]string(nil), launcher.environment...)
	}
	diagnostics := &boundedBuffer{limit: MaxProcessStderrBytes}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("coding worker stderr pipe: %w", err)
	}
	command.Stderr = stderrWriter
	domain, err := prepareProcessDomain(command)
	if err != nil {
		_ = stderr.Close()
		_ = stderrWriter.Close()
		return nil, err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = stderr.Close()
		_ = stderrWriter.Close()
		_ = domain.close()
		return nil, fmt.Errorf("coding worker stdin pipe: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		_ = domain.close()
		return nil, fmt.Errorf("coding worker stdout pipe: %w", err)
	}
	if err = command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		_ = domain.close()
		return nil, fmt.Errorf("start coding worker: %w", err)
	}
	diagnosticsDone := drainDiagnostics(stderr, diagnostics)
	if err = stderrWriter.Close(); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = domain.close()
		_ = stderr.Close()
		<-diagnosticsDone
		return nil, fmt.Errorf("close coding worker parent stderr handle: %w", err)
	}
	if err = domain.started(); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = domain.close()
		_ = stderr.Close()
		<-diagnosticsDone
		return nil, err
	}
	client, err := worker.NewClient(stdout, stdin)
	if err != nil {
		_ = domain.stop(launcher.stopTimeout)
		_ = command.Wait()
		_ = stderr.Close()
		<-diagnosticsDone
		_ = domain.close()
		return nil, err
	}
	process := &Process{
		binding:         binding,
		client:          client,
		command:         command,
		domain:          domain,
		diagnostics:     diagnostics,
		stderr:          stderr,
		diagnosticsDone: diagnosticsDone,
		stopTimeout:     launcher.stopTimeout,
		done:            make(chan struct{}),
		terminateDone:   make(chan struct{}),
	}
	go process.wait()

	initializeCtx, cancel := context.WithTimeout(ctx, launcher.initializeTimeout)
	_, err = client.Initialize(initializeCtx, initializeKey(binding), params)
	cancel()
	if err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), launcher.stopTimeout)
		cleanupErr := process.terminate(cleanupCtx)
		cleanupCancel()
		return nil, errors.Join(err, cleanupErr)
	}
	return process, nil
}

// Process is one initialized, immutable worker generation. Its control
// methods derive identity from Binding so callers cannot accidentally address
// a different task generation.
type Process struct {
	binding         worker.Binding
	client          *worker.Client
	command         *exec.Cmd
	domain          processDomain
	diagnostics     *boundedBuffer
	stderr          io.ReadCloser
	diagnosticsDone <-chan error
	stopTimeout     time.Duration
	done            chan struct{}
	terminateDone   chan struct{}

	mu            sync.Mutex
	result        Result
	terminateOnce sync.Once
	terminateErr  error
}

type Diagnostics struct {
	Stderr    string
	Truncated bool
}

// Result separates the authenticated worker terminal event from the OS
// process and control-stream observations. A missing terminal event is not
// treated as successful completion.
type Result struct {
	Binding      worker.Binding
	WorkerStop   *worker.WorkerStoppedPayload
	ProcessError error
	ClientError  error
	Diagnostics  Diagnostics
}

func (process *Process) Binding() worker.Binding {
	if process == nil {
		return worker.Binding{}
	}
	return process.binding
}

func (process *Process) StartTurn(
	ctx context.Context,
	idempotencyKey string,
	text string,
	attachments []worker.TurnAttachment,
) error {
	if err := process.running(); err != nil {
		return err
	}
	return process.client.StartTurn(ctx, idempotencyKey, worker.TurnStartParams{
		ControlIdentity: process.binding.ControlIdentity(),
		Text:            text,
		Attachments:     append([]worker.TurnAttachment(nil), attachments...),
	})
}

func (process *Process) Steer(
	ctx context.Context,
	idempotencyKey string,
	text string,
	questionAnswer *worker.QuestionAnswerRef,
) error {
	if err := process.running(); err != nil {
		return err
	}
	var answer *worker.QuestionAnswerRef
	if questionAnswer != nil {
		cloned := *questionAnswer
		answer = &cloned
	}
	return process.client.Steer(ctx, idempotencyKey, worker.TurnSteerParams{
		ControlIdentity: process.binding.ControlIdentity(),
		Text:            text,
		QuestionAnswer:  answer,
	})
}

func (process *Process) Interrupt(ctx context.Context, idempotencyKey string) error {
	if err := process.running(); err != nil {
		return err
	}
	return process.client.Interrupt(ctx, idempotencyKey, process.generationParams())
}

// HardCancel asks the native controller to abort the active turn. The caller
// can observe the terminal event through Wait; Terminate is the separate
// process-domain backstop for an unresponsive or disconnected worker.
func (process *Process) HardCancel(ctx context.Context, idempotencyKey string) error {
	if err := process.running(); err != nil {
		return err
	}
	return process.client.Cancel(ctx, idempotencyKey, process.generationParams())
}

func (process *Process) Snapshot(ctx context.Context) (worker.SnapshotResult, error) {
	if err := process.running(); err != nil {
		return worker.SnapshotResult{}, err
	}
	return process.client.ReadSnapshot(ctx, process.generationParams())
}

func (process *Process) Shutdown(ctx context.Context, idempotencyKey string) error {
	if err := process.running(); err != nil {
		return err
	}
	return process.client.Shutdown(ctx, idempotencyKey, process.generationParams())
}

func (process *Process) EventsAfter(cursor uint64) worker.EventPage {
	if process == nil || process.client == nil {
		return worker.EventPage{HistoryGap: true}
	}
	return process.client.EventsAfter(cursor)
}

func (process *Process) Wake() <-chan struct{} {
	if process == nil || process.client == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return process.client.Wake()
}

func (process *Process) Done() <-chan struct{} {
	if process == nil || process.done == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return process.done
}

func (process *Process) Wait(ctx context.Context) (Result, error) {
	if process == nil || process.done == nil {
		return Result{}, ErrProcessNotRunning
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-process.done:
		process.mu.Lock()
		defer process.mu.Unlock()
		return cloneResult(process.result), nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// Terminate closes the control stream and kills the owned platform process
// boundary. Windows uses a Job Object and Unix uses the worker process group;
// a deliberately detached Unix process is outside that portable boundary.
// The call is safe to repeat. Callers must inspect the Result returned by Wait.
func (process *Process) Terminate(ctx context.Context) error {
	if process == nil || process.done == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return process.terminate(ctx)
}

func (process *Process) Close() error {
	if process == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), process.stopTimeout)
	defer cancel()
	return process.terminate(ctx)
}

func (process *Process) terminate(ctx context.Context) error {
	select {
	case <-process.done:
		return nil
	default:
	}
	process.terminateOnce.Do(func() {
		process.terminateErr = errors.Join(
			process.domain.stop(process.stopTimeout),
			process.client.Close(),
		)
		close(process.terminateDone)
	})
	select {
	case <-process.terminateDone:
	case <-ctx.Done():
		return errors.Join(process.terminateErr, ctx.Err())
	}
	select {
	case <-process.done:
		return process.terminateErr
	case <-ctx.Done():
		return errors.Join(process.terminateErr, ctx.Err())
	}
}

func (process *Process) wait() {
	<-process.client.Done()
	// StdoutPipe requires the reader to finish before Wait closes the pipe;
	// otherwise a final worker.stopped record can be lost in a scheduling race.
	processErr := process.command.Wait()
	// Stderr uses an explicit OS pipe rather than os/exec's internal copy
	// goroutine. That lets Wait observe the worker leader before a descendant
	// holding fd 2 open, so the owned process domain can be drained next. Once
	// the domain is empty, EOF proves the bounded diagnostic stream is complete.
	domainErr := process.domain.close()
	if domainErr != nil {
		_ = process.stderr.Close()
	}
	diagnosticsErr := waitForDiagnostics(
		process.diagnosticsDone,
		process.stderr,
		min(process.stopTimeout, DefaultDiagnosticsDrain),
	)
	stderrErr := process.stderr.Close()
	clientErr := process.client.Err()
	page := process.client.EventsAfter(0)
	result := Result{
		Binding:      process.binding,
		WorkerStop:   terminalWorkerStop(page.Events),
		ProcessError: processErr,
		ClientError:  clientErr,
		Diagnostics:  process.diagnostics.snapshot(),
	}
	result.ProcessError = errors.Join(
		result.ProcessError,
		domainErr,
		normalizePipeCloseError(stderrErr),
		normalizePipeCloseError(diagnosticsErr),
	)
	process.mu.Lock()
	process.result = result
	process.mu.Unlock()
	close(process.done)
}

func (process *Process) running() error {
	if process == nil || process.client == nil || process.done == nil {
		return ErrProcessNotRunning
	}
	select {
	case <-process.done:
		return ErrProcessNotRunning
	default:
		return nil
	}
}

func (process *Process) generationParams() worker.GenerationParams {
	return worker.GenerationParams{ControlIdentity: process.binding.ControlIdentity()}
}

func terminalWorkerStop(events []worker.RetainedEvent) *worker.WorkerStoppedPayload {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Record.Event != worker.EventWorkerStopped {
			continue
		}
		payload, err := worker.DecodeEventPayload(worker.EventWorkerStopped, events[index].Record.Payload)
		if err != nil {
			return nil
		}
		stopped, ok := payload.(*worker.WorkerStoppedPayload)
		if !ok {
			return nil
		}
		cloned := *stopped
		if stopped.Error != nil {
			protocolError := *stopped.Error
			protocolError.Details = append([]byte(nil), stopped.Error.Details...)
			cloned.Error = &protocolError
		}
		return &cloned
	}
	return nil
}

func cloneResult(result Result) Result {
	cloned := result
	if result.WorkerStop != nil {
		workerStop := *result.WorkerStop
		if result.WorkerStop.Error != nil {
			protocolError := *result.WorkerStop.Error
			protocolError.Details = append([]byte(nil), result.WorkerStop.Error.Details...)
			workerStop.Error = &protocolError
		}
		cloned.WorkerStop = &workerStop
	}
	return cloned
}

func initializeKey(binding worker.Binding) string {
	digest := sha256.Sum256([]byte(
		binding.TaskID + "\x00" + binding.TaskGenerationID + "\x00" + binding.WorkerGenerationID,
	))
	return "initialize-" + hex.EncodeToString(digest[:])
}

func canonicalExecutablePath(path string) (string, error) {
	if path == "" || !utf8.ValidString(path) || strings.TrimSpace(path) != path ||
		strings.ContainsAny(path, "\x00\r\n") {
		return "", errors.New("coding worker executable path is invalid")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve coding worker executable: %w", err)
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect coding worker executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("coding worker executable is not a regular file")
	}
	return absolute, nil
}

func lifecycleTimeout(value, defaultValue time.Duration, label string) (time.Duration, error) {
	if value == 0 {
		return defaultValue, nil
	}
	if value < 0 || value > maxLifecycleTimeout {
		return 0, fmt.Errorf("coding worker %s timeout is invalid", label)
	}
	return value, nil
}

type boundedBuffer struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	remaining := max(buffer.limit-len(buffer.data), 0)
	accepted := min(len(data), remaining)
	buffer.data = append(buffer.data, data[:accepted]...)
	if accepted != len(data) {
		buffer.truncated = true
	}
	return len(data), nil
}

func (buffer *boundedBuffer) snapshot() Diagnostics {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return Diagnostics{Stderr: string(buffer.data), Truncated: buffer.truncated}
}

func drainDiagnostics(input io.Reader, output io.Writer) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(output, input)
		done <- err
	}()
	return done
}

func waitForDiagnostics(done <-chan error, input io.Closer, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultDiagnosticsDrain
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		closeErr := input.Close()
		drainErr := <-done
		return errors.Join(
			ErrDiagnosticsDrainTimeout,
			normalizePipeCloseError(closeErr),
			normalizePipeCloseError(drainErr),
		)
	}
}

func normalizePipeCloseError(err error) error {
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}
