package eval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

type nativeStopConversation struct {
	item   Case
	target admittedTarget
	args   activeToolArgs
	task   string
	proof  *NativeStopEvidence
}

func newNativeStopConversation(item Case, target admittedTarget, sessionID string) (*nativeStopConversation, error) {
	if item.NativeStop == nil || item.Validate() != nil || validateNativeStopBinding(target.preset, item) != nil ||
		!activeToolMatches(target.preset, target.activeTool) || !validID(sessionID) || target.ownedLog == "" {
		return nil, errNativeEvidence
	}
	args := activeToolArgs{TrialID: sessionID, Slot: 1}
	if err := target.activeTool.register(sessionID, sessionID, 1); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, errNativeEvidence
	}
	return &nativeStopConversation{
		item: item, target: target, args: args, task: item.Task + "\nEval tool arguments: " + string(raw),
		proof: &NativeStopEvidence{
			Version: Version, Code: "not_completed", Workspace: target.preset.ExpectedWorkspace,
			Profile: *target.preset.ActiveTool, Log: []string{}, History: []string{},
			Outbox: []SteeringOutboxEvidence{}, OutboxNames: []string{},
		},
	}, nil
}

func (domain *nativeStopConversation) initialTask() string { return domain.task }
func (*nativeStopConversation) request(context.Context, Action, Event) (protocol.MintClawMessage, bool, error) {
	return protocol.MintClawMessage{}, false, nil
}
func (domain *nativeStopConversation) stopRequestID() string { return domain.proof.StopRequestID }

func (domain *nativeStopConversation) afterSend(
	ctx context.Context, initial protocol.MintClawMessage, number int, send func(string) (Event, error),
) error {
	if number != 1 {
		return nil
	}
	domain.proof.PrimaryRequestID, domain.proof.Code = initial.ID, "active_tool_unavailable"
	if err := domain.target.activeTool.waitEntered(ctx, domain.args); err != nil {
		return err
	}
	domain.proof.Code = "enqueue_unavailable"
	input, err := send(nativeStopGuidance(domain.item.NativeStop.QueuedMarker, domain.args.TrialID))
	if err != nil {
		return err
	}
	observed, err := waitActiveToolEnqueue(ctx, domain.target, domain.args, input,
		domain.target.config.SensitiveDataReplacer())
	if err != nil {
		return err
	}
	domain.proof.Guidance = SteeringGuidanceEvidence{
		InputEventID: input.ID, Enqueue: observed.raw, SHA256: digest([]byte(observed.raw)),
		Held: observed.barrier, ObservedAt: observed.observedAt,
	}
	domain.proof.Code = "stop_not_sent"
	stop, err := send("/stop")
	if err != nil {
		return err
	}
	domain.proof.StopRequestID = stop.RequestID
	return nil
}

func (domain *nativeStopConversation) observeStop(ctx context.Context, reply Event) error {
	if reply.RequestID != domain.proof.StopRequestID || reply.AgentID != domain.target.preset.ExpectedAgent ||
		reply.SessionKey == "" || len(reply.TraceScopes) != 0 {
		return errNativeEvidence
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	domain.proof.Code = "native_abort_unavailable"
	for {
		if !activeToolMatches(domain.target.preset, domain.target.activeTool) {
			return errNativeEvidence
		}
		fixture, err := domain.target.activeTool.snapshot(domain.args)
		if err != nil || fixture.State == "released" || fixture.State == "closed" ||
			fixture.Cancellation == "deadline_exceeded" {
			return errNativeEvidence
		}
		records, readErr := readActiveToolLog(ctx, domain.target, nil,
			domain.target.config.SensitiveDataReplacer())
		if errors.Is(readErr, ErrPrivateEvidence) {
			return readErr
		}
		if readErr == nil {
			domain.proof.Fixture = fixture
			domain.proof.Log = stopLogLines(records)
			domain.proof.LogSHA256 = digest([]byte(strings.Join(domain.proof.Log, "\n")))
			if _, valid := inspectNativeStopAbort(domain.proof, reply.AgentID, reply.SessionKey,
				domain.args.TrialID); valid {
				domain.proof.AbortObservedAt = time.Now().UTC()
				domain.proof.Code = "abort_observed"
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errNativeEvidence
		case <-ticker.C:
		}
	}
}

func (*nativeStopConversation) observe(_ context.Context, event Event) error {
	if event.Kind != "stopped" && event.Kind != "final" {
		return errNativeEvidence
	}
	return nil
}

func (domain *nativeStopConversation) conclude(_ context.Context, trial *Trial) {
	trial.NativeStop = domain.proof
	if trial.ExecutionVerdict == Pass && domain.proof.Code == "abort_observed" {
		domain.proof.Code = "awaiting_stop"
	}
}

func stopLogLines(records []activeToolLogObservation) []string {
	lines := make([]string, 0, len(records))
	for _, record := range records {
		lines = append(lines, record.raw)
	}
	return lines
}

func refreshNativeStopScope(
	ctx context.Context, target admittedTarget, trial *Trial, facts map[string]string, filter *strings.Replacer,
) {
	proof := trial.NativeStop
	if proof == nil || proof.Code != "awaiting_stop" || trial.ExecutionVerdict != Pass {
		return
	}
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
	fixture, err := target.activeTool.snapshot(activeToolArgs{TrialID: trial.SessionID, Slot: 1})
	if err != nil || fixture != proof.Fixture {
		return
	}
	proof.Metadata, proof.History = history.metadata, history.history
	proof.MetadataSHA256 = digest([]byte(history.metadata))
	proof.HistorySHA256 = digest([]byte(strings.Join(history.history, "\n")))
	proof.Log = stopLogLines(records)
	proof.LogSHA256 = digest([]byte(strings.Join(proof.Log, "\n")))
	proof.Outbox, proof.OutboxNames = rows, names
	proof.Code = "observed"
}
