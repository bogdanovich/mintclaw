package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCatalog(t *testing.T, root string, policies ...SuitePolicy) {
	t.Helper()
	data, err := json.Marshal(corpusCatalog{Version: Version, Suites: policies})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "catalog.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogDefaultsPreserveCaseIdentityAndPrivateSuites(t *testing.T) {
	root := t.TempDir()
	for _, suite := range []string{"general", "private", "default"} {
		item := testCase()
		item.ID, item.Suite = suite+".example", suite
		writeCase(t, root, suite+".json", item)
	}
	before, err := LoadCorpus(root)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(before, Selection{DefaultOnly: true})
	if err != nil || len(selected) != 3 || CheckRunAvailability(selected, false) != nil {
		t.Fatal("catalog-free private corpus changed defaults", err)
	}
	writeCatalog(t, root,
		SuitePolicy{ID: "general", Status: "active", Default: true},
		SuitePolicy{ID: "private", Status: "pending", Reason: "Deferred by operator."},
		SuitePolicy{ID: "default", Status: "active"},
	)
	after, err := LoadCorpus(root)
	if err != nil {
		t.Fatal(err)
	}
	for index := range before {
		if before[index].Digest != after[index].Digest || before[index].Path != after[index].Path {
			t.Fatal("operational policy changed immutable case identity")
		}
	}
	selected, err = Select(after, Selection{DefaultOnly: true})
	if err != nil || len(selected) != 1 || selected[0].Case.Suite != "general" {
		t.Fatal("default set did not follow policy", err)
	}
	if _, err = Select(after, Selection{DefaultOnly: true, Suite: "private"}); !errors.Is(err, ErrInvalidFilter) {
		t.Fatal("default-only selection ignored intersection")
	}
	selected, err = Select(after, Selection{Suite: "default"})
	if err != nil || len(selected) != 1 || selected[0].Case.Suite != "default" {
		t.Fatal("private suite named default became a reserved selector", err)
	}
	all, err := Select(after, Selection{Suite: "all"})
	if err != nil || len(all) != 3 || !errors.Is(CheckRunAvailability(all, false), ErrPendingCases) ||
		CheckRunAvailability(all, true) != nil {
		t.Fatal("pending inspection/explicit opt-in contract failed", err)
	}
}

func TestCatalogStrictValidation(t *testing.T) {
	valid := `{"version":1,"suites":[{"id":"general","status":"active","default":true}]}`
	for name, data := range map[string]string{
		"version":         strings.Replace(valid, `"version":1`, `"version":2`, 1),
		"unknown-field":   strings.Replace(valid, `"version":1`, `"extra":true,"version":1`, 1),
		"duplicate-key":   strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		"empty":           `{"version":1,"suites":[]}`,
		"missing":         `{"version":1}`,
		"missing-default": strings.Replace(valid, `,"default":true`, "", 1),
		"missing-status":  strings.Replace(valid, `"status":"active",`, "", 1),
		"unknown-suite":   strings.Replace(valid, "general", "unknown", 1),
		"duplicate-suite": `{"version":1,"suites":[{"id":"general","status":"active","default":true},` +
			`{"id":"general","status":"active","default":false}]}`,
		"unknown-status": strings.Replace(valid, "active", "disabled", 1),
		"pending-default": `{"version":1,"suites":[{"id":"general","status":"pending",` +
			`"default":true,"reason":"Deferred."}]}`,
		"pending-no-reason": `{"version":1,"suites":[{"id":"general","status":"pending","default":false}]}`,
		"pending-blank-reason": `{"version":1,"suites":[{"id":"general","status":"pending",` +
			`"default":false,"reason":" "}]}`,
		"long-reason": `{"version":1,"suites":[{"id":"general","status":"pending","default":false,"reason":"` +
			strings.Repeat("x", 1025) + `"}]}`,
		"active-reason":  strings.Replace(valid, `"status":"active"`, `"status":"active","reason":"why"`, 1),
		"oversized":      strings.Repeat(" ", MaxManifestBytes+1),
		"trailing-value": valid + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeCase(t, root, "one.json", testCase())
			if err := os.WriteFile(filepath.Join(root, "catalog.json"), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadCorpus(root); !errors.Is(err, ErrInvalidCorpus) {
				t.Fatal("invalid catalog admitted", err)
			}
		})
	}
	root := t.TempDir()
	writeCase(t, root, "one.json", testCase())
	other := testCase()
	other.ID, other.Suite = "other.example", "other"
	writeCase(t, root, "other.json", other)
	writeCatalog(t, root, SuitePolicy{ID: "general", Status: "active", Default: true})
	if _, err := LoadCorpus(root); !errors.Is(err, ErrInvalidCorpus) {
		t.Fatal("incomplete suite catalog admitted", err)
	}
}

func TestCatalogRejectsNonRegularPolicy(t *testing.T) {
	for _, mode := range []string{"directory", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			writeCase(t, root, "one.json", testCase())
			name := filepath.Join(root, "catalog.json")
			if mode == "directory" {
				if err := os.Mkdir(name, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				writeCatalog(t, root, SuitePolicy{ID: "general", Status: "active", Default: true})
				target := filepath.Join(root, "policy.json")
				if err := os.Rename(name, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, name); err != nil {
					t.Skip("platform does not permit symlink fixture")
				}
			}
			if _, err := LoadCorpus(root); !errors.Is(err, ErrInvalidCorpus) {
				t.Fatal("non-regular policy admitted", err)
			}
		})
	}
}

func TestPendingRunRefusesBeforeTargetAdmission(t *testing.T) {
	fixture := newAdmissionFixture(t)
	active := loadedTestCase(t, "general.active")
	pending := loadedTestCase(t, "general.pending")
	pending.Policy = &SuitePolicy{ID: "general", Status: "pending", Reason: "Not qualified."}
	for _, cases := range [][]LoadedCase{{pending}, {active, pending}} {
		result, err := Run(t.Context(), fixture.preset, cases, RunOptions{
			Home: fixture.home, OutputParent: fixture.output,
		})
		if !errors.Is(err, ErrPendingCases) || result.ID != "" || fixture.probes.Load() != 0 ||
			fixture.tasks.Load() != 0 {
			t.Fatal("pending selection reached target or produced partial report", err)
		}
	}
	_, err := Run(t.Context(), Preset{}, []LoadedCase{pending}, RunOptions{IncludePending: true})
	if err == nil || errors.Is(err, ErrPendingCases) {
		t.Fatal("pending opt-in bypassed ordinary target admission", err)
	}
	for _, policy := range []SuitePolicy{
		{ID: "other", Status: "active", Default: true},
		{ID: "general", Status: "pending", Default: true, Reason: "invalid"},
		{ID: "general", Status: "unknown"},
	} {
		pending.Policy = &policy
		if !errors.Is(CheckRunAvailability([]LoadedCase{pending}, true), ErrInvalidCorpus) {
			t.Fatal("opt-in accepted malformed in-process policy")
		}
	}
}

func TestCatalogAllowsDeferredOnlyCorpusWithoutInventingDefaultPass(t *testing.T) {
	root := t.TempDir()
	writeCase(t, root, "one.json", testCase())
	writeCatalog(t, root, SuitePolicy{ID: "general", Status: "pending", Reason: "Deferred."})
	items, err := LoadCorpus(root)
	if err != nil || len(items) != 1 {
		t.Fatal("deferred corpus cannot be inspected", err)
	}
	if _, err = Select(items, Selection{DefaultOnly: true}); !errors.Is(err, ErrInvalidFilter) {
		t.Fatal("empty default set admitted as success", err)
	}
}

func TestDeferredPolicyDoesNotInvalidateHistoricalGrade(t *testing.T) {
	fixture, item, source, calls := gradeFixture(t, false)
	probes, tasks := fixture.probes.Load(), fixture.tasks.Load()
	item.Policy = &SuitePolicy{ID: item.Case.Suite, Status: "pending", Reason: "Deferred after source execution."}
	result, err := Grade(t.Context(), fixture.preset, []LoadedCase{item}, source.ReportDir,
		RunOptions{Home: fixture.home, OutputParent: fixture.output})
	if err != nil || result.Verdict != Pass || calls.Load() != 1 || result.Trials[0].CaseSHA256 != item.Digest ||
		fixture.probes.Load() != probes || fixture.tasks.Load() != tasks {
		t.Fatal("policy-only change blocked frozen grading or replayed target", err)
	}
}
