//go:build linux || darwin

package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

const (
	jobHelperEnabled = "MINTCLAW_JOB_HELPER"
	jobHelperAction  = "MINTCLAW_JOB_ACTION"
)

func TestJobHelperProcess(t *testing.T) {
	if os.Getenv(jobHelperEnabled) != "1" {
		return
	}
	switch os.Getenv(jobHelperAction) {
	case "success":
		countPath := filepath.Join("launch-count")
		count, _ := os.ReadFile(countPath)
		_ = os.WriteFile(countPath, append(count, 'x'), 0o600)
		_ = os.WriteFile("artifact.out", []byte("durable artifact\n"), 0o600)
		_, _ = os.Stdout.WriteString("job stdout\n")
		_, _ = os.Stderr.WriteString("job stderr\n")
	case "large":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", 8192))
	case "sleep":
		time.Sleep(30 * time.Second)
	case "descendant":
		_ = os.WriteFile("artifact.out", []byte("stable\n"), 0o600)
		child := exec.Command(os.Args[0], "-test.run=^TestJobHelperProcess$")
		child.Env = []string{
			jobHelperEnabled + "=1",
			jobHelperAction + "=descendant-child",
		}
		if err := child.Start(); err != nil {
			os.Exit(65)
		}
		_ = os.WriteFile("descendant.pid", []byte(strconv.Itoa(child.Process.Pid)), 0o600)
	case "descendant-child":
		time.Sleep(time.Second)
		_ = os.WriteFile("artifact.out", []byte("mutated\n"), 0o600)
	case "symlink":
		_ = os.WriteFile("outside", []byte("not a snapshot"), 0o600)
		_ = os.Symlink("outside", "artifact.out")
	default:
		os.Exit(64)
	}
}

func TestDirectJobManagerPersistsLogsArtifactAndDeduplicates(t *testing.T) {
	manager, store, root, executable := newTestDirectJobManager(t, DirectJobLimits{})
	plan := testDirectJobPlan(t, executable, root, "success", []JobArtifactDeclaration{
		{Name: "result", Path: "artifact.out"},
	}, 5)
	started, err := manager.Start(plan)
	if err != nil || started.State != JobRunning {
		t.Fatalf("Start() = %#v, error %v", started, err)
	}
	duplicate, err := manager.Start(plan)
	if err != nil || duplicate.JobID != started.JobID {
		t.Fatalf("duplicate Start() = %#v, error %v", duplicate, err)
	}
	record := waitForTerminalJob(t, store, started.JobID)
	if record.State != JobSucceeded || record.ExitCode == nil || *record.ExitCode != 0 {
		t.Fatalf("terminal job = %#v", record)
	}
	count, err := os.ReadFile(filepath.Join(root, "launch-count"))
	if err != nil || string(count) != "x" {
		t.Fatalf("launch count = %q, error %v", count, err)
	}
	stdout, err := manager.ReadLog(record.Owner, record.JobID, false, 0, 4096)
	if err != nil || !strings.Contains(string(stdout.Data), "job stdout\n") ||
		stdout.Next != int64(len(stdout.Data)) {
		t.Fatalf("stdout = %#v, error %v", stdout, err)
	}
	wrongOwner := record.Owner
	wrongOwner.ActorID = "actor_wrong"
	if _, err := manager.ReadLog(wrongOwner, record.JobID, false, 0, 10); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("wrong-owner ReadLog() error = %v", err)
	}
	artifacts, err := manager.Artifacts(record.Owner, record.JobID)
	if err != nil || len(artifacts) != 1 || artifacts[0].State != JobArtifactAvailable {
		t.Fatalf("Artifacts() = %#v, error %v", artifacts, err)
	}
	artifact, metadata, err := manager.OpenArtifact(
		record.Owner,
		record.JobID,
		artifacts[0].ArtifactRef,
	)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(artifact)
	_ = artifact.Close()
	if readErr != nil || string(data) != "durable artifact\n" || metadata.Size != int64(len(data)) {
		t.Fatalf("artifact = %q, metadata %#v, error %v", data, metadata, readErr)
	}
}

func TestDirectJobManagerBoundsLogsWithoutBlockingProcess(t *testing.T) {
	manager, store, root, executable := newTestDirectJobManager(t, DirectJobLimits{
		StdoutBytes: 128,
		StderrBytes: 128,
	})
	plan := testDirectJobPlan(t, executable, root, "large", []JobArtifactDeclaration{}, 5)
	started, err := manager.Start(plan)
	if err != nil {
		t.Fatal(err)
	}
	record := waitForTerminalJob(t, store, started.JobID)
	if record.State != JobSucceeded || record.Stdout.Bytes != 128 || !record.Stdout.Truncated {
		t.Fatalf("bounded log record = %#v", record)
	}
	chunk, err := manager.ReadLog(record.Owner, record.JobID, false, 0, 256)
	if err != nil || len(chunk.Data) != 128 || !chunk.Truncated {
		t.Fatalf("bounded log chunk = %#v, error %v", chunk, err)
	}
}

func TestDirectJobManagerCancellationIsDurableAndBounded(t *testing.T) {
	manager, store, root, executable := newTestDirectJobManager(t, DirectJobLimits{})
	plan := testDirectJobPlan(t, executable, root, "sleep", []JobArtifactDeclaration{}, 20)
	started, err := manager.Start(plan)
	if err != nil {
		t.Fatal(err)
	}
	requested, err := manager.Cancel(started.Owner, started.JobID)
	if err != nil || requested.State != JobCancelRequested {
		t.Fatalf("Cancel() = %#v, error %v", requested, err)
	}
	record := waitForTerminalJob(t, store, started.JobID)
	if record.State != JobCanceled && record.State != JobCancelUnknown {
		t.Fatalf("canceled job = %#v", record)
	}
	if !record.CancellationSignal {
		t.Fatalf("cancellation signal was not recorded: %#v", record)
	}
	repeated, err := manager.Cancel(started.Owner, started.JobID)
	if err != nil || repeated.State != record.State {
		t.Fatalf("repeated Cancel() = %#v, error %v", repeated, err)
	}
}

func TestJobTimeoutDelayUsesPersistedAbsoluteDeadline(t *testing.T) {
	now := time.Unix(100, 0)
	if delay := jobTimeoutDelay(now.Add(-time.Second).UnixNano(), now); delay != 0 {
		t.Fatalf("expired deadline delay = %v", delay)
	}
	if delay := jobTimeoutDelay(now.Add(3*time.Second).UnixNano(), now); delay != 3*time.Second {
		t.Fatalf("future deadline delay = %v", delay)
	}
}

func TestPrepareActiveRetainsRootOwnershipOnFailure(t *testing.T) {
	manager, store, root, executable := newTestDirectJobManager(t, DirectJobLimits{})
	rootFile, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	anchored := &fileRoot{path: root, file: rootFile}
	prepared := preparedDirectJob{
		command: preparedSystemExec{
			executable: executable,
			cwd:        root,
			env:        os.Environ(),
		},
		root: anchored,
	}
	store.Close()
	active, err := manager.prepareActive("job_prepare_failure", &prepared)
	if err == nil || active != nil {
		t.Fatalf("prepareActive() = %#v, error %v", active, err)
	}
	if prepared.root != anchored || prepared.root.file == nil {
		t.Fatalf("prepared root ownership was lost: %#v", prepared.root)
	}
	if err := prepared.root.close(); err != nil {
		t.Fatal(err)
	}
}

func TestDirectJobManagerEnforcesTimeoutWithoutReplay(t *testing.T) {
	manager, store, root, executable := newTestDirectJobManager(t, DirectJobLimits{})
	plan := testDirectJobPlan(t, executable, root, "sleep", []JobArtifactDeclaration{}, 1)
	started, err := manager.Start(plan)
	if err != nil {
		t.Fatal(err)
	}
	record := waitForTerminalJob(t, store, started.JobID)
	if record.State != JobTimedOut || record.FailureCode != "TIMEOUT" ||
		!record.CancellationSignal {
		t.Fatalf("timed-out job = %#v", record)
	}
	duplicate, err := manager.Start(plan)
	if err != nil || duplicate.JobID != record.JobID || duplicate.State != JobTimedOut {
		t.Fatalf("timed-out duplicate = %#v, error %v", duplicate, err)
	}
}

func TestDirectJobManagerRejectsSymlinkArtifact(t *testing.T) {
	manager, store, root, executable := newTestDirectJobManager(t, DirectJobLimits{})
	plan := testDirectJobPlan(t, executable, root, "symlink", []JobArtifactDeclaration{
		{Name: "result", Path: "artifact.out"},
	}, 5)
	started, err := manager.Start(plan)
	if err != nil {
		t.Fatal(err)
	}
	record := waitForTerminalJob(t, store, started.JobID)
	if record.State != JobSucceeded || len(record.Artifacts) != 1 ||
		record.Artifacts[0].State != JobArtifactFailed ||
		record.Artifacts[0].ArtifactRef != "" {
		t.Fatalf("symlink artifact record = %#v", record)
	}
}

func TestCopyBoundedJobArtifactProbesOverflowWithoutWritingIt(t *testing.T) {
	destination := &bytes.Buffer{}
	written, overflow, err := copyBoundedJobArtifact(
		destination,
		bytes.NewReader([]byte("12345")),
		4,
	)
	if err != nil || written != 4 || !overflow || destination.String() != "1234" {
		t.Fatalf(
			"copyBoundedJobArtifact() = written %d, overflow %t, data %q, error %v",
			written,
			overflow,
			destination.String(),
			err,
		)
	}
}

func TestDirectJobManagerRejectsArtifactGrowthAfterOpen(t *testing.T) {
	manager, store, root, _ := newTestDirectJobManager(t, DirectJobLimits{})
	path := filepath.Join(root, "growing.out")
	if err := os.WriteFile(path, []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := identityFromInfo(info)
	if err != nil {
		t.Fatal(err)
	}
	source := &resolvedFile{file: file, info: info, identity: identity}
	appendFile, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendFile.WriteString("5"); err != nil {
		_ = appendFile.Close()
		t.Fatal(err)
	}
	if err := appendFile.Close(); err != nil {
		t.Fatal(err)
	}
	result := manager.snapshotArtifact("result", source, 4)
	if result.State != JobArtifactFailed || result.FailureCode != "SOURCE_CHANGED" ||
		result.ArtifactRef != "" {
		t.Fatalf("growing artifact result = %#v", result)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".artifact") {
			t.Fatalf("rejected snapshot was retained: %s", entry.Name())
		}
	}
}

func TestDirectJobManagerRejectsArtifactPathBeforeAcceptance(t *testing.T) {
	manager, store, root, executable := newTestDirectJobManager(t, DirectJobLimits{})
	plan := testDirectJobPlan(t, executable, root, "success", []JobArtifactDeclaration{
		{Name: "result", Path: filepath.Join(root, "artifact.out")},
	}, 5)
	if _, err := manager.Start(plan); err == nil {
		t.Fatal("Start() accepted an absolute artifact path")
	}
	if records := store.Records(); len(records) != 0 {
		t.Fatalf("denied job was durably accepted: %#v", records)
	}
}

func TestDirectJobManagerOwnsGroupUntilDescendantsExit(t *testing.T) {
	manager, store, root, executable := newTestDirectJobManager(t, DirectJobLimits{})
	plan := testDirectJobPlan(t, executable, root, "descendant", []JobArtifactDeclaration{
		{Name: "result", Path: "artifact.out"},
	}, 5)
	started, err := manager.Start(plan)
	if err != nil {
		t.Fatal(err)
	}
	record := waitForTerminalJob(t, store, started.JobID)
	if record.State != JobFailed || record.FailureCode != "PROCESS_GROUP_OUTLIVED_LEADER" {
		t.Fatalf("descendant job = %#v", record)
	}
	artifacts, err := manager.Artifacts(record.Owner, record.JobID)
	if err != nil || len(artifacts) != 1 || artifacts[0].State != JobArtifactAvailable {
		t.Fatalf("descendant artifacts = %#v, error %v", artifacts, err)
	}
	artifact, _, err := manager.OpenArtifact(record.Owner, record.JobID, artifacts[0].ArtifactRef)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(artifact)
	_ = artifact.Close()
	if readErr != nil || len(data) == 0 {
		t.Fatalf("snapshot after descendant cleanup = %q, error %v", data, readErr)
	}
	source, err := os.ReadFile(filepath.Join(root, "artifact.out"))
	if err != nil || !bytes.Equal(source, data) {
		t.Fatalf("source %q does not match terminal snapshot %q, error %v", source, data, err)
	}
	pidData, err := os.ReadFile(filepath.Join(root, "descendant.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidData))
	if err != nil {
		t.Fatal(err)
	}
	waitForProcessExit(t, pid)
	finalSource, err := os.ReadFile(filepath.Join(root, "artifact.out"))
	if err != nil || !bytes.Equal(finalSource, data) {
		t.Fatalf("source changed after terminal snapshot = %q, error %v", finalSource, err)
	}
}

func newTestDirectJobManager(
	t *testing.T,
	limits DirectJobLimits,
) (*DirectJobManager, *JobStore, string, string) {
	t.Helper()
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := normalizeSystemExecPolicy(SystemExecPolicy{
		WorkingRoots: []string{root},
		Executables:  []string{executable},
		Environment:  []string{jobHelperEnabled, jobHelperAction},
		Discovery: &SystemExecDiscovery{
			ExecutableAliases:   map[string]string{"helper": executable},
			WorkingScopeAliases: map[string]string{"root": root},
			EnvironmentNames:    []string{jobHelperEnabled, jobHelperAction},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewJobStore(filepath.Join(t.TempDir(), "jobs"), JobStoreLimits{
		Records: 16, IndexBytes: 1024 * 1024,
		PayloadBytes: DefaultJobStorePayloadBytes, Retention: DefaultJobRetention,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	manager, err := NewDirectJobManager(store, policy, "test-jobs", "job-profile-v1", limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	return manager, store, root, executable
}

func testDirectJobPlan(
	t *testing.T,
	executable string,
	root string,
	action string,
	artifacts []JobArtifactDeclaration,
	timeout int,
) nodes.ExecutionPlan {
	t.Helper()
	input, err := json.Marshal(directJobInput{
		Argv: []string{"helper", "-test.run=^TestJobHelperProcess$"},
		CWD:  "root",
		Env: map[string]string{
			jobHelperEnabled: "1",
			jobHelperAction:  action,
		},
		TimeoutSeconds: float64(timeout),
		Artifacts:      artifacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	profiles := []nodes.JobProfileDescriptor{{
		Alias: "test-jobs", Revision: "job-profile-v1", Executor: "system_exec",
		AuthorityDigest: strings.Repeat("b", 64), TimeoutSecondsMax: timeout,
		ConcurrentJobs: 2, StdoutBytesMax: DefaultJobLogBytes, StderrBytesMax: DefaultJobLogBytes,
		ArtifactCountMax: DefaultJobArtifactCount, ArtifactBytesMax: DefaultJobArtifactBytes,
		ArtifactsTotalBytesMax: DefaultJobArtifactBytes, RetentionSeconds: int(DefaultJobRetention / time.Second),
		CancelGuarantee: string(JobCancelProcessGroup), ExecutableAliases: []string{"helper"},
		WorkingScopes: []string{"root"}, EnvironmentNames: []string{jobHelperAction, jobHelperEnabled},
		Approval: nodes.JobProfileApproval{Start: "required", Read: "none", Cancel: "required"},
	}}
	descriptors, err := nodes.JobCommandDescriptors(profiles)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, ok := nodes.ProjectJobDescriptorForProfile(descriptors[0], "test-jobs")
	if !ok {
		t.Fatal("project job descriptor")
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	plan, err := nodes.PrepareExecutionPlan(nodes.InvocationRequest{
		InvocationID: "inv_job_" + suffix, IdempotencyKey: "idem_job_" + suffix,
		NodeID: "node_test", CatalogHash: strings.Repeat("a", 64), Command: JobCommandStart,
		Input: input, AgentID: "agent_test", SessionID: "session_test", ActorID: "actor_test",
		// The invocation only accepts the durable start; it does not own the
		// payload deadline.
		TimeoutSeconds: 1, OutputLimitBytes: 4096, JobProfile: "test-jobs",
	}, descriptor, LocalExecutor, "runtime-policy-v1", time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func waitForTerminalJob(t *testing.T, store *JobStore, jobID string) JobRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		record, found, err := store.Lookup(jobID)
		if err != nil {
			t.Fatal(err)
		}
		if found && record.State.terminal() {
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	record, _, err := store.Lookup(jobID)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("job did not become terminal: %#v", record)
	return JobRecord{}
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant process %d remained alive", pid)
}
