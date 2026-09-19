package tui

import (
	"context"
	"reflect"
	"strconv"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

type herdrTestCall struct {
	binary      string
	args        []string
	environment []string
}

func TestHerdrReporterRequiresCompleteInjectedEnvironment(t *testing.T) {
	complete := []string{
		"HERDR_ENV=1",
		"HERDR_PANE_ID=w1:p1",
		"HERDR_BIN_PATH=/opt/herdr",
		"HERDR_SOCKET_PATH=/tmp/herdr.sock",
	}
	if reporter := newHerdrLifecycleReporter(complete, nil); reporter == nil {
		t.Fatal("complete Herdr environment did not enable reporter")
	}
	for index := range complete {
		environment := append([]string(nil), complete...)
		environment = append(environment[:index], environment[index+1:]...)
		if reporter := newHerdrLifecycleReporter(environment, nil); reporter != nil {
			t.Fatalf("reporter enabled without %q", complete[index])
		}
	}
}

func TestHerdrReporterMapsLifecycleAndReleasesAuthority(t *testing.T) {
	var mu sync.Mutex
	var calls []herdrTestCall
	runner := func(_ context.Context, binary string, args []string, environment []string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, herdrTestCall{
			binary: binary, args: append([]string(nil), args...),
			environment: append([]string(nil), environment...),
		})
		return nil
	}
	environment := []string{
		"HERDR_ENV=1",
		"HERDR_PANE_ID=w1:p1",
		"HERDR_BIN_PATH=/opt/herdr",
		"HERDR_SOCKET_PATH=/tmp/herdr.sock",
	}
	reporter := newHerdrLifecycleReporter(environment, runner)
	reporter.sequence = 100

	idle := frontend.ThreadSnapshot{Activity: frontend.ActivityIdle}
	runHerdrTestCommand(t, reporter.reportCmd(idle, false))
	if duplicate := reporter.reportCmd(idle, false); duplicate != nil {
		t.Fatal("duplicate lifecycle state produced another command")
	}
	runHerdrTestCommand(t, reporter.reportCmd(
		frontend.ThreadSnapshot{Activity: frontend.ActivityRunning},
		false,
	))
	runHerdrTestCommand(t, reporter.reportCmd(frontend.ThreadSnapshot{
		Activity: frontend.ActivityWaitingInput,
		Status:   "choose a target",
	}, false))
	runHerdrTestCommand(t, reporter.reportCmd(idle, false))
	reporter.release()

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 5 {
		t.Fatalf("Herdr calls = %+v", calls)
	}
	wantStates := []string{"idle", "working", "blocked", "idle"}
	for index, state := range wantStates {
		call := calls[index]
		if call.binary != "/opt/herdr" || !reflect.DeepEqual(call.environment, environment) {
			t.Fatalf("call %d execution contract = %+v", index, call)
		}
		if len(call.args) < 3 || call.args[0] != "pane" || call.args[1] != "report-agent" ||
			call.args[2] != "w1:p1" || flagValue(call.args, "--source") != herdrReporterSource ||
			flagValue(call.args, "--agent") != herdrReporterAgent {
			t.Fatalf("call %d reporter identity = %v", index, call.args)
		}
		if got := flagValue(call.args, "--state"); got != state {
			t.Fatalf("call %d state = %q, want %q: %v", index, got, state, call.args)
		}
		if got := flagValue(call.args, "--seq"); got != strconv.Itoa(101+index) {
			t.Fatalf("call %d sequence = %q: %v", index, got, call.args)
		}
	}
	if got := flagValue(calls[2].args, "--message"); got != "choose a target" {
		t.Fatalf("blocked message = %q", got)
	}
	if got := calls[4].args; len(got) < 3 || got[0] != "pane" || got[1] != "release-agent" ||
		flagValue(got, "--seq") != "105" {
		t.Fatalf("release command = %v", got)
	}
}

func TestHerdrLifecycleTreatsPendingAdmissionAsWorking(t *testing.T) {
	got := herdrLifecycleForSnapshot(
		frontend.ThreadSnapshot{Activity: frontend.ActivityWaitingInput},
		true,
	)
	if got.state != "working" || got.message != "" {
		t.Fatalf("pending lifecycle = %+v", got)
	}
}

func runHerdrTestCommand(t *testing.T, command tea.Cmd) {
	t.Helper()
	if command == nil {
		t.Fatal("expected Herdr command")
	}
	if message := command(); message != nil {
		t.Fatalf("Herdr command returned message %#v", message)
	}
}

func flagValue(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	return ""
}
