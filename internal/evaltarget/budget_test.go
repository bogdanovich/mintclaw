package evaltarget

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

func testSpec() Spec {
	return Spec{
		Version: Version, Nonce: "11111111-1111-4111-8111-111111111111",
		Limits: providers.CallBudgetLimits{
			MaxCalls: 4, MaxInputBytes: 32768, MaxOutputTokens: 128,
			MaxReportedTokens: 10000, MaxInFlight: 2,
		},
	}
}

func testSnapshot(spec Spec) providers.CallBudgetSnapshot {
	return providers.CallBudgetSnapshot{
		Limits: spec.Limits, Calls: 1, Completed: 1,
		ReportedTokens: 6, Sealed: true, UsageKnown: true,
	}
}

func privateTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestSpecCanonicalRoundTripAndAdmission(t *testing.T) {
	spec := testSpec()
	encoded, err := spec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, budget, err := ParseSpec(encoded)
	if err != nil || decoded != spec || budget == nil || budget.Snapshot().Calls != 0 {
		t.Fatalf("parse = %+v/%v", decoded, err)
	}
	for _, malformed := range []string{
		"", " " + encoded, encoded + "\n", encoded + "{}", strings.Repeat("x", MaxJSONBytes+1),
		strings.Replace(encoded, `"version":1`, `"version":2`, 1),
		strings.Replace(encoded, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(encoded, `"version":1`, `"version":1,"unknown":false`, 1),
		strings.Replace(encoded, spec.Nonce, "private-secret-value", 1),
		strings.Replace(encoded, `"max_calls":4`, `"max_calls":0`, 1),
		strings.Replace(encoded, spec.Nonce, string([]byte{0xff}), 1),
	} {
		if _, budget, err := ParseSpec(malformed); !errors.Is(err, ErrBudgetProtocol) || budget != nil ||
			strings.Contains(err.Error(), "private-secret-value") {
			t.Fatalf("invalid specification admitted or echoed: %v", err)
		}
	}
}

func TestProofFrozenBindingAndUnknownEvidence(t *testing.T) {
	spec := testSpec()
	proof := Proof{
		Version: Version, Scope: Scope, Nonce: spec.Nonce,
		PID: os.Getpid(), GatewayClean: true, Snapshot: testSnapshot(spec),
	}
	data, _ := json.Marshal(proof)
	decoded, err := DecodeProof(data, spec, os.Getpid())
	if err != nil || decoded != proof {
		t.Fatalf("proof = %+v/%v", decoded, err)
	}
	for _, mutate := range []func(*Proof){
		func(p *Proof) { p.Version++ },
		func(p *Proof) { p.Scope = "foreign" },
		func(p *Proof) { p.Nonce = "22222222-2222-4222-8222-222222222222" },
		func(p *Proof) { p.PID++ },
		func(p *Proof) { p.Snapshot.Sealed = false },
		func(p *Proof) { p.Snapshot.Limits.MaxCalls++ },
		func(p *Proof) { p.Snapshot.Calls = spec.Limits.MaxCalls + 1 },
		func(p *Proof) { p.Snapshot.Completed = 2 },
		func(p *Proof) { p.Snapshot.InFlight = -1 },
		func(p *Proof) { p.Snapshot.UnknownUsage = 1 },
		func(p *Proof) { p.Snapshot.ReportedTokens = -1 },
		func(p *Proof) { p.Snapshot.StoppedReason = "private-secret-value" },
	} {
		changed := proof
		mutate(&changed)
		data, _ := json.Marshal(changed)
		if _, err := DecodeProof(data, spec, os.Getpid()); !errors.Is(err, ErrBudgetProtocol) {
			t.Fatalf("altered proof admitted: %+v", changed)
		}
	}
	for _, changed := range [][]byte{
		append(append([]byte(nil), data...), '\n'),
		[]byte(strings.Replace(string(data), `"version":1`, `"version":1,"version":1`, 1)),
		[]byte(strings.Repeat("x", MaxJSONBytes+1)),
	} {
		if _, err := DecodeProof(changed, spec, os.Getpid()); !errors.Is(err, ErrBudgetProtocol) {
			t.Fatal("noncanonical or oversized proof admitted")
		}
	}
	proof.GatewayClean = false
	proof.Snapshot.UnknownUsage, proof.Snapshot.UsageKnown = 1, false
	proof.Snapshot.StoppedReason = "unknown_usage"
	proof.Snapshot.ReportedTokens = 0
	data, _ = json.Marshal(proof)
	if _, err := DecodeProof(data, spec, os.Getpid()); err != nil {
		t.Fatal("valid non-passing terminal observation was discarded", err)
	}
}

func TestProofRejectsImpossibleReportedCounters(t *testing.T) {
	spec := testSpec()
	for _, test := range []struct {
		name      string
		completed int
		unknown   int
		tokens    int64
	}{
		{"known_completion_without_tokens", 1, 0, 0},
		{"tokens_without_completions", 0, 0, 7},
		{"tokens_from_only_unknown_completions", 1, 1, 7},
		{"fewer_tokens_than_known_completions", 2, 0, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			proof := Proof{
				Version:  Version,
				Scope:    Scope,
				Nonce:    spec.Nonce,
				PID:      os.Getpid(),
				Snapshot: testSnapshot(spec),
			}
			proof.Snapshot.Calls, proof.Snapshot.Completed = test.completed, test.completed
			proof.Snapshot.UnknownUsage, proof.Snapshot.ReportedTokens = test.unknown, test.tokens
			proof.Snapshot.UsageKnown = test.unknown == 0
			if test.unknown != 0 {
				proof.Snapshot.StoppedReason = "unknown_usage"
			}
			raw, _ := json.Marshal(proof)
			if _, err := DecodeProof(raw, spec, os.Getpid()); !errors.Is(err, ErrBudgetProtocol) {
				t.Fatal("impossible native counters admitted", proof.Snapshot, err)
			}
		})
	}
}

func TestProofAdmitsNativeReportedCounterFloor(t *testing.T) {
	spec := testSpec()
	for _, test := range []struct {
		completed, unknown int
		tokens             int64
	}{{0, 0, 0}, {2, 0, 2}, {2, 1, 1}, {1, 1, 0}} {
		proof := Proof{
			Version:  Version,
			Scope:    Scope,
			Nonce:    spec.Nonce,
			PID:      os.Getpid(),
			Snapshot: testSnapshot(spec),
		}
		proof.Snapshot.Calls, proof.Snapshot.Completed = test.completed, test.completed
		proof.Snapshot.UnknownUsage, proof.Snapshot.ReportedTokens = test.unknown, test.tokens
		proof.Snapshot.UsageKnown = test.unknown == 0
		if test.unknown != 0 {
			proof.Snapshot.StoppedReason = "unknown_usage"
		}
		raw, _ := json.Marshal(proof)
		if _, err := DecodeProof(raw, spec, os.Getpid()); err != nil {
			t.Fatal("native floor or honest unknown counters rejected", proof.Snapshot, err)
		}
	}
}

func TestRecorderIsPrivateExclusiveAndSingleUse(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("owned gateway launch supports Linux/macOS only")
	}
	home, spec := privateTestHome(t), testSpec()
	recorder, err := NewRecorder(home, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(testSnapshot(spec), true); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(testSnapshot(spec), true); !errors.Is(err, ErrBudgetProtocol) {
		t.Fatal("terminal recorder reused")
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	name, _ := spec.ReceiptName()
	path := filepath.Join(home, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || len(data) > MaxJSONBytes {
		t.Fatalf("receipt privacy = %v/%v", info, err)
	}
	if proof, err := DecodeProof(data, spec, os.Getpid()); err != nil || !proof.GatewayClean {
		t.Fatalf("retained proof = %+v/%v", proof, err)
	}
	if adopted, err := NewRecorder(home, spec); adopted != nil || !errors.Is(err, ErrBudgetProtocol) {
		t.Fatal("old receipt adopted")
	}
	current, _ := os.ReadFile(path)
	if string(current) != string(data) {
		t.Fatal("old receipt overwritten")
	}
}

func TestRecorderRefusesUnsafeDestinations(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("owned gateway launch supports Linux/macOS only")
	}
	spec := testSpec()
	home := t.TempDir()
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "home")
	private := privateTestHome(t)
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{".", home, link, link + string(os.PathSeparator)} {
		if recorder, err := NewRecorder(unsafe, spec); recorder != nil || !errors.Is(err, ErrBudgetProtocol) {
			t.Fatalf("unsafe home admitted: %s", unsafe)
		}
	}
	name, _ := spec.ReceiptName()
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(private, name)); err != nil {
		t.Fatal(err)
	}
	if recorder, err := NewRecorder(private, spec); recorder != nil || !errors.Is(err, ErrBudgetProtocol) {
		t.Fatal("receipt symlink admitted")
	}
}
