package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type credentialWaitToolFixture struct {
	credentialBrowserToolFixture
	state toolshared.ExternalWaitState
	owner browser.Owner
}

func (source *credentialWaitToolFixture) CredentialApprovalState(
	_ context.Context,
	owner browser.Owner,
	_ string,
) toolshared.ExternalWaitState {
	source.owner = owner
	return source.state
}

func TestBrowserLoginApprovalUsesPrivateDurableWaitControl(t *testing.T) {
	for _, step := range []string{"credentials", "authorize"} {
		t.Run(step, func(t *testing.T) { testBrowserLoginApprovalWait(t, step) })
	}
}

func testBrowserLoginApprovalWait(t *testing.T, step string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Hour)
	reference := strings.Repeat("a", 64)
	source := &credentialWaitToolFixture{credentialBrowserToolFixture: credentialBrowserToolFixture{
		credentialAvailable: true, result: browser.CredentialLoginResult{
			Alias: "vault-logins", Outcome: "approval_required",
			ApprovalWait: &browser.CredentialApprovalWait{Reference: reference, Deadline: deadline},
		},
	}, state: toolshared.ExternalWaitReady}
	tool := NewBrowserLoginTool(browserToolTestConfig(), source)
	ctx := browserToolTestContext()
	result := tool.Execute(ctx, websiteStepArguments(step))
	if result.Control.Suspension == nil || result.Control.Suspension.Kind != interactions.KindExternalWait ||
		result.Control.Suspension.ConditionRef != reference || result.Control.Suspension.Deadline != deadline || len(result.WriteAudit) != 0 {
		t.Fatal("approval did not transfer safe durable control")
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), reference) || strings.Contains(string(encoded), "deadline") {
		t.Fatal("wait control reached serialized output")
	}
	if tool.ResolveExternalWait(ctx, reference) != toolshared.ExternalWaitReady {
		t.Fatal("trusted source was not resolved")
	}
	owner, err := browserOwnerFromContext(ctx)
	if err != nil || source.owner != owner {
		t.Fatal("wait resolver lost original browser owner")
	}
}

func TestBrowserLoginCannotTurnOtherOutcomesIntoApprovalWaits(t *testing.T) {
	for _, outcome := range []string{"submitted", "outcome_unknown", "denied", "handoff_required"} {
		source := &credentialWaitToolFixture{credentialBrowserToolFixture: credentialBrowserToolFixture{
			credentialAvailable: true, result: browser.CredentialLoginResult{
				Outcome: outcome,
				ApprovalWait: &browser.CredentialApprovalWait{
					Reference: strings.Repeat("a", 64),
					Deadline:  time.Now().Add(time.Hour),
				},
			},
		}}
		result := NewBrowserLoginTool(
			browserToolTestConfig(),
			source,
		).Execute(browserToolTestContext(), websiteStepArguments("credentials"))
		if result.Control.Suspension != nil {
			t.Fatalf("%s became an automatic wait/retry", outcome)
		}
	}
}
