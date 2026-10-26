package eval

import (
	"context"
	"errors"
	"strings"
	"time"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

type CompactionEvidence struct {
	Version        int                      `json:"version"`
	Code           string                   `json:"code"`
	Profile        CompactionProfile        `json:"profile"`
	Barrier        []string                 `json:"barrier"`
	BarrierSHA256  string                   `json:"barrier_sha256"`
	Log            []string                 `json:"log"`
	LogSHA256      string                   `json:"log_sha256"`
	Metadata       string                   `json:"metadata"`
	MetadataSHA256 string                   `json:"metadata_sha256"`
	History        []string                 `json:"history"`
	HistorySHA256  string                   `json:"history_sha256"`
	Outbox         []SteeringOutboxEvidence `json:"outbox"`
	OutboxNames    []string                 `json:"outbox_names"`
}

type compactionConversation struct {
	item    Case
	target  admittedTarget
	session string
	finals  []Event
	barrier []string
}

func (domain *compactionConversation) initialTask() string { return domain.item.Task }
func (*compactionConversation) request(context.Context, Action, Event) (protocol.MintClawMessage, bool, error) {
	return protocol.MintClawMessage{}, false, nil
}

func (domain *compactionConversation) observe(ctx context.Context, event Event) error {
	if event.Kind != "final" || len(domain.finals) >= 5 {
		return errNativeEvidence
	}
	domain.finals = append(domain.finals, event)
	trial := Trial{
		AgentID:    domain.target.preset.ExpectedAgent,
		SessionID:  domain.session,
		SessionKey: event.SessionKey,
		Events:     domain.finals,
	}
	_, err := waitFinalDeliverySettlement(ctx, domain.target, trial, event, len(domain.finals))
	return err
}

func (domain *compactionConversation) beforeTurn(ctx context.Context, trial Trial, number int) error {
	if number != 5 {
		return nil
	}
	if len(trial.Events) != 8 || len(domain.finals) != 4 {
		return errNativeEvidence
	}
	barrierCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		records, err := readOwnedToolLog(barrierCtx, domain.target, nil, domain.target.config.SensitiveDataReplacer())
		if errors.Is(err, ErrPrivateEvidence) {
			return err
		}
		if err == nil {
			lines := stopLogLines(records)
			if _, valid := compactionBarrier(lines, trial.AgentID, trial.SessionKey); valid {
				domain.barrier = lines
				return nil
			}
		}
		select {
		case <-barrierCtx.Done():
			return errNativeEvidence
		case <-ticker.C:
		}
	}
}

func (domain *compactionConversation) conclude(_ context.Context, trial *Trial) {
	code := "not_completed"
	if trial.ExecutionVerdict == Pass && len(domain.barrier) > 0 && len(domain.finals) == 5 {
		code = "awaiting_stop"
	}
	barrier := append([]string{}, domain.barrier...)
	trial.Compaction = &CompactionEvidence{
		Version: Version, Code: code, Profile: *domain.target.preset.Compaction,
		Barrier: barrier, BarrierSHA256: digest([]byte(strings.Join(barrier, "\n"))),
		Log: []string{}, History: []string{}, Outbox: []SteeringOutboxEvidence{}, OutboxNames: []string{},
	}
}

func refreshCompactionScope(
	ctx context.Context,
	target admittedTarget,
	trial *Trial,
	facts map[string]string,
	filter *strings.Replacer,
) {
	proof := trial.Compaction
	proof.Code = "post_stop_evidence_unavailable"
	history, err := readNativeSessionHistory(
		ctx,
		target.preset.IsolationRoot,
		target.preset.ExpectedWorkspace,
		trial.SessionKey,
		facts,
		filter,
	)
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
	proof.Metadata, proof.History = history.metadata, history.history
	proof.MetadataSHA256 = digest([]byte(history.metadata))
	proof.HistorySHA256 = digest([]byte(strings.Join(history.history, "\n")))
	proof.Log = stopLogLines(records)
	proof.LogSHA256 = digest([]byte(strings.Join(proof.Log, "\n")))
	proof.Outbox, proof.OutboxNames, proof.Code = rows, names, "observed"
}
