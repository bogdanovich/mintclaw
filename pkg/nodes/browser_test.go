package nodes

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBrowserCommandDescriptorsAreTypedAndInternal(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 6 {
		t.Fatalf("descriptor count = %d", len(descriptors))
	}
	for _, descriptor := range descriptors {
		if descriptor.ModelContract != nil {
			t.Fatalf("%s unexpectedly has a model contract", descriptor.Name)
		}
		if len(descriptor.BrowserProfiles) != 1 ||
			descriptor.BrowserProfiles[0].Alias != "managed" {
			t.Fatalf("%s browser profiles = %#v", descriptor.Name, descriptor.BrowserProfiles)
		}
		encoded, marshalErr := json.Marshal(descriptor)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		for _, secretField := range []string{"executable", "profile_directory", "lock_file", "endpoint"} {
			if strings.Contains(string(encoded), secretField) {
				t.Fatalf("%s descriptor leaked %q", descriptor.Name, secretField)
			}
		}
	}
	if descriptors[0].Name != BrowserCommandSessionOpen || descriptors[0].Risk != RiskWrite ||
		descriptors[1].Name != BrowserCommandSessionStatus || descriptors[1].Risk != RiskRead ||
		descriptors[2].Name != BrowserCommandObserve || descriptors[2].Risk != RiskRead ||
		descriptors[3].Name != BrowserCommandAct || descriptors[3].Risk != RiskWrite ||
		descriptors[4].Name != BrowserCommandContexts || descriptors[4].Risk != RiskWrite ||
		descriptors[5].Name != BrowserCommandSessionClose || descriptors[5].Risk != RiskWrite {
		t.Fatalf("descriptor order or risks = %#v", descriptors)
	}
}

func TestBrowserSelectDispatchAcceptsWorstCaseJSONEscaping(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.Actions = []string{"select"}
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	var descriptor CommandDescriptor
	for _, candidate := range descriptors {
		if candidate.Name == BrowserCommandAct {
			descriptor = candidate
			break
		}
	}
	if descriptor.Name == "" {
		t.Fatal("browser action descriptor is unavailable")
	}
	value := strings.Repeat("\x01", MaxBrowserTextInputBytes)
	input := browserActInputFixture()
	input["action"] = map[string]any{"kind": "select", "ref": "host_ref_1"}
	input["effect"] = "local_edit"
	input["current_origin"] = "https://example.com"
	input["expected_role"] = "combobox"
	input["expected_name"] = "State"
	input["input_digest"] = BrowserInputDigest(value)
	input["input_bytes"] = len(value)
	inputRaw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	catalog := CapabilityCatalog{Commands: descriptors}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareExecutionPlan(InvocationRequest{
		InvocationID: "inv_browser_select_escape", IdempotencyKey: "idem_browser_select_escape",
		NodeID: ID("node_test"), CatalogHash: catalogHash, Command: BrowserCommandAct,
		Input: inputRaw, AgentID: "main", SessionID: "session_test", ActorID: "user_test",
		TimeoutSeconds: 30, OutputLimitBytes: MaxBrowserToolResultBytes,
	}, descriptor, "local", "policy-1", time.Unix(1, 0), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ephemeral, err := json.Marshal(struct {
		Value string `json:"value"`
	}{Value: value})
	if err != nil {
		t.Fatal(err)
	}
	if len(ephemeral) <= MaxBrowserTextInputBytes+128 || len(ephemeral) > MaxBrowserEphemeralInputBytes {
		t.Fatalf("escaped envelope bytes = %d", len(ephemeral))
	}
	if err = (InvocationDispatch{Plan: plan, EphemeralInput: ephemeral}).Validate(); err != nil {
		t.Fatalf("InvocationDispatch.Validate() error = %v", err)
	}
}

func TestBrowserCommandDescriptorsBindExplicitApprovedActionMode(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.DryRun = false
	profile.AllowApprovedActions = true
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	input := browserSessionOpenInputFixture(profile.Limits)
	input["dry_run"] = false
	if err = validateDescriptorInvocationInput(descriptors[0], input); err != nil {
		t.Fatalf("approved-action open input rejected: %v", err)
	}
	input["dry_run"] = true
	if err = validateDescriptorInvocationInput(descriptors[0], input); err == nil {
		t.Fatal("approved-action descriptor accepted dry-run open input")
	}
}

func TestBrowserSessionResultDecodesCanonicalIntegerTimestamps(t *testing.T) {
	var result BrowserSessionResult
	if err := json.Unmarshal([]byte(`{
		"session_id":"session_1",
		"state":"ready",
		"tab_id":"tab_primary",
		"controller":"agent",
		"features":{"observe":true,"navigate":true,"screenshot":false,"download":false},
		"expires_at":1.786223585e9,
		"idle_expires_at":1.786220045e9
	}`), &result); err != nil {
		t.Fatal(err)
	}
	if result.ExpiresAt != 1786223585 || result.IdleExpiresAt != 1786220045 {
		t.Fatalf("decoded browser session timestamps = %#v", result)
	}

	for _, invalid := range []string{
		`{"session_id":"session_1","state":"ready","expires_at":1.5}`,
		`{"session_id":"session_1","state":"ready","expires_at":-1}`,
		`{"session_id":"session_1","state":"ready","expires_at":1e100}`,
	} {
		if err := json.Unmarshal([]byte(invalid), &result); err == nil {
			t.Fatalf("BrowserSessionResult accepted invalid timestamp %s", invalid)
		}
	}
}

func TestBrowserPayloadsDecodeCanonicalSnapshotGenerationsExactly(t *testing.T) {
	var observe BrowserObserveInput
	if err := json.Unmarshal([]byte(`{"session_id":"session_1","tab_id":"tab_primary",`+
		`"snapshot_generation":1e1,"screenshot":false}`), &observe); err != nil {
		t.Fatal(err)
	}
	var action BrowserActInput
	if err := json.Unmarshal([]byte(`{"session_id":"session_1","tab_id":"tab_primary",`+
		`"snapshot_generation":1e2,"action_invocation_id":"action_1",`+
		`"action":{"kind":"navigate","url":"https://example.com"},`+
		`"effect":"navigation","current_origin":"about:blank",`+
		`"prepared_action_hash":"`+strings.Repeat("a", 64)+`",`+
		`"browser_policy_revision":"`+strings.Repeat("b", 64)+`",`+
		`"profile_revision":"managed-v1"}`), &action); err != nil {
		t.Fatal(err)
	}
	var observation BrowserObservationResult
	if err := json.Unmarshal([]byte(`{"session_id":"session_1","tab_id":"tab_primary",`+
		`"snapshot_generation":1e2,"url":"about:blank","origin":"about:blank",`+
		`"snapshot":"","elements":[],"truncated":false}`), &observation); err != nil {
		t.Fatal(err)
	}
	var actionResult BrowserActResult
	if err := json.Unmarshal([]byte(`{"action_invocation_id":"action_1","state":"succeeded",`+
		`"observation":{"session_id":"session_1","tab_id":"tab_primary",`+
		`"snapshot_generation":1e1,"url":"about:blank","origin":"about:blank",`+
		`"snapshot":"","elements":[],"truncated":false}}`), &actionResult); err != nil {
		t.Fatal(err)
	}
	if observe.SnapshotGeneration != 10 || action.SnapshotGeneration != 100 ||
		observation.SnapshotGeneration != 100 || observation.Elements == nil ||
		actionResult.Observation == nil || actionResult.Observation.SnapshotGeneration != 10 {
		t.Fatalf("decoded generations = observe %d, action %d, observation %#v",
			observe.SnapshotGeneration, action.SnapshotGeneration, observation)
	}

	for _, invalid := range []string{"1.5", "-1", "1e100"} {
		data := []byte(`{"session_id":"session_1","tab_id":"tab_primary",` +
			`"snapshot_generation":` + invalid + `,"screenshot":false}`)
		if err := json.Unmarshal(data, &observe); err == nil {
			t.Fatalf("BrowserObserveInput accepted invalid generation %s", invalid)
		}
	}
}

func TestBrowserActSchemaBindsActionsToProfileRevision(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.Actions = []string{"navigate"}
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	act := descriptors[3]
	base := map[string]any{
		"session_id": "session_1", "tab_id": "tab_1", "snapshot_generation": 1,
		"action_invocation_id": "action_1", "effect": "navigation",
		"current_origin":          "about:blank",
		"prepared_action_hash":    strings.Repeat("a", 64),
		"browser_policy_revision": strings.Repeat("b", 64),
		"profile_revision":        "managed-v1",
	}
	base["action"] = map[string]any{"kind": "navigate", "url": "https://example.com"}
	if err = validateInvocationInput(act.InputSchema, base); err != nil {
		t.Fatalf("navigate input rejected: %v", err)
	}
	base["action"] = map[string]any{"kind": "download", "ref": "ref_1"}
	if err = validateInvocationInput(act.InputSchema, base); err == nil {
		t.Fatal("act schema accepted an action absent from profile authority")
	}
	base["action"] = map[string]any{"kind": "navigate", "url": "https://example.com"}
	base["effect"] = "download"
	if err = validateInvocationInput(act.InputSchema, base); err == nil {
		t.Fatal("act schema accepted an effect that did not match the action")
	}
	base["effect"] = "navigation"
	base["method"] = "Runtime.evaluate"
	if err = validateInvocationInput(act.InputSchema, base); err == nil {
		t.Fatal("act schema accepted an extra raw driver field")
	}
}

func TestBrowserActSchemaAcceptsBoundedScrollAndCanonicalAmount(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.Actions = []string{"navigate", "scroll"}
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	input := browserActInputFixture()
	input["action"] = map[string]any{"kind": "scroll", "direction": "down", "amount": 5}
	input["effect"] = "read"
	if err = validateInvocationInput(descriptors[3].InputSchema, input); err != nil {
		t.Fatalf("bounded scroll input rejected: %v", err)
	}
	input["action"] = map[string]any{"kind": "scroll", "direction": "down", "amount": 6}
	if err = validateInvocationInput(descriptors[3].InputSchema, input); err == nil {
		t.Fatal("scroll amount above the bound was accepted")
	}
	var decoded BrowserAction
	if err = json.Unmarshal([]byte(`{"kind":"scroll","direction":"up","amount":1e0}`), &decoded); err != nil ||
		decoded.Amount != 1 {
		t.Fatalf("canonical scroll action = %#v, %v", decoded, err)
	}
}

func TestBrowserActSchemaBindsTypedPressAndSelect(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.DryRun = false
	profile.AllowApprovedActions = true
	profile.Actions = []string{"navigate", "press", "select"}
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	act := descriptors[3]

	press := browserActInputFixture()
	press["action"] = map[string]any{"kind": "press", "target": "document", "key": "Tab"}
	press["effect"] = "unknown"
	press["approval_digest"] = strings.Repeat("c", 64)
	if err = validateInvocationInput(act.InputSchema, press); err != nil {
		t.Fatalf("typed press input rejected: %v", err)
	}
	press["action"].(map[string]any)["key"] = "Control+L"
	if err = validateInvocationInput(act.InputSchema, press); err == nil {
		t.Fatal("press schema accepted a privileged browser-chrome shortcut")
	}
	press["action"].(map[string]any)["key"] = "Tab"
	press["expected_role"] = "button"
	if err = validateInvocationInput(act.InputSchema, press); err == nil {
		t.Fatal("document press schema accepted an element semantic binding")
	}
	delete(press, "expected_role")
	delete(press, "approval_digest")
	if err = validateInvocationInput(act.InputSchema, press); err == nil {
		t.Fatal("press schema accepted missing approval attestation")
	}

	selection := browserActInputFixture()
	selection["action"] = map[string]any{"kind": "select", "ref": "host_ref_1"}
	selection["effect"] = "local_edit"
	selection["expected_role"] = "combobox"
	selection["expected_name"] = "State"
	selection["input_digest"] = BrowserInputDigest("CA")
	selection["input_bytes"] = 2
	if err = validateInvocationInput(act.InputSchema, selection); err != nil {
		t.Fatalf("typed select input rejected: %v", err)
	}
	selection["expected_role"] = "textbox"
	if err = validateInvocationInput(act.InputSchema, selection); err == nil {
		t.Fatal("select schema accepted a non-combobox semantic role")
	}
	selection["expected_role"] = "combobox"
	selection["approval_digest"] = strings.Repeat("d", 64)
	if err = validateInvocationInput(act.InputSchema, selection); err == nil {
		t.Fatal("select schema accepted an unrelated approval attestation")
	}
}

func TestBrowserDescriptorAcceptsExactPreScrollCatalogDuringRollingUpgrade(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.Actions = []string{"navigate"}
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	for index := range descriptors {
		descriptors[index].InputSchema = legacyBrowserCommandInputSchema(
			descriptors[index].Name,
			descriptors[index].BrowserProfiles,
		)
	}
	if err = (CapabilityCatalog{Commands: descriptors}).Validate(); err != nil {
		t.Fatalf("pre-scroll catalog rejected during rolling upgrade: %v", err)
	}

	for index := range descriptors {
		if descriptors[index].Name == BrowserCommandAct {
			descriptors[index].BrowserProfiles[0].Actions = []string{"navigate", "scroll"}
			break
		}
	}
	if err = (CapabilityCatalog{Commands: descriptors}).Validate(); err == nil {
		t.Fatal("pre-scroll action schema accepted scroll authority")
	}

	profile.Actions = []string{"click", "download", "navigate"}
	descriptors, err = BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	for index := range descriptors {
		if descriptors[index].Name == BrowserCommandAct {
			descriptors[index].InputSchema = legacyBrowserCommandInputSchema(
				descriptors[index].Name,
				descriptors[index].BrowserProfiles,
			)
			break
		}
	}
	if err = (CapabilityCatalog{Commands: descriptors}).Validate(); err == nil {
		t.Fatal("pre-click action schema accepted click authority")
	}
}

func TestBrowserDescriptorAcceptsExactPreApprovedActionCatalogDuringRollingUpgrade(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	for index := range descriptors {
		if descriptors[index].Name == BrowserCommandSessionOpen {
			descriptors[index].InputSchema = legacyDryRunBrowserCommandInputSchema(
				descriptors[index].Name,
				descriptors[index].BrowserProfiles,
			)
		}
	}
	if err = (CapabilityCatalog{Commands: descriptors}).Validate(); err != nil {
		t.Fatalf("pre-approved-action catalog rejected during rolling upgrade: %v", err)
	}

	approved := browserProfileDescriptorFixture()
	approved.DryRun = false
	approved.AllowApprovedActions = true
	descriptors, err = BrowserCommandDescriptors([]BrowserProfileDescriptor{approved})
	if err != nil {
		t.Fatal(err)
	}
	for index := range descriptors {
		if descriptors[index].Name == BrowserCommandSessionOpen {
			descriptors[index].InputSchema = legacyDryRunBrowserCommandInputSchema(
				descriptors[index].Name,
				descriptors[index].BrowserProfiles,
			)
		}
	}
	if err = (CapabilityCatalog{Commands: descriptors}).Validate(); err == nil {
		t.Fatal("legacy dry-run schema granted approved-action authority")
	}

	dryRunClick := browserProfileDescriptorFixture()
	dryRunClick.Actions = []string{"click", "download", "navigate"}
	descriptors, err = BrowserCommandDescriptors([]BrowserProfileDescriptor{dryRunClick})
	if err != nil {
		t.Fatal(err)
	}
	for index := range descriptors {
		if descriptors[index].Name == BrowserCommandSessionOpen {
			descriptors[index].InputSchema = legacyDryRunBrowserCommandInputSchema(
				descriptors[index].Name,
				descriptors[index].BrowserProfiles,
			)
			break
		}
	}
	if err = (CapabilityCatalog{Commands: descriptors}).Validate(); err == nil {
		t.Fatal("legacy session-open schema accepted click authority")
	}
}

func TestBrowserActSchemaRequiresApprovalForDownloadsAndClicks(t *testing.T) {
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{
		browserProfileDescriptorFixture(),
	})
	if err != nil {
		t.Fatal(err)
	}
	act := descriptors[3]
	input := browserActInputFixture()
	input["action"] = map[string]any{"kind": "navigate", "url": "https://example.com"}
	if err = validateInvocationInput(act.InputSchema, input); err != nil {
		t.Fatalf("unapproved navigation input rejected: %v", err)
	}

	input["action"] = map[string]any{"kind": "download", "ref": "ref_1"}
	input["effect"] = "download"
	if err = validateInvocationInput(act.InputSchema, input); err == nil {
		t.Fatal("download input without approval_digest was accepted")
	}
	input["approval_digest"] = strings.Repeat("c", 64)
	if err = validateInvocationInput(act.InputSchema, input); err != nil {
		t.Fatalf("approved download input rejected: %v", err)
	}

	profile := browserProfileDescriptorFixture()
	profile.Actions = []string{"click", "download", "navigate"}
	descriptors, err = BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	act = descriptors[3]
	input = browserActInputFixture()
	input["action"] = map[string]any{"kind": "click", "ref": "host_ref_1"}
	input["effect"] = "external_commit"
	input["expected_role"] = "button"
	input["expected_name"] = "Save"
	if err = validateInvocationInput(act.InputSchema, input); err == nil {
		t.Fatal("click input without approval_digest was accepted")
	}
	input["approval_digest"] = strings.Repeat("d", 64)
	if err = validateInvocationInput(act.InputSchema, input); err != nil {
		t.Fatalf("approved button click rejected: %v", err)
	}
	delete(input, "expected_name")
	if err = validateInvocationInput(act.InputSchema, input); err != nil {
		t.Fatalf("approved unnamed button click rejected: %v", err)
	}
	input["expected_name"] = "Save"
	input["effect"] = "unknown"
	if err = validateInvocationInput(act.InputSchema, input); err == nil {
		t.Fatal("button click with lowered effect was accepted")
	}
	input["expected_role"] = "link"
	if err = validateInvocationInput(act.InputSchema, input); err != nil {
		t.Fatalf("unknown-effect link click rejected: %v", err)
	}
}

func TestBrowserApprovalDigestBindsClickInput(t *testing.T) {
	input := BrowserActInput{
		SessionID: "session_1", TabID: "tab_1", SnapshotGeneration: 7,
		ActionInvocationID: "invocation_1", Action: BrowserAction{Kind: "click", Ref: "host_ref_1"},
		Effect: "external_commit", CurrentOrigin: "https://example.com",
		PreparedActionHash: strings.Repeat("a", 64), BrowserPolicyRevision: strings.Repeat("b", 64),
		ProfileRevision: "managed-v1", ExpectedRole: "button", ExpectedName: "Save",
	}
	digest, err := BrowserApprovalDigest(input)
	if err != nil {
		t.Fatal(err)
	}
	input.ApprovalDigest = digest
	if !BrowserApprovalDigestMatches(input) {
		t.Fatal("exact click approval digest did not match")
	}
	input.ExpectedName = "Delete"
	if BrowserApprovalDigestMatches(input) {
		t.Fatal("changed click semantics retained approval digest authority")
	}
}

func TestBrowserSessionOpenSchemaBindsProfileLimits(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.Limits.DownloadBytes = 1024
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	input := browserSessionOpenInputFixture(profile.Limits)
	if err = validateInvocationInput(descriptors[0].InputSchema, input); err != nil {
		t.Fatalf("profile-bounded open input rejected: %v", err)
	}
	input["limits"].(map[string]any)["download_bytes"] = 1025
	if err = validateInvocationInput(descriptors[0].InputSchema, input); err == nil {
		t.Fatal("open input above the selected profile's download ceiling was accepted")
	}
}

func TestBrowserStatusAndCloseSchemasBindProfileRevision(t *testing.T) {
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{
		browserProfileDescriptorFixture(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range []CommandDescriptor{descriptors[1], descriptors[5]} {
		input := map[string]any{"session_id": "session_1", "profile_revision": "managed-v1"}
		if err = validateInvocationInput(descriptor.InputSchema, input); err != nil {
			t.Fatalf("%s advertised revision rejected: %v", descriptor.Name, err)
		}
		input["profile_revision"] = "stale-v1"
		if err = validateInvocationInput(descriptor.InputSchema, input); err == nil {
			t.Fatalf("%s accepted an unadvertised profile revision", descriptor.Name)
		}
	}
}

func TestBrowserContextsSchemaBindsOpaqueAuthorityAndCanonicalGenerations(t *testing.T) {
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{
		browserProfileDescriptorFixture(),
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := descriptors[4]
	list := map[string]any{
		"session_id": "session_1", "profile_revision": "managed-v1",
		"operation": "list", "request_id": "request_1",
	}
	if err = validateInvocationInput(descriptor.InputSchema, list); err != nil {
		t.Fatalf("list input rejected: %v", err)
	}
	authority := map[string]any{
		"context_catalog_id": "catalog_1", "context_generation": 1e1,
		"selected_tab_id": "tab_1",
		"tabs": []any{map[string]any{
			"tab_id": "tab_1", "kind": "primary", "creation_sequence": 1e0,
			"document_generation": 2e0, "url": "about:blank", "origin": "about:blank",
		}},
	}
	selectInput := map[string]any{
		"session_id": "session_1", "profile_revision": "managed-v1",
		"operation": "select", "request_id": "request_2",
		"authority_digest": strings.Repeat("a", 64), "authority_bytes": 1024,
		"context_catalog_id": "catalog_1", "context_generation": 10,
		"tab_id": "tab_1",
	}
	if err = validateInvocationInput(descriptor.InputSchema, selectInput); err != nil {
		t.Fatalf("select input rejected: %v", err)
	}
	raw, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	var decoded BrowserContextCatalog
	if err = json.Unmarshal(raw, &decoded); err != nil ||
		decoded.Generation != 10 || decoded.Tabs[0].DocumentGeneration != 2 {
		t.Fatalf("canonical context input = %#v, %v", decoded, err)
	}
	selectInput["frame_id"] = "frame_1"
	selectInput["operation"] = "close"
	if err = validateInvocationInput(descriptor.InputSchema, selectInput); err == nil {
		t.Fatal("close accepted a frame target")
	}
}

func TestBrowserSessionOpenSemanticValidationRejectsInconsistentLifetimes(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	input := browserSessionOpenInputFixture(profile.Limits)
	limits := input["limits"].(map[string]any)
	limits["session_seconds"] = 1
	limits["idle_seconds"] = 2
	limits["prepared_seconds"] = 1
	if err = validateDescriptorInvocationInput(descriptors[0], input); err == nil {
		t.Fatal("open input with idle_seconds above session_seconds was accepted")
	}
	limits["idle_seconds"] = 1
	limits["prepared_seconds"] = 2
	if err = validateDescriptorInvocationInput(descriptors[0], input); err == nil {
		t.Fatal("open input with prepared_seconds above session_seconds was accepted")
	}
}

func TestBrowserSemanticValidationUsesUTF8ByteCeilings(t *testing.T) {
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{
		browserProfileDescriptorFixture(),
	})
	if err != nil {
		t.Fatal(err)
	}
	input := browserActInputFixture()
	input["action"] = map[string]any{
		"kind": "navigate", "url": strings.Repeat("🙂", MaxBrowserURLBytes/4),
	}
	if err = validateDescriptorInvocationInput(descriptors[3], input); err != nil {
		t.Fatalf("navigate input at UTF-8 byte ceiling rejected: %v", err)
	}
	input["action"].(map[string]any)["url"] = strings.Repeat("🙂", MaxBrowserURLBytes/4+1)
	if err = validateDescriptorInvocationInput(descriptors[3], input); err == nil {
		t.Fatal("navigate input above UTF-8 byte ceiling was accepted")
	}

	observation := map[string]any{
		"session_id": "session_1", "tab_id": "tab_1", "snapshot_generation": 1,
		"url": "", "origin": "", "snapshot": strings.Repeat("🙂", MaxBrowserSnapshotBytes/4),
		"elements": []any{}, "truncated": false,
	}
	assertBrowserOutputValid(t, descriptors[2], observation)
	observation["snapshot"] = strings.Repeat("🙂", MaxBrowserSnapshotBytes/4+1)
	assertBrowserOutputInvalid(t, descriptors[2], observation)
}

func TestBrowserDescriptorRejectsProfileOrSchemaBroadening(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*CommandDescriptor)
	}{
		{
			name: "non dry run",
			mutate: func(descriptor *CommandDescriptor) {
				descriptor.BrowserProfiles[0].DryRun = false
			},
		},
		{
			name: "conflicting action modes",
			mutate: func(descriptor *CommandDescriptor) {
				descriptor.BrowserProfiles[0].AllowApprovedActions = true
			},
		},
		{
			name: "raw action",
			mutate: func(descriptor *CommandDescriptor) {
				descriptor.BrowserProfiles[0].Actions = []string{"evaluate"}
			},
		},
		{
			name: "model projection",
			mutate: func(descriptor *CommandDescriptor) {
				descriptor.ModelContract = &CommandModelContract{}
			},
		},
		{
			name: "schema replacement",
			mutate: func(descriptor *CommandDescriptor) {
				descriptor.InputSchema = json.RawMessage(`{"type":"object","additionalProperties":true}`)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptor := descriptors[3]
			descriptor.BrowserProfiles = CloneBrowserProfileDescriptors(descriptor.BrowserProfiles)
			test.mutate(&descriptor)
			if err := descriptor.Validate(); err == nil {
				t.Fatal("Validate() accepted broadened browser descriptor")
			}
		})
	}
}

func TestBrowserArtifactSchemasUseCapabilitySpecificCeilings(t *testing.T) {
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{
		browserProfileDescriptorFixture(),
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact := func(size int) map[string]any {
		return map[string]any{
			"transfer_id": "transfer_1", "sha256": strings.Repeat("a", 64),
			"size": size, "content_type": "image/png",
		}
	}
	observation := map[string]any{
		"session_id": "session_1", "tab_id": "tab_1", "snapshot_generation": 1,
		"url": "", "origin": "", "snapshot": "", "elements": []any{}, "truncated": false,
		"screenshot": artifact(MaxBrowserScreenshotBytes),
	}
	assertBrowserOutputValid(t, descriptors[2], observation)
	observation["screenshot"] = artifact(MaxBrowserScreenshotBytes + 1)
	assertBrowserOutputInvalid(t, descriptors[2], observation)

	action := map[string]any{
		"action_invocation_id": "action_1", "state": "succeeded",
		"artifact": artifact(MaxBrowserDownloadBytes),
	}
	assertBrowserOutputValid(t, descriptors[3], action)
	action["artifact"] = artifact(MaxBrowserDownloadBytes + 1)
	assertBrowserOutputInvalid(t, descriptors[3], action)
}

func TestBrowserOutputSchemasUseStrictestAdvertisedProfileCeilings(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.Limits.SnapshotBytes = 1024
	profile.Limits.ScreenshotBytes = 2048
	profile.Limits.DownloadBytes = 4096
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	artifact := func(size int) map[string]any {
		return map[string]any{
			"transfer_id": "transfer_1", "sha256": strings.Repeat("a", 64),
			"size": size, "content_type": "image/png",
		}
	}
	observation := map[string]any{
		"session_id": "session_1", "tab_id": "tab_1", "snapshot_generation": 1,
		"url": "", "origin": "", "snapshot": strings.Repeat("🙂", 256),
		"elements": []any{}, "truncated": false, "screenshot": artifact(2048),
	}
	assertBrowserOutputValid(t, descriptors[2], observation)
	observation["snapshot"] = strings.Repeat("🙂", 257)
	assertBrowserOutputInvalid(t, descriptors[2], observation)
	observation["snapshot"] = ""
	observation["screenshot"] = artifact(2049)
	assertBrowserOutputInvalid(t, descriptors[2], observation)

	action := map[string]any{
		"action_invocation_id": "action_1", "state": "succeeded", "artifact": artifact(4096),
	}
	assertBrowserOutputValid(t, descriptors[3], action)
	action["artifact"] = artifact(4097)
	assertBrowserOutputInvalid(t, descriptors[3], action)
}

func assertBrowserOutputValid(t *testing.T, descriptor CommandDescriptor, value map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ValidateInvocationOutput(descriptor, encoded, MaxInvocationOutput); err != nil {
		t.Fatalf("ValidateInvocationOutput() error = %v", err)
	}
}

func assertBrowserOutputInvalid(t *testing.T, descriptor CommandDescriptor, value map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ValidateInvocationOutput(descriptor, encoded, MaxInvocationOutput); err == nil {
		t.Fatal("ValidateInvocationOutput() accepted an oversized artifact")
	}
}

func browserProfileDescriptorFixture() BrowserProfileDescriptor {
	return BrowserProfileDescriptor{
		Alias: "managed", Revision: "managed-v1", Driver: "playwright_mcp",
		Mode: "managed", NetworkMode: "any_http", DryRun: true,
		Actions: []string{"download", "navigate"},
		Limits: BrowserLimits{
			Sessions: 1, Tabs: 1, SessionSeconds: 3600, IdleSeconds: 600,
			PreparedSeconds: 300, ActionSeconds: 60, SnapshotBytes: MaxBrowserSnapshotBytes,
			ScreenshotBytes: MaxBrowserScreenshotBytes,
			UploadBytes:     MaxBrowserUploadBytes, DownloadBytes: MaxBrowserDownloadBytes,
			SnapshotRefs: 500, TextInputBytes: MaxBrowserTextInputBytes,
			ToolResultBytes: MaxBrowserToolResultBytes, RetentionSecs: MaxBrowserRetentionSeconds,
		},
	}
}

func browserActInputFixture() map[string]any {
	return map[string]any{
		"session_id": "session_1", "tab_id": "tab_1", "snapshot_generation": 1,
		"action_invocation_id": "action_1", "effect": "navigation",
		"current_origin":          "about:blank",
		"prepared_action_hash":    strings.Repeat("a", 64),
		"browser_policy_revision": strings.Repeat("b", 64),
		"profile_revision":        "managed-v1",
	}
}

func browserSessionOpenInputFixture(limits BrowserLimits) map[string]any {
	return map[string]any{
		"session_id": "session_1", "profile": "managed", "profile_revision": "managed-v1",
		"browser_policy_revision": strings.Repeat("a", 64), "dry_run": true,
		"limits": browserLimitsValue(limits),
	}
}

func browserLimitsValue(limits BrowserLimits) map[string]any {
	return map[string]any{
		"sessions": limits.Sessions, "tabs": limits.Tabs,
		"session_seconds": limits.SessionSeconds, "idle_seconds": limits.IdleSeconds,
		"prepared_seconds": limits.PreparedSeconds, "action_seconds": limits.ActionSeconds,
		"snapshot_bytes": limits.SnapshotBytes, "screenshot_bytes": limits.ScreenshotBytes,
		"upload_bytes": limits.UploadBytes, "download_bytes": limits.DownloadBytes,
		"snapshot_refs": limits.SnapshotRefs, "text_input_bytes": limits.TextInputBytes,
		"tool_result_bytes": limits.ToolResultBytes, "retention_seconds": limits.RetentionSecs,
	}
}
