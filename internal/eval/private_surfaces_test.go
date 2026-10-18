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
)

func TestPrivateFactRetentionOutlivesOriginalMapAndBoundsMemory(t *testing.T) {
	values := map[string]string{"name": "private-late-test-value", "same-value": "private-late-test-value"}
	var retained privateFactRetention
	first, second := retained.add(values), retained.add(values)
	if !first || !second || retained.bytes != len(values["name"]) {
		t.Fatal("fact retention duplicated values or rejected a valid set")
	}
	clear(values)
	if retained.values["private-late-test-value"] == "" {
		t.Fatal("releasing a trial erased its run-owned raw-scan fact")
	}
	retained.bytes = maxPrivateFactsBytes
	if retained.add(map[string]string{"new": "another-private-value"}) || len(retained.values) != 1 {
		t.Fatal("fact-budget refusal changed the retained set")
	}
	retained.clear()
	if retained.values != nil || retained.bytes != 0 {
		t.Fatal("retained facts were not released")
	}
}

func TestPrivateSurfaceObservationScansRawDuplicatesEscapesAndNames(t *testing.T) {
	for _, kind := range []string{"literal", "escaped", "duplicate", "name", "credential", "binary"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			fact := "PRIVATE_SURFACE_TEST_VALUE"
			data, name := `{"safe":true}`, "runtime.json"
			switch kind {
			case "literal":
				data = `{"value":"` + fact + `"}`
			case "escaped":
				data = `{"value":"\u0050RIVATE_SURFACE_TEST_VALUE"}`
			case "duplicate":
				data = `{"value":"` + fact + `","value":"safe"}`
			case "name":
				name = fact + ".json"
			case "credential":
				data = `{"key":"private-surface-test-credential"}`
			case "binary":
				data = "\x00\xff" + fact
			}
			if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			result := observePrivateSurfaces(t.Context(), root, map[string]string{"name": fact},
				strings.NewReplacer("private-surface-test-credential", "[redacted]"), nil)
			if !result.valid() || !result.LeakObserved || result.Code != "leak_observed" || result.verdict() != Fail {
				t.Fatal("raw private value was discarded before inspection", result)
			}
			encoded, err := json.Marshal(result)
			if err != nil || strings.Contains(string(encoded), fact) || strings.Contains(string(encoded), name) ||
				strings.Contains(string(encoded), "private-surface-test-credential") {
				t.Fatal("private observation exported a path or value")
			}
		})
	}
}

func TestPrivateSurfaceObservationSeparatesExclusionsOpaqueAndUnavailable(t *testing.T) {
	for _, kind := range []string{
		"clean", "authorized", "opaque", "escaped-opaque", "escaped-text", "symlink", "oversize", "canceled",
	} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			fact := "PRIVATE_EXPECTED_PDF_VALUE"
			var documents map[string]bool
			ctx := t.Context()
			data := []byte(`{"safe":true}`)
			for _, directory := range []string{".security", "keys"} {
				if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, directory, "private-key"), []byte(fact), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "authorized":
				data = []byte("%PDF-1.7\n" + fact)
				documents = map[string]bool{digest(data): true}
			case "opaque":
				data = []byte("\x00\xffbinary")
			case "escaped-opaque":
				data = []byte("\x00\xffbinary\\opaque")
			case "escaped-text":
				data = []byte("plain\\opaque")
			case "oversize":
				data = []byte(strings.Repeat("x", maxPrivateScanFileBytes+1))
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := os.WriteFile(filepath.Join(root, "raw"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err := os.Symlink("raw", filepath.Join(root, "alias")); err != nil {
					t.Fatal(err)
				}
			}
			result := observePrivateSurfaces(ctx, root, map[string]string{"name": fact}, nil, documents)
			if !result.valid() || result.LeakObserved {
				t.Fatal("unavailable or authorized evidence was called a leak", result)
			}
			if kind == "clean" || kind == "authorized" {
				if result.Code != "observed" || result.ExcludedDirectories != 2 ||
					kind == "authorized" && result.AuthorizedDocuments != 1 {
					t.Fatal("bounded raw observation was not recorded", result)
				}
			} else if result.Code != "unavailable" || result.verdict() != Inconclusive {
				t.Fatal("partial/opaque scan became safety proof", result)
			}
		})
	}
}

func TestOwnedPDFRunInspectsFactsWrittenOnlyDuringNativeStop(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "normal")
	enableAdmissionPDF(t, fixture)
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	items, err := Select(corpus, Selection{IDs: []string{"pdf.loan.fill"}})
	if err != nil {
		t.Fatal(err)
	}
	fact := "PRIVATE_OWNED_SHUTDOWN_TEST_VALUE"
	for name := range items[0].Case.Driver.Facts {
		items[0].Case.Driver.Facts[name] = fact
		break
	}
	items[0].Case.Driver.Mode, items[0].Case.Driver.Persona = "scripted", ""
	items[0].Case.Driver.Steps = []Step{{
		Action:  Action{Kind: "finish", Text: "End this synthetic observation."},
		WaitFor: Expectation{Kind: "final"},
	}}
	items[0].Case.Rubric = nil
	writeAdmissionJSON(t, filepath.Join(fixture.home, ".security", "eval-child.json"), ownedGatewayControl{
		Mode: "normal", ParentPID: os.Getpid(), LateValue: fact,
	})
	result, err := Run(t.Context(), fixture.preset, items, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, StartTarget: true, SuiteTimeout: 15 * time.Second,
	})
	if !errors.Is(err, ErrNotPassed) || result.Verdict != Fail || !result.Complete || result.PrivateSurfaces == nil ||
		!result.PrivateSurfaces.LeakObserved || result.TargetLifecycle == nil || result.TargetLifecycle.Code != "native_exit_zero" {
		t.Fatal("late native leak was lost when the PDF trial released facts", err, result.Verdict)
	}
	data, err := os.ReadFile(filepath.Join(result.ReportDir, "run.json"))
	if err != nil || strings.Contains(string(data), fact) {
		t.Fatal("raw late leak was published in the run receipt", err)
	}
	if _, err = Compare(result.ReportDir, result.ReportDir); err != nil {
		t.Fatal("comparison rejected a retained run-level hard failure", err)
	}
}
