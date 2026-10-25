package eval

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

const maxQuestionAnswerEvidenceBytes = 3 * 1024 * 1024

func questionAnswerCheckID(item Case) string {
	for _, check := range item.Checks {
		if check.Kind == "interaction_answered" {
			return check.ID
		}
	}
	return ""
}

func hasQuestionAnswerCheck(item Case) bool { return questionAnswerCheckID(item) != "" }

// The first answer pack deliberately admits one synthetic ordinary choice, not
// arbitrary protected questions, approval answers or adaptive answer generation.
func validQuestionAnswerCase(item Case) bool {
	count := 0
	for _, check := range item.Checks {
		if check.Kind == "interaction_answered" {
			count++
		}
	}
	return count == 1 && item.Suite == "general" && item.PDF == nil && item.Browser == nil &&
		slices.Contains(item.Requires, "interactions") && len(item.Fixtures) == 0 && len(item.Driver.Facts) == 0 &&
		item.Driver.Mode == "scripted" && len(item.Driver.Steps) == 2 && item.Limits.MaxUserTurns == 3 &&
		item.Driver.Steps[0].WaitFor.Kind == "question" && item.Driver.Steps[0].Action == (Action{Kind: "answer", Text: "paper"}) &&
		item.Driver.Steps[1].WaitFor.Kind == "final" && item.Driver.Steps[1].Action.Kind == "message" &&
		requiredRubric(item.Rubric)
}

func paperQuestion(record interactions.Record) bool {
	if record.Kind != interactions.KindQuestion || record.ProtectedAnswer != nil || len(record.Questions) != 1 {
		return false
	}
	question := record.Questions[0]
	if question.MultiSelect || len(question.Options) != 2 {
		return false
	}
	labels := map[string]bool{}
	for _, option := range question.Options {
		label := strings.ToLower(option.Label)
		if option.Value != "" || (label != "email" && label != "paper") || labels[label] {
			return false
		}
		labels[label] = true
	}
	return labels["email"] && labels["paper"]
}

func frozenQuestion(encoded string) (interactions.Record, bool) {
	if len(encoded) > MaxManifestBytes {
		return interactions.Record{}, false
	}
	data, err := json.Marshal(struct {
		SchemaVersion  string            `json:"schema_version"`
		CommitSequence uint64            `json:"commit_sequence"`
		Records        []json.RawMessage `json:"records"`
	}{
		SchemaVersion: interactions.SnapshotSchemaVersion, Records: []json.RawMessage{json.RawMessage(encoded)},
	})
	if err != nil || len(data) > MaxManifestBytes {
		return interactions.Record{}, false
	}
	snapshot, err := interactions.InspectSnapshot(data)
	if err != nil || len(snapshot.Records) != 1 {
		return interactions.Record{}, false
	}
	return snapshot.Records[0], true
}

func validQuestionAnswer(trial Trial, workspace string) bool {
	proof := trial.Answer
	if proof == nil || proof.Version != Version || proof.Workspace != workspace || !validID(trial.SessionID) ||
		!validID(proof.AnswerRequestID) || !validID(proof.ResumeFinalEventID) ||
		proof.AnswerAt < trial.StartedAt.UnixMilli() || proof.ObservedAt < proof.AnswerAt {
		return false
	}
	encoded, err := json.Marshal(proof)
	if err != nil || len(encoded) > maxQuestionAnswerEvidenceBytes {
		return false
	}
	before, waitingValid := frozenQuestion(proof.Waiting)
	current, resolvedValid := frozenQuestion(proof.Resolved)
	if !waitingValid || !resolvedValid {
		return false
	}
	route := before.Route
	if !paperQuestion(before) || before.Status != interactions.StatusWaiting || before.Answer != nil ||
		!validID(before.ID) || !validID(before.PromptDeliveryID) || !boundedIdentity(before.ShortID, 64) ||
		before.Outcome != "" || before.ResumeTries != 0 || before.CreatedAt < trial.StartedAt.UnixMilli() ||
		before.UpdatedAt > proof.AnswerAt || before.ApprovalAction != "" || current.ApprovalAction != "" ||
		before.ExpiresAt <= proof.AnswerAt || !validID(before.Origin.TurnID) ||
		!boundedIdentity(before.Origin.ToolCallID, 128) || before.Origin.ToolName != "request_user_input" ||
		before.Origin.TaskID != "" || before.Origin.ExecutionContext == nil ||
		before.Origin.ContinuationSessionKey != "" && before.Origin.ContinuationSessionKey != trial.SessionKey ||
		route.AgentID != trial.AgentID || route.SessionKey != trial.SessionKey ||
		route.RouteSessionKey != "" && route.RouteSessionKey != trial.SessionKey ||
		route.Channel != "mintclaw" || route.ChatID != "mintclaw:"+trial.SessionID ||
		route.ChatType != "direct" || route.SenderID != "mintclaw-user" ||
		route.AccountID != "" || route.TopicID != "" || route.SpaceID != "" || route.SpaceType != "" ||
		current.ID != before.ID || current.ShortID != before.ShortID || current.CreatedAt != before.CreatedAt ||
		current.PromptDeliveryID != before.PromptDeliveryID || current.Route != route ||
		!reflect.DeepEqual(current.Origin, before.Origin) || !reflect.DeepEqual(current.Questions, before.Questions) ||
		current.Kind != interactions.KindQuestion || current.Status != interactions.StatusResolved ||
		current.Outcome != interactions.OutcomeAnswered || current.ProtectedAnswer != nil ||
		current.Revision <= before.Revision || current.ResumeTries != 1 || current.ResumeError != "" ||
		current.FailureCode != "" || current.ApprovalConsumedAt != 0 || current.ResolvedAt < proof.AnswerAt ||
		current.LastResumeAt < proof.AnswerAt || current.LastResumeAt > current.ResolvedAt ||
		current.ResolvedAt > proof.ObservedAt || current.UpdatedAt > proof.ObservedAt ||
		current.Answer == nil || current.Answer.Superseded || current.Answer.Protected != nil ||
		current.Answer.Choice != "" ||
		current.Answer.MessageID != proof.AnswerRequestID || current.Answer.Text != "paper" ||
		current.Answer.ReceivedAt < proof.AnswerAt || current.Answer.ReceivedAt > current.ResolvedAt ||
		!maps.Equal(current.Answer.Values, map[string]string{before.Questions[0].ID: "paper"}) {
		return false
	}
	prompts, answers, finals := 0, 0, 0
	promptIndex, answerIndex, finalIndex := -1, -1, -1
	for index, event := range trial.Events {
		if event.Kind == "input" && event.Turn == 2 && event.RequestID == proof.AnswerRequestID &&
			event.Content == "paper" {
			answers++
			answerIndex = index
		}
		if event.Kind == "final" && event.Turn == 2 && event.ID == proof.ResumeFinalEventID &&
			event.RequestID == proof.AnswerRequestID &&
			event.AgentID == trial.AgentID &&
			event.SessionKey == trial.SessionKey &&
			len(event.TraceScopes) > 0 {
			for _, scope := range event.TraceScopes {
				if !scope.Complete() || scope.Workspace != workspace {
					return false
				}
			}
			finals++
			finalIndex = index
		}
		if event.Kind == "question" && event.Turn == 1 && event.InteractionID == before.ID &&
			event.InteractionShortID == before.ShortID &&
			event.Controls == bus.OutboundInteractionControlsPrompt && event.InteractionKind == "question" &&
			boundedIdentity(event.MessageID, 128) &&
			event.RequestID == before.Origin.ExecutionContext.MessageID && event.AgentID == trial.AgentID &&
			event.SessionKey == trial.SessionKey &&
			event.MessageID == current.Answer.ResponseMessageID {
			for _, scope := range event.TraceScopes {
				if scope.Complete() && scope.Workspace == workspace && scope.TurnID == before.Origin.TurnID {
					prompts++
					promptIndex = index
				}
			}
		}
	}
	return prompts == 1 && answers == 1 && finals == 1 && promptIndex < answerIndex && answerIndex < finalIndex &&
		validQuestionAnswerHistory(proof, trial, before, current)
}

func validQuestionAnswerHistory(proof *QuestionAnswerEvidence, trial Trial, before, current interactions.Record) bool {
	if len(proof.Metadata) > MaxManifestBytes || len(proof.History) > memory.MaxJSONLInspectionRecords+1 ||
		digest([]byte(proof.Metadata)) != proof.MetadataSHA256 {
		return false
	}
	bytes := len(proof.Metadata)
	for _, line := range proof.History {
		bytes += len(line) + 1
		if len(line) > MaxInputBytes || bytes > memory.MaxJSONLInspectionBytes+1 {
			return false
		}
	}
	history := []byte(strings.Join(proof.History, "\n"))
	if digest(history) != proof.HistorySHA256 {
		return false
	}
	snapshot, err := memory.InspectJSONLSnapshot(
		context.Background(),
		trial.SessionKey,
		[]byte(proof.Metadata),
		history,
	)
	if err != nil {
		return false
	}
	meta := snapshot.Metadata
	if meta.Key != trial.SessionKey || meta.HistoryDirty || meta.Skip != 0 || meta.Summary != "" ||
		meta.Count != len(snapshot.History) || meta.Count < 4 || meta.Count > memory.MaxJSONLInspectionRecords ||
		len(meta.ClientSessionIDs) != 1 || meta.ClientSessionIDs[0] != trial.SessionID ||
		meta.UpdatedAt.UnixMilli() > proof.ObservedAt {
		return false
	}
	scope, err := session.DecodeCurrentSessionScope(meta.Scope)
	if err != nil || scope.AgentID != trial.AgentID || scope.Channel != "mintclaw" ||
		scope.Account != routing.NormalizeAccountID(before.Route.AccountID) ||
		session.BuildSessionKey(*scope) != trial.SessionKey ||
		scope.ClientSessionID != trial.SessionID ||
		scope.Values["chat"] != "" && scope.Values["chat"] != "direct:mintclaw:"+trial.SessionID {
		return false
	}
	calls, results, callIndex, resultIndex := 0, 0, -1, -1
	for index, message := range snapshot.History {
		for _, call := range message.ToolCalls {
			calls++
			if message.Role != "assistant" || call.Name != "request_user_input" || call.ID != before.Origin.ToolCallID {
				return false
			}
			callIndex = index
		}
		if message.Role == "tool" {
			results++
			if message.ToolCallID != before.Origin.ToolCallID ||
				message.ToolResultStatus != providers.ToolResultStatusSuccess || !matchesQuestionResult(message.Content, current) {
				return false
			}
			resultIndex = index
		}
	}
	if calls != 1 || results != 1 || callIndex < 0 || resultIndex <= callIndex {
		return false
	}
	continuations := 0
	for _, message := range snapshot.History[resultIndex+1:] {
		if message.Role == "user" {
			break
		}
		if message.Role == "assistant" && len(message.ToolCalls) == 0 && strings.TrimSpace(message.Content) != "" {
			continuations++
		}
	}
	return continuations == 1
}

func matchesQuestionResult(content string, record interactions.Record) bool {
	payload, err := DecodeManifest[struct {
		InteractionID      string               `json:"interaction_id,omitempty"`
		Outcome            interactions.Outcome `json:"outcome"`
		ReceiptIDs         []string             `json:"receipt_ids,omitempty"`
		Answers            map[string]string    `json:"answers,omitempty"`
		Text               string               `json:"text,omitempty"`
		ProtectedAnswerRef string               `json:"protected_answer_ref,omitempty"`
	}]([]byte(content))
	return err == nil && payload.InteractionID == record.ID && payload.Outcome == interactions.OutcomeAnswered &&
		payload.Text == "paper" && maps.Equal(payload.Answers, record.Answer.Values) &&
		len(payload.ReceiptIDs) == 0 && payload.ProtectedAnswerRef == ""
}

func gradeQuestionAnswerChecks(item Case, trial Trial, workspace string) []CheckResult {
	checks := gradeConversation(item.Checks, trial.Events, lastOutcome(trial.Events))
	for index, check := range item.Checks {
		if check.Kind == "interaction_answered" && validQuestionAnswer(trial, workspace) {
			checks[index] = booleanCheck(check.ID, true, []string{"question_answer"})
		}
	}
	return checks
}
