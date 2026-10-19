package browser

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestProtectedFillBindingStagesOnlyExistingNativePreparation(t *testing.T) {
	broker, store, worker, request := protectedFillTestFixture(t, admittedBrowserConfig())
	before := protectedFillTestState(t, store)
	token, err := broker.CreateProtectedFillBinding(t.Context(), request)
	if err != nil || token == "" || len(token) > maxProtectedFillBindingBytes {
		t.Fatal("native value-free binding unavailable", err)
	}
	if !bytes.Equal(before, protectedFillTestState(t, store)) || len(worker.actions) != 0 ||
		len(broker.slots[request.SessionID].inputs) != 0 {
		t.Fatal("binding issuance prepared input or changed durable state")
	}
	secret := "private-user-fill-canary"
	prepared, err := broker.PrepareProtectedFill(t.Context(), request.Owner, token, secret)
	if err != nil || prepared.Action.Effect != EffectLocalEdit || prepared.RequiresApproval ||
		prepared.Action.Action.Value != "" || prepared.Action.InputBytes != len(secret) ||
		len(worker.actions) != 0 {
		t.Fatal("native preparation lost protected local-edit semantics", err)
	}
	if bytes.Contains(protectedFillTestState(t, store), []byte(secret)) || strings.Contains(token, secret) {
		t.Fatal("private answer crossed a durable boundary")
	}
	if broker.slots[request.SessionID].inputs[prepared.Action.ID] != secret {
		t.Fatal("answer was not staged in the existing native input slot")
	}
	duplicate, err := broker.PrepareProtectedFill(t.Context(), request.Owner, token, secret)
	if err != nil || !reflect.DeepEqual(prepared, duplicate) || len(store.prepared) != 1 {
		t.Fatal("duplicate answer created another native preparation", err)
	}
	if _, err = broker.PrepareProtectedFill(
		t.Context(),
		request.Owner,
		token,
		"different-private-answer",
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatal("a different answer replaced the first staged input", err)
	}
	first, err := broker.ExecuteAction(t.Context(), request.Owner, prepared.Action.ID, nil)
	if err != nil || first.State != InvocationSucceeded || len(worker.actions) != 1 ||
		worker.actions[0].Value != secret || worker.actions[0].Target != "e1" {
		t.Fatal("existing native execution did not consume the staged input once", err)
	}
	second, err := broker.ExecuteAction(t.Context(), request.Owner, prepared.Action.ID, nil)
	if err != nil || !reflect.DeepEqual(first, second) || len(worker.actions) != 1 {
		t.Fatal("native terminal replay dispatched the fill again", err)
	}
	if len(broker.slots[request.SessionID].inputs) != 0 ||
		bytes.Contains(protectedFillTestState(t, store), []byte(secret)) {
		t.Fatal("completed native fill retained the private input")
	}
}

func TestProtectedFillBindingRejectsBroadenedOrMalformedIntents(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*PrepareActionRequest)
	}{
		{name: "plaintext", mutate: func(r *PrepareActionRequest) { r.Action.Value = "do-not-retain" }},
		{name: "click", mutate: func(r *PrepareActionRequest) { r.Action.Kind = ActionClick }},
		{name: "select", mutate: func(r *PrepareActionRequest) { r.Action.Kind = ActionSelect }},
		{name: "navigate", mutate: func(r *PrepareActionRequest) { r.Action = Action{Kind: ActionNavigate} }},
		{name: "extra URL", mutate: func(r *PrepareActionRequest) { r.Action.URL = "https://example.com" }},
		{name: "confirmation", mutate: func(r *PrepareActionRequest) { r.Confirmation = "request" }},
		{name: "declared effect", mutate: func(r *PrepareActionRequest) { r.DeclaredEffect = EffectLocalEdit }},
		{name: "upload", mutate: func(r *PrepareActionRequest) { r.Upload = &UploadBinding{} }},
		{name: "missing ref", mutate: func(r *PrepareActionRequest) { r.Action.Ref = "" }},
		{name: "missing request", mutate: func(r *PrepareActionRequest) { r.RequestID = "" }},
		{name: "missing owner", mutate: func(r *PrepareActionRequest) { r.Owner = Owner{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			broker, store, worker, request := protectedFillTestFixture(t, admittedBrowserConfig())
			before := protectedFillTestState(t, store)
			test.mutate(&request)
			if _, err := broker.CreateProtectedFillBinding(t.Context(), request); !errors.Is(err, ErrInvalid) {
				t.Fatal("invalid intent obtained a binding", err)
			}
			if !bytes.Equal(before, protectedFillTestState(t, store)) || len(worker.actions) != 0 {
				t.Fatal("invalid binding request changed native state")
			}
		})
	}
}

func TestProtectedFillBindingRejectsUntrustedTokensAndPrivateInputShapes(t *testing.T) {
	broker, store, worker, request := protectedFillTestFixture(t, admittedBrowserConfig())
	token, err := broker.CreateProtectedFillBinding(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	payload, signature, _ := strings.Cut(token, ".")
	for _, bad := range []string{
		"", "unsigned", strings.Repeat("x", maxProtectedFillBindingBytes+1), token + ".extra",
		payload + "." + strings.Repeat("a", len(signature)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"version":2}`)) + "." + signature,
		"!" + payload + "." + signature,
	} {
		if _, err = broker.PrepareProtectedFill(
			t.Context(),
			request.Owner,
			bad,
			"private-input",
		); !errors.Is(
			err,
			ErrInvalid,
		) {
			t.Fatal("untrusted token staged input", err)
		}
	}
	for _, value := range []string{"", "\xff", strings.Repeat("x", broker.config.Limits.Effective().TextInputBytes+1)} {
		if _, err = broker.PrepareProtectedFill(t.Context(), request.Owner, token, value); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid input shape was staged", err)
		}
	}
	for _, field := range []string{"actor", "agent", "session", "execution"} {
		owner := request.Owner
		switch field {
		case "actor":
			owner.ActorID = "other_actor"
		case "agent":
			owner.AgentID = "other_agent"
		case "session":
			owner.SessionKey = "other_session"
		case "execution":
			owner.ExecutionID = "other_execution"
		}
		if _, err = broker.PrepareProtectedFill(
			t.Context(),
			owner,
			token,
			"private-input",
		); !errors.Is(
			err,
			ErrNotFound,
		) {
			t.Fatal("foreign owner staged input", err)
		}
	}
	if len(store.prepared) != 0 || len(worker.actions) != 0 || len(broker.slots[request.SessionID].inputs) != 0 {
		t.Fatal("untrusted input reached preparation or dispatch")
	}
}

func TestProtectedFillBindingCannotSurviveNativeAuthorityChanges(t *testing.T) {
	for _, change := range []string{"snapshot", "controller", "profile", "policy", "closed", "expired", "restart"} {
		t.Run(change, func(t *testing.T) {
			broker, store, worker, request := protectedFillTestFixture(t, admittedBrowserConfig())
			token, err := broker.CreateProtectedFillBinding(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "snapshot":
				_, err = broker.Observe(t.Context(), request.Owner, request.SessionID, request.TabID)
			case "controller":
				_, err = broker.Handoff(t.Context(), request.Owner, request.SessionID)
				if err == nil {
					_, err = broker.ReleaseHandoff(t.Context(), request.Owner, request.SessionID)
				}
				if err == nil {
					_, err = broker.Resume(t.Context(), request.Owner, request.SessionID)
				}
			case "profile":
				target := broker.config.Targets["gateway"]
				profile := target.Profiles["managed"]
				profile.Revision = "other_profile_revision"
				target.Profiles["managed"] = profile
				broker.config.Targets["gateway"] = target
			case "policy":
				broker.policyRevision = "other_policy_revision"
			case "closed":
				_, err = broker.Close(t.Context(), request.Owner, request.SessionID)
			case "expired":
				binding, openErr := broker.openProtectedFillBinding(token)
				if openErr != nil {
					t.Fatal(openErr)
				}
				broker.now = func() time.Time { return time.Unix(0, binding.ExpiresAt) }
			case "restart":
				broker, err = NewBroker(admittedBrowserConfig(), store, &actionTestFactory{worker: worker})
			}
			if err != nil {
				t.Fatal("native authority control failed", err)
			}
			if _, err = broker.PrepareProtectedFill(
				t.Context(),
				request.Owner,
				token,
				"private-after-change",
			); err == nil ||
				strings.Contains(err.Error(), "private-after-change") {
				t.Fatal("obsolete binding retained authority or echoed input", err)
			}
			if len(store.prepared) != 0 || len(worker.actions) != 0 {
				t.Fatal("obsolete binding reached preparation or dispatch")
			}
		})
	}
}

func TestProtectedFillBindingCancellationNeverStagesInput(t *testing.T) {
	broker, store, worker, request := protectedFillTestFixture(t, admittedBrowserConfig())
	token, err := broker.CreateProtectedFillBinding(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = broker.CreateProtectedFillBinding(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled creation was admitted", err)
	}
	if _, err = broker.PrepareProtectedFill(
		ctx,
		request.Owner,
		token,
		"canceled-private-input",
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal("canceled input was admitted", err)
	}
	if len(store.prepared) != 0 || len(worker.actions) != 0 || len(broker.slots[request.SessionID].inputs) != 0 {
		t.Fatal("canceled input reached preparation or dispatch")
	}
}

func TestProtectedFillBindingRejectsChangedNativeTargetBeforeStaging(t *testing.T) {
	for _, change := range []string{"origin", "element", "role", "reference"} {
		t.Run(change, func(t *testing.T) {
			broker, store, worker, request := protectedFillTestFixture(t, admittedBrowserConfig())
			token, err := broker.CreateProtectedFillBinding(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "origin":
				worker.resolveOrigin = "https://other.example"
			case "element":
				worker.resolveElement.Name = "Another field"
			case "role":
				worker.resolveElement.Role = "button"
			case "reference":
				delete(broker.slots[request.SessionID].refs, request.Action.Ref)
			}
			if _, err = broker.PrepareProtectedFill(
				t.Context(),
				request.Owner,
				token,
				"private-changed-target",
			); !errors.Is(
				err,
				ErrStale,
			) {
				t.Fatal("changed target retained input authority", err)
			}
			if len(store.prepared) != 0 || len(worker.actions) != 0 ||
				len(broker.slots[request.SessionID].inputs) != 0 {
				t.Fatal("changed target received staged input")
			}
		})
	}
}

func TestProtectedFillBindingCannotBeForgedUsingNativeInputDigest(t *testing.T) {
	broker, _, _, request := protectedFillTestFixture(t, admittedBrowserConfig())
	token, err := broker.CreateProtectedFillBinding(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := broker.openProtectedFillBinding(token)
	if err != nil {
		t.Fatal(err)
	}
	binding.Request.RequestID = "forged_request"
	payload, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	request.RequestID = "native_input_digest_oracle"
	request.Action.Value = protectedFillBindingDomain + string(payload)
	native, err := broker.PrepareAction(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	mac, err := hex.DecodeString(native.Action.InputDigest)
	if err != nil {
		t.Fatal(err)
	}
	forged := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac)
	if _, err = broker.PrepareProtectedFill(
		t.Context(),
		request.Owner,
		forged,
		"private-forged-answer",
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatal("native input digest forged a protected binding", err)
	}
	request.RequestID = "native_derived_key_oracle"
	request.Action.Value = protectedFillBindingDomain
	native, err = broker.PrepareAction(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	oracleKey, err := hex.DecodeString(native.Action.InputDigest)
	if err != nil {
		t.Fatal(err)
	}
	attacker := hmac.New(sha256.New, oracleKey)
	_, _ = attacker.Write(payload)
	forged = base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(attacker.Sum(nil))
	_, err = broker.PrepareProtectedFill(t.Context(), request.Owner, forged, "private-forged-answer")
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("native input digest exposed a protected-binding signing key", err)
	}
}

func TestProtectedFillBindingUsesNativeFileStoreWithoutPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "browser.json")
	store, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	broker, worker, session := openActionTestBroker(t, store)
	observed, err := broker.Observe(t.Context(), testOwner(), session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	request := PrepareActionRequest{
		Owner: testOwner(), RequestID: "request_disk_protected_fill", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observed.Snapshot)},
	}
	token, err := broker.CreateProtectedFillBinding(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	secret := "private-disk-fill-canary"
	prepared, err := broker.PrepareProtectedFill(t.Context(), request.Owner, token, secret)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || bytes.Contains(raw, []byte(secret)) || len(worker.actions) != 0 {
		t.Fatal("native disk state retained or dispatched private input", err)
	}
	snapshot, err := InspectStateSnapshot(raw)
	if err != nil || snapshot.PreparedActions[prepared.Action.ID] != prepared.Action || len(snapshot.Invocations) != 1 {
		t.Fatal("staged input did not use the native durable preparation schema", err)
	}
	if _, err = broker.Close(t.Context(), request.Owner, session.ID); err != nil {
		t.Fatal(err)
	}
	if broker.slots[session.ID] != nil || len(worker.actions) != 0 {
		t.Fatal("native close retained or dispatched private input")
	}
}

func TestProtectedFillBindingKeepsNativeRestrictedPolicyAndApproval(t *testing.T) {
	for _, decision := range []string{browserpolicy.DecisionAllow, browserpolicy.DecisionAsk, browserpolicy.DecisionDeny} {
		t.Run(decision, func(t *testing.T) {
			root := restrictedBrowserConfig(browserpolicy.Policy{DefaultDecision: decision})
			broker, store, worker, request := protectedFillTestFixture(t, root)
			token, err := broker.CreateProtectedFillBinding(t.Context(), request)
			if decision == browserpolicy.DecisionDeny {
				if !errors.Is(err, ErrDenied) || len(store.prepared) != 0 || len(worker.actions) != 0 {
					t.Fatal("denied target obtained input authority", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := broker.PrepareProtectedFill(t.Context(), request.Owner, token, "protected-policy-canary")
			if err != nil || prepared.RequiresApproval != (decision == browserpolicy.DecisionAsk) ||
				len(worker.actions) != 0 {
				t.Fatal("binding overrode native restricted policy", err)
			}
			var approval *ApprovalBinding
			if prepared.RequiresApproval {
				if _, err = broker.ExecuteAction(
					t.Context(),
					request.Owner,
					prepared.Action.ID,
					nil,
				); !errors.Is(err, ErrApprovalRequired) ||
					len(worker.actions) != 0 {
					t.Fatal("protected input bypassed native approval", err)
				}
				approval = &prepared.Approval
			}
			invocation, err := broker.ExecuteAction(t.Context(), request.Owner, prepared.Action.ID, approval)
			if err != nil || invocation.State != InvocationSucceeded || len(worker.actions) != 1 {
				t.Fatal("approved native fill did not execute", err)
			}
		})
	}
}

func TestProtectedFillBindingConcurrentAnswersCannotReplaceStagedValue(t *testing.T) {
	broker, store, worker, request := protectedFillTestFixture(t, admittedBrowserConfig())
	token, err := broker.CreateProtectedFillBinding(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errorsByAnswer := make([]error, 2)
	for index, value := range []string{"private-first", "private-second"} {
		group.Go(func() {
			_, errorsByAnswer[index] = broker.PrepareProtectedFill(t.Context(), request.Owner, token, value)
		})
	}
	group.Wait()
	winners, conflicts := 0, 0
	for _, err := range errorsByAnswer {
		if err == nil {
			winners++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		}
	}
	if winners != 1 || conflicts != 1 || len(store.prepared) != 1 ||
		len(broker.slots[request.SessionID].inputs) != 1 || len(worker.actions) != 0 {
		t.Fatal("concurrent answers did not converge on one undispatched native preparation")
	}
	state := protectedFillTestState(t, store)
	if bytes.Contains(state, []byte("private-first")) || bytes.Contains(state, []byte("private-second")) {
		t.Fatal("concurrent input escaped native ephemeral storage")
	}
	if _, err = broker.Close(t.Context(), request.Owner, request.SessionID); err != nil {
		t.Fatal("owned native cleanup failed", err)
	}
	if broker.slots[request.SessionID] != nil {
		t.Fatal("native close retained private input slot")
	}
}

func protectedFillTestFixture(
	t *testing.T, cfg *config.Config,
) (*Broker, *MemoryStore, *actionTestWorker, PrepareActionRequest) {
	t.Helper()
	store := NewMemoryStore()
	broker, worker, session := openActionTestBrokerWithConfig(t, cfg, store)
	observed, err := broker.Observe(t.Context(), testOwner(), session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	return broker, store, worker, PrepareActionRequest{
		Owner: testOwner(), RequestID: "request_protected_fill", SessionID: session.ID, TabID: session.TabID,
		SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
		Action: Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observed.Snapshot)},
	}
}

func protectedFillTestState(t *testing.T, store *MemoryStore) []byte {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	data, err := json.Marshal(StateSnapshot{
		Sessions: store.sessions, PreparedActions: store.prepared, Invocations: store.invocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
