package eval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/internal/evaltarget"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func TestOwnedRestartExitHelper(t *testing.T) {
	if os.Getenv("MINTCLAW_EVAL_RESTART_EXIT_HELPER") == "1" {
		os.Exit(0)
	}
}

// This real exited process tests the ownership/budget validators only. It is
// not a native AgentLoop, a restart scenario or live qualification evidence.
func stoppedRestartOwner(t *testing.T) (*ownedGateway, *admissionFixture, evaltarget.Proof) {
	t.Helper()
	fixture, _ := preparationFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helperCtx, helperCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer helperCancel()
	command := exec.CommandContext(helperCtx, executable, "-test.run=^TestOwnedRestartExitHelper$")
	command.Dir = fixture.home
	command.Env = []string{"HOME=" + fixture.home, "MINTCLAW_EVAL_RESTART_EXIT_HELPER=1"}
	if err := command.Run(); err != nil {
		t.Fatal("own exit helper", err)
	}
	if os.Mkdir(filepath.Join(fixture.home, ownedGatewayTempDirectory), 0o700) != nil {
		t.Fatal("private temp parent")
	}
	root, err := os.OpenRoot(fixture.home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	profile := preparedProfile{config: fixture.cfg, home: fixture.home, workspace: fixture.workspace}
	identity, err := newOwnedLaunchIdentity(root, profile, executable, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity.temporary, _ = root.Lstat(ownedGatewayTempDirectory)
	probe := fixture.probe
	probe.PID = command.Process.Pid
	identity.bind(fixture.preset, profile, admittedTarget{probe: probe})
	if !validDigest(identity.inputsSHA256) {
		t.Fatal("restart input snapshot unavailable")
	}
	spec := evaltarget.Spec{Version: evaltarget.Version, Nonce: uuid.NewString(), Limits: targetBudgetTestLimits()}
	done := make(chan struct{})
	close(done)
	child := &ownedGateway{
		command: command, profile: profile, done: done, admitted: true, log: &boundedGatewayLog{},
		budgetSpec: &spec, identity: identity,
	}
	proof := evaltarget.Proof{
		Version: evaltarget.Version, Scope: evaltarget.Scope, Nonce: spec.Nonce,
		PID: command.Process.Pid, GatewayClean: true,
		Snapshot: providers.CallBudgetSnapshot{
			Limits: spec.Limits, Calls: 2, Completed: 2, ReportedTokens: 18, UsageKnown: true, Sealed: true,
		},
	}
	return child, fixture, proof
}

func writeRestartBudget(t *testing.T, child *ownedGateway, proof evaltarget.Proof) {
	t.Helper()
	name, err := child.budgetSpec.ReceiptName()
	data, marshalErr := json.Marshal(proof)
	if err != nil || marshalErr != nil || os.WriteFile(filepath.Join(child.profile.home, name), data, 0o600) != nil {
		t.Fatal("synthetic native-protocol unit receipt", err, marshalErr)
	}
}

func TestOwnedRestartBudgetNeverResetsOrAcceptsUnknownWork(t *testing.T) {
	for _, kind := range []string{"positive", "unknown", "inflight", "calls", "tokens", "stopped", "pid", "nonce", "unclean"} {
		t.Run(kind, func(t *testing.T) {
			child, _, proof := stoppedRestartOwner(t)
			switch kind {
			case "unknown":
				proof.Snapshot.UnknownUsage, proof.Snapshot.UsageKnown = 1, false
				proof.Snapshot.StoppedReason = "unknown_usage"
			case "inflight":
				proof.Snapshot.Completed, proof.Snapshot.InFlight, proof.Snapshot.UsageKnown = 1, 1, false
			case "calls":
				proof.Snapshot.Calls, proof.Snapshot.Completed = proof.Snapshot.Limits.MaxCalls, proof.Snapshot.Limits.MaxCalls
			case "tokens":
				proof.Snapshot.ReportedTokens = proof.Snapshot.Limits.MaxReportedTokens
			case "stopped":
				proof.Snapshot.StoppedReason = "output_limit"
			case "pid":
				proof.PID++
			case "nonce":
				proof.Nonce = uuid.NewString()
			case "unclean":
				proof.GatewayClean = false
			}
			writeRestartBudget(t, child, proof)
			observation, limits, err := remainingOwnedBudget(child)
			if kind != "positive" {
				if !errors.Is(err, ErrOwnedGateway) {
					t.Fatal("unsafe budget admitted", kind, err)
				}
				return
			}
			want := child.budgetSpec.Limits
			want.MaxCalls, want.MaxReportedTokens = want.MaxCalls-2, want.MaxReportedTokens-18
			if err != nil || observation == nil || observation.ProofSHA256 != digest([]byte(observation.Proof)) ||
				limits != want {
				t.Fatal("native remaining ceilings were reset or widened", err)
			}
		})
	}
}

func TestOwnedRestartIdentityAndSingleSuccessorGuards(t *testing.T) {
	for _, kind := range []string{"positive", "unconsumed", "generation", "home", "temp", "program", "inputs", "nil_home"} {
		t.Run(kind, func(t *testing.T) {
			child, _, _ := stoppedRestartOwner(t)
			child.restartUsed.Store(true)
			other, err := os.Stat(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unconsumed":
				child.restartUsed.Store(false)
			case "generation":
				child.identity.generation = maxOwnedGatewayGenerations
			case "home":
				child.identity.home = other
			case "temp":
				child.identity.temporary = other
			case "program":
				child.identity.executable = other
			case "inputs":
				child.identity.inputsSHA256 = digest(nil)
			case "nil_home":
				child.identity.home = nil
			}
			root, err := os.OpenRoot(child.profile.home)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			identity, err := newOwnedLaunchIdentity(root, child.profile, child.command.Path, child)
			if (err == nil) != (kind == "positive") || err == nil && identity.generation != 2 {
				t.Fatal("successor identity guard", kind, err)
			}
		})
	}
}

func TestOwnedRestartLauncherRefusesResetBudgetBeforeCreatingOwner(t *testing.T) {
	child, _, proof := stoppedRestartOwner(t)
	writeRestartBudget(t, child, proof)
	child.restartUsed.Store(true)
	reset := child.budgetSpec.Limits
	if next, err := launchOwnedGatewayGeneration(child.profile, child.command.Path, &reset, child); next != nil ||
		!errors.Is(err, ErrOwnedGateway) {
		t.Fatal("launcher accepted reset budget", err)
	}
	if _, err := os.Lstat(filepath.Join(child.profile.home, ".eval-gateway-owner")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused budget created owner state", err)
	}
}

func TestOwnedRestartRejectsUnownedAndUncleanHandles(t *testing.T) {
	for _, kind := range []string{"nil", "unadmitted", "running", "forced", "truncated", "abnormal"} {
		t.Run(kind, func(t *testing.T) {
			child, _, _ := stoppedRestartOwner(t)
			switch kind {
			case "nil":
				child = nil
			case "unadmitted":
				child.admitted = false
			case "running":
				child.done = make(chan struct{})
			case "forced":
				child.forced.Store(true)
			case "truncated":
				child.log.truncated = true
			case "abnormal":
				child.err = ErrOwnedGateway
			}
			if child.cleanOwnedExit() {
				t.Fatal("unowned/unclean process certified clean")
			}
			if _, _, err := remainingOwnedBudget(child); !errors.Is(err, ErrOwnedGateway) {
				t.Fatal("unclean owner supplied budget")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if next, _, _, err := restartOwnedGateway(ctx, nil, Preset{}); next != nil || !errors.Is(err, ErrOwnedGateway) {
		t.Fatal("canceled or unowned restart admitted")
	}
}
