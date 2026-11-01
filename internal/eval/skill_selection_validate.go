package eval

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/skills"
)

type skillSelectionLogEvent struct {
	activeToolLogEvent
	SkillVersion int    `json:"skill_context_version"`
	SkillCount   int    `json:"skill_context_count"`
	SkillCode    string `json:"skill_context_code"`
	SkillSHA256  string `json:"skill_context_sha256"`
}

func expectedSkillContextHash(item Case, workspace string, selected bool) string {
	snapshots := []agent.SkillContextSnapshot{}
	if selected {
		snapshots = append(snapshots, agent.SkillContextSnapshot{
			Sequence: 1, Trigger: "initial_build", SkillNames: []string{item.SkillSelection.Name},
			Selections: []agent.SkillRevisionIdentity{{
				Name: item.SkillSelection.Name, Path: filepath.Join(workspace, item.SkillSelection.path()),
				Scope: string(skills.SkillScopeWorkspace), Revision: "sha256:" + item.Fixtures[0].SHA256,
			}},
		})
	}
	data, err := json.Marshal(snapshots)
	if err != nil {
		return ""
	}
	return digest(data)
}

func skillSelectionEvidenceCode(trial Trial, item Case, workspace string) string {
	proof := trial.SkillSelection
	if proof == nil || item.SkillSelection == nil || item.Validate() != nil || proof.Version != Version ||
		proof.Code != "observed" || !skillSelectionFixtureMatches(proof.Before, item) ||
		!skillSelectionFixtureMatches(proof.After, item) ||
		proof.MetadataSHA256 != digest([]byte(proof.Metadata)) || len(proof.Metadata) > MaxManifestBytes ||
		proof.HistorySHA256 != digest([]byte(strings.Join(proof.History, "\n"))) ||
		len(strings.Join(proof.History, "\n")) > 2*1024*1024 ||
		len(trial.Events) != 6 || !validTrialConversation(trial, trial.AgentID, workspace) ||
		!validCoreDeliveryCensus(proof.Outbox, proof.OutboxNames, trial, workspace, 3) {
		return "skill_selection_proof_invalid"
	}
	snapshot, err := memory.InspectJSONLSnapshot(context.Background(), trial.SessionKey,
		[]byte(proof.Metadata), []byte(strings.Join(proof.History, "\n")))
	if err != nil || snapshot.Metadata.Skip != 0 || snapshot.Metadata.Summary != "" ||
		snapshot.Metadata.Count != 6 || len(snapshot.History) != 6 ||
		!slices.Equal(snapshot.Metadata.ClientSessionIDs, []string{trial.SessionID}) {
		return "skill_selection_history_invalid"
	}
	scope, err := session.DecodeCurrentSessionScope(snapshot.Metadata.Scope)
	if err != nil || scope.AgentID != trial.AgentID || scope.Channel != "mintclaw" ||
		scope.ClientSessionID != trial.SessionID || session.BuildSessionKey(*scope) != trial.SessionKey {
		return "skill_selection_history_scope"
	}
	seenTurns := map[string]bool{}
	for index := range 3 {
		input, final := trial.Events[2*index], trial.Events[2*index+1]
		wantedInput, wantedOutput := item.Task, item.SkillSelection.Value
		if index == 1 {
			wantedInput, wantedOutput = item.Driver.Steps[0].Action.Text, item.SkillSelection.Output
		}
		if input.Kind != "input" || input.Turn != index+1 || input.Content != wantedInput ||
			final.Kind != "final" || final.RequestID != input.RequestID || final.Turn != index+1 ||
			len(final.TraceScopes) != 1 || !final.TraceScopes[0].Complete() ||
			final.TraceScopes[0].Workspace != workspace || seenTurns[final.TraceScopes[0].TurnID] {
			return "skill_selection_turn_binding"
		}
		seenTurns[final.TraceScopes[0].TurnID] = true
		user, assistant := snapshot.History[2*index], snapshot.History[2*index+1]
		if user.Role != "user" || user.Content != item.Task || len(user.ToolCalls) != 0 || user.ToolCallID != "" ||
			assistant.Role != "assistant" || len(assistant.ToolCalls) != 0 || assistant.ToolCallID != "" ||
			strings.TrimSpace(
				assistant.Content,
			) != wantedOutput || !strings.HasPrefix(final.Content, assistant.Content) {
			return "skill_selection_behavior_invalid"
		}
		content, found := coreFinalDeliveryContent(proof.Outbox, final.RequestID)
		if !found || content != assistant.Content {
			return "skill_selection_delivery_mismatch"
		}
	}
	return skillSelectionLogCode(proof, trial, item, workspace)
}

func skillSelectionLogCode(proof *SkillSelectionEvidence, trial Trial, item Case, workspace string) string {
	if len(proof.Log) == 0 || len(proof.Log) > 4096 ||
		proof.LogSHA256 != digest([]byte(strings.Join(proof.Log, "\n"))) ||
		len(strings.Join(proof.Log, "\n")) > 2*1024*1024 {
		return "skill_selection_log_unavailable"
	}
	matched := map[string]bool{}
	for _, raw := range proof.Log {
		base, valid := decodeStopLog(raw)
		if !valid {
			return "skill_selection_log_invalid"
		}
		if base.Kind != "agent.turn.end" {
			continue
		}
		index := -1
		for number := range 3 {
			if base.MessageID == trial.Events[2*number].RequestID {
				index = number
			}
		}
		if base.SessionKey != trial.SessionKey && index < 0 {
			continue
		}
		if index < 0 || base.SessionKey != trial.SessionKey || base.AgentID != trial.AgentID ||
			base.TurnID != trial.Events[2*index+1].TraceScopes[0].TurnID || base.Status != "completed" ||
			base.LLMCalls != 1 || matched[base.MessageID] {
			return "skill_selection_log_binding"
		}
		var event skillSelectionLogEvent
		wantedCount := 0
		if index == 1 {
			wantedCount = 1
		}
		if json.Unmarshal([]byte(raw), &event) != nil || event.SkillVersion != 1 || event.SkillCode != "observed" ||
			event.SkillCount != wantedCount ||
			event.SkillSHA256 != expectedSkillContextHash(item, workspace, index == 1) {
			return "skill_selection_revision_mismatch"
		}
		matched[base.MessageID] = true
	}
	if len(matched) != 3 {
		return "skill_selection_log_incomplete"
	}
	return ""
}
