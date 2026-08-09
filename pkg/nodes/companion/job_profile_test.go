//go:build linux || darwin

package companion

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestNormalizeJobProfilesIsDenyByDefaultAndProjectsAliases(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := normalizeSystemExecPolicy(SystemExecPolicy{
		WorkingRoots: []string{root}, Executables: []string{executable},
		Discovery: &SystemExecDiscovery{
			ExecutableAliases:   map[string]string{"runner": executable},
			WorkingScopeAliases: map[string]string{"project": root},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := normalizeJobProfiles(JobProfiles{
		"disabled": {},
		"builds":   {Enabled: true, Revision: "builds-v1"},
	}, &policy)
	if err != nil {
		t.Fatal(err)
	}
	if !HasEnabledJobProfile(profiles) || profiles["disabled"].Enabled {
		t.Fatalf("normalized profiles = %#v", profiles)
	}
	descriptors, err := jobProfileDescriptorsForPolicy(profiles, policy)
	if err != nil || len(descriptors) != 1 {
		t.Fatalf("jobProfileDescriptorsForPolicy() = %#v, %v", descriptors, err)
	}
	encoded, err := json.Marshal(descriptors)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), root) || strings.Contains(string(encoded), executable) ||
		!strings.Contains(string(encoded), "runner") || !strings.Contains(string(encoded), "project") {
		t.Fatalf("unsafe or incomplete job projection: %s", encoded)
	}
	profile := descriptors[0]
	if profile.Approval.Start != "required" || profile.Approval.Read != "none" ||
		profile.Approval.Cancel != "required" || profile.RetentionSeconds != int(DefaultJobRetention/time.Second) {
		t.Fatalf("job profile defaults = %#v", profile)
	}
	changed := policy
	changed.Environment = append([]string(nil), policy.Environment...)
	changed.Environment = append(changed.Environment, "HIDDEN_JOB_SECRET")
	if directJobSystemExecAuthorityDigest(changed) == directJobSystemExecAuthorityDigest(policy) {
		t.Fatal("hidden system_exec authority change did not invalidate job authority")
	}
}

func TestNormalizeJobProfilesFailsClosed(t *testing.T) {
	if _, err := normalizeJobProfiles(JobProfiles{
		"builds": {Enabled: true, Revision: "builds-v1"},
	}, nil); err == nil {
		t.Fatal("enabled job profile without system_exec accepted")
	}
	if _, err := normalizeJobProfiles(JobProfiles{
		"disabled": {Revision: "unexpected"},
	}, nil); err == nil {
		t.Fatal("disabled job profile with authority accepted")
	}
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := normalizeSystemExecPolicy(SystemExecPolicy{
		WorkingRoots: []string{root}, Executables: []string{executable},
		Discovery: &SystemExecDiscovery{
			ExecutableAliases:   map[string]string{"runner": executable},
			WorkingScopeAliases: map[string]string{"project": root},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeJobProfiles(JobProfiles{
		"builds": {
			Enabled: true, Revision: "builds-v1",
			TimeoutSecondsMax: nodes.MaxJobTimeoutSeconds + 1,
		},
	}, &policy); err == nil {
		t.Fatal("oversized job profile timeout accepted")
	}
}

func TestJobRuntimeExecutesTypedCommandsAndPreservesOwnership(t *testing.T) {
	jobRuntime, commandRuntime, root := newTestJobCommandRuntime(t)
	startInput := json.RawMessage(
		`{"argv":["helper","-test.run=^TestJobHelperProcess$"],"cwd":"project","timeout_seconds":5,` +
			`"env":{"MINTCLAW_JOB_HELPER":"1","MINTCLAW_JOB_ACTION":"success"},` +
			`"artifacts":[{"name":"result","path":"artifact.out"}]}`,
	)
	startPlan := testRuntimePlanAtWithOutputLimit(
		t,
		commandRuntime,
		nodes.JobCommandStart,
		startInput,
		time.Now(),
		time.Minute,
		4096,
	)
	result, err := commandRuntime.Invoke(t.Context(), startPlan)
	if err != nil {
		t.Fatal(err)
	}
	var started map[string]any
	if err := json.Unmarshal(result, &started); err != nil || started["state"] != string(JobRunning) {
		t.Fatalf("start output = %s, error %v", result, err)
	}
	jobID, ok := started["job_id"].(string)
	if !ok {
		t.Fatalf("start output lacks job ID: %s", result)
	}
	startResult := append(json.RawMessage(nil), result...)
	record := waitForTerminalJob(t, jobRuntime.store, jobID)
	if record.State != JobSucceeded {
		t.Fatalf("terminal job = %#v", record)
	}
	statusPlan := testRuntimePlanAtWithOutputLimit(
		t,
		commandRuntime,
		nodes.JobCommandStatus,
		json.RawMessage(`{"job_id":"`+jobID+`"}`),
		time.Now(),
		time.Minute,
		4096,
	)
	wrongOwner := statusPlan
	wrongOwner.ActorID = "actor_wrong"
	if err := commandRuntime.handlers[nodes.JobCommandStatus].(commandAuthorizer).authorize(
		wrongOwner,
	); !errors.Is(
		err,
		ErrJobNotFound,
	) {
		t.Fatalf("wrong-owner status authorization error = %v", err)
	}
	result, err = commandRuntime.Invoke(t.Context(), statusPlan)
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err := json.Unmarshal(result, &status); err != nil || status["state"] != string(JobSucceeded) ||
		status["exit_code_known"] != true || status["exit_code"] != float64(0) ||
		status["artifact_count"] != float64(1) {
		t.Fatalf("status output = %s, error %v", result, err)
	}
	logsPlan := testRuntimePlanAtWithOutputLimit(
		t,
		commandRuntime,
		nodes.JobCommandLogs,
		json.RawMessage(`{"job_id":"`+jobID+`","stream":"stdout","cursor":0,"limit_bytes":1024}`),
		time.Now(),
		time.Minute,
		4096,
	)
	if err := commandRuntime.handlers[nodes.JobCommandLogs].(commandAuthorizer).authorize(logsPlan); err != nil {
		t.Fatalf("authorize logs plan: %v (input %s)", err, logsPlan.Input)
	}
	result, err = commandRuntime.Invoke(t.Context(), logsPlan)
	if err != nil || !strings.Contains(string(result), "job stdout") {
		t.Fatalf("logs output = %s, error %v", result, err)
	}
	artifactsPlan := testRuntimePlanAtWithOutputLimit(
		t,
		commandRuntime,
		nodes.JobCommandArtifacts,
		json.RawMessage(`{"job_id":"`+jobID+`"}`),
		time.Now(),
		time.Minute,
		4096,
	)
	result, err = commandRuntime.Invoke(t.Context(), artifactsPlan)
	if err != nil || !strings.Contains(string(result), "jobart_") {
		t.Fatalf("artifacts output = %s, error %v", result, err)
	}
	if count, readErr := os.ReadFile(root + "/launch-count"); readErr != nil || string(count) != "x" {
		t.Fatalf("launch count = %q, error %v", count, readErr)
	}
	duplicate, err := commandRuntime.Invoke(t.Context(), startPlan)
	if err != nil || string(duplicate) != string(startResult) {
		t.Fatalf("duplicate start = %s, want %s, error %v", duplicate, startResult, err)
	}
}

func TestJobRuntimeCancelIsSeparateAndIdempotent(t *testing.T) {
	jobRuntime, commandRuntime, _ := newTestJobCommandRuntime(t)
	startPlan := testRuntimePlanAtWithOutputLimit(
		t,
		commandRuntime,
		nodes.JobCommandStart,
		json.RawMessage(
			`{"argv":["helper","-test.run=^TestJobHelperProcess$"],"cwd":"project","timeout_seconds":20,`+
				`"env":{"MINTCLAW_JOB_HELPER":"1","MINTCLAW_JOB_ACTION":"sleep"}}`,
		),
		time.Now(),
		time.Minute,
		4096,
	)
	result, err := commandRuntime.Invoke(t.Context(), startPlan)
	if err != nil {
		t.Fatal(err)
	}
	var started map[string]any
	if err := json.Unmarshal(result, &started); err != nil {
		t.Fatal(err)
	}
	jobID := started["job_id"].(string)
	cancelPlan := testRuntimePlanAtWithOutputLimit(
		t,
		commandRuntime,
		nodes.JobCommandCancel,
		json.RawMessage(`{"job_id":"`+jobID+`"}`),
		time.Now(),
		time.Minute,
		4096,
	)
	canceled, err := commandRuntime.Invoke(t.Context(), cancelPlan)
	if err != nil || !strings.Contains(string(canceled), string(JobCancelRequested)) {
		t.Fatalf("cancel output = %s, error %v", canceled, err)
	}
	replayed, err := commandRuntime.Invoke(t.Context(), cancelPlan)
	if err != nil || string(replayed) != string(canceled) {
		t.Fatalf("duplicate cancel = %s, want %s, error %v", replayed, canceled, err)
	}
	record := waitForTerminalJob(t, jobRuntime.store, jobID)
	if record.State != JobCanceled || !record.CancellationSignal {
		t.Fatalf("terminal canceled job = %#v", record)
	}
}

func TestFitJobLogOutputHonorsEncodedLimit(t *testing.T) {
	input := jobLogsInput{JobID: "job_0123456789abcdef0123456789abcdef", Stream: "stdout", Cursor: 7}
	chunk := JobLogChunk{
		Data: []byte(strings.Repeat(`"`, 256)), Available: 256,
		State: JobRunning,
	}
	output, err := fitJobLogOutput(input, chunk, 256)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(output)
	if err != nil || len(encoded) > 256 || output.NextCursor <= input.Cursor || output.NextCursor > 263 {
		t.Fatalf("bounded log output = %s, error %v", encoded, err)
	}
	if _, err := fitJobLogOutput(input, chunk, 1); err == nil {
		t.Fatal("impossible log output limit accepted")
	}
}

func TestJobStartRejectsTinyOutputBeforeLaunch(t *testing.T) {
	jobRuntime, commandRuntime, root := newTestJobCommandRuntime(t)
	plan := testRuntimePlanAtWithOutputLimit(
		t,
		commandRuntime,
		nodes.JobCommandStart,
		json.RawMessage(
			`{"argv":["helper","-test.run=^TestJobHelperProcess$"],"cwd":"project","timeout_seconds":5,`+
				`"env":{"MINTCLAW_JOB_HELPER":"1","MINTCLAW_JOB_ACTION":"success"}}`,
		),
		time.Now(),
		time.Minute,
		32,
	)
	if _, err := commandRuntime.Invoke(t.Context(), plan); !errors.Is(err, nodes.ErrCommandDenied) {
		t.Fatalf("tiny-output start error = %v", err)
	}
	if _, err := os.Stat(root + "/launch-count"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tiny-output start crossed launch boundary: %v", err)
	}
	if len(jobRuntime.store.records) != 0 {
		t.Fatalf("tiny-output start created durable job: %#v", jobRuntime.store.records)
	}
}

func newTestJobCommandRuntime(t *testing.T) (*JobRuntime, *Runtime, string) {
	t.Helper()
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	systemExec, err := normalizeSystemExecPolicy(SystemExecPolicy{
		WorkingRoots: []string{root}, Executables: []string{executable},
		Environment: []string{jobHelperEnabled, jobHelperAction},
		Discovery: &SystemExecDiscovery{
			ExecutableAliases:   map[string]string{"helper": executable},
			WorkingScopeAliases: map[string]string{"project": root},
			EnvironmentNames:    []string{jobHelperEnabled, jobHelperAction},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := normalizeJobProfiles(JobProfiles{
		"test-jobs": {Enabled: true, Revision: "test-jobs-v1", TimeoutSecondsMax: 30},
	}, &systemExec)
	if err != nil {
		t.Fatal(err)
	}
	jobRuntime, err := NewJobRuntime(t.TempDir(), profiles, systemExec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := jobRuntime.Shutdown(ctx); err != nil {
			t.Errorf("JobRuntime.Shutdown() error = %v", err)
		}
	})
	commands := []string{
		nodes.JobCommandStart,
		nodes.JobCommandStatus,
		nodes.JobCommandLogs,
		nodes.JobCommandArtifacts,
		nodes.JobCommandCancel,
	}
	commandRuntime, err := NewRuntime(
		"node_test",
		"test",
		nodes.LocalCommandPolicy{
			Revision: "policy-v1", AllowedCommands: commands, MaximumRisk: nodes.RiskWrite,
			MaxTimeoutSeconds: 30, MaxOutputBytes: 64 * 1024,
		},
		newMemoryInvocationLedger(),
		WithJobRuntime(jobRuntime),
	)
	if err != nil {
		t.Fatal(err)
	}
	return jobRuntime, commandRuntime, root
}
