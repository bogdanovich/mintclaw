package eval

import (
	"context"
	"errors"
	"strings"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

type RestartHistoryEvidence struct {
	Metadata       string   `json:"metadata"`
	MetadataSHA256 string   `json:"metadata_sha256"`
	History        []string `json:"history"`
	HistorySHA256  string   `json:"history_sha256"`
}

type RestartEvidence struct {
	Version          int                      `json:"version"`
	Code             string                   `json:"code"`
	BeforeGeneration int                      `json:"before_generation"`
	AfterGeneration  int                      `json:"after_generation"`
	Before           RestartHistoryEvidence   `json:"before"`
	After            RestartHistoryEvidence   `json:"after"`
	Outbox           []SteeringOutboxEvidence `json:"outbox"`
	OutboxNames      []string                 `json:"outbox_names"`
}

type restartTransport struct {
	conversationTransport
	number  int
	failure error
}

func (transport *restartTransport) generation() int { return transport.number }
func (transport *restartTransport) Close() {
	transport.conversationTransport.Close()
	transport.failure = errors.Join(transport.failure, transport.conversationTransport.Failure())
}

func (transport *restartTransport) Failure() error {
	return errors.Join(transport.failure, transport.conversationTransport.Failure())
}

type restartConversation struct {
	item            Case
	target          admittedTarget
	transport       *restartTransport
	options         gatewayclient.Options
	next            func(context.Context, Trial) (admittedTarget, int, error)
	settlement      finalDeliveryConversation
	before          RestartHistoryEvidence
	firstGeneration int
}

func (domain *restartConversation) initialTask() string { return domain.item.Task }
func (*restartConversation) request(context.Context, Action, Event) (protocol.MintClawMessage, bool, error) {
	return protocol.MintClawMessage{}, false, nil
}

func (domain *restartConversation) observe(ctx context.Context, event Event) error {
	domain.settlement.target = domain.target
	return domain.settlement.observe(ctx, event)
}

func (domain *restartConversation) beforeTurn(ctx context.Context, trial Trial, number int) error {
	if number != 2 || domain.next == nil || len(trial.Events) != 2 || trial.Events[1].Kind != "final" ||
		len(domain.settlement.settled) != 1 {
		return errNativeEvidence
	}
	domain.transport.Close()
	if domain.transport.Failure() != nil {
		return errNativeEvidence
	}
	target, generation, err := domain.next(ctx, trial)
	if err != nil || generation != domain.firstGeneration+1 {
		return errNativeEvidence
	}
	domain.before, err = freezeRestartHistory(ctx, target, trial.SessionKey)
	if err != nil {
		return err
	}
	client, err := gatewayclient.Connect(ctx, target.config, trial.SessionID, domain.options)
	if err != nil {
		return err
	}
	domain.target = target
	domain.transport.conversationTransport, domain.transport.number = client, generation
	return nil
}

func (domain *restartConversation) conclude(_ context.Context, trial *Trial) {
	code := "not_completed"
	if trial.ExecutionVerdict == Pass && len(domain.settlement.settled) == 2 {
		code = "awaiting_stop"
	}
	trial.Restart = &RestartEvidence{
		Version: Version, Code: code, BeforeGeneration: domain.firstGeneration,
		AfterGeneration: domain.transport.number, Before: domain.before,
		After:  RestartHistoryEvidence{History: []string{}},
		Outbox: []SteeringOutboxEvidence{}, OutboxNames: []string{},
	}
}

func freezeRestartHistory(
	ctx context.Context,
	target admittedTarget,
	sessionKey string,
) (RestartHistoryEvidence, error) {
	history, err := readNativeSessionHistory(ctx, target.preset.IsolationRoot,
		target.preset.ExpectedWorkspace, sessionKey, nil, target.config.SensitiveDataReplacer())
	if err != nil {
		return RestartHistoryEvidence{}, err
	}
	return RestartHistoryEvidence{
		Metadata: history.metadata, MetadataSHA256: digest([]byte(history.metadata)),
		History: history.history, HistorySHA256: digest([]byte(strings.Join(history.history, "\n"))),
	}, nil
}

func refreshRestartScope(ctx context.Context, target admittedTarget, trial *Trial) {
	proof := trial.Restart
	proof.Code = "post_stop_evidence_unavailable"
	after, err := freezeRestartHistory(ctx, target, trial.SessionKey)
	if err != nil {
		return
	}
	rows, names, err := readSteeringOutbox(ctx, target)
	if err != nil {
		return
	}
	proof.After, proof.Outbox, proof.OutboxNames, proof.Code = after, rows, names, "observed"
}
