package eval

import (
	"encoding/json"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/events"
)

type compactionLogEvent struct {
	activeToolLogEvent
	AttemptID   string `json:"attempt_id"`
	Revision    uint64 `json:"transcript_revision"`
	Count       int    `json:"transcript_count"`
	Reason      string `json:"reason"`
	Background  bool   `json:"background"`
	TokensSaved int    `json:"tokens_saved"`
}

// A bounded native start/end pair must include the correction and filler in
// its canonical frontier. This observes compaction, not its internal algorithm.
func compactionBarrier(lines []string, agent, key string) (compactionLogEvent, bool) {
	var empty compactionLogEvent
	if len(lines) == 0 || len(lines) > 4096 || len(strings.Join(lines, "\n")) > 2*1024*1024 {
		return empty, false
	}
	starts, ends := map[string]compactionLogEvent{}, map[string]compactionLogEvent{}
	owners, seen := map[string]string{}, map[string]bool{}
	for _, raw := range lines {
		base, valid := decodeStopLog(raw)
		if !valid || seen[base.ID] {
			return empty, false
		}
		seen[base.ID] = true
		if base.Kind != events.KindAgentContextCompressStart.String() &&
			base.Kind != events.KindAgentContextCompressEnd.String() &&
			base.Kind != events.KindAgentContextCompressProgress.String() {
			continue
		}
		var event compactionLogEvent
		if json.Unmarshal([]byte(raw), &event) != nil || !validID(event.AttemptID) {
			return empty, false
		}
		owner := event.AgentID + "\x00" + event.SessionKey
		if previous, exists := owners[event.AttemptID]; exists && previous != owner {
			return empty, false
		}
		owners[event.AttemptID] = owner
		if event.AgentID != agent || event.SessionKey != key {
			continue
		}
		if event.Kind == events.KindAgentContextCompressStart.String() {
			if _, exists := starts[event.AttemptID]; exists {
				return empty, false
			}
			starts[event.AttemptID] = event
		} else if event.Kind == events.KindAgentContextCompressEnd.String() {
			if _, exists := ends[event.AttemptID]; exists {
				return empty, false
			}
			start, exists := starts[event.AttemptID]
			if !exists || start.Time.After(event.Time) || start.Count != event.Count ||
				start.Revision != event.Revision ||
				start.Reason != event.Reason ||
				start.Background != event.Background ||
				start.Status != "started" {
				return empty, false
			}
			ends[event.AttemptID] = event
		}
	}
	for id, end := range ends {
		if _, exists := starts[id]; exists && end.Status == "completed" && end.TokensSaved > 0 &&
			end.Count == 8 && end.Revision == 8 && end.Reason == "proactive_budget" && end.Background {
			return end, true
		}
	}
	return empty, false
}

func compactionLogCode(proof *CompactionEvidence, trial Trial) string {
	if proof.BarrierSHA256 != digest([]byte(strings.Join(proof.Barrier, "\n"))) ||
		proof.LogSHA256 != digest([]byte(strings.Join(proof.Log, "\n"))) || len(proof.Log) < len(proof.Barrier) ||
		len(proof.Log) > 4096 || len(strings.Join(proof.Log, "\n")) > 2*1024*1024 {
		return "compaction_log_invalid"
	}
	for index, raw := range proof.Barrier {
		if proof.Log[index] != raw {
			return "compaction_barrier_changed"
		}
	}
	if _, valid := compactionBarrier(proof.Barrier, trial.AgentID, trial.SessionKey); !valid {
		return "compaction_receipt_unavailable"
	}
	starts, ends, seen := map[string]int{}, map[string]int{}, map[string]bool{}
	for position, raw := range proof.Log {
		event, valid := decodeStopLog(raw)
		if !valid || seen[event.ID] {
			return "compaction_log_invalid"
		}
		seen[event.ID] = true
		if event.Kind != events.KindAgentTurnStart.String() && event.Kind != events.KindAgentTurnEnd.String() {
			continue
		}
		index := -1
		for number := range 5 {
			if event.MessageID == trial.Events[2*number].RequestID {
				index = number
			}
		}
		if event.SessionKey != trial.SessionKey && index < 0 {
			continue
		}
		if index < 0 || event.AgentID != trial.AgentID || event.SessionKey != trial.SessionKey ||
			event.TurnID != trial.Events[2*index+1].TraceScopes[0].TurnID {
			return "compaction_turn_binding"
		}
		if event.Kind == events.KindAgentTurnStart.String() {
			starts[event.MessageID]++
			if index == 4 && position < len(proof.Barrier) || index < 4 && position >= len(proof.Barrier) {
				return "compaction_not_before_recall"
			}
		} else {
			ends[event.MessageID]++
			if event.Status != "completed" || event.LLMCalls != 1 {
				return "compaction_turn_not_completed"
			}
		}
	}
	for number := range 5 {
		request := trial.Events[2*number].RequestID
		if starts[request] != 1 || ends[request] != 1 {
			return "compaction_turn_missing"
		}
	}
	return ""
}
