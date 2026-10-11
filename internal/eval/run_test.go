package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/health"
)

func loadedTestCase(t *testing.T, id string) LoadedCase {
	t.Helper()
	item := testCase()
	item.ID = id
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	return LoadedCase{Case: item, Digest: digest(data)}
}

func runAdmissionFixture(t *testing.T, fixture *admissionFixture, cases ...LoadedCase) (RunReport, error) {
	t.Helper()
	return Run(t.Context(), fixture.preset, cases, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: 10 * time.Second,
	})
}

func TestPreparedRunKeepsFollowupsAndUsesFreshTrialSessions(t *testing.T) {
	fixture := newAdmissionFixture(t)
	result, err := runAdmissionFixture(t, fixture,
		loadedTestCase(t, "general.test.first"), loadedTestCase(t, "general.test.second"))
	if err != nil || result.Verdict != Pass || !result.Complete || len(result.Trials) != 2 ||
		result.Coverage[Pass] != 2 || fixture.tasks.Load() != 4 || fixture.probes.Load() != 3 {
		t.Fatal("prepared scripted run failed", err, result.Verdict)
	}
	requests := make([]protocol.MintClawMessage, 4)
	for index := range requests {
		requests[index] = <-fixture.requests
	}
	if requests[0].SessionID != requests[1].SessionID || requests[2].SessionID != requests[3].SessionID ||
		requests[0].SessionID == requests[2].SessionID || requests[0].ID == requests[1].ID {
		t.Fatal("trial sessions were reused or followup was replayed")
	}
	for _, summary := range result.Trials {
		data, readErr := os.ReadFile(filepath.Join(result.ReportDir, summary.File))
		if readErr != nil || strings.Contains(string(data), admissionTestToken) ||
			strings.Contains(string(data), "test-channel-token") {
			t.Fatal("private evidence unavailable or credential leaked")
		}
		var trial Trial
		if json.Unmarshal(data, &trial) != nil || !trial.TransportClosed || trial.RemoteState != "unknown" {
			t.Fatal("local closure implied remote cancellation")
		}
	}
	assertPrivateReport(t, result)
}

func TestPreparedRunRefusesChangedTargetWithoutTask(t *testing.T) {
	fixture := newAdmissionFixture(t)
	fixture.mutateProbe = func(probe *health.RuntimeProbe, number int32) {
		if number > 1 {
			probe.InstanceSHA256 = strings.Repeat("0", 64)
		}
	}
	result, err := runAdmissionFixture(t, fixture, loadedTestCase(t, "general.test.changed"))
	if !errors.Is(err, ErrNotPassed) || result.Verdict != Inconclusive || !result.Complete ||
		len(result.Trials) != 1 || result.Trials[0].Code != "target_changed" || fixture.tasks.Load() != 0 {
		t.Fatal("changed instance admitted a task", err)
	}
}

func TestPreparedGeneralRunInspectsDuplicateKeyBeforeProjection(t *testing.T) {
	fixture := newAdmissionFixture(t)
	fixture.rawResponse = func(response protocol.MintClawMessage) []byte {
		data, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		return []byte(strings.Replace(string(data), `"content":"synthetic-marker"`,
			`"content":"test-channel-token","content":"synthetic-marker"`, 1))
	}
	result, err := runAdmissionFixture(t, fixture, loadedTestCase(t, "general.raw-credential"))
	if !errors.Is(err, ErrNotPassed) || result.Verdict != Fail || len(result.Trials) != 1 ||
		result.Trials[0].Code != "private_wire_evidence" || fixture.tasks.Load() != 1 {
		t.Fatal("duplicate-key projection concealed raw credential evidence", err, result.Verdict)
	}
	data, err := os.ReadFile(filepath.Join(result.ReportDir, result.Trials[0].File))
	if err != nil || strings.Contains(string(data), "test-channel-token") {
		t.Fatal("unsafe frame was retained instead of withheld")
	}
}

func TestPreparedRunReportsUnavailableCoverageWithoutPassing(t *testing.T) {
	for _, unavailable := range []string{"capability", "driver", "domain"} {
		t.Run(unavailable, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			item := loadedTestCase(t, "general.test.unavailable")
			switch unavailable {
			case "capability":
				item.Case.Requires = []string{"pdf"}
			case "driver":
				item.Case.Driver = Driver{Mode: "llm_user", Persona: "A synthetic user."}
			case "domain":
				item.Case.Driver.Facts = map[string]string{"name": "synthetic-name"}
			}
			result, err := runAdmissionFixture(t, fixture, item)
			if !errors.Is(err, ErrNotPassed) || result.Verdict != Skipped || result.Coverage[Skipped] != 1 ||
				fixture.tasks.Load() != 0 || fixture.probes.Load() != 1 {
				t.Fatal("unsupported coverage became success or reached the target", err)
			}
		})
	}
}

func TestPreparedRunReportFailureNeverReturnsPassOrErasesHardFailure(t *testing.T) {
	for _, hardFailure := range []bool{false, true} {
		t.Run(string(map[bool]Verdict{false: Error, true: Fail}[hardFailure]), func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			setup := make(chan error, 1)
			fixture.respond = func(_ protocol.MintClawMessage, response *protocol.MintClawMessage) {
				if fixture.tasks.Load() == 1 {
					entries, readErr := os.ReadDir(fixture.output)
					if readErr != nil || len(entries) != 1 {
						setup <- errors.New("test report directory not found")
					} else {
						setup <- os.Mkdir(filepath.Join(fixture.output, entries[0].Name(), "summary.md"), 0o700)
					}
				}
				if hardFailure {
					response.Payload[protocol.PayloadKeyTraceScopes] = []events.TraceScope{{
						Workspace: "/unexpected-workspace", TurnID: "wrong-scope",
					}}
				}
			}
			result, err := runAdmissionFixture(t, fixture, loadedTestCase(t, "general.test.report-error"))
			if setupErr := <-setup; setupErr != nil {
				t.Fatal(setupErr)
			}
			want := Error
			if hardFailure {
				want = Fail
			}
			if !errors.Is(err, ErrReport) || result.Verdict != want || result.Complete || len(result.Trials) != 1 {
				t.Fatal("report failure claimed success or erased a hard failure", err, result.Verdict)
			}
			if _, statErr := os.Stat(filepath.Join(result.ReportDir, "run.json")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal("a completed run.json was published before its summary")
			}
		})
	}
}

func TestPreparedRunRejectsInputsBeforeAdmission(t *testing.T) {
	fixture := newAdmissionFixture(t)
	item := loadedTestCase(t, "general.test.invalid")
	for _, test := range []struct {
		cases   []LoadedCase
		timeout time.Duration
	}{
		{nil, time.Minute},
		{[]LoadedCase{item, item}, time.Minute},
		{[]LoadedCase{item}, -time.Second},
		{[]LoadedCase{item}, 5 * time.Hour},
	} {
		result, err := Run(t.Context(), fixture.preset, test.cases, RunOptions{
			Home: fixture.home, OutputParent: fixture.output, SuiteTimeout: test.timeout,
		})
		if err == nil || result.ID != "" || fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
			t.Fatal("invalid input reached admission or produced a success report")
		}
	}
}

func TestPreparedRunRejectsRelativeOracleBeforeAdmission(t *testing.T) {
	fixture := newAdmissionFixture(t)
	result, err := Run(
		t.Context(),
		fixture.preset,
		[]LoadedCase{loadedTestCase(t, "general.test.oracle-path")},
		RunOptions{
			Home: fixture.home, OutputParent: fixture.output, PDFOraclePython: "./private-oracle",
		},
	)
	if !errors.Is(err, ErrRunOptions) || result.ID != "" || fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
		t.Fatal("relative oracle reached target admission or produced a completed report")
	}
}

func assertPrivateReport(t *testing.T, result RunReport) {
	t.Helper()
	info, err := os.Stat(result.ReportDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatal("run directory not private", err)
	}
	entries, err := os.ReadDir(result.ReportDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		entryInfo, statErr := entry.Info()
		if statErr != nil || !entryInfo.Mode().IsRegular() || entryInfo.Mode().Perm() != 0o600 {
			t.Fatal("report evidence not private", statErr)
		}
	}
	data, err := os.ReadFile(filepath.Join(result.ReportDir, "run.json"))
	var persisted RunReport
	if err != nil || json.Unmarshal(data, &persisted) != nil || !persisted.Complete ||
		persisted.Verdict != result.Verdict || persisted.Usage != "unknown" {
		t.Fatal("completed report does not match the observed run", err)
	}
}
