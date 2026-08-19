package interactions

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

func (r *Registry) nowMillis() int64 {
	return r.options.Now().UnixMilli()
}

func randomID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("create interaction id: %w", err)
	}
	return "interaction_" + hex.EncodeToString(raw), nil
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "interaction_")
	if len(id) > 8 {
		id = id[:8]
	}
	return id
}

func bounded(value string, maxLength int) string {
	if utf8.RuneCountInString(value) <= maxLength {
		return value
	}
	return string([]rune(value)[:maxLength])
}

var regexpID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func normalizeRoute(route Route) Route {
	route.AgentID = strings.TrimSpace(route.AgentID)
	route.SessionKey = strings.TrimSpace(route.SessionKey)
	route.RouteSessionKey = strings.TrimSpace(route.RouteSessionKey)
	route.Channel = strings.TrimSpace(route.Channel)
	route.AccountID = strings.TrimSpace(route.AccountID)
	route.ChatID = strings.TrimSpace(route.ChatID)
	route.TopicID = strings.TrimSpace(route.TopicID)
	route.SenderID = strings.TrimSpace(route.SenderID)
	return route
}

func normalizeOrigin(origin Origin) Origin {
	origin.TurnID = strings.TrimSpace(origin.TurnID)
	origin.ExecutionID = strings.TrimSpace(origin.ExecutionID)
	origin.ToolCallID = strings.TrimSpace(origin.ToolCallID)
	origin.ToolName = strings.TrimSpace(origin.ToolName)
	origin.TaskID = strings.TrimSpace(origin.TaskID)
	origin.ContinuationSessionKey = strings.TrimSpace(origin.ContinuationSessionKey)
	origin.ArgumentHash = strings.TrimSpace(origin.ArgumentHash)
	origin.ExecutionContext = cloneExecutionContext(origin.ExecutionContext)
	return origin
}

func routesEqual(left, right Route) bool {
	return left.AgentID == right.AgentID &&
		left.SessionKey == right.SessionKey &&
		left.RouteSessionKey == right.RouteSessionKey &&
		left.Channel == right.Channel &&
		left.AccountID == right.AccountID &&
		left.ChatID == right.ChatID &&
		left.TopicID == right.TopicID &&
		left.SenderID == right.SenderID
}

func cloneRecord(rec Record) Record {
	rec.Origin.ExecutionContext = cloneExecutionContext(rec.Origin.ExecutionContext)
	rec.Questions = cloneQuestions(rec.Questions)
	if rec.Answer != nil {
		answer := *rec.Answer
		answer.Values = cloneStringMap(rec.Answer.Values)
		answer.Media = append([]string(nil), rec.Answer.Media...)
		rec.Answer = &answer
	}
	return rec
}

func cloneEvent(event Event) Event {
	if event.Success != nil {
		success := *event.Success
		event.Success = &success
	}
	return event
}

func cloneEvents(events []Event) []Event {
	out := make([]Event, len(events))
	for i := range events {
		out[i] = cloneEvent(events[i])
	}
	return out
}

func cloneExecutionContext(src *bus.InboundContext) *bus.InboundContext {
	if src == nil {
		return nil
	}
	cloned := *src
	cloned.ReplyHandles = cloneStringMap(src.ReplyHandles)
	cloned.Raw = cloneStringMap(src.Raw)
	return &cloned
}

func validateInteractionCreateMetadata(
	kind Kind,
	executionContext *bus.InboundContext,
	action string,
) error {
	if kind != KindApproval {
		if strings.TrimSpace(action) != "" {
			return fmt.Errorf("%w: question interactions cannot carry an approval action", ErrInvalidInteraction)
		}
		if executionContext != nil {
			return validateExecutionContext(executionContext)
		}
		return nil
	}
	if executionContext == nil {
		return fmt.Errorf("%w: approval requires the original execution context", ErrInvalidInteraction)
	}
	if strings.TrimSpace(action) == "" || !validBoundedString(action, MaxApprovalAction) {
		return fmt.Errorf("%w: approval requires a bounded action description", ErrInvalidInteraction)
	}
	return validateExecutionContext(executionContext)
}

func validateStoredInteractionMetadata(
	kind Kind,
	executionContext *bus.InboundContext,
	action string,
) error {
	if kind != KindApproval {
		if strings.TrimSpace(action) != "" {
			return fmt.Errorf("question interaction carries an approval action")
		}
		if executionContext != nil {
			return validateExecutionContext(executionContext)
		}
		return nil
	}
	// Obsolete snapshots can remain readable, but the agent recovery path
	// refuses to execute approvals without current authority metadata.
	if executionContext != nil {
		if err := validateExecutionContext(executionContext); err != nil {
			return err
		}
	}
	if !validBoundedString(action, MaxApprovalAction) {
		return fmt.Errorf("approval action exceeds bounds")
	}
	return nil
}

func validateExecutionContext(ctx *bus.InboundContext) error {
	data, err := json.Marshal(ctx)
	if err != nil {
		return fmt.Errorf("%w: encode execution context: %w", ErrInvalidInteraction, err)
	}
	if len(data) > MaxExecutionContext {
		return fmt.Errorf("%w: execution context exceeds %d bytes", ErrInvalidInteraction, MaxExecutionContext)
	}
	return nil
}

func cloneQuestions(questions []Question) []Question {
	if len(questions) == 0 {
		return nil
	}
	out := make([]Question, len(questions))
	for i, question := range questions {
		out[i] = question
		out[i].Options = append([]Option(nil), question.Options...)
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func sortRecords(records []Record) {
	slices.SortFunc(records, func(a, b Record) int {
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}
