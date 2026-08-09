//go:build linux || darwin

package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/update/control"
)

const (
	realProcessHealthy = "candidate=healthy\n"
	realProcessFail    = "candidate=fail\n"
	realProcessHold    = "candidate=hold\n"
)

func TestMain(testingMain *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "run" {
		os.Exit(runRealProcessCompanion(os.Args[2:]))
	}
	os.Exit(testingMain.Run())
}

func TestRealProcessUpdateCanaries(t *testing.T) {
	for _, test := range []struct {
		name       string
		scope      string
		behavior   string
		wantPhase  Phase
		wantLaunch string
	}{
		{
			name: "user scope healthy activation", scope: "user", behavior: realProcessHealthy,
			wantPhase: PhaseHealthy, wantLaunch: "b",
		},
		{
			name: "system scope verified rollback", scope: "system", behavior: realProcessFail,
			wantPhase: PhaseRolledBack, wantLaunch: "bbba",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
			fixture := newReleaseFixture(t, now, "mintclaw-node")
			defer fixture.server.Close()
			updateCoordinator, configPath, _ := realProcessCoordinator(t, fixture, now, test.scope, test.behavior)
			defer func() { _ = updateCoordinator.Close() }()
			request := fixture.stageRequest(now)
			if _, err := updateCoordinator.Stage(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if _, err := updateCoordinator.Activate(request.Identity); err != nil {
				t.Fatal(err)
			}

			state := runRealProcessSupervisorUntil(t, updateCoordinator, test.wantPhase)
			if launches := readRealProcessLaunches(t, configPath); launches != test.wantLaunch {
				t.Fatalf("real process launches = %q, want %q", launches, test.wantLaunch)
			}
			switch test.wantPhase {
			case PhaseHealthy:
				if !state.Transaction.SuccessorVerified || state.Active.Version != "v1.1.0" ||
					state.Transaction.RollbackAttempted {
					t.Fatalf("healthy activation = %#v", state)
				}
			case PhaseRolledBack:
				if !state.Transaction.ActivationAttempted || !state.Transaction.RollbackAttempted ||
					!state.Transaction.RollbackVerified || state.Active.Version != "v1.0.0" {
					t.Fatalf("verified rollback = %#v", state)
				}
			}
		})
	}
}

func TestRealProcessDisconnectRecoversWithoutSecondActivation(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	fixture := newReleaseFixture(t, now, "mintclaw-node")
	defer fixture.server.Close()
	updateCoordinator, configPath, stateRoot := realProcessCoordinator(t, fixture, now, "user", realProcessHold)
	request := fixture.stageRequest(now)
	if _, err := updateCoordinator.Stage(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	activated, err := updateCoordinator.Activate(request.Identity)
	if err != nil {
		t.Fatal(err)
	}
	releaseRequests := fixture.requests.Load()

	supervisor, err := NewSupervisor(updateCoordinator, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	waitForRealProcessLaunches(t, configPath, "b")
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("first Supervisor.Run() error = %v", err)
	}
	if err = updateCoordinator.Close(); err != nil {
		t.Fatal(err)
	}
	updateCoordinator = reopenRealProcessCoordinator(t, stateRoot, fixture, now)
	defer func() { _ = updateCoordinator.Close() }()
	supervisor, err = NewSupervisor(updateCoordinator, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	unknown, err := supervisor.status(request.Identity)
	if err != nil || unknown.Transaction.Phase != PhaseActivating ||
		!unknown.Transaction.ActivationAttempted || unknown.Transaction.LaunchAttempts != 1 {
		t.Fatalf("durable unknown activation = %#v, %v", unknown, err)
	}
	beforeStatusGeneration := unknown.Generation
	for range 2 {
		observed, statusErr := supervisor.status(request.Identity)
		if statusErr != nil || observed.Generation != beforeStatusGeneration ||
			observed.Transaction.LaunchAttempts != 1 {
			t.Fatalf("status recovery mutated activation = %#v, %v", observed, statusErr)
		}
	}
	if fixture.requests.Load() != releaseRequests {
		t.Fatalf("status recovery repeated release requests: %d -> %d", releaseRequests, fixture.requests.Load())
	}
	if unknown.Active != activated.Active || unknown.Transaction.Previous == nil ||
		*unknown.Transaction.Previous != *activated.Transaction.Previous {
		t.Fatalf("status recovery changed selected payload = %#v", unknown)
	}

	if err = os.WriteFile(configPath, []byte(realProcessHealthy), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered := runRealProcessSupervisorUntil(t, updateCoordinator, PhaseHealthy)
	if !recovered.Transaction.SuccessorVerified || recovered.Transaction.LaunchAttempts != 2 ||
		fixture.requests.Load() != releaseRequests {
		t.Fatalf("restart recovery = %#v, release requests = %d", recovered, fixture.requests.Load())
	}
	if launches := readRealProcessLaunches(t, configPath); launches != "bb" {
		t.Fatalf("restart launches = %q, want %q", launches, "bb")
	}
}

func realProcessCoordinator(
	t *testing.T,
	fixture *releaseFixture,
	now time.Time,
	scope string,
	behavior string,
) (*Coordinator, string, string) {
	t.Helper()
	stateDirectory := privateRoot(t)
	installation := testState(t).Installation
	configureRuntimeInstallation(&installation)
	installation.Scope = scope
	if runtime.GOOS == "linux" {
		installation.Service = "mintclaw-node-canary.service"
	} else {
		installation.Service = "io.github.bogdanovich.mintclaw.node.canary"
	}
	configPath := filepath.Join(stateDirectory, "config.json")
	if err := os.WriteFile(configPath, []byte(behavior), 0o600); err != nil {
		t.Fatal(err)
	}
	installation.ConfigPath = configPath
	adoption, err := BeginAdoption(
		stateDirectory,
		installation,
		currentTestExecutable(t),
		"v1.0.0",
		"v1.0.0",
		os.Geteuid(),
		os.Getegid(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = adoption.Commit(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(stateDirectory, StoreDirectoryName))
	if err != nil {
		t.Fatal(err)
	}
	updateCoordinator, err := New(
		store,
		staticResolver{authority: fixture.authority},
		"v1.0.0",
		WithHTTPClient(fixture.client),
		WithClock(func() time.Time { return now }),
	)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	return updateCoordinator, configPath, filepath.Join(stateDirectory, StoreDirectoryName)
}

func reopenRealProcessCoordinator(
	t *testing.T,
	stateRoot string,
	fixture *releaseFixture,
	now time.Time,
) *Coordinator {
	t.Helper()
	store, err := OpenStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	updateCoordinator, err := New(
		store,
		staticResolver{authority: fixture.authority},
		"v1.0.0",
		WithHTTPClient(fixture.client),
		WithClock(func() time.Time { return now }),
	)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	return updateCoordinator
}

func runRealProcessSupervisorUntil(t *testing.T, updateCoordinator *Coordinator, phase Phase) State {
	t.Helper()
	supervisor, err := NewSupervisor(updateCoordinator, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	state := waitForRealProcessPhase(t, updateCoordinator.store, phase)
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Supervisor.Run() error = %v", err)
	}
	return state
}

func waitForRealProcessPhase(t *testing.T, store *Store, phase Phase) State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		state, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if state.Transaction != nil && state.Transaction.Phase == phase {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("real process transaction did not reach %s", phase)
	return State{}
}

func waitForRealProcessLaunches(t *testing.T, configPath string, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(configPath + ".launches")
		if err == nil && strings.ReplaceAll(string(data), "\n", "") == want {
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("real process launches did not reach %q", want)
}

func readRealProcessLaunches(t *testing.T, configPath string) string {
	t.Helper()
	data, err := os.ReadFile(configPath + ".launches")
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\n", "")
}

func runRealProcessCompanion(args []string) int {
	if len(args) != 2 || args[0] != "--config" || !filepath.IsAbs(args[1]) {
		return 2
	}
	executable, err := os.Executable()
	if err != nil {
		return 3
	}
	slot := strings.TrimPrefix(filepath.Base(executable), "payload-")
	if slot != "a" && slot != "b" {
		return 4
	}
	behavior, err := os.ReadFile(args[1])
	if err != nil {
		return 5
	}
	launches, err := os.OpenFile(args[1]+".launches", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 6
	}
	if _, err = launches.WriteString(slot + "\n"); err != nil || launches.Close() != nil {
		return 7
	}
	if slot == "b" && string(behavior) == realProcessFail {
		return 8
	}
	controlFile := os.NewFile(3, "coordinator-control")
	if controlFile == nil {
		return 9
	}
	defer func() { _ = controlFile.Close() }()
	if slot == "b" && string(behavior) == realProcessHold {
		_, _ = io.Copy(io.Discard, controlFile)
		return 0
	}
	codec, err := control.NewCodec(controlFile, controlFile)
	if err != nil {
		return 10
	}
	version := "v1.0.0"
	if slot == "b" {
		version = "v1.1.0"
	}
	catalogDigest := sha256.Sum256([]byte("catalog"))
	err = codec.WriteHealth(control.Health{
		SchemaVersion: control.SchemaVersion,
		Kind:          control.KindHealth,
		NodeID:        string(nodes.ID("node_" + strings.Repeat("a", 52))),
		Version:       version,
		Platform:      runtime.GOOS,
		Architecture:  runtime.GOARCH,
		CatalogHash:   hex.EncodeToString(catalogDigest[:]),
	})
	if err != nil {
		return 11
	}
	_, _ = io.Copy(io.Discard, controlFile)
	return 0
}
