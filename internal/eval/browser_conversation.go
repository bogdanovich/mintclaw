package eval

import (
	"context"
	"errors"
	"strings"
	"time"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

var errBrowserEvidence = errors.New("eval: native browser evidence unavailable")

type browserConversation struct {
	fixture   *browserFixture
	questions questionConversation
	item      Case
	target    admittedTarget
	approval  *browserApprovalObservation
	wire      *mediaWireObservation
}

func newBrowserConversation(
	item Case, fixture *browserFixture, target admittedTarget, sessionID string,
) *browserConversation {
	return &browserConversation{
		fixture: fixture, item: item, target: target,
		wire: &mediaWireObservation{workspace: target.preset.ExpectedWorkspace}, questions: questionConversation{
			task: strings.ReplaceAll(item.Task, "{{fixture}}", fixture.origin+fixture.path),
			home: target.preset.IsolationRoot, workspace: target.preset.ExpectedWorkspace,
			sessionID: sessionID, agent: target.preset.ExpectedAgent, started: time.Now().UTC(),
			filter: target.config.SensitiveDataReplacer(),
		},
	}
}

func (domain *browserConversation) initialTask() string { return domain.questions.task }

func (domain *browserConversation) request(
	ctx context.Context, action Action, prompt Event,
) (protocol.MintClawMessage, bool, error) {
	if action.Kind == "cancel" && prompt.Kind == "approval" && domain.item.Browser.Scenario == "denied" {
		return protocol.MintClawMessage{}, false, domain.prepareCancellation(prompt)
	}
	if action.Kind == "allow_once" && prompt.Kind == "approval" && domain.item.Browser.Scenario == "artifact" {
		request, err := domain.approveDownload(prompt)
		return request, true, err
	}
	if action.Kind == "protected_answer" || action.Kind == "confirm" || action.Kind == "allow_once" ||
		prompt.Kind == "approval" {
		return protocol.MintClawMessage{}, false, errBrowserEvidence
	}
	return domain.questions.request(ctx, action, prompt)
}

func (domain *browserConversation) observe(ctx context.Context, prompt Event) error {
	if prompt.Kind == "approval" {
		return domain.observeApproval(ctx, prompt)
	}
	return domain.questions.observe(ctx, prompt)
}

func (domain *browserConversation) conclude(_ context.Context, trial *Trial) {
	trial.EvidenceEligible = false
	if domain.item.Browser.Scenario == "denied" {
		trial.Browser = &BrowserTrialEvidence{Denial: domain.denialEvidence()}
	}
}
