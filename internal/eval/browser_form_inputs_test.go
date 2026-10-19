package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

type browserFillControl struct {
	*browserInputControl
	attempt browserInputAttempt
	current interactions.Record
	state   browser.StateSnapshot
}

func newBrowserFillControl(t *testing.T) *browserFillControl {
	t.Helper()
	c := browserInputControlWithLocalEdits(t, true)
	request, _, err := c.domain.request(t.Context(), Action{Kind: "protected_answer", Fact: "name"}, c.prompt)
	if err != nil || len(c.domain.inputAttempts) != 1 {
		t.Fatal("native input attempt unavailable", err)
	}
	prepared, err := c.broker.PrepareProtectedFill(t.Context(), c.request.Owner,
		c.record.ProtectedAnswer.Token, c.domain.item.Driver.Facts["name"])
	if err != nil || prepared.RequiresApproval {
		t.Fatal("native allowed local fill unavailable", err)
	}
	current, err := c.registry.ClaimProtectedAnswer(c.record.ID, c.record.Revision, interactions.Answer{
		MessageID:  request.ID,
		ReceivedAt: time.Now().UnixMilli(),
		Protected: &interactions.ProtectedAnswerReceipt{
			Reference: "browser_fill." + prepared.Action.ID,
			State:     "prepared",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err = c.registry.MarkResuming(current.ID, current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.broker.ExecuteAction(t.Context(), c.request.Owner, prepared.Action.ID, nil); err != nil {
		t.Fatal(err)
	}
	if c.worker.values["name"] != c.domain.item.Driver.Facts["name"] {
		t.Fatal("native fill did not reach the synthetic worker")
	}
	current, err = c.registry.Resolve(current.ID, current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	data, err := c.domain.questions.read(
		c.domain.questions.workspace,
		"state/browser/browser.json",
		browser.DefaultFileStoreBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(data)
	state, err := browser.InspectStateSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	return &browserFillControl{
		browserInputControl: c,
		attempt:             c.domain.inputAttempts[0],
		current:             current,
		state:               state,
	}
}

func TestBrowserFormInputProofRequiresActualNativeFill(t *testing.T) {
	c := newBrowserFillControl(t)
	proof, err := projectBrowserInputFill(c.attempt, c.current, c.state, time.Now())
	if err != nil || proof.Code != "native_fill_observed" || proof.Fact != "name" ||
		proof.Target != c.attempt.observation.target || proof.PreparedActionID == "" || proof.InvocationID == "" {
		t.Fatal("accepted native field fill was not joined", err)
	}
	data, err := json.Marshal(proof)
	if err != nil || strings.Contains(string(data), c.domain.item.Driver.Facts["name"]) ||
		strings.Contains(string(data), c.record.ProtectedAnswer.Token) || strings.Contains(string(data), "What name") {
		t.Fatal("fill projection exposed a value, token or question")
	}
	// A completed fill may precede the original interaction's final delivery.
	// This is a pre-submit observation, not post-stop settlement authority.
	resuming := c.current
	resuming.Status, resuming.ResolvedAt = interactions.StatusResuming, 0
	if _, err = projectBrowserInputFill(c.attempt, resuming, c.state, time.Now()); err != nil {
		t.Fatal("successful native fill was confused with final delivery settlement", err)
	}
}

func TestBrowserFormInputProofRefusesMissingOrForeignNativeJoins(t *testing.T) {
	original := newBrowserFillControl(t)
	type projectionSnapshot struct {
		Current interactions.Record
		State   browser.StateSnapshot
	}
	frozen, err := json.Marshal(projectionSnapshot{Current: original.current, State: original.state})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{
		"waiting", "claimed-only", "failed", "foreign-message", "foreign-prompt", "foreign-route", "changed-origin",
		"changed-binding", "changed-question", "replayed-revision", "late-answer", "plaintext", "receipt", "receipt-state",
		"preparation", "wrong-owner", "wrong-request", "wrong-ref", "wrong-snapshot", "wrong-role", "wrong-position",
		"wrong-catalog", "model-value", "before-send", "missing-session", "session-owner", "profile", "missing-invocation", "duplicate-invocation", "not-executed", "unknown",
		"trace-source", "trace-hash", "trace-turn", "generation",
	} {
		t.Run(mode, func(t *testing.T) {
			var snapshot projectionSnapshot
			if err := json.Unmarshal(frozen, &snapshot); err != nil {
				t.Fatal(err)
			}
			c := *original
			current, state := snapshot.Current, snapshot.State
			preparedID := ""
			for id, action := range state.PreparedActions {
				if action.Action.Kind == browser.ActionFill {
					preparedID = id
				}
			}
			action := state.PreparedActions[preparedID]
			switch mode {
			case "trace-source":
				c.attempt.observation.source.Commit = "unknown"
			case "trace-hash":
				c.attempt.observation.traceSHA256 = ""
			case "trace-turn":
				c.attempt.observation.traceScope.TurnID = "foreign-turn"
			case "generation":
				c.attempt.observation.generation = ""
			case "waiting":
				current.Status = interactions.StatusWaiting
			case "claimed-only":
				current.Status = interactions.StatusClaimed
			case "failed":
				current.Status = interactions.StatusFailed
			case "foreign-message":
				current.Answer.MessageID = "another-input"
			case "foreign-prompt":
				current.Answer.ResponseMessageID = "another-prompt"
			case "foreign-route":
				current.Route.SessionKey = "another-route"
			case "changed-origin":
				current.Origin.ToolCallID = "another-call"
			case "changed-binding":
				binding := *current.ProtectedAnswer
				binding.Token += "changed"
				current.ProtectedAnswer = &binding
			case "changed-question":
				current.Questions = nil
			case "replayed-revision":
				current.Revision = c.record.Revision
			case "late-answer":
				current.Answer.ReceivedAt = current.ExpiresAt
			case "plaintext":
				current.Answer.Text = "unprotected"
			case "receipt":
				current.Answer.Protected.Reference = "browser_fill.prepared_foreign"
			case "receipt-state":
				current.Answer.Protected.State = "collected"
			case "preparation":
				delete(state.PreparedActions, preparedID)
			case "wrong-owner":
				action.Owner.ExecutionID = "another-execution"
			case "wrong-request":
				action.RequestID = "another-request"
			case "wrong-ref":
				action.Action.Ref = "another-ref"
			case "wrong-snapshot":
				action.SnapshotGeneration++
			case "wrong-role":
				action.ElementRole = "combobox"
			case "wrong-position":
				action.ElementPosition++
			case "wrong-catalog":
				action.CatalogRevision = strings.Repeat("f", 64)
			case "model-value":
				action.Action.Value = "model supplied"
			case "before-send":
				action.CreatedAt = c.attempt.attemptedAt.Add(-time.Second).UnixNano()
			case "missing-session":
				delete(state.Sessions, action.SessionID)
			case "session-owner", "profile":
				session := state.Sessions[action.SessionID]
				if mode == "session-owner" {
					session.Owner.ExecutionID = "foreign-execution"
				} else {
					session.ProfileRevision = "foreign-profile"
				}
				state.Sessions[action.SessionID] = session
			case "missing-invocation":
				for id, inv := range state.Invocations {
					if inv.PreparedActionID == preparedID {
						delete(state.Invocations, id)
					}
				}
			case "duplicate-invocation", "not-executed", "unknown":
				for id, inv := range state.Invocations {
					if inv.PreparedActionID != preparedID {
						continue
					}
					if mode == "duplicate-invocation" {
						inv.ID = "duplicate-invocation"
						state.Invocations[inv.ID] = inv
						break
					}
					if mode == "not-executed" {
						inv.State = browser.InvocationPrepared
						inv.AcceptedAt = 0
					} else {
						inv.State = browser.InvocationUnknown
					}
					state.Invocations[id] = inv
				}
			}
			if mode != "preparation" {
				state.PreparedActions[preparedID] = action
			}
			if proof, err := projectBrowserInputFill(
				c.attempt,
				current,
				state,
				time.Now(),
			); !errors.Is(err, errBrowserEvidence) ||
				proof != nil {
				t.Fatal("incomplete or foreign native fill was promoted", mode, err)
			}
		})
	}
}

func TestBrowserFormInputCheckpointScansOriginalNativeBytes(t *testing.T) {
	for _, surface := range []string{"registry", "browser"} {
		t.Run(surface, func(t *testing.T) {
			c := newBrowserFillControl(t)
			name := interactions.WorkspaceStorePath(c.domain.questions.workspace)
			if surface == "browser" {
				name = filepath.Join(c.domain.questions.workspace, "state/browser/browser.json")
			}
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			private := c.domain.item.Driver.Facts["name"]
			fields["opaque_private"] = json.RawMessage(`"` + strings.ReplaceAll(private, "-", `\u002d`) + `"`)
			data, err = json.Marshal(fields)
			if err != nil || os.WriteFile(name, data, 0o600) != nil {
				t.Fatal("raw privacy control unavailable", err)
			}
			proof, err := c.domain.readFormInputs()
			if !errors.Is(err, ErrPrivateEvidence) || proof.Code == "native_inputs_observed" || len(proof.Fills) != 0 {
				t.Fatal("private original evidence was discarded through native decoding", err)
			}
			trial := Trial{Verdict: Pass, ExecutionVerdict: Pass}
			c.domain.conclude(t.Context(), &trial)
			if trial.Verdict != Fail || trial.ExecutionVerdict != Fail || trial.EvidenceEligible {
				t.Fatal("private native checkpoint did not dominate the execution result")
			}
		})
	}
}

func TestBrowserFormInputCheckpointIsNotSubmissionOrCleanupProof(t *testing.T) {
	c := newBrowserFillControl(t)
	proof, err := c.domain.readFormInputs()
	if err != nil || proof.Code != "native_inputs_observed" || len(proof.Fills) != 1 ||
		!validDigest(proof.StateSHA256) || !validDigest(proof.RegistrySHA256) {
		t.Fatal("native batch input checkpoint was unavailable", err)
	}
	trial := Trial{ExecutionVerdict: Pass, Verdict: Pass}
	c.domain.conclude(t.Context(), &trial)
	if trial.EvidenceEligible || trial.Browser == nil || trial.Browser.FormInputs.Code != "native_inputs_observed" ||
		trial.Browser.NativeAuthority == "observed" || trial.Browser.NativeCleanup == "observed" {
		t.Fatal("input checkpoint promoted submission, permission, cleanup or judge eligibility")
	}
	data, err := json.Marshal(trial)
	if err != nil || strings.Contains(string(data), c.domain.item.Driver.Facts["name"]) {
		t.Fatal("private field value reached a checkpoint report")
	}
}
