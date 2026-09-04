package nodes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
)

func TestBrowserCommandDescriptorsAreTypedAndInternal(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 9 {
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
		descriptors[5].Name != BrowserCommandSessionClose || descriptors[5].Risk != RiskWrite ||
		descriptors[6].Name != BrowserCommandCapture || descriptors[6].Risk != RiskRead ||
		descriptors[7].Name != BrowserCommandDiagnostics || descriptors[7].Risk != RiskRead ||
		descriptors[8].Name != BrowserCommandPolicyEvaluate || descriptors[8].Risk != RiskRead {
		t.Fatalf("descriptor order or risks = %#v", descriptors)
	}
}

func TestBrowserCatalogRequiresOneCurrentProfileSet(t *testing.T) {
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{browserProfileDescriptorFixture()})
	if err != nil {
		t.Fatal(err)
	}
	descriptors[0].BrowserProfiles[0].Limits.SnapshotRefs--
	descriptors[0].InputSchema = BrowserCommandInputSchema(
		descriptors[0].Name,
		descriptors[0].BrowserProfiles,
	)
	descriptors[0].OutputSchema = BrowserCommandOutputSchema(
		descriptors[0].Name,
		descriptors[0].BrowserProfiles,
	)
	if err = (CapabilityCatalog{Commands: descriptors}).Validate(); err == nil {
		t.Fatal("browser catalog accepted command-specific profile authority")
	}
}

func TestBrowserCatalogRejectsCommandSetWithoutDiagnostics(t *testing.T) {
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{browserProfileDescriptorFixture()})
	if err != nil {
		t.Fatal(err)
	}
	withoutDiagnostics := append([]CommandDescriptor(nil), descriptors[:7]...)
	withoutDiagnostics = append(withoutDiagnostics, descriptors[8])
	if err = (CapabilityCatalog{Commands: withoutDiagnostics}).Validate(); err == nil {
		t.Fatal("browser catalog without diagnostics was accepted")
	}
}

func TestBrowserCatalogRejectsIncompleteSupportedCommandSet(t *testing.T) {
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{browserProfileDescriptorFixture()})
	if err != nil {
		t.Fatal(err)
	}
	withoutCapture := append([]CommandDescriptor(nil), descriptors[:6]...)
	withoutCapture = append(withoutCapture, descriptors[7:]...)
	if err = (CapabilityCatalog{Commands: withoutCapture}).Validate(); err == nil {
		t.Fatal("browser catalog accepted diagnostics while omitting a core command")
	}
}

func TestBrowserCaptureProtocolV2IntegersDecodeExactly(t *testing.T) {
	var input BrowserCaptureInput
	if err := json.Unmarshal([]byte(`{
		"session_id":"session_1","tab_id":"tab_1","snapshot_id":"snapshot_1",
		"snapshot_generation":1,"document_id":"document_1","invocation_id":"capture_1",
		"workspace_id":"workspace_1","route_id":"route_1","browser_target":"companion","target":"page",
		"profile_revision":"managed-v1","browser_policy_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}`), &input); err != nil || input.SnapshotGeneration != 1 || input.BrowserTarget != "companion" {
		t.Fatalf("BrowserCaptureInput = %+v, %v", input, err)
	}
	var descriptor BrowserOutputDescriptor
	if err := json.Unmarshal([]byte(`{
		"transfer_id":"output_1","kind":"screenshot","session_id":"session_1",
		"routed_session_id":"route_1","agent_id":"agent_1","actor_id":"actor_1",
		"workspace_id":"workspace_1","route_id":"route_1","target":"companion","profile_revision":"managed-v1",
		"browser_policy_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"invocation_id":"capture_1","tab_id":"tab_1","document_id":"document_1",
		"snapshot_id":"snapshot_1","snapshot_generation":1,"capture_target":"page",
		"filename":"browser-screenshot.png","content_type":"image/png","size":9,
		"sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"captured_at":1786816223,"expires_at":1786816283,"cleanup_policy":"session_or_expiry"
	}`), &descriptor); err != nil || descriptor.Size != 9 || descriptor.CapturedAt != 1786816223 {
		t.Fatalf("BrowserOutputDescriptor = %+v, %v", descriptor, err)
	}
	if err := json.Unmarshal([]byte(`{"snapshot_generation":1e0}`), &input); err == nil {
		t.Fatal("BrowserCaptureInput accepted a protocol-v1 exponent integer")
	}
}

func TestBrowserSessionOpenProtocolV2ValidatesCanonicalIntegerSpellings(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	catalogHash, err := (CapabilityCatalog{Commands: descriptors}).HashForProtocol(ProtocolV2)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(browserSessionOpenInputFixture(profile.Limits))
	if err != nil {
		t.Fatal(err)
	}
	sessions := fmt.Sprintf(`"sessions":%d`, profile.Limits.Sessions)
	input := json.RawMessage(strings.Replace(string(encoded), sessions, sessions+"e0", 1))
	request := InvocationRequest{
		InvocationID: "browser_open_v2", IdempotencyKey: "browser_open_v2", NodeID: ID("browser_node_v2"),
		CatalogHash: catalogHash, Command: BrowserCommandSessionOpen, Input: input,
		AgentID: "main", SessionID: "session_v2", ActorID: "user_v2",
		TimeoutSeconds: 30, OutputLimitBytes: MaxInvocationOutput,
	}
	plan, err := PrepareExecutionPlanForProtocol(
		ProtocolV2,
		request,
		descriptors[0],
		"local",
		"policy-v2",
		time.Unix(1, 0),
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plan.Input), "e0") {
		t.Fatalf("v2 browser input retained exponent spelling: %s", plan.Input)
	}
}

func TestBrowserDiagnosticsProtocolV2ValidatesCanonicalIntegerSpellings(t *testing.T) {
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{
		browserProfileDescriptorFixture(),
	})
	if err != nil {
		t.Fatal(err)
	}
	output := json.RawMessage(`{
		"session_id":"session_1","tab_id":"tab_1","snapshot_generation":26e0,
		"categories":[{
			"category":"console_errors","count":1e0,"omitted_count":0e0,"truncated":false,
			"entries":[{
				"timestamp":1e0,"severity":"error","origin":"https://example.com","path":"/safe",
				"message_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}]
		}]
	}`)
	canonical, err := ValidateInvocationOutputForProtocol(
		ProtocolV2,
		descriptors[7],
		output,
		MaxInvocationOutput,
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(canonical), "e0") {
		t.Fatalf("v2 browser output retained exponent spelling: %s", canonical)
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

func TestBrowserFileChooserCommandSchemaBindsArtifactMetadata(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.Actions = []string{
		"check",
		"click",
		"dialog",
		"drag",
		"file_chooser",
		"fill",
		"hover",
		"navigate",
		"press",
		"scroll",
		"select",
		"uncheck",
	}
	profile.DryRun = false
	profile.AllowApprovedActions = true
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	rawCatalog, err := json.Marshal(CapabilityCatalog{Commands: descriptors})
	if err != nil {
		t.Fatal(err)
	}
	var roundTripped CapabilityCatalog
	if err = json.Unmarshal(rawCatalog, &roundTripped); err != nil {
		t.Fatal(err)
	}
	if err = roundTripped.Validate(); err != nil {
		t.Fatalf("round-tripped file chooser catalog is invalid: %v", err)
	}
	input := browserActInputFixture()
	input["action"] = map[string]any{
		"kind": "file_chooser", "ref": "host_ref_1",
		"artifact_ref": TransferArtifactRefPrefix + "artifact_1",
	}
	input["effect"] = "local_edit"
	input["expected_role"] = "button"
	input["expected_name"] = "Choose file"
	input["artifact_sha256"] = strings.Repeat("a", 64)
	input["artifact_bytes"] = 7
	input["artifact_filename"] = "photo.jpg"
	input["artifact_content_type"] = "image/jpeg"
	if err = validateDescriptorInvocationInput(descriptors[3], input); err != nil {
		t.Fatalf("file chooser input rejected: %v", err)
	}
	delete(input, "artifact_sha256")
	if err = validateDescriptorInvocationInput(descriptors[3], input); err == nil {
		t.Fatal("file chooser schema accepted missing artifact digest")
	}
	input["artifact_sha256"] = strings.Repeat("a", 64)
	input["approval_digest"] = strings.Repeat("b", 64)
	if err = validateDescriptorInvocationInput(descriptors[3], input); err == nil {
		t.Fatal("file chooser schema accepted approval metadata")
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

func TestBrowserDialogCommandSchemaBindsProtectedPromptAndApproval(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.Actions = []string{"dialog"}
	profile.DryRun = false
	profile.AllowApprovedActions = true
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	act := descriptors[3]
	for _, secret := range []string{"dialog-command-canary", ""} {
		input := BrowserActInput{
			SessionID: "session_1", TabID: "tab_1", SnapshotGeneration: 1,
			ActionInvocationID: "action_dialog_" + strings.Repeat("x", len(secret)%2),
			Action: browser.Action{
				Kind: "dialog", DialogID: "dialog_authority_1", Decision: "accept", PromptProvided: true,
			},
			Effect: "external_commit", CurrentOrigin: "https://example.com",
			PreparedActionHash: strings.Repeat("a", 64), BrowserPolicyRevision: strings.Repeat("b", 64),
			ProfileRevision: profile.Revision, DialogType: "prompt",
			DialogMessageDigest: BrowserDialogMessageDigest("prompt", "Type confirmation"),
			DialogMessageBytes:  len("Type confirmation"),
			InputDigest:         BrowserInputDigest(secret), InputBytes: len(secret),
		}
		input.ApprovalDigest, err = BrowserApprovalDigest(input)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(secret)) && secret != "" || bytes.Contains(raw, []byte(`"value"`)) {
			t.Fatalf("durable dialog input exposed prompt: %s", raw)
		}
		var object map[string]any
		if err = json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		if err = validateDescriptorInvocationInput(act, object); err != nil {
			t.Fatalf("protected dialog input rejected: %v", err)
		}
		delete(object, "input_digest")
		if err = validateDescriptorInvocationInput(act, object); err == nil {
			t.Fatal("prompt dialog without protected digest was accepted")
		}
	}

	dismiss := browserActInputFixture()
	dismiss["action"] = map[string]any{
		"kind": "dialog", "dialog_id": "dialog_authority_2", "decision": "dismiss",
	}
	dismiss["effect"] = "read"
	dismiss["current_origin"] = "https://example.com"
	dismiss["dialog_type"] = "confirm"
	dismiss["dialog_message_digest"] = BrowserDialogMessageDigest("confirm", "Discard?")
	dismiss["dialog_message_bytes"] = len("Discard?")
	if err = validateDescriptorInvocationInput(act, dismiss); err != nil {
		t.Fatalf("dialog dismissal rejected: %v", err)
	}
	dismiss["action"].(map[string]any)["dialog_id"] = ""
	if err = validateDescriptorInvocationInput(act, dismiss); err == nil {
		t.Fatal("dialog without authority was accepted")
	}
}

func TestBrowserActInputMarshalPreservesOnlyDialogZeroByteCount(t *testing.T) {
	for _, test := range []struct {
		kind string
		want bool
	}{
		{kind: "dialog", want: true},
		{kind: "navigate", want: false},
	} {
		raw, err := json.Marshal(BrowserActInput{Action: browser.Action{Kind: browser.ActionKind(test.kind)}})
		if err != nil {
			t.Fatal(err)
		}
		got := bytes.Contains(raw, []byte(`"dialog_message_bytes":0`))
		if got != test.want {
			t.Fatalf("%s dialog byte count presence = %v in %s, want %v", test.kind, got, raw, test.want)
		}
	}
}

func TestBrowserOrdinaryInteractionCommandSchemaBindsSemanticRoleAndEffect(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.Actions = []string{"check", "hover", "uncheck"}
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	act := descriptors[3]
	for _, test := range []struct {
		kind, role, effect string
	}{
		{kind: "check", role: "radio", effect: "local_edit"},
		{kind: "uncheck", role: "checkbox", effect: "local_edit"},
		{kind: "hover", role: "button", effect: "read"},
	} {
		input := browserActInputFixture()
		input["profile_revision"] = profile.Revision
		input["action"] = map[string]any{"kind": test.kind, "ref": "semantic_ref_1"}
		input["effect"] = test.effect
		input["expected_role"] = test.role
		input["expected_name"] = "Control"
		if err = validateDescriptorInvocationInput(act, input); err != nil {
			t.Fatalf("%s input rejected: %v", test.kind, err)
		}
		input["approval_digest"] = strings.Repeat("d", 64)
		if err = validateDescriptorInvocationInput(act, input); err == nil {
			t.Fatalf("%s input accepted unexpected approval", test.kind)
		}
	}

	invalid := browserActInputFixture()
	invalid["profile_revision"] = profile.Revision
	invalid["action"] = map[string]any{"kind": "uncheck", "ref": "semantic_ref_1"}
	invalid["effect"] = "local_edit"
	invalid["expected_role"] = "radio"
	invalid["expected_name"] = "Primary"
	if err = validateDescriptorInvocationInput(act, invalid); err == nil {
		t.Fatal("uncheck schema accepted a radio control")
	}
}

func TestBrowserDragCommandSchemaBindsBothSemanticTargetsAndApproval(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.DryRun = false
	profile.AllowApprovedActions = true
	profile.Actions = []string{"drag"}
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	act := descriptors[3]
	input := browserActInputFixture()
	input["profile_revision"] = profile.Revision
	input["action"] = map[string]any{
		"kind": "drag", "source_ref": "semantic_ref_1", "destination_ref": "semantic_ref_2",
	}
	input["effect"] = "unknown"
	input["expected_role"] = "listitem"
	input["expected_name"] = "Todo"
	input["destination_expected_role"] = "list"
	input["destination_expected_name"] = "Done"
	bindBrowserApprovalDigest(t, input)
	if err = validateDescriptorInvocationInput(act, input); err != nil {
		t.Fatalf("drag input rejected: %v", err)
	}
	input["action"].(map[string]any)["destination_ref"] = "semantic_ref_1"
	if err = validateDescriptorInvocationInput(act, input); err == nil {
		t.Fatal("drag input accepted identical source and destination references")
	}
}

func TestBrowserCommandDescriptorsRejectDragInDryRunProfile(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.DryRun = true
	profile.AllowApprovedActions = false
	profile.Actions = []string{"drag", "navigate"}
	if _, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile}); err == nil {
		t.Fatal("BrowserCommandDescriptors accepted approval-only drag in a dry-run profile")
	}
}

func TestBrowserSessionResultDecodesProtocolV2IntegerTimestamps(t *testing.T) {
	var result BrowserSessionResult
	if err := json.Unmarshal([]byte(`{
		"session_id":"session_1",
		"state":"ready",
		"tab_id":"tab_primary",
		"controller":"agent",
		"features":{"observe":true,"navigate":true,"screenshot":false,"download":false},
		"expires_at":1786223585,
		"idle_expires_at":1786220045
	}`), &result); err != nil {
		t.Fatal(err)
	}
	if result.ExpiresAt != 1786223585 || result.IdleExpiresAt != 1786220045 {
		t.Fatalf("decoded browser session timestamps = %#v", result)
	}

	for _, invalid := range []string{
		`{"session_id":"session_1","state":"ready","expires_at":1.5}`,
		`{"session_id":"session_1","state":"ready","expires_at":-1}`,
		`{"session_id":"session_1","state":"ready","expires_at":1e1}`,
		`{"session_id":"session_1","state":"ready","expires_at":1e100}`,
	} {
		if err := json.Unmarshal([]byte(invalid), &result); err == nil {
			t.Fatalf("BrowserSessionResult accepted invalid timestamp %s", invalid)
		}
	}
}

func TestBrowserPayloadsDecodeProtocolV2SnapshotGenerationsExactly(t *testing.T) {
	var observe BrowserObserveInput
	if err := json.Unmarshal([]byte(`{"session_id":"session_1","tab_id":"tab_primary",`+
		`"snapshot_generation":10,"screenshot":false}`), &observe); err != nil {
		t.Fatal(err)
	}
	var action BrowserActInput
	if err := json.Unmarshal([]byte(`{"session_id":"session_1","tab_id":"tab_primary",`+
		`"snapshot_generation":100,"action_invocation_id":"action_1",`+
		`"action":{"kind":"navigate","url":"https://example.com"},`+
		`"effect":"navigation","current_origin":"about:blank",`+
		`"prepared_action_hash":"`+strings.Repeat("a", 64)+`",`+
		`"browser_policy_revision":"`+strings.Repeat("b", 64)+`",`+
		`"profile_revision":"managed-v1"}`), &action); err != nil {
		t.Fatal(err)
	}
	var observation BrowserObservationResult
	if err := json.Unmarshal([]byte(`{"session_id":"session_1","tab_id":"tab_primary",`+
		`"snapshot_generation":100,"url":"about:blank","origin":"about:blank",`+
		`"snapshot":"","elements":[],"truncated":false}`), &observation); err != nil {
		t.Fatal(err)
	}
	var actionResult BrowserActResult
	if err := json.Unmarshal([]byte(`{"action_invocation_id":"action_1","state":"succeeded",`+
		`"observation":{"session_id":"session_1","tab_id":"tab_primary",`+
		`"snapshot_generation":10,"url":"about:blank","origin":"about:blank",`+
		`"snapshot":"","elements":[],"truncated":false}}`), &actionResult); err != nil {
		t.Fatal(err)
	}
	if observe.SnapshotGeneration != 10 || action.SnapshotGeneration != 100 ||
		observation.SnapshotGeneration != 100 || observation.Elements == nil ||
		actionResult.Observation == nil || actionResult.Observation.SnapshotGeneration != 10 {
		t.Fatalf("decoded generations = observe %d, action %d, observation %#v",
			observe.SnapshotGeneration, action.SnapshotGeneration, observation)
	}

	for _, invalid := range []string{"1.5", "-1", "1e1", "1e100"} {
		data := []byte(`{"session_id":"session_1","tab_id":"tab_primary",` +
			`"snapshot_generation":` + invalid + `,"screenshot":false}`)
		if err := json.Unmarshal(data, &observe); err == nil {
			t.Fatalf("BrowserObserveInput accepted invalid generation %s", invalid)
		}
	}
}

func TestBrowserActContractBindsActionsToProfileRevision(t *testing.T) {
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
	if err = validateDescriptorInvocationInput(act, base); err != nil {
		t.Fatalf("navigate input rejected: %v", err)
	}
	base["action"] = map[string]any{"kind": "download", "ref": "ref_1"}
	if err = validateDescriptorInvocationInput(act, base); err == nil {
		t.Fatal("act contract accepted an action absent from profile authority")
	}
	base["action"] = map[string]any{"kind": "navigate", "url": "https://example.com"}
	base["effect"] = "download"
	if err = validateDescriptorInvocationInput(act, base); err == nil {
		t.Fatal("act contract accepted an effect that did not match the action")
	}
	base["effect"] = "navigation"
	base["future_metadata"] = true
	base["action"].(map[string]any)["future_option"] = "ignored"
	if err = validateDescriptorInvocationInput(act, base); err != nil {
		t.Fatalf("act contract rejected additive fields: %v", err)
	}
	base["action"].(map[string]any)["value"] = "raw driver input"
	if err = validateDescriptorInvocationInput(act, base); err == nil {
		t.Fatal("act contract accepted a known field outside navigate semantics")
	}
}

func TestBrowserActContractStaysCompactAcrossProfiles(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.Actions = []string{
		"check", "click", "dialog", "download", "drag", "file_chooser", "fill",
		"hover", "navigate", "press", "scroll", "select", "uncheck",
	}
	oneProfile := BrowserCommandInputSchema(BrowserCommandAct, []BrowserProfileDescriptor{profile})
	second := profile
	second.Alias = "managed-secondary"
	second.Revision = "managed-v2"
	twoProfiles := BrowserCommandInputSchema(BrowserCommandAct, []BrowserProfileDescriptor{profile, second})

	if len(oneProfile) > 5*1024 {
		t.Fatalf("one-profile browser action schema is %d bytes, want at most 5 KiB", len(oneProfile))
	}
	if growth := len(twoProfiles) - len(oneProfile); growth > 128 {
		t.Fatalf("second equivalent profile grew browser action schema by %d bytes", growth)
	}
	for _, combinator := range [][]byte{[]byte(`"oneOf"`), []byte(`"allOf"`)} {
		if bytes.Contains(oneProfile, combinator) {
			t.Fatalf("browser action schema retained profile/action cross-product %s", combinator)
		}
	}
}

func TestBrowserActContractAcceptsBoundedScrollAndCanonicalAmount(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.Actions = []string{"navigate", "scroll"}
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	input := browserActInputFixture()
	input["action"] = map[string]any{"kind": "scroll", "direction": "down", "amount": 5}
	input["effect"] = "read"
	if err = validateDescriptorInvocationInput(descriptors[3], input); err != nil {
		t.Fatalf("bounded scroll input rejected: %v", err)
	}
	input["action"] = map[string]any{"kind": "scroll", "direction": "down", "amount": 6}
	if err = validateDescriptorInvocationInput(descriptors[3], input); err == nil {
		t.Fatal("scroll amount above the bound was accepted")
	}
	var decoded browser.Action
	if err = json.Unmarshal(
		[]byte(`{"kind":"scroll","direction":"up","amount":1,"future_option":true}`),
		&decoded,
	); err != nil ||
		decoded.Amount != 1 {
		t.Fatalf("canonical scroll action = %#v, %v", decoded, err)
	}
	if err = json.Unmarshal([]byte(`{"kind":"scroll","direction":"up","amount":1.5}`), &decoded); err == nil {
		t.Fatal("fractional scroll amount was accepted")
	}
}

func TestBrowserActContractBindsTypedPressAndSelect(t *testing.T) {
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
	bindBrowserApprovalDigest(t, press)
	if err = validateDescriptorInvocationInput(act, press); err != nil {
		t.Fatalf("typed press input rejected: %v", err)
	}
	press["action"].(map[string]any)["key"] = "Control+L"
	if err = validateDescriptorInvocationInput(act, press); err == nil {
		t.Fatal("press schema accepted a privileged browser-chrome shortcut")
	}
	press["action"].(map[string]any)["key"] = "Tab"
	press["expected_role"] = "button"
	if err = validateDescriptorInvocationInput(act, press); err == nil {
		t.Fatal("document press schema accepted an element semantic binding")
	}
	delete(press, "expected_role")
	delete(press, "approval_digest")
	if err = validateDescriptorInvocationInput(act, press); err == nil {
		t.Fatal("press schema accepted missing approval attestation")
	}

	selection := browserActInputFixture()
	selection["action"] = map[string]any{"kind": "select", "ref": "host_ref_1"}
	selection["effect"] = "local_edit"
	selection["expected_role"] = "combobox"
	selection["expected_name"] = "State"
	selection["input_digest"] = BrowserInputDigest("CA")
	selection["input_bytes"] = 2
	if err = validateDescriptorInvocationInput(act, selection); err != nil {
		t.Fatalf("typed select input rejected: %v", err)
	}
	selection["expected_role"] = "textbox"
	if err = validateDescriptorInvocationInput(act, selection); err == nil {
		t.Fatal("select schema accepted a non-combobox semantic role")
	}
	selection["expected_role"] = "combobox"
	selection["approval_digest"] = strings.Repeat("d", 64)
	if err = validateDescriptorInvocationInput(act, selection); err == nil {
		t.Fatal("select schema accepted an unrelated approval attestation")
	}
}

func TestBrowserDescriptorAcceptsCurrentCapabilitySubset(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.Actions = []string{"navigate"}
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	if err = (CapabilityCatalog{Commands: descriptors}).Validate(); err != nil {
		t.Fatalf("current capability subset rejected: %v", err)
	}
	act := descriptors[3]
	input := browserActInputFixture()
	input["action"] = map[string]any{"kind": "navigate", "url": "https://example.com"}
	if err = validateDescriptorInvocationInput(act, input); err != nil {
		t.Fatalf("advertised navigation rejected: %v", err)
	}
	input["action"] = map[string]any{"kind": "scroll", "direction": "down", "amount": 1}
	if err = validateDescriptorInvocationInput(act, input); err == nil {
		t.Fatal("unadvertised scroll capability accepted")
	}
}

func TestBrowserActContractRequiresApprovalOnlyForApprovalBoundClicks(t *testing.T) {
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{
		browserProfileDescriptorFixture(),
	})
	if err != nil {
		t.Fatal(err)
	}
	act := descriptors[3]
	input := browserActInputFixture()
	input["action"] = map[string]any{"kind": "navigate", "url": "https://example.com"}
	if err = validateDescriptorInvocationInput(act, input); err != nil {
		t.Fatalf("unapproved navigation input rejected: %v", err)
	}

	input["action"] = map[string]any{"kind": "download", "ref": "ref_1"}
	input["effect"] = "unknown"
	input["expected_role"] = "link"
	input["expected_name"] = "Download"
	input["workspace_id"] = "workspace_1"
	input["route_id"] = "route_1"
	input["browser_target"] = "companion"
	if err = validateDescriptorInvocationInput(act, input); err == nil {
		t.Fatal("download input without approval_digest was accepted")
	}
	bindBrowserApprovalDigest(t, input)
	if err = validateDescriptorInvocationInput(act, input); err != nil {
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
	if err = validateDescriptorInvocationInput(act, input); err == nil {
		t.Fatal("click input without approval_digest was accepted")
	}
	bindBrowserApprovalDigest(t, input)
	if err = validateDescriptorInvocationInput(act, input); err != nil {
		t.Fatalf("approved button click rejected: %v", err)
	}
	delete(input, "expected_name")
	bindBrowserApprovalDigest(t, input)
	if err = validateDescriptorInvocationInput(act, input); err != nil {
		t.Fatalf("approved unnamed button click rejected: %v", err)
	}
	input["expected_name"] = "Save"
	input["effect"] = "unknown"
	if err = validateDescriptorInvocationInput(act, input); err == nil {
		t.Fatal("unknown click without matching approval digest was accepted")
	}
	bindBrowserApprovalDigest(t, input)
	if err = validateDescriptorInvocationInput(act, input); err != nil {
		t.Fatalf("approved unknown-effect button click rejected: %v", err)
	}
	for _, effect := range []string{"read", "navigation", "local_edit"} {
		input["effect"] = effect
		delete(input, "approval_digest")
		if err = validateDescriptorInvocationInput(act, input); err != nil {
			t.Fatalf("unapproved %s click rejected: %v", effect, err)
		}
		bindBrowserApprovalDigest(t, input)
		if err = validateDescriptorInvocationInput(act, input); err == nil {
			t.Fatalf("safe %s click accepted an approval digest", effect)
		}
	}
}

func TestBrowserActFullAccessAndModelRequestedApproval(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.CapabilityMode = "full_access"
	profile.ApprovalMode = "model_requested"
	profile.Actions = []string{"click", "fill", "navigate"}
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	act := descriptors[3]
	input := browserActInputFixture()
	input["action"] = map[string]any{"kind": "fill", "ref": "host_ref_1"}
	input["effect"] = "local_edit"
	input["expected_role"] = "spinbutton"
	input["expected_name"] = "Price"
	input["input_digest"] = BrowserInputDigest("0")
	input["input_bytes"] = 1
	if err = validateDescriptorInvocationInput(act, input); err != nil {
		t.Fatalf("full-access price fill rejected: %v", err)
	}

	input = browserActInputFixture()
	input["action"] = map[string]any{"kind": "click", "ref": "host_ref_1"}
	input["effect"] = "external_commit"
	input["expected_role"] = "button"
	input["expected_name"] = "Save"
	if err = validateDescriptorInvocationInput(act, input); err != nil {
		t.Fatalf("autonomous external commit rejected: %v", err)
	}
	input["confirmation"] = "request"
	if err = validateDescriptorInvocationInput(act, input); err == nil {
		t.Fatal("model-requested confirmation without a bound digest was accepted")
	}
	bindBrowserApprovalDigest(t, input)
	if err = validateDescriptorInvocationInput(act, input); err != nil {
		t.Fatalf("confirmed external commit rejected: %v", err)
	}
}

func TestBrowserRestrictedPolicyCommandsBindDecisionRevisionAndApproval(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.CapabilityMode = browserpolicy.CapabilityRestricted
	profile.ApprovalMode = browserpolicy.ApprovalPolicy
	profile.PolicyRevision = strings.Repeat("d", 64)
	profile.DryRun = false
	profile.AllowApprovedActions = true
	profile.Actions = []string{"click", "navigate"}
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	policyInput := map[string]any{
		"profile_revision": "managed-v1", "policy_revision": strings.Repeat("d", 64),
		"action": "click", "effect": "external_commit", "origin": "https://example.com",
		"role": "button", "name": "Save",
	}
	if err = validateDescriptorInvocationInput(descriptors[8], policyInput); err != nil {
		t.Fatalf("restricted policy evaluation input rejected: %v", err)
	}
	policyInput["effect"] = "read"
	if err = validateDescriptorInvocationInput(descriptors[8], policyInput); err == nil {
		t.Fatal("restricted policy evaluation accepted a model-downgraded button effect")
	}
	policyInput["effect"] = "external_commit"
	policyInput["policy_revision"] = strings.Repeat("e", 64)
	if err = validateDescriptorInvocationInput(descriptors[8], policyInput); err == nil {
		t.Fatal("restricted policy evaluation accepted a changed revision")
	}

	act := descriptors[3]
	input := browserActInputFixture()
	input["action"] = map[string]any{"kind": "click", "ref": "host_ref_1"}
	input["effect"] = "read"
	input["current_origin"] = "https://example.com"
	input["expected_role"] = "button"
	input["expected_name"] = "Save"
	input["restricted_decision"] = browserpolicy.DecisionAllow
	input["restricted_policy_revision"] = strings.Repeat("d", 64)
	input["restricted_origin"] = "https://example.com"
	input["policy_effect"] = "external_commit"
	if err = validateDescriptorInvocationInput(act, input); err != nil {
		t.Fatalf("restricted allow action rejected: %v", err)
	}
	input["policy_effect"] = "read"
	if err = validateDescriptorInvocationInput(act, input); err == nil {
		t.Fatal("restricted button click accepted a model-downgraded policy effect")
	}
	input["policy_effect"] = "external_commit"
	input["confirmation"] = browserpolicy.ConfirmationRequest
	if err = validateDescriptorInvocationInput(act, input); err == nil {
		t.Fatal("restricted allow ignored explicit confirmation without approval digest")
	}
	bindBrowserApprovalDigest(t, input)
	if err = validateDescriptorInvocationInput(act, input); err != nil {
		t.Fatalf("restricted explicitly confirmed allow action rejected: %v", err)
	}
	delete(input, "approval_digest")
	delete(input, "confirmation")
	input["restricted_decision"] = browserpolicy.DecisionAsk
	if err = validateDescriptorInvocationInput(act, input); err == nil {
		t.Fatal("restricted ask action without approval digest was accepted")
	}
	bindBrowserApprovalDigest(t, input)
	if err = validateDescriptorInvocationInput(act, input); err != nil {
		t.Fatalf("restricted ask action rejected: %v", err)
	}
	input["restricted_policy_revision"] = strings.Repeat("e", 64)
	bindBrowserApprovalDigest(t, input)
	if err = validateDescriptorInvocationInput(act, input); err == nil {
		t.Fatal("restricted action accepted a changed policy revision")
	}
}

func TestBrowserActSchemaBindsApprovedUploadAlias(t *testing.T) {
	profile := browserProfileDescriptorFixture()
	profile.DryRun = false
	profile.AllowApprovedActions = true
	profile.Actions = []string{"navigate", "upload"}
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{profile})
	if err != nil {
		t.Fatal(err)
	}
	input := browserActInputFixture()
	input["action"] = map[string]any{
		"kind": "upload", "ref": "ref_1",
		"artifact_ref": TransferArtifactRefPrefix + "artifact_1",
	}
	input["effect"] = "unknown"
	input["expected_role"] = "button"
	input["expected_name"] = "Choose file"
	input["artifact_sha256"] = strings.Repeat("c", 64)
	input["artifact_bytes"] = 7
	input["artifact_filename"] = "fixture.txt"
	input["artifact_content_type"] = "text/plain"
	if err = validateDescriptorInvocationInput(descriptors[3], input); err == nil {
		t.Fatal("upload input without approval_digest was accepted")
	}
	bindBrowserApprovalDigest(t, input)
	if err = validateDescriptorInvocationInput(descriptors[3], input); err != nil {
		t.Fatalf("approved upload input rejected: %v", err)
	}
}

func TestBrowserApprovalDigestBindsClickInput(t *testing.T) {
	input := BrowserActInput{
		SessionID: "session_1", TabID: "tab_1", SnapshotGeneration: 7,
		ActionInvocationID: "invocation_1", Action: browser.Action{Kind: browser.ActionClick, Ref: "host_ref_1"},
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
		"kind": "navigate", "url": strings.Repeat("🙂", browser.MaxURLBytes/4),
	}
	if err = validateDescriptorInvocationInput(descriptors[3], input); err != nil {
		t.Fatalf("navigate input at UTF-8 byte ceiling rejected: %v", err)
	}
	input["action"].(map[string]any)["url"] = strings.Repeat("🙂", browser.MaxURLBytes/4+1)
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

func TestBrowserOutputSchemasAcceptOnlyExactProtectedRecoveryReceipts(t *testing.T) {
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{
		browserProfileDescriptorFixture(),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertBrowserOutputValid(t, descriptors[2], map[string]any{"protected_result": true})
	assertBrowserOutputValid(t, descriptors[7], map[string]any{"protected_result": true})
	assertBrowserOutputValid(t, descriptors[4], map[string]any{
		"operation": "select", "protected_result": true,
	})
	for _, invalid := range []struct {
		descriptor CommandDescriptor
		value      map[string]any
	}{
		{descriptors[2], map[string]any{"protected_result": false}},
		{descriptors[2], map[string]any{"protected_result": true, "url": "https://private.example"}},
		{descriptors[7], map[string]any{"protected_result": false}},
		{descriptors[7], map[string]any{"protected_result": true, "count": 1}},
		{descriptors[4], map[string]any{"protected_result": true}},
		{descriptors[4], map[string]any{"operation": "unknown", "protected_result": true}},
		{descriptors[4], map[string]any{
			"operation": "list", "protected_result": true,
			"context_catalog": map[string]any{"context_catalog_id": "private"},
		}},
	} {
		assertBrowserOutputInvalid(t, invalid.descriptor, invalid.value)
	}
}

func TestBrowserDiagnosticsOutputRejectsSensitiveOrInconsistentFields(t *testing.T) {
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{
		browserProfileDescriptorFixture(),
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := map[string]any{
		"timestamp": 1, "severity": "error", "origin": "https://example.com", "path": "/safe",
		"message_hash": strings.Repeat("a", 64),
	}
	result := map[string]any{
		"session_id": "session_1", "tab_id": "tab_1", "snapshot_generation": 26,
		"categories": []any{map[string]any{
			"category": "console_errors", "count": 1, "omitted_count": 0,
			"truncated": false, "entries": []any{entry},
		}},
	}
	assertBrowserOutputValid(t, descriptors[7], result)

	unsafeEntry := maps.Clone(entry)
	unsafeEntry["origin"] = "https://example.com?credential=canary"
	result["categories"] = []any{map[string]any{
		"category": "console_errors", "count": 1, "omitted_count": 0,
		"truncated": false, "entries": []any{unsafeEntry},
	}}
	assertBrowserOutputInvalid(t, descriptors[7], result)

	result["categories"] = []any{map[string]any{
		"category": "console_errors", "count": 2, "omitted_count": 0,
		"truncated": false, "entries": []any{entry},
	}}
	assertBrowserOutputInvalid(t, descriptors[7], result)
}

func TestBrowserDiagnosticsInputRejectsNonCanonicalGenerationAndUnknownFields(t *testing.T) {
	for _, input := range []string{
		`{"session_id":"session_1","tab_id":"tab_1","snapshot_generation":1.5,"categories":["console_errors"]}`,
		`{"session_id":"session_1","tab_id":"tab_1","snapshot_generation":0,"categories":["console_errors"],"raw_console":"secret"}`,
	} {
		var value BrowserDiagnosticsInput
		if err := json.Unmarshal([]byte(input), &value); err == nil {
			t.Fatalf("Unmarshal(%s) accepted malformed diagnostics input", input)
		}
	}
	var value BrowserDiagnosticsInput
	if err := json.Unmarshal([]byte(
		`{"session_id":"session_1","tab_id":"tab_1","snapshot_generation":0,"categories":["console_errors"]}`,
	), &value); err != nil || value.SnapshotGeneration != 0 {
		t.Fatalf("canonical zero-generation input = %+v, %v", value, err)
	}
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
		Mode: "managed", NetworkMode: "any_http",
		CapabilityMode: browserpolicy.CapabilityFullAccess,
		ApprovalMode:   browserpolicy.ApprovalAlwaysCommit,
		DryRun:         true,
		Actions:        []string{"download", "navigate"},
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

func bindBrowserApprovalDigest(t *testing.T, input map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var action BrowserActInput
	if err = json.Unmarshal(encoded, &action); err != nil {
		t.Fatal(err)
	}
	digest, err := BrowserApprovalDigest(action)
	if err != nil {
		t.Fatal(err)
	}
	input["approval_digest"] = digest
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

func TestDecodeBrowserSnapshotPayloadRejectsUntrustedShape(t *testing.T) {
	limits := BrowserLimits{}.Effective()
	invalidUTF8 := append([]byte(`{"snapshot":"page`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`","elements":[]}`)...)
	tests := map[string][]byte{
		"unknown field":      []byte(`{"snapshot":"page","elements":[],"secret":"value"}`),
		"trailing JSON":      []byte(`{"snapshot":"page","elements":[]} {}`),
		"duplicate snapshot": []byte(`{"snapshot":"first","snapshot":"second","elements":[]}`),
		"duplicate element role": []byte(
			`{"snapshot":"page","elements":[{"ref":"ref_1","role":"button","role":"link","name":"Save"}]}`,
		),
		"invalid UTF-8":    invalidUTF8,
		"missing snapshot": []byte(`{"elements":[]}`),
		"missing elements": []byte(`{"snapshot":"page"}`),
		"null elements":    []byte(`{"snapshot":"page","elements":null}`),
		"missing ref":      []byte(`{"snapshot":"page","elements":[{"role":"button","name":"Save"}]}`),
		"null ref":         []byte(`{"snapshot":"page","elements":[{"ref":null,"role":"button","name":"Save"}]}`),
		"missing role":     []byte(`{"snapshot":"page","elements":[{"ref":"ref_1","name":"Save"}]}`),
		"null role":        []byte(`{"snapshot":"page","elements":[{"ref":"ref_1","role":null,"name":"Save"}]}`),
		"missing name":     []byte(`{"snapshot":"page","elements":[{"ref":"ref_1","role":"button"}]}`),
		"null name": []byte(
			`{"snapshot":"page","elements":[{"ref":"ref_1","role":"button","name":null}]}`,
		),
		"duplicate ref": []byte(
			`{"snapshot":"page","elements":[{"ref":"ref_1","role":"button","name":"Save"},` +
				`{"ref":"ref_1","role":"button","name":"Save again"}]}`,
		),
		"oversized snapshot": []byte(
			`{"snapshot":"` + strings.Repeat("x", limits.SnapshotBytes+1) + `","elements":[]}`,
		),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if decoded, err := DecodeBrowserSnapshotPayload(payload, limits); err == nil {
				t.Fatalf("DecodeBrowserSnapshotPayload() accepted %#v", decoded)
			}
		})
	}
}

func TestDecodeBrowserSnapshotPayloadRejectsElement501BeforeMaterializingIt(t *testing.T) {
	limits := BrowserLimits{}.Effective()
	var payload strings.Builder
	payload.WriteString(`{"snapshot":"page","elements":[`)
	for index := 0; index < limits.SnapshotRefs; index++ {
		if index > 0 {
			payload.WriteByte(',')
		}
		fmt.Fprintf(
			&payload,
			`{"ref":"ref_%d","role":"button","name":"Save"}`,
			index,
		)
	}
	// The 501st value is intentionally not an element object. The decoder must
	// reject the count before attempting to decode or allocate this value.
	payload.WriteString(`,"not-an-element"]}`)
	_, err := DecodeBrowserSnapshotPayload([]byte(payload.String()), limits)
	if err == nil || !strings.Contains(err.Error(), "exceeds bounds") {
		t.Fatalf("DecodeBrowserSnapshotPayload() error = %v", err)
	}
}

func TestDecodeBrowserSnapshotPayloadPreservesPresentEmptyElementStrings(t *testing.T) {
	decoded, err := DecodeBrowserSnapshotPayload(
		[]byte(`{"snapshot":"page","elements":[{"ref":"ref_1","role":"","name":""}]}`),
		BrowserLimits{}.Effective(),
	)
	if err != nil || len(decoded.Elements) != 1 || decoded.Elements[0].Role != "" || decoded.Elements[0].Name != "" {
		t.Fatalf("DecodeBrowserSnapshotPayload() = %#v, %v", decoded, err)
	}
}

func TestDecodeBrowserSnapshotPayloadAcceptsPrivateEnvelopeAboveToolResultBudget(t *testing.T) {
	limits := BrowserLimits{}.Effective()
	elements := make([]BrowserElement, 0, limits.SnapshotRefs)
	for index := 0; index < limits.SnapshotRefs; index++ {
		elements = append(elements, BrowserElement{
			Ref:  fmt.Sprintf("ref_%d", index),
			Role: "region",
			Name: strings.Repeat("nested semantic name ", 12),
		})
	}
	want := BrowserSnapshotPayload{
		Snapshot: strings.Repeat("nested semantic snapshot\n", 9000),
		Elements: elements,
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) <= limits.ToolResultBytes {
		t.Fatalf("fixture payload = %d, want above tool result budget %d", len(payload), limits.ToolResultBytes)
	}
	decoded, err := DecodeBrowserSnapshotPayload(payload, limits)
	if err != nil || !reflect.DeepEqual(decoded, want) {
		t.Fatalf("DecodeBrowserSnapshotPayload() = %#v, %v", decoded, err)
	}
}

func TestBrowserContextOutputSchemaAcceptsStreamedSelectionSnapshot(t *testing.T) {
	descriptors, err := BrowserCommandDescriptors([]BrowserProfileDescriptor{browserProfileDescriptorFixture()})
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]any{
		"operation": "select",
		"context_catalog": map[string]any{
			"context_catalog_id": "catalog_1", "context_generation": 2,
			"selected_tab_id": "tab_1", "tabs": []any{map[string]any{
				"tab_id": "tab_1", "kind": "primary", "creation_sequence": 1,
				"document_generation": 2, "url": "https://example.com/", "origin": "https://example.com",
			}},
		},
		"observation": map[string]any{
			"session_id": "session_1", "tab_id": "tab_1", "snapshot_generation": 2,
			"url": "https://example.com/", "origin": "https://example.com",
			"snapshot": "", "elements": []any{}, "truncated": false, "document_id": "document_2",
			"output": map[string]any{
				"transfer_id": "transfer_1", "kind": BrowserOutputSnapshot,
				"session_id": "session_1", "routed_session_id": "routed_1",
				"agent_id": "browser", "actor_id": "actor_1", "workspace_id": "workspace_1",
				"target": "companion", "profile_revision": "managed-v1",
				"browser_policy_revision": strings.Repeat("a", 64), "invocation_id": "invocation_1",
				"tab_id": "tab_1", "document_id": "document_2", "snapshot_generation": 2,
				"filename": "browser-snapshot.json", "content_type": "application/json",
				"size": 128 * 1024, "sha256": strings.Repeat("b", 64),
				"captured_at": 1, "expires_at": 2, "cleanup_policy": "session_or_expiry",
			},
		},
	}
	assertBrowserOutputValid(t, descriptors[4], result)
}
