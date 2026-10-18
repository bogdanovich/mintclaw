package eval

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func browserOracleCase(t *testing.T, scenario string) Case {
	t.Helper()
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range corpus {
		if item.Case.Browser != nil && item.Case.Browser.Scenario == scenario {
			candidate := item.Case
			for key, value := range candidate.Driver.Facts {
				if value == "{{generate}}" {
					candidate.Driver.Facts[key] = "synthetic-private-" + key
				}
			}
			return candidate
		}
	}
	t.Fatal("missing browser scenario")
	return Case{}
}

func browserOracleGet(t *testing.T, fixture *browserFixture, suffix string) []byte {
	t.Helper()
	response := httptest.NewRecorder()
	fixture.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fixture.origin+fixture.path+suffix, nil))
	if response.Code != http.StatusOK {
		t.Fatal("fixture read failed")
	}
	return response.Body.Bytes()
}

func browserOracleSubmit(t *testing.T, fixture *browserFixture, item Case) {
	t.Helper()
	form := url.Values{"nonce": {fixture.nonce}}
	for _, field := range item.Browser.Fields {
		value := field.Value
		if field.Fact != "" {
			value = item.Driver.Facts[field.Fact]
		}
		form.Set(field.Name, value)
	}
	response := httptest.NewRecorder()
	fixture.ServeHTTP(response, browserFixtureRequest(fixture, "submit", form))
	if response.Code != http.StatusOK {
		t.Fatal("fixture submit failed")
	}
}

func browserOracleRemove(t *testing.T, fixture *browserFixture, account string) {
	t.Helper()
	response := httptest.NewRecorder()
	fixture.ServeHTTP(response, browserFixtureRequest(fixture, "remove",
		url.Values{"nonce": {fixture.nonce}, "account": {account}}))
	if response.Code != http.StatusOK {
		t.Fatal("fixture removal failed")
	}
}

func TestBrowserFixtureOracleChecksEveryFrozenScenarioWithoutNativeClaims(t *testing.T) {
	for _, scenario := range []string{"read", "submit", "followup", "ambiguous", "artifact", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			item := browserOracleCase(t, scenario)
			fixture := newBrowserFixtureForTest(t)
			browserOracleGet(t, fixture, "")
			var artifact []byte
			switch scenario {
			case "submit", "followup":
				browserOracleSubmit(t, fixture, item)
			case "ambiguous":
				browserOracleRemove(t, fixture, "south")
			case "artifact":
				artifact = browserOracleGet(t, fixture, "report.csv")
			}
			fixture.seal()
			events := []Event{{ID: "event-1", Kind: "final", Content: "Reference: " + fixture.code}}
			result, err := inspectBrowserFixture(item, fixture, events, artifact)
			if err != nil || result.Version != Version || result.Scope != "local_fixture_only" ||
				result.Effects != Pass || !result.FixtureClosed || !validDigest(result.PageSHA256) ||
				!validDigest(result.CSVSourceSHA256) || scenario == "read" && result.Read != Pass ||
				scenario == "artifact" && (result.Artifact != Pass || result.ArtifactSHA256 != digest(artifact)) {
				t.Fatal("valid fixture-only evidence not recognized", err)
			}
			for _, check := range gradeConversation(item.Checks, events, events[0]) {
				if check.Verdict != Inconclusive {
					t.Fatal("fixture predicates silently qualified a native browser case")
				}
			}
			encoded, err := json.Marshal(result)
			if err != nil || strings.Contains(string(encoded), "synthetic-private-") ||
				strings.Contains(string(encoded), fixture.code) || strings.Contains(string(encoded), fixture.nonce) ||
				strings.Contains(string(encoded), fixture.origin) || strings.Contains(string(encoded), fixture.path) {
				t.Fatal("oracle exposed private expectations or fixture authority")
			}
		})
	}
}

func TestBrowserFixtureOracleRetainsWrongOrDuplicateEffects(t *testing.T) {
	for _, scenario := range []string{"submit", "followup", "ambiguous", "denied", "read", "artifact"} {
		t.Run(scenario, func(t *testing.T) {
			item := browserOracleCase(t, scenario)
			fixture := newBrowserFixtureForTest(t)
			browserOracleGet(t, fixture, "")
			browserOracleRemove(t, fixture, "north")
			switch scenario {
			case "submit", "followup":
				browserOracleSubmit(t, fixture, item)
			case "ambiguous":
				browserOracleRemove(t, fixture, "south")
			}
			fixture.mu.Lock()
			fixture.state.failed, fixture.state.overflow = true, true
			fixture.mu.Unlock()
			result, err := inspectBrowserFixture(item, fixture, nil, nil)
			if err != nil || result.Effects != Fail || result.EffectsCode != "fixture_effect_mismatch" {
				t.Fatal("wrong effect erased by later correct effect or witness failure", err)
			}
		})
	}
	for _, duplicate := range []bool{false, true} {
		item := browserOracleCase(t, "submit")
		fixture := newBrowserFixtureForTest(t)
		if duplicate {
			browserOracleSubmit(t, fixture, item)
			browserOracleSubmit(t, fixture, item)
		} else {
			changed := browserOracleCase(t, "submit")
			changed.Driver.Facts[changed.Browser.Fields[0].Fact] = "wrong-private-name"
			browserOracleSubmit(t, fixture, changed)
		}
		result, err := inspectBrowserFixture(item, fixture, nil, nil)
		if err != nil || result.Effects != Fail {
			t.Fatal("wrong values or duplicate writes admitted", err)
		}
	}
}

func TestBrowserFixtureOracleCannotPassMissingUnsettledOrOverflowedWitnesses(t *testing.T) {
	for _, mutate := range []func(*browserFixture){
		func(*browserFixture) {},
		func(f *browserFixture) { f.seal(); f.mu.Lock(); f.state.failed = true; f.mu.Unlock() },
		func(f *browserFixture) { f.seal(); f.mu.Lock(); f.state.overflow = true; f.mu.Unlock() },
	} {
		fixture := newBrowserFixtureForTest(t)
		mutate(fixture)
		result, err := inspectBrowserFixture(browserOracleCase(t, "denied"), fixture, nil, nil)
		if err != nil || result.Effects != Inconclusive {
			t.Fatal("absence of writes without a closed healthy witness passed", err)
		}
	}
	fixture := newBrowserFixtureForTest(t)
	fixture.seal()
	result, err := inspectBrowserFixture(browserOracleCase(t, "submit"), fixture, nil, nil)
	if err != nil || result.Effects != Fail || result.EffectsCode != "fixture_effect_missing" {
		t.Fatal("required missing submission passed", err)
	}
	item := browserOracleCase(t, "submit")
	item.Driver.Facts[item.Browser.Fields[0].Fact] = "{{generate}}"
	if _, err := inspectBrowserFixture(item, fixture, nil, nil); !errors.Is(err, ErrBrowserFixture) {
		t.Fatal("unresolved private expectation admitted")
	}
}

func TestBrowserFixtureOracleRequiresActualReadAndExactCSV(t *testing.T) {
	fixture := newBrowserFixtureForTest(t)
	read := browserOracleCase(t, "read")
	caption := []Event{{ID: "event-1", Kind: "final", Content: "Done"}}
	result, err := inspectBrowserFixture(read, fixture, caption, nil)
	if err != nil || result.Read != Inconclusive {
		t.Fatal("read passed without fixture access", err)
	}
	browserOracleGet(t, fixture, "")
	result, err = inspectBrowserFixture(read, fixture, caption, nil)
	if err != nil || result.Read != Fail {
		t.Fatal("read passed with wrong reference", err)
	}
	item := browserOracleCase(t, "artifact")
	for _, artifact := range [][]byte{
		nil, []byte("Done"), []byte("session,reference\nforeign,wrong\n"),
		append(append([]byte{}, fixture.file...), []byte("extra,row\n")...),
	} {
		result, err = inspectBrowserFixture(item, fixture, nil, artifact)
		if err != nil || result.Artifact == Pass || len(artifact) > 0 && result.Artifact != Fail {
			t.Fatal("missing, malformed, foreign or extended artifact passed", err)
		}
	}
	result, err = inspectBrowserFixture(item, fixture, nil, fixture.file)
	if err != nil || result.Artifact != Inconclusive || result.ArtifactCode != "fixture_download_missing" {
		t.Fatal("golden fixture bytes passed without an actual fixture download", err)
	}
	other := newBrowserFixtureForTest(t)
	browserOracleGet(t, fixture, "report.csv")
	result, err = inspectBrowserFixture(item, fixture, nil, other.file)
	if err != nil || result.Artifact != Fail {
		t.Fatal("cross-trial CSV passed", err)
	}
}

func TestBrowserFixtureOracleReadArtifactRequireSealedHealthyWitness(t *testing.T) {
	for _, scenario := range []string{"read", "artifact"} {
		for _, health := range []string{"healthy", "unsealed", "failed", "overflow"} {
			t.Run(scenario+"/"+health, func(t *testing.T) {
				fixture := newBrowserFixtureForTest(t)
				browserOracleGet(t, fixture, "")
				artifact := browserOracleGet(t, fixture, "report.csv")
				if health != "unsealed" {
					fixture.seal()
				}
				fixture.mu.Lock()
				fixture.state.failed = health == "failed"
				fixture.state.overflow = health == "overflow"
				fixture.mu.Unlock()
				events := []Event{{ID: "event-1", Kind: "final", Content: "Reference: " + fixture.code}}
				item := browserOracleCase(t, scenario)
				result, err := inspectBrowserFixture(item, fixture, events, artifact)
				verdict := result.Read
				if scenario == "artifact" {
					verdict = result.Artifact
				}
				if err != nil || (health == "healthy") != (verdict == Pass) ||
					health != "healthy" && (verdict != Inconclusive || result.ArtifactSHA256 != "") {
					t.Fatal("positive predicate bypassed witness settlement or health", err)
				}
				events[0].Content = "Wrong reference"
				artifact = append(artifact, '\n')
				wrong, err := inspectBrowserFixture(item, fixture, events, artifact)
				if err != nil || scenario == "read" && wrong.Read != Fail ||
					scenario == "artifact" && wrong.Artifact != Fail {
					t.Fatal("unhealthy witness erased an observed mismatch", err)
				}
			})
		}
	}
}
