package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/events"
)

type activeToolLogEvent struct {
	ID                 string    `json:"event_id"`
	Kind               string    `json:"event_kind"`
	Time               time.Time `json:"event_time"`
	AgentID            string    `json:"agent_id"`
	SessionKey         string    `json:"session_key"`
	TurnID             string    `json:"turn_id"`
	Tool               string    `json:"tool"`
	ToolCallID         string    `json:"tool_call_id"`
	ToolCallGeneration string    `json:"tool_call_generation"`
	MessageHash        string    `json:"message_hash"`
	InterruptKind      string    `json:"interrupt_kind"`
	Role               string    `json:"role"`
	QueueDepth         int       `json:"queue_depth"`
}

type activeToolLogObservation struct {
	raw        string
	event      activeToolLogEvent
	barrier    activeToolObservation
	observedAt time.Time
}

// This observes the exact file owned by the launched child. Raw privacy and
// duplicate-key checks precede projection, including unrelated event records.
func readActiveToolLog(
	ctx context.Context, target admittedTarget, facts map[string]string, filter *strings.Replacer,
) ([]activeToolLogObservation, error) {
	if ctx == nil || ctx.Err() != nil || target.config == nil ||
		!activeToolMatches(target.preset, target.activeTool) || target.ownedLog == "" {
		return nil, errNativeEvidence
	}
	home, err := privateHome(target.preset.IsolationRoot)
	if err != nil || filepath.Dir(target.ownedLog) != home {
		return nil, errNativeEvidence
	}
	name := filepath.Base(target.ownedLog)
	nonce := strings.TrimSuffix(strings.TrimPrefix(name, "eval-gateway-"), ".jsonl")
	id, parseErr := uuid.Parse(nonce)
	if parseErr != nil || id.String() != nonce || id.Version() != 4 ||
		name != "eval-gateway-"+nonce+".jsonl" {
		return nil, errNativeEvidence
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, errNativeEvidence
	}
	defer func() { _ = root.Close() }()
	data, _, err := readCensusFile(root, name, ownedGatewayLogBytes)
	if err != nil {
		return nil, errNativeEvidence
	}
	defer clear(data)
	scan := privateContentScan{facts: facts, filter: filter, tokens: 65536}
	if scan.jsonLines(data) {
		if !scan.uncertain {
			return nil, ErrPrivateEvidence
		}
		return nil, errNativeEvidence
	}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) > 4096 {
		return nil, errNativeEvidence
	}
	observations := make([]activeToolLogObservation, 0)
	for _, line := range lines {
		if ctx.Err() != nil || len(line) > MaxManifestBytes {
			return nil, errNativeEvidence
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if _, err := decodeJSONValue(json.NewDecoder(bytes.NewReader(line)), 0, true); err != nil {
			return nil, errNativeEvidence
		}
		var record activeToolLogEvent
		if json.Unmarshal(line, &record) != nil {
			return nil, errNativeEvidence
		}
		if record.Kind != "" {
			observations = append(observations, activeToolLogObservation{raw: string(line), event: record})
		}
	}
	return observations, nil
}

func waitActiveToolEnqueue(
	ctx context.Context, target admittedTarget, args activeToolArgs, input Event, filter *strings.Replacer,
) (activeToolLogObservation, error) {
	if target.activeTool == nil || input.Kind != "input" || !validID(input.RequestID) ||
		!validID(args.TrialID) || !strings.Contains(input.Content, args.TrialID) {
		return activeToolLogObservation{}, errNativeEvidence
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var redact func(string) string
	if filter != nil {
		redact = filter.Replace
	}
	want := diagnostictrace.FilteredHash(input.Content, redact)
	for {
		active, activeErr := target.activeTool.snapshot(args)
		if activeErr != nil || active.State != "active" || active.ReleaseSequence != 0 {
			return activeToolLogObservation{}, errNativeEvidence
		}
		observations, err := readActiveToolLog(ctx, target, nil, filter)
		if errors.Is(err, ErrPrivateEvidence) {
			return activeToolLogObservation{}, err
		}
		var match activeToolLogObservation
		count := 0
		if err == nil {
			for _, observed := range observations {
				event := observed.event
				if event.Kind == events.KindAgentInterruptReceived.String() && event.MessageHash == want {
					count++
					if event.InterruptKind != "steering" || event.Role != "user" || event.QueueDepth < 1 ||
						event.AgentID != target.preset.ExpectedAgent || !validID(event.TurnID) ||
						!boundedIdentity(event.SessionKey, 512) || !validID(event.ID) || event.Time.IsZero() {
						return activeToolLogObservation{}, errNativeEvidence
					}
					match = observed
				}
			}
			if count == 1 {
				current, err := target.activeTool.snapshot(args)
				if err != nil || current != active {
					return activeToolLogObservation{}, errNativeEvidence
				}
				match.barrier, match.observedAt = current, time.Now().UTC()
				return match, nil
			}
			if count > 1 {
				return activeToolLogObservation{}, errNativeEvidence
			}
		}
		select {
		case <-ctx.Done():
			return activeToolLogObservation{}, errNativeEvidence
		case <-ticker.C:
		}
	}
}
