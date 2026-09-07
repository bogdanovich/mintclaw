//go:build linux && amd64

package document

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const workerSecretCanary = "MINTCLAW_DOCUMENT_SECRET_CANARY"

func TestProcessWorkerSuccessUsesRealSubprocessAndCleansScratch(t *testing.T) {
	snapshot, input := processWorkerFixture(t)
	t.Setenv(workerSecretCanary, "must-not-reach-worker")
	worker := testProcessWorker("serve")

	result := worker.Verify(t.Context(), snapshot, input)
	if result.State != StateSucceeded || result.Input == nil {
		t.Fatalf("worker result = %#v", result)
	}
	assertOnlySnapshotRemains(t, snapshot)
}

func TestProcessWorkerReturnsTypedTerminalFailures(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		code      FailureCode
		timeout   time.Duration
		maxOutput int
	}{
		{name: "crash", mode: "crash", code: FailureWorkerCrashed},
		{name: "malformed", mode: "malformed", code: FailureWorkerProtocol},
		{name: "unknown output field", mode: "unknown-field", code: FailureWorkerProtocol},
		{name: "stdout limit", mode: "oversized-stdout", code: FailureWorkerOutputLimit, maxOutput: 128},
		{name: "stderr limit", mode: "oversized-stderr", code: FailureWorkerOutputLimit, maxOutput: 128},
		{name: "timeout", mode: "hang", code: FailureWorkerTimeout, timeout: 50 * time.Millisecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot, input := processWorkerFixture(t)
			worker := testProcessWorker(test.mode)
			if test.timeout > 0 {
				worker.timeout = test.timeout
			}
			if test.maxOutput > 0 {
				worker.maxOutput = test.maxOutput
			}
			result := worker.Verify(t.Context(), snapshot, input)
			assertWorkerFailure(t, result, StateFailed, test.code)
			assertOnlySnapshotRemains(t, snapshot)
		})
	}
}

func TestProcessWorkerCancellationKillsDescendantProcessGroup(t *testing.T) {
	snapshot, input := processWorkerFixture(t)
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	worker := testProcessWorker("descendant", pidFile)
	worker.timeout = time.Minute
	ctx, cancel := context.WithCancel(t.Context())
	resultCh := make(chan WorkerResult, 1)
	go func() {
		resultCh <- worker.Verify(ctx, snapshot, input)
	}()

	childPID := waitForWorkerChildPID(t, pidFile)
	cancel()
	select {
	case result := <-resultCh:
		assertWorkerFailure(t, result, StateCanceled, FailureCanceled)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled worker did not return")
	}
	waitForProcessExit(t, childPID)
	assertOnlySnapshotRemains(t, snapshot)
}

func TestDocumentWorkerHelperProcess(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	mode := os.Args[separator+1]
	switch mode {
	case "serve":
		if os.Getenv(workerSecretCanary) != "" {
			os.Exit(91)
		}
		input := os.NewFile(WorkerInputFileDescriptor(), "document-snapshot")
		if input == nil {
			os.Exit(92)
		}
		defer func() { _ = input.Close() }()
		if err := ServeWorker(os.Stdin, input, os.Stdout); err != nil {
			os.Exit(93)
		}
	case "crash":
		os.Exit(23)
	case "malformed":
		_, _ = fmt.Fprint(os.Stdout, "{")
	case "unknown-field":
		request, decodeErr := decodeWorkerRequest(os.Stdin)
		if decodeErr != nil {
			os.Exit(94)
		}
		_, _ = fmt.Fprintf(
			os.Stdout,
			`{"schema_version":%q,"operation_id":%q,"state":"failed","failure":{"code":"internal_failure","message":"failed"},"output_path":"/tmp/escape"}`,
			WorkerResultSchemaVersion,
			request.OperationID,
		)
	case "oversized-stdout":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", defaultWorkerOutputSize+1))
	case "oversized-stderr":
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("x", defaultWorkerOutputSize+1))
	case "hang":
		time.Sleep(time.Minute)
	case "descendant":
		if separator+2 >= len(os.Args) {
			os.Exit(95)
		}
		child := exec.Command("/bin/sleep", "60")
		if err := child.Start(); err != nil {
			os.Exit(96)
		}
		if err := os.WriteFile(os.Args[separator+2], []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			os.Exit(97)
		}
		time.Sleep(time.Minute)
	default:
		os.Exit(98)
	}
}

func testProcessWorker(mode string, extraArgs ...string) *processWorker {
	args := []string{"-test.run=^TestDocumentWorkerHelperProcess$", "--", mode}
	args = append(args, extraArgs...)
	return &processWorker{
		executable: os.Args[0],
		args:       args,
		timeout:    2 * time.Second,
		maxOutput:  defaultWorkerOutputSize,
	}
}

func processWorkerFixture(t *testing.T) (*Snapshot, DocumentRef) {
	t.Helper()
	dir := filepath.Join(directTempDir(t), "operation")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create operation directory: %v", err)
	}
	path := filepath.Join(dir, "snapshot.pdf")
	data := []byte("%PDF-1.7\nreal process fixture\n%%EOF\n")
	writeFixture(t, path, data)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("protect snapshot: %v", err)
	}
	request := testWorkerRequest(data)
	return &Snapshot{path: path, dir: dir}, DocumentRef{
		Ref:         "document://local/" + request.OperationID,
		ContentType: request.Input.ContentType,
		Size:        request.Input.Size,
		SHA256:      request.Input.SHA256,
	}
}

func assertOnlySnapshotRemains(t *testing.T, snapshot *Snapshot) {
	t.Helper()
	entries, err := os.ReadDir(snapshot.dir)
	if err != nil {
		t.Fatalf("read operation directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(snapshot.path) {
		t.Fatalf("worker scratch survived cleanup: %+v", entries)
	}
}

func waitForWorkerChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(data))
			if parseErr != nil {
				t.Fatalf("parse child PID: %v", parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read child PID: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker descendant PID was not published")
	return 0
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("worker descendant %d survived process-group cancellation", pid)
}
