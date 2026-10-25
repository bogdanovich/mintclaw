package eval

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func validReadRecoveryEvidence(trial Trial, item Case, workspace string) bool {
	proof := trial.ReadRecovery
	if proof == nil || item.ReadRecovery == nil || item.Validate() != nil ||
		proof.Version != Version || proof.Code != "observed" ||
		!readFixtureMatches(proof.Before, item.Fixtures[0].SHA256) ||
		!readFixtureMatches(proof.After, item.Fixtures[0].SHA256) ||
		proof.MetadataSHA256 != digest([]byte(proof.Metadata)) ||
		proof.HistorySHA256 != digest([]byte(strings.Join(proof.History, "\n"))) ||
		!validTrialConversation(trial, trial.AgentID, workspace) {
		return false
	}
	encoded, err := json.Marshal(proof)
	if err != nil || len(encoded) > 3*1024*1024 || len(proof.Metadata) > MaxManifestBytes {
		return false
	}
	snapshot, err := memory.InspectJSONLSnapshot(context.Background(), trial.SessionKey,
		[]byte(proof.Metadata), []byte(strings.Join(proof.History, "\n")))
	if err != nil || snapshot.Metadata.Skip != 0 || snapshot.Metadata.Summary != "" ||
		snapshot.Metadata.Count != len(snapshot.History) ||
		!slices.Equal(snapshot.Metadata.ClientSessionIDs, []string{trial.SessionID}) {
		return false
	}
	scope, err := session.DecodeCurrentSessionScope(snapshot.Metadata.Scope)
	if err != nil || scope.AgentID != trial.AgentID || scope.Channel != "mintclaw" ||
		scope.ClientSessionID != trial.SessionID || session.BuildSessionKey(*scope) != trial.SessionKey ||
		scope.Values["chat"] != "" && scope.Values["chat"] != "direct:mintclaw:"+trial.SessionID {
		return false
	}
	inputs, finals := []Event{}, []Event{}
	for _, event := range trial.Events {
		switch event.Kind {
		case "input":
			inputs = append(inputs, event)
		case "final":
			finals = append(finals, event)
		}
	}
	if len(inputs) != 3 || len(finals) != 3 {
		return false
	}
	for index := range inputs {
		if inputs[index].Turn != index+1 || finals[index].Turn != index+1 ||
			finals[index].RequestID != inputs[index].RequestID ||
			strings.Contains(finals[index].Content, item.ReadRecovery.Marker) != (index > 0) {
			return false
		}
	}
	expected := []string{item.Task, item.Driver.Steps[0].Action.Text, item.Driver.Steps[1].Action.Text}
	for index := range expected {
		if inputs[index].Content != expected[index] {
			return false
		}
	}
	return validRecoveryHistory(snapshot.History, expected, item.ReadRecovery.Marker)
}

func validRecoveryHistory(history []providers.Message, inputs []string, marker string) bool {
	if len(inputs) != 3 {
		return false
	}
	users := 0
	callIDs := [2]string{}
	results := [2]bool{}
	finals := [3]int{}
	paths := [2]string{readMissingPath, readFixturePath}
	statuses := [2]providers.ToolResultStatus{providers.ToolResultStatusError, providers.ToolResultStatusSuccess}
	for _, message := range history {
		if message.Role != "assistant" && len(message.ToolCalls) != 0 {
			return false
		}
		switch message.Role {
		case "user":
			if users >= 3 || message.Content != inputs[users] || users > 0 && finals[users-1] != 1 {
				return false
			}
			users++
		case "assistant":
			phase := users - 1
			if phase < 0 || phase > 2 {
				return false
			}
			if len(message.ToolCalls) != 0 {
				if phase > 1 || len(message.ToolCalls) != 1 || callIDs[phase] != "" || finals[phase] != 0 {
					return false
				}
				call := message.ToolCalls[0]
				path, pathOK := call.Arguments["path"].(string)
				if call.Name != "read_file" || !boundedIdentity(call.ID, 128) || !pathOK || path != paths[phase] ||
					phase == 1 && call.ID == callIDs[0] {
					return false
				}
				callIDs[phase] = call.ID
				continue
			}
			if message.Content == "" || phase < 2 && !results[phase] ||
				strings.Contains(message.Content, marker) != (phase > 0) {
				return false
			}
			finals[phase]++
			if finals[phase] != 1 {
				return false
			}
		case "tool":
			phase := users - 1
			if phase < 0 || phase > 1 || callIDs[phase] == "" || results[phase] || finals[phase] != 0 ||
				message.ToolCallID != callIDs[phase] || message.ToolResultStatus != statuses[phase] ||
				message.Content == "" || strings.Contains(message.Content, marker) != (phase == 1) {
				return false
			}
			results[phase] = true
		default:
			return false
		}
	}
	return users == 3 && results == [2]bool{true, true} && finals == [3]int{1, 1, 1}
}
