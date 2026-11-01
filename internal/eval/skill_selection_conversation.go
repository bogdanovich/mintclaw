package eval

import (
	"context"
	"strings"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

type SkillSelectionEvidence struct {
	Version        int                      `json:"version"`
	Code           string                   `json:"code"`
	Before         ReadFixtureSnapshot      `json:"before"`
	After          ReadFixtureSnapshot      `json:"after"`
	Metadata       string                   `json:"metadata"`
	MetadataSHA256 string                   `json:"metadata_sha256"`
	History        []string                 `json:"history"`
	HistorySHA256  string                   `json:"history_sha256"`
	Log            []string                 `json:"log"`
	LogSHA256      string                   `json:"log_sha256"`
	Outbox         []SteeringOutboxEvidence `json:"outbox"`
	OutboxNames    []string                 `json:"outbox_names"`
}

type skillSelectionConversation struct {
	item   Case
	target admittedTarget
	before ReadFixtureSnapshot
}

func newSkillSelectionConversation(
	ctx context.Context, item Case, target admittedTarget,
) (*skillSelectionConversation, error) {
	if item.SkillSelection == nil || validateSkillSelectionBinding(target.preset, item) != nil ||
		validateSkillSelectionPolicy(target.config, target.preset) != nil || target.ownedLog == "" {
		return nil, errNativeEvidence
	}
	before := observeWorkspaceFixture(ctx, target.preset.ExpectedWorkspace, item.SkillSelection.path())
	if !skillSelectionFixtureMatches(before, item) {
		return nil, errNativeEvidence
	}
	return &skillSelectionConversation{item: item, target: target, before: before}, nil
}

func (domain *skillSelectionConversation) initialTask() string { return domain.item.Task }

func (*skillSelectionConversation) request(context.Context, Action, Event) (protocol.MintClawMessage, bool, error) {
	return protocol.MintClawMessage{}, false, nil
}

func (*skillSelectionConversation) observe(_ context.Context, event Event) error {
	if event.Kind != "final" {
		return errNativeEvidence
	}
	return nil
}

func (domain *skillSelectionConversation) conclude(_ context.Context, trial *Trial) {
	code := "not_completed"
	if trial.ExecutionVerdict == Pass {
		code = "awaiting_stop"
	}
	trial.SkillSelection = &SkillSelectionEvidence{
		Version: Version, Code: code, Before: domain.before,
		After: ReadFixtureSnapshot{Code: "unavailable"}, History: []string{}, Log: []string{},
		Outbox: []SteeringOutboxEvidence{}, OutboxNames: []string{},
	}
}

func refreshSkillSelectionScope(
	ctx context.Context, target admittedTarget, trial *Trial, item Case,
	facts map[string]string, filter *strings.Replacer,
) {
	proof := trial.SkillSelection
	proof.Code = "post_stop_evidence_unavailable"
	history, err := readNativeSessionHistory(ctx, target.preset.IsolationRoot,
		target.preset.ExpectedWorkspace, trial.SessionKey, facts, filter)
	if err != nil {
		return
	}
	records, err := readOwnedToolLog(ctx, target, facts, filter)
	if err != nil {
		return
	}
	rows, names, err := readSteeringOutbox(ctx, target)
	if err != nil {
		return
	}
	proof.After = observeWorkspaceFixture(ctx, target.preset.ExpectedWorkspace, item.SkillSelection.path())
	if proof.After.Code != "observed" {
		return
	}
	proof.Metadata, proof.History = history.metadata, history.history
	proof.MetadataSHA256 = digest([]byte(history.metadata))
	proof.HistorySHA256 = digest([]byte(strings.Join(history.history, "\n")))
	proof.Log = stopLogLines(records)
	proof.LogSHA256 = digest([]byte(strings.Join(proof.Log, "\n")))
	proof.Outbox, proof.OutboxNames, proof.Code = rows, names, "observed"
}

func skillSelectionFixtureMatches(snapshot ReadFixtureSnapshot, item Case) bool {
	return item.SkillSelection != nil && len(item.Fixtures) == 1 && snapshot.Code == "observed" &&
		snapshot.SHA256 == item.Fixtures[0].SHA256 && len(snapshot.Entries) == 1 && snapshot.Entries[0] == "SKILL.md"
}
