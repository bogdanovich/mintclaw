package eval

import (
	"context"
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

func TestRepeatedRunRetainsEveryFreshTrialAndSameSessionFollowup(t *testing.T) {
	fixture := newAdmissionFixture(t)
	cases := []LoadedCase{loadedTestCase(t, "general.repeat.first"), loadedTestCase(t, "general.repeat.second")}
	result, err := Run(t.Context(), fixture.preset, cases, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, Repeat: 3, SuiteTimeout: 10 * time.Second,
	})
	if err != nil || !result.Complete || result.Repeat != 3 || result.Verdict != Pass ||
		len(result.Trials) != 6 || result.Coverage[Pass] != 6 || fixture.tasks.Load() != 12 ||
		fixture.probes.Load() != 7 {
		t.Fatal("independent repetitions or retained coverage lost", err)
	}
	identities, sessions, requests := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for ordinal, summary := range result.Trials {
		first, second := <-fixture.requests, <-fixture.requests
		if first.SessionID != second.SessionID || sessions[first.SessionID] || requests[first.ID] ||
			requests[second.ID] || first.ID == second.ID || identities[summary.ID] ||
			summary.CaseID != cases[ordinal/3].Case.ID || summary.Repetition != ordinal%3+1 {
			t.Fatal("a repeated trial reused authority or lost case-major order")
		}
		identities[summary.ID], sessions[first.SessionID] = true, true
		requests[first.ID], requests[second.ID] = true, true
		data, err := os.ReadFile(filepath.Join(result.ReportDir, summary.File))
		var trial Trial
		if err != nil || json.Unmarshal(data, &trial) != nil || trial.ID != summary.ID ||
			trial.SessionID != first.SessionID || trial.Repetition != summary.Repetition ||
			trial.Verdict != summary.Verdict {
			t.Fatal("a repetition was not independently retained")
		}
	}
	summary, err := os.ReadFile(filepath.Join(result.ReportDir, "summary.md"))
	if err != nil || !strings.Contains(string(summary), "1/3") || !strings.Contains(string(summary), "3/3") {
		t.Fatal("summary lost repetition identity")
	}
	assertPrivateReport(t, result)
}

func TestRepeatedRunNeverDropsFirstFailureOrUnattemptedTrials(t *testing.T) {
	for _, condition := range []string{"hard-failure", "target-changed", "capability"} {
		t.Run(condition, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			item := loadedTestCase(t, "general.repeat.coverage")
			switch condition {
			case "hard-failure":
				fixture.respond = func(_ protocol.MintClawMessage, response *protocol.MintClawMessage) {
					if fixture.tasks.Load() == 1 {
						response.Payload[protocol.PayloadKeyTraceScopes] = []events.TraceScope{{
							Workspace: "/wrong-repeat-workspace", TurnID: response.ID,
						}}
					}
				}
			case "target-changed":
				fixture.mutateProbe = func(probe *health.RuntimeProbe, number int32) {
					if number > 2 {
						probe.InstanceSHA256 = strings.Repeat("0", 64)
					}
				}
			case "capability":
				item.Case.Requires = []string{"pdf"}
			}
			result, err := Run(t.Context(), fixture.preset, []LoadedCase{item}, RunOptions{
				Home: fixture.home, OutputParent: fixture.output, Repeat: 3, SuiteTimeout: 10 * time.Second,
			})
			if !errors.Is(err, ErrNotPassed) || !result.Complete || len(result.Trials) != 3 {
				t.Fatal("failed or unattempted repetition disappeared", err)
			}
			switch condition {
			case "hard-failure":
				if result.Verdict != Fail || result.Coverage[Fail] != 1 || result.Coverage[Pass] != 2 {
					t.Fatal("later passing repetitions hid the first hard failure")
				}
			case "target-changed":
				if result.Verdict != Inconclusive || result.Coverage[Pass] != 1 ||
					result.Coverage[Inconclusive] != 2 || fixture.tasks.Load() != 2 {
					t.Fatal("changed target received extra repeated work or lost unknown coverage")
				}
			case "capability":
				if result.Verdict != Skipped || result.Coverage[Skipped] != 3 || fixture.tasks.Load() != 0 {
					t.Fatal("unavailable repetitions became passes or reached the target")
				}
			}
			assertPrivateReport(t, result)
		})
	}
}

func TestRepeatedRunRejectsInvalidBudgetsBeforeTargetAdmission(t *testing.T) {
	fixture := newAdmissionFixture(t)
	cases := []LoadedCase{}
	for _, name := range []string{"one", "two", "three", "four", "five"} {
		cases = append(cases, loadedTestCase(t, "general.repeat."+name))
	}
	for _, repeat := range []int{-1, MaxRepeat + 1, MaxRepeat} {
		result, err := Run(t.Context(), fixture.preset, cases, RunOptions{
			Home: fixture.home, OutputParent: fixture.output, Repeat: repeat,
		})
		if !errors.Is(err, ErrRunOptions) || result.ID != "" || fixture.probes.Load() != 0 ||
			fixture.tasks.Load() != 0 {
			t.Fatal("invalid repetition budget reached target admission")
		}
	}
}

func TestRepeatedRunPreservesRemainingTrialsAfterSuiteInterruption(t *testing.T) {
	fixture := newAdmissionFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fixture.respond = func(_ protocol.MintClawMessage, _ *protocol.MintClawMessage) {
		if fixture.tasks.Load() == 2 {
			cancel()
		}
	}
	result, err := Run(ctx, fixture.preset, []LoadedCase{loadedTestCase(t, "general.repeat.interrupted")}, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, Repeat: 3, SuiteTimeout: 10 * time.Second,
	})
	if !errors.Is(err, ErrNotPassed) || !result.Complete || result.Verdict != Inconclusive ||
		len(result.Trials) != 3 || result.Coverage[Inconclusive] != 3 || fixture.tasks.Load() != 2 ||
		result.Trials[1].Code != "suite_interrupted" || result.Trials[2].Code != "suite_interrupted" {
		t.Fatal("suite interruption dropped trials, replayed work or claimed success", err)
	}
	assertPrivateReport(t, result)
}
