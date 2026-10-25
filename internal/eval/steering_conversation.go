package eval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

type steeringConversation struct {
	item    Case
	target  admittedTarget
	session string
	args    activeToolArgs
	proof   *SteeringEvidence
	primary Event
	task    string
}

func newSteeringConversation(item Case, target admittedTarget, session string) (*steeringConversation, error) {
	if item.Steering == nil || item.Validate() != nil || target.preset.ActiveTool == nil ||
		!activeToolMatches(target.preset, target.activeTool) || !validID(session) || target.ownedLog == "" {
		return nil, errNativeEvidence
	}
	args := activeToolArgs{TrialID: session, Slot: 1}
	if err := target.activeTool.register(args.TrialID, session, args.Slot); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, errNativeEvidence
	}
	return &steeringConversation{
		item: item, target: target, session: session, args: args,
		task: item.Task + "\nEval tool arguments: " + string(encoded),
		proof: &SteeringEvidence{
			Version: Version, Code: "not_completed", Profile: *target.preset.ActiveTool,
			Guidance: []SteeringGuidanceEvidence{},
		},
	}, nil
}

func (domain *steeringConversation) initialTask() string { return domain.task }

func (*steeringConversation) request(context.Context, Action, Event) (protocol.MintClawMessage, bool, error) {
	return protocol.MintClawMessage{}, false, nil
}

func steeringGuidance(marker, trial string) string {
	return "Correction: the current label is now " + marker +
		". Finish the admitted read, then report this label only. [Eval scope " + trial + "]"
}

func (domain *steeringConversation) afterSend(
	ctx context.Context, request protocol.MintClawMessage, number int, send func(string) (Event, error),
) error {
	if number != 1 {
		return nil
	}
	domain.proof.PrimaryRequestID = request.ID
	domain.proof.Code = "active_tool_unavailable"
	if err := domain.target.activeTool.waitEntered(ctx, domain.args); err != nil {
		return err
	}
	for _, marker := range domain.item.Steering.Updates {
		domain.proof.Code = "guidance_not_sent"
		input, err := send(steeringGuidance(marker, domain.args.TrialID))
		if err != nil {
			return err
		}
		domain.proof.Code = "enqueue_not_observed"
		observed, err := waitActiveToolEnqueue(ctx, domain.target, domain.args, input,
			domain.target.config.SensitiveDataReplacer())
		if err != nil {
			return err
		}
		domain.proof.Guidance = append(domain.proof.Guidance, SteeringGuidanceEvidence{
			InputEventID: input.ID, Enqueue: observed.raw, SHA256: digest([]byte(observed.raw)),
			Held: observed.barrier, ObservedAt: observed.observedAt,
		})
	}
	domain.proof.Code = "release_unavailable"
	return domain.target.activeTool.release(domain.args)
}

func (domain *steeringConversation) observe(_ context.Context, reply Event) error {
	if reply.Kind != "final" || reply.AgentID != domain.target.preset.ExpectedAgent {
		return errNativeEvidence
	}
	if domain.primary.ID == "" {
		if reply.RequestID != domain.proof.PrimaryRequestID || len(reply.TraceScopes) != 1 {
			return errNativeEvidence
		}
		domain.primary = reply
	}
	return nil
}

func (domain *steeringConversation) conclude(ctx context.Context, trial *Trial) {
	trial.Steering = domain.proof
	if trial.ExecutionVerdict != Pass || domain.primary.ID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := domain.capture(ctx, *trial)
		if err == nil {
			domain.proof.Code = "awaiting_stop"
			return
		}
		if errors.Is(err, ErrPrivateEvidence) {
			domain.proof.Code = "private_native_evidence"
			trial.Verdict, trial.Code = Fail, domain.proof.Code
			trial.ExecutionVerdict, trial.ExecutionCode = Fail, domain.proof.Code
			trial.EvidenceEligible = false
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (domain *steeringConversation) capture(ctx context.Context, trial Trial) error {
	domain.proof.Code = "fixture_settlement_unavailable"
	fixture, err := domain.target.activeTool.snapshot(domain.args)
	if err != nil || fixture.State != "released" {
		return errNativeEvidence
	}
	domain.proof.Fixture = fixture
	domain.proof.Code = "history_unavailable"
	filter := domain.target.config.SensitiveDataReplacer()
	history, err := readNativeSessionHistory(ctx, domain.target.preset.IsolationRoot,
		domain.target.preset.ExpectedWorkspace, trial.SessionKey, nil, filter)
	if err != nil {
		return err
	}
	domain.proof.Metadata, domain.proof.History = history.metadata, history.history
	domain.proof.MetadataSHA256 = digest([]byte(history.metadata))
	domain.proof.HistorySHA256 = digest([]byte(strings.Join(history.history, "\n")))
	domain.proof.Code = "trace_unavailable"
	binding := nativeModelBinding{
		TrialID: trial.ID, CaseSHA256: trial.CaseSHA256, AgentID: trial.AgentID,
		SessionKey: trial.SessionKey, TraceScope: domain.primary.TraceScopes[0],
		RequestIDs: []string{domain.proof.PrimaryRequestID},
	}
	reason := visitNativeTraceSnapshotsRaw(ctx, domain.target, []nativeModelBinding{binding}, nil, filter,
		func(_ int, _ diagnostictrace.Trace, sha, raw string) {
			domain.proof.Trace, domain.proof.TraceSHA256 = raw, sha
		})
	if reason == "raw_private_evidence" {
		return ErrPrivateEvidence
	}
	if reason != "" || domain.proof.Trace == "" {
		return errNativeEvidence
	}
	domain.proof.Code = "delivery_unavailable"
	rows, names, err := readSteeringOutbox(ctx, domain.target)
	if err != nil {
		return err
	}
	domain.proof.Outbox, domain.proof.OutboxNames = rows, names
	return nil
}
