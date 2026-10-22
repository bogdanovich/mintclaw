//go:build (linux || darwin) && integration

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
)

// This is a qualification harness, not a computer-input implementation. The
// actual approved app launches the private fixture test as its own child using
// an ephemeral, single-executable system.exec policy. Nothing is installed or
// added to a production policy, and the public computer host stays read-only.
func TestCompanionNativeInputMechanismsOverWSS(t *testing.T) {
	runCompanionNativeMechanisms(t, "MINTCLAW_COMPUTER_NATIVE_INPUT_TEST_BINARY",
		"^TestNativeQualification$/^Input$", nativeInputChildVerified)
}

func TestCompanionNativeInputOwnershipOverWSS(t *testing.T) {
	runCompanionNativeMechanisms(t, "MINTCLAW_COMPUTER_NATIVE_OWNERSHIP_TEST_BINARY",
		"^TestNativeMacInputOwnership$", nativeOwnershipChildVerified)
}

func runCompanionNativeMechanisms(
	t *testing.T, helperEnv, selection string, verified func(nativeInputChildResult) bool,
) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("actual macOS companion permission identity required")
	}
	driver := os.Getenv("MINTCLAW_COMPUTER_NATIVE_DRIVER")
	helper := os.Getenv(helperEnv)
	app := os.Getenv("MINTCLAW_COMPUTER_COMPANION_APP")
	fixture := os.Getenv("MINTCLAW_COMPUTER_NATIVE_FIXTURE")
	if driver == "" && helper == "" && app == "" && fixture == "" {
		t.Skip("explicit pinned driver, native-input helper, disposable fixture and actual app required")
	}
	for _, path := range []string{driver, helper, fixture} {
		info, err := os.Lstat(path)
		if !filepath.IsAbs(path) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			t.Fatal("qualification requires explicit regular executable paths")
		}
	}
	if !filepath.IsAbs(app) || !strings.HasSuffix(app, ".app") {
		t.Fatal("actual companion app required; no SSH/Herdr permission substitution")
	}
	registry, admission, _ := newProcessTestGateway(t)
	server := httptest.NewTLSServer(admission)
	defer server.Close()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := admission.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	root := t.TempDir()
	commands := []string{"system.exec.v1"}
	fingerprint := sha256.Sum256(server.Certificate().Raw)
	inputEnv := map[string]string{
		"PATH":                             "/usr/bin:/bin",
		"MINTCLAW_COMPUTER_NATIVE_DRIVER":  driver,
		"MINTCLAW_COMPUTER_NATIVE_SHA256":  os.Getenv("MINTCLAW_COMPUTER_NATIVE_SHA256"),
		"MINTCLAW_COMPUTER_NATIVE_FIXTURE": fixture,
	}
	environment := make([]string, 0, len(inputEnv))
	for name := range inputEnv {
		environment = append(environment, name)
	}
	cfg := companion.Config{
		GatewayURL: strings.Replace(server.URL, "https://", "wss://", 1) + companion.GatewayPath,
		StateDir:   filepath.Join(root, "state"),
		TLS:        companion.TLSConfig{CertificateSHA256: hex.EncodeToString(fingerprint[:])},
		Reconnect:  companion.ReconnectConfig{MinDelaySeconds: 1, MaxDelaySeconds: 1, PendingDelaySeconds: 1},
		Policy: nodes.LocalCommandPolicy{
			Revision: "native-input-fixture-only", AllowedCommands: commands,
			MaximumRisk: nodes.RiskWrite, MaxTimeoutSeconds: 180, MaxOutputBytes: 64 * 1024,
		},
		SystemExec: &companion.SystemExecPolicy{
			WorkingRoots: []string{root}, Executables: []string{helper}, Environment: environment,
		},
	}
	config := filepath.Join(root, "config.json")
	writeProcessTestConfig(t, config, cfg)
	stop := startNativeMacCompanion(t, app, config)
	defer stop()
	pending := waitForOnlyNodeState(t, registry, nodes.StatePendingPairing)
	if _, err := registry.Approve(pending.ID, nodes.PairingApproval{
		AllowedCommands: commands, At: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	connected := waitForOnlyNodeState(t, registry, nodes.StateConnected)
	registration, exists, err := registry.Registration(connected.ID)
	if err != nil || !exists {
		t.Fatal("actual companion registration unavailable", err)
	}
	descriptor, err := registration.ApprovedCommand("system.exec.v1")
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{
		"argv": []string{
			helper, "-test.run=" + selection, "-test.v", "-test.count=1", "-test.timeout=160s",
		},
		"cwd": root, "env": inputEnv, "timeout_seconds": 170,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := nodes.PrepareExecutionPlan(nodes.InvocationRequest{
		InvocationID: "native-input-fixture", IdempotencyKey: "native-input-fixture", NodeID: connected.ID,
		CatalogHash: registration.Snapshot.CatalogHash, Command: descriptor.Name, Input: input,
		AgentID: "native-fixture", ActorID: "native-fixture", SessionID: "native-input-fixture",
		TimeoutSeconds: 180, OutputLimitBytes: 64 * 1024,
	}, descriptor, registration.Snapshot.Executor, registration.Snapshot.PolicyRevision, time.Now(), 3*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 185*time.Second)
	defer cancel()
	data, _, err := admission.Invoke(ctx, connected.ID, plan, nil, nil)
	if err != nil {
		t.Fatal("actual companion could not run the private fixture qualification", err)
	}
	var result nativeInputChildResult
	if json.Unmarshal(data, &result) != nil || !verified(result) {
		// Only this helper's owned fixture output is included, never arbitrary
		// application state, raw screenshots, provider credentials or typed secrets.
		t.Log(result.Stdout)
		t.Log(result.Stderr)
		t.Fatalf("native input not qualified: child exit=%d truncated=%t", result.ExitCode, result.Truncated)
	}
	t.Log(result.Stdout)
	t.Log("actual companion child independently verified selected native fixture checks; no public input enabled")
}

type nativeInputChildResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
}

func nativeInputChildVerified(result nativeInputChildResult) bool {
	names := []string{"UnicodeInsertion", "UnicodeReplacement", "ButtonClick", "ScopeDenial"}
	for index := range 7 {
		names = append(names, "UnicodeInsertion/"+strconv.Itoa(index))
	}
	return nativeMechanismChildVerified(result,
		"native worker TCC preflight: Accessibility=true, Screen Recording=true",
		"TestNativeQualification/Input/", names)
}

func nativeOwnershipChildVerified(result nativeInputChildResult) bool {
	names := make([]string, 20)
	for index := range names {
		names[index] = strconv.Itoa(index)
	}
	return nativeMechanismChildVerified(result,
		"native ownership TCC preflight: Accessibility=true, Screen Recording=true",
		"TestNativeMacInputOwnership/", names)
}

func nativeMechanismChildVerified(result nativeInputChildResult, preflight, testPrefix string, names []string) bool {
	if result.ExitCode != 0 || result.Truncated || result.Stderr != "" ||
		!strings.Contains(result.Stdout, preflight) {
		return false
	}
	lines := strings.Split(result.Stdout, "\n")
	for _, name := range names {
		prefix := "--- PASS: " + testPrefix + name + " ("
		found := false
		for _, line := range lines {
			found = found || strings.HasPrefix(strings.TrimSpace(line), prefix)
		}
		if !found {
			return false
		}
	}
	for _, line := range lines {
		if line == "PASS" {
			return true
		}
	}
	return false
}

func TestNativeOwnershipChildVerificationRequiresAllTwentyRounds(t *testing.T) {
	valid := "native ownership TCC preflight: Accessibility=true, Screen Recording=true\n"
	for index := range 20 {
		valid += "--- PASS: TestNativeMacInputOwnership/" + strconv.Itoa(index) + " (0.1s)\n"
	}
	valid += "PASS\n"
	if !nativeOwnershipChildVerified(nativeInputChildResult{Stdout: valid}) {
		t.Fatal("complete twenty-round independent native proof rejected")
	}
	for _, result := range []nativeInputChildResult{
		{},
		{Stdout: "PASS\n"},
		{Stdout: valid, ExitCode: 1},
		{Stdout: valid, Truncated: true},
		{Stdout: valid, Stderr: "child warning"},
		{Stdout: strings.Replace(valid, "Accessibility=true", "Accessibility=false", 1)},
		{Stdout: strings.Replace(valid, "Screen Recording=true", "Screen Recording=false", 1)},
		{Stdout: strings.TrimSuffix(valid, "PASS\n")},
	} {
		if nativeOwnershipChildVerified(result) {
			t.Fatal("failed/absent/denied/truncated ownership proof was accepted")
		}
	}
	for index := range 20 {
		missing := strings.Replace(valid,
			"--- PASS: TestNativeMacInputOwnership/"+strconv.Itoa(index)+" (", "--- SKIP: missing (", 1)
		if nativeOwnershipChildVerified(nativeInputChildResult{Stdout: missing}) {
			t.Fatal("missing native ownership round was accepted", index)
		}
	}
}

func TestNativeInputChildVerificationRequiresEffectsAndExit(t *testing.T) {
	valid := "native worker TCC preflight: Accessibility=true, Screen Recording=true\n"
	for _, name := range []string{"UnicodeInsertion", "UnicodeReplacement", "ButtonClick", "ScopeDenial"} {
		valid += "    --- PASS: TestNativeQualification/Input/" + name + " (0.1s)\n"
	}
	for index := range 7 {
		valid += "        --- PASS: TestNativeQualification/Input/UnicodeInsertion/" + strconv.Itoa(index) + " (0.1s)\n"
	}
	valid += "PASS\n"
	if !nativeInputChildVerified(nativeInputChildResult{Stdout: valid}) {
		t.Fatal("complete independent fixture proof rejected")
	}
	for _, result := range []nativeInputChildResult{
		{},
		{Stdout: "PASS\n"},
		{Stdout: valid, ExitCode: 1},
		{Stdout: valid, Truncated: true},
		{Stdout: valid, Stderr: "child warning"},
		{Stdout: strings.Replace(valid, "Accessibility=true", "Accessibility=false", 1)},
		{Stdout: strings.Replace(valid, "Screen Recording=true", "Screen Recording=false", 1)},
		{Stdout: strings.Replace(valid, "--- PASS: TestNativeQualification/Input/UnicodeInsertion", "--- SKIP: omitted", 1)},
		{Stdout: strings.TrimSuffix(valid, "PASS\n")},
	} {
		if nativeInputChildVerified(result) {
			t.Fatal("missing, skipped, failed, denied or truncated child proof reported as qualified")
		}
	}
	for index := range 7 {
		missing := strings.Replace(valid,
			"--- PASS: TestNativeQualification/Input/UnicodeInsertion/"+strconv.Itoa(index)+" (",
			"--- SKIP: omitted (", 1)
		if nativeInputChildVerified(nativeInputChildResult{Stdout: missing}) {
			t.Fatal("missing nonempty Unicode caret case reported as qualified", index)
		}
	}
}
