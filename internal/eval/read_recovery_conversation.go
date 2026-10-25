package eval

import (
	"context"
	"strings"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

type ReadRecoveryEvidence struct {
	Version        int                 `json:"version"`
	Code           string              `json:"code"`
	Before         ReadFixtureSnapshot `json:"before"`
	After          ReadFixtureSnapshot `json:"after"`
	Metadata       string              `json:"metadata"`
	MetadataSHA256 string              `json:"metadata_sha256"`
	History        []string            `json:"history"`
	HistorySHA256  string              `json:"history_sha256"`
}

type readRecoveryConversation struct {
	item  Case
	proof *ReadRecoveryEvidence
}

func newReadRecoveryConversation(
	ctx context.Context,
	item Case,
	target admittedTarget,
) (*readRecoveryConversation, error) {
	if item.ReadRecovery == nil || validateReadRecoveryBinding(target.preset, item) != nil {
		return nil, errNativeEvidence
	}
	before := observeWorkspaceReadFixture(ctx, target.preset.ExpectedWorkspace)
	if !readFixtureMatches(before, item.Fixtures[0].SHA256) {
		return nil, errNativeEvidence
	}
	return &readRecoveryConversation{item: item, proof: &ReadRecoveryEvidence{
		Version: Version, Code: "not_completed", Before: before,
		After: ReadFixtureSnapshot{Code: "unavailable"}, History: []string{},
	}}, nil
}

func (domain *readRecoveryConversation) initialTask() string { return domain.item.Task }

func (*readRecoveryConversation) request(context.Context, Action, Event) (protocol.MintClawMessage, bool, error) {
	return protocol.MintClawMessage{}, false, nil
}

func (*readRecoveryConversation) observe(_ context.Context, event Event) error {
	if event.Kind != "final" {
		return errNativeEvidence
	}
	return nil
}

func (domain *readRecoveryConversation) conclude(_ context.Context, trial *Trial) {
	trial.ReadRecovery = domain.proof
	if trial.ExecutionVerdict == Pass {
		domain.proof.Code = "awaiting_stop"
	}
}

func refreshReadRecoveryScope(
	ctx context.Context, target admittedTarget, trial *Trial, facts map[string]string, filter *strings.Replacer,
) {
	if trial.ReadRecovery == nil || trial.ExecutionVerdict != Pass {
		return
	}
	proof := trial.ReadRecovery
	proof.Code = "post_stop_evidence_unavailable"
	proof.After = observeWorkspaceReadFixture(ctx, target.preset.ExpectedWorkspace)
	history, err := readNativeSessionHistory(ctx, target.preset.IsolationRoot,
		target.preset.ExpectedWorkspace, trial.SessionKey, facts, filter)
	if err != nil || proof.After.Code != "observed" {
		return
	}
	proof.Metadata, proof.History = history.metadata, history.history
	proof.MetadataSHA256 = digest([]byte(history.metadata))
	proof.HistorySHA256 = digest([]byte(strings.Join(history.history, "\n")))
	proof.Code = "observed"
}

func readFixtureMatches(snapshot ReadFixtureSnapshot, expected string) bool {
	return snapshot.Code == "observed" && validDigest(expected) && snapshot.SHA256 == expected &&
		len(snapshot.Entries) == 1 && snapshot.Entries[0] == "recovery.txt"
}

func validateReadRecoveryBinding(preset Preset, item Case) error {
	if item.ReadRecovery == nil {
		return nil
	}
	file, pinned := readFixtureInput(preset)
	if !hasReadTool(preset) || !pinned || len(item.Fixtures) != 1 || file.SHA256 != item.Fixtures[0].SHA256 {
		return ErrUnsafeTarget
	}
	return nil
}

func validReadToolWorkspace(ctx context.Context, preset Preset, workspace string) bool {
	if !hasReadTool(preset) {
		return true
	}
	file, pinned := readFixtureInput(preset)
	return pinned && readFixtureMatches(observeWorkspaceReadFixture(ctx, workspace), file.SHA256)
}
