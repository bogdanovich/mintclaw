//go:build windows && amd64

package document

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const documentWorkerDescendantEnvironment = "MINTCLAW_DOCUMENT_WORKER_DESCENDANT"

func TestWindowsDocumentWorkerJobHasResourceBoundaries(t *testing.T) {
	command := exec.Command("cmd.exe", "/c", "exit", "0")
	configureDocumentWorkerProcess(command)
	prepared, err := prepareDocumentWorkerProcess(t.Context(), command, t.TempDir(), workerOperationVerify)
	if err != nil {
		t.Fatal(err)
	}
	boundary, ok := prepared.(*windowsDocumentWorkerProcessBoundary)
	if !ok {
		t.Fatalf("process boundary = %T", prepared)
	}
	t.Cleanup(func() { _ = boundary.close() })

	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	err = windows.QueryInformationJobObject(
		boundary.state.job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantFlags := uint32(windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
		windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS |
		windows.JOB_OBJECT_LIMIT_JOB_MEMORY)
	if limits.BasicLimitInformation.LimitFlags&wantFlags != wantFlags ||
		limits.BasicLimitInformation.ActiveProcessLimit != documentWorkerJobProcessLimit ||
		limits.JobMemoryLimit != documentWorkerJobMemoryLimit {
		t.Fatalf("document worker job limits = %#v", limits)
	}
}

func TestWindowsDocumentWorkerCancellationTerminatesDescendants(t *testing.T) {
	snapshot, input := portableAcquiredFixture(t, portableTestProcessWorker("serve"), "text.pdf")
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	worker := &processWorker{
		executable: os.Args[0],
		args: []string{
			"-test.run=^TestWindowsDocumentWorkerDescendantHelperProcess$",
			"--",
			pidFile,
		},
		timeout:   time.Minute,
		maxOutput: defaultWorkerOutputSize,
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan WorkerResult, 1)
	go func() {
		result <- worker.Verify(ctx, snapshot, input)
	}()
	pid := waitForDocumentWorkerDescendantPID(t, pidFile)
	descendant, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer windows.CloseHandle(descendant)

	cancel()
	select {
	case terminal := <-result:
		assertWorkerFailure(t, terminal, StateCanceled, FailureCanceled)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled document worker did not terminate")
	}
	status, err := windows.WaitForSingleObject(descendant, 5_000)
	if err != nil {
		t.Fatal(err)
	}
	if status != windows.WAIT_OBJECT_0 {
		t.Fatalf("document worker descendant %d remains alive", pid)
	}
	entries, err := os.ReadDir(snapshot.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".worker-") {
			t.Fatalf("document worker scratch survived cancellation: %+v", entries)
		}
	}
	if err = snapshot.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsDocumentWorkerDescendantHelperProcess(t *testing.T) {
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
	child := exec.Command(os.Args[0], "-test.run=^TestWindowsDocumentWorkerDescendantProcess$")
	child.Env = append(os.Environ(), documentWorkerDescendantEnvironment+"=1")
	if err := child.Start(); err != nil {
		os.Exit(91)
	}
	if err := os.WriteFile(os.Args[separator+1], []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		os.Exit(92)
	}
	time.Sleep(time.Minute)
}

func TestWindowsDocumentWorkerDescendantProcess(t *testing.T) {
	if os.Getenv(documentWorkerDescendantEnvironment) != "1" {
		return
	}
	time.Sleep(time.Minute)
}

func waitForDocumentWorkerDescendantPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil || pid <= 0 {
				t.Fatalf("document worker descendant PID = %q, %v", data, parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for document worker descendant PID")
	return 0
}
