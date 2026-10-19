package browser

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func regionObservationText(metadata string) string {
	return "- Page URL: https://example.com/\n- Page Title: Fixture\n- Snapshot encoding: json-names-v1\n" +
		"- Reference regions: " + metadata + "\n### Snapshot\n```yaml\n" +
		"- button \"Remove account\" [ref=e1]\n- button \"Remove account\" [ref=e2]\n```"
}

func TestPlaywrightReferenceRegionsBindExactRefsWithoutCaptionOrPositionGuess(t *testing.T) {
	const raw = `{"version":1,"complete":true,"entries":[{"ref":"e2","name":"South account"},{"ref":"e1","name":"North account"}]}`
	text := regionObservationText(raw)
	observed, err := parsePlaywrightObservation(text, 8192, 32, testPlaywrightToolResultBytes)
	if err != nil || len(observed.Elements) != 2 || observed.Elements[0].RegionName != "North account" ||
		observed.Elements[1].RegionName != "South account" || strings.Contains(observed.Snapshot, "Reference regions") {
		t.Fatal("native region binding was lost or exposed in the visible snapshot", err)
	}
	for _, current := range []string{
		strings.Replace(text, "- Reference regions: "+raw+"\n", "", 1),
		regionObservationText(`{"version":1,"complete":false,"entries":[]}`),
	} {
		legacy, err := parsePlaywrightObservation(current, 8192, 32, testPlaywrightToolResultBytes)
		if err != nil || legacy.Elements[0].RegionName != "" || legacy.Elements[1].RegionName != "" {
			t.Fatal("legacy/incomplete evidence gained context authority", err)
		}
	}
	truncated, err := parsePlaywrightObservation(text, 8192, 1, testPlaywrightToolResultBytes)
	if err != nil || !truncated.Truncated || truncated.Elements[0].RegionName != "" {
		t.Fatal("truncated snapshot retained qualification", err)
	}
}

func TestPlaywrightReferenceRegionsRefuseMalformedOrForeignProjection(t *testing.T) {
	for _, raw := range []string{
		`{"version":2,"complete":true,"entries":[]}`,
		`{"version":1,"version":1,"complete":true,"entries":[]}`,
		`{"Version":1,"complete":true,"entries":[]}`,
		`{"version":1,"complete":null,"entries":[]}`,
		`{"version":1,"complete":true,"entries":null}`,
		`{"version":1,"complete":true,"entries":[],"extra":true}`,
		`{"version":1,"complete":true,"entries":[{"ref":"e9","name":"South"}]}`,
		`{"version":1,"complete":true,"entries":[{"ref":"f1e2","name":"South"}]}`,
		`{"version":1,"complete":true,"entries":[{"ref":"e1","name":"South"},{"ref":"e1","name":"North"}]}`,
		`{"version":1,"complete":true,"entries":[{"Ref":"e1","name":"South"}]}`,
		`{"version":1,"complete":true,"entries":[{"ref":"e1","name":""}]}`,
		`{"version":1,"complete":false,"entries":[{"ref":"e1","name":"South"}]}`,
		`{"version":1,"complete":true,"entries":[{"ref":"e1","name":"South","name":"North"}]}`,
		`{"version":1,"complete":true,"entries":[]} trailing`,
		`{"version":1,"complete":true,"entries":[{"ref":"e1","name":"` + strings.Repeat("x", 513) + `"}]}`,
		strings.Repeat(" ", referenceRegionMetadataBytes+1),
	} {
		if _, err := parsePlaywrightObservation(
			regionObservationText(raw),
			8192,
			32,
			testPlaywrightToolResultBytes,
		); !errors.Is(
			err,
			ErrDriverIncompatible,
		) {
			t.Fatal("malformed reference-region projection admitted")
		}
	}
	text := regionObservationText(`{"version":1,"complete":true,"entries":[]}`)
	for _, changed := range []string{
		strings.Replace(text, "### Snapshot", "- Reference regions: {}\n### Snapshot", 1),
		strings.Replace(text, "- Snapshot encoding: json-names-v1\n", "", 1),
		strings.Replace(text, "- button \"Remove account\" [ref=e1]", "- textbox \"Remove account\" [ref=e1]", 1),
	} {
		if strings.Contains(changed, "textbox") {
			changed = strings.Replace(changed, `"entries":[]`, `"entries":[{"ref":"e1","name":"South"}]`, 1)
		}
		if _, err := parsePlaywrightObservation(
			changed,
			8192,
			32,
			testPlaywrightToolResultBytes,
		); !errors.Is(
			err,
			ErrDriverIncompatible,
		) {
			t.Fatal("invalid header, encoding or role admitted")
		}
	}
}

func TestPlaywrightReferenceRegionsResolveUsesFreshContextNotCachedName(t *testing.T) {
	for _, qualified := range []bool{false, true} {
		client := &fakePlaywrightClient{callResults: map[string]*sdkmcp.CallToolResult{
			"browser_snapshot": playwrightTextResult(regionObservationText(
				`{"version":1,"complete":true,"entries":[{"ref":"e2","name":"North account"}]}`,
			)),
		}}
		worker := &playwrightWorker{client: client, limits: config.BrowserLimitsConfig{}.Effective()}
		if qualified {
			worker.lastObservation = DriverObservation{Elements: []DriverElement{{
				Target: "e2", Role: "button", Name: "Remove account", RegionName: "South account",
			}}}
		}
		element, origin, err := worker.Resolve(t.Context(), "e2")
		if err != nil || origin != "https://example.com" || element.RegionName != "North account" {
			t.Fatal("resolve borrowed stale cached context", err)
		}
		if len(client.calls) != 1 || client.calls[0].arguments["target"] != "e2" {
			t.Fatal("resolve did not request exact target")
		}
		requested, _ := client.calls[0].arguments["region_context"].(bool)
		if requested != qualified {
			t.Fatal("resolve did not request the required fresh region projection")
		}
	}
}

func TestBrokerReferenceRegionIsStoredHashBoundAndRevalidated(t *testing.T) {
	for _, mode := range []string{"unchanged", "before-prepare", "after-approval", "missing-after-approval"} {
		t.Run(mode, func(t *testing.T) {
			root := admittedBrowserConfig()
			target := root.Tools.Browser.Targets["gateway"]
			profile := target.Profiles["managed"]
			profile.DryRun, profile.AllowApprovedActions = false, true
			target.Profiles["managed"] = profile
			root.Tools.Browser.Targets["gateway"] = target
			store := NewMemoryStore()
			broker, worker, session := openActionTestBrokerWithConfig(t, root, store)
			elements := []DriverElement{
				{Target: "north", Role: "button", Name: "Remove account", RegionName: "North account"},
				{Target: "south", Role: "button", Name: "Remove account", RegionName: "South account"},
			}
			worker.observation = driverObservationFixture(elements...)
			worker.resolveElements = map[string]DriverElement{"north": elements[0], "south": elements[1]}
			worker.resolveOrigin = worker.observation.Origin
			observed, err := broker.Observe(t.Context(), testOwner(), session.ID, session.TabID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "before-prepare" {
				changed := elements[1]
				changed.RegionName = "North account"
				worker.resolveElements["south"] = changed
			}
			prepared, err := broker.PrepareAction(t.Context(), PrepareActionRequest{
				Owner: testOwner(), RequestID: "region-click", SessionID: session.ID, TabID: session.TabID,
				SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
				Action: Action{Kind: ActionClick, Ref: visibleRefs(t, observed.Snapshot)[1]},
			})
			if mode == "before-prepare" {
				if !errors.Is(err, ErrStale) || len(worker.actions) != 0 {
					t.Fatal("changed region was prepared", err)
				}
				return
			}
			if err != nil || !prepared.RequiresApproval || prepared.Action.ElementRegionName != "South account" {
				t.Fatal("native region was not bound before approval", err)
			}
			stored, err := store.GetPreparedAction(t.Context(), prepared.Action.ID)
			if err != nil || stored.ElementRegionName != "South account" {
				t.Fatal("native region was not retained", err)
			}
			forged := prepared.Action
			forged.ElementRegionName = "North account"
			hash, err := hashPreparedAction(forged)
			if err != nil || hash == prepared.Approval.ActionHash {
				t.Fatal("region changed without changing approval hash", err)
			}
			if mode != "unchanged" {
				changed := elements[1]
				changed.RegionName = "North account"
				if mode == "missing-after-approval" {
					changed.RegionName = ""
				}
				worker.resolveElements["south"] = changed
			}
			invocation, err := broker.ExecuteAction(t.Context(), testOwner(), prepared.Action.ID, &prepared.Approval)
			if mode == "unchanged" {
				if err != nil || invocation.State != InvocationSucceeded || len(worker.actions) != 1 {
					t.Fatal("unchanged native binding failed", err)
				}
			} else if !errors.Is(err, ErrStale) || len(worker.actions) != 0 {
				t.Fatal("stale/unknown region dispatched after approval", err)
			}
		})
	}
}

func TestReferenceRegionEmptyFieldsPreserveLegacyJSONAndTruncationIsUnknown(t *testing.T) {
	data, err := json.Marshal(PreparedAction{})
	if err != nil || bytes.Contains(data, []byte("region")) {
		t.Fatal("legacy empty context changed the stored/hash projection", err)
	}
	observed := redactCredentialObservation(DriverObservation{
		Truncated: true, Elements: []DriverElement{{Target: "e1", Role: "button", Name: "Remove", RegionName: "South"}},
	})
	if observed.Elements[0].RegionName != "" {
		t.Fatal("truncated driver retained qualification")
	}
}
