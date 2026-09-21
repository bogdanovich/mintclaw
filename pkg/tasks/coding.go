package tasks

import (
	"fmt"
	"regexp"
	"strings"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
)

const CodingProjectionSchemaV4 = "coding_task.v4"

const (
	MaxCodingObjectiveBytes    = 128 << 10
	MaxCodingDoneCriteriaBytes = 128 << 10
	MaxCodingSummaryBytes      = 16 << 10
)

var codingTargetPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// CodingProjection is the bounded gateway-owned projection for one remote
// coding task. The companion ledger remains execution authority and coding
// thread files remain transcript authority; this projection contains only the
// immutable grant plus the latest safe lifecycle facts needed for recovery,
// interaction correlation, and delivery.
type CodingProjection struct {
	SchemaVersion string              `json:"schema_version"`
	Alias         string              `json:"alias"`
	Target        string              `json:"target"`
	Scope         string              `json:"scope"`
	Revision      string              `json:"revision"`
	Profile       codingtask.TaskMode `json:"profile"`
	RequestDigest string              `json:"request_digest"`
	DoneCriteria  string              `json:"done_criteria,omitempty"`

	RouteSessionKey string `json:"route_session_key"`
	SessionKey      string `json:"session_key"`
	ActorID         string `json:"actor_id"`
	SenderID        string `json:"sender_id"`
	AccountID       string `json:"account_id,omitempty"`
	ChatType        string `json:"chat_type,omitempty"`
	SpaceID         string `json:"space_id,omitempty"`
	SpaceType       string `json:"space_type,omitempty"`
	OriginMessageID string `json:"origin_message_id,omitempty"`

	ThreadID           string `json:"thread_id,omitempty"`
	WorkerGenerationID string `json:"worker_generation_id,omitempty"`
	NodeState          string `json:"node_state,omitempty"`
	NodeRevision       uint64 `json:"node_revision,omitempty"`
	NodeResultDigest   string `json:"node_result_digest,omitempty"`
	Activity           string `json:"activity,omitempty"`
	WorktreeID         string `json:"worktree_id,omitempty"`
	Branch             string `json:"branch,omitempty"`
	HandoffID          string `json:"handoff_id,omitempty"`
	FailureCode        string `json:"failure_code,omitempty"`
	UpdatedAt          int64  `json:"updated_at,omitempty"`
	RetainUntil        int64  `json:"retain_until,omitempty"`

	Question *CodingQuestionProjection `json:"question,omitempty"`
}

// CodingQuestionProjection binds one durable channel interaction to the exact
// blocking worker question it may answer. It intentionally excludes prompts
// once the question is no longer current.
type CodingQuestionProjection struct {
	ID            string                 `json:"id"`
	Revision      uint64                 `json:"revision"`
	InteractionID string                 `json:"interaction_id,omitempty"`
	Options       []CodingQuestionOption `json:"options,omitempty"`
}

type CodingQuestionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func cloneCodingProjection(projection *CodingProjection) *CodingProjection {
	if projection == nil {
		return nil
	}
	cloned := *projection
	if projection.Question != nil {
		question := *projection.Question
		question.Options = append([]CodingQuestionOption(nil), projection.Question.Options...)
		cloned.Question = &question
	}
	return &cloned
}

func validateCodingProjection(taskID, generationID string, projection *CodingProjection) error {
	if projection == nil {
		return fmt.Errorf("coding task %q is missing coding projection", taskID)
	}
	if projection.SchemaVersion != CodingProjectionSchemaV4 {
		return fmt.Errorf("coding task %q has invalid coding projection schema", taskID)
	}
	if !codingtask.ValidAlias(projection.Alias) || !codingTargetPattern.MatchString(projection.Target) ||
		!codingtask.ValidAlias(projection.Scope) || !codingtask.ValidRevision(projection.Revision) ||
		!projection.Profile.AdmittedInV4() || !validCodingDigest(projection.RequestDigest) ||
		len(projection.DoneCriteria) > MaxCodingDoneCriteriaBytes ||
		!codingtask.ValidIdentifier(generationID) {
		return fmt.Errorf("coding task %q has invalid immutable coding authority", taskID)
	}
	if !validCodingOwnedIdentity(projection.RouteSessionKey, 1024) ||
		!validCodingOwnedIdentity(projection.SessionKey, 1024) ||
		!validCodingOwnedIdentity(projection.ActorID, 1024) ||
		!validCodingOwnedIdentity(projection.SenderID, 1024) {
		return fmt.Errorf("coding task %q has invalid requester identity", taskID)
	}
	for _, value := range []string{
		projection.AccountID,
		projection.ChatType,
		projection.SpaceID,
		projection.SpaceType,
		projection.OriginMessageID,
	} {
		if len(value) > 1024 || value != strings.TrimSpace(value) {
			return fmt.Errorf("coding task %q has invalid route projection", taskID)
		}
	}
	for _, value := range []string{
		projection.ThreadID,
		projection.WorkerGenerationID,
		projection.WorktreeID,
		projection.HandoffID,
	} {
		if value != "" && !codingtask.ValidIdentifier(value) {
			return fmt.Errorf("coding task %q has invalid projected identity", taskID)
		}
	}
	if len(projection.Branch) > codingtask.MaxBranchBytes ||
		len(projection.FailureCode) > codingtask.MaxFailureCodeBytes ||
		len(projection.Activity) > 64 || len(projection.NodeState) > 64 {
		return fmt.Errorf("coding task %q has oversized lifecycle projection", taskID)
	}
	if (projection.NodeRevision == 0 && projection.NodeResultDigest != "") ||
		(projection.NodeRevision > 0 && !validCodingDigest(projection.NodeResultDigest)) {
		return fmt.Errorf("coding task %q has invalid node result digest", taskID)
	}
	if projection.Question == nil {
		return nil
	}
	question := projection.Question
	if !codingtask.ValidIdentifier(question.ID) || question.Revision == 0 ||
		(question.InteractionID != "" && !codingtask.ValidIdentifier(question.InteractionID)) ||
		len(question.Options) > codingtask.MaxQuestionOptions {
		return fmt.Errorf("coding task %q has invalid question projection", taskID)
	}
	seen := make(map[string]struct{}, len(question.Options))
	for _, option := range question.Options {
		if !codingtask.ValidIdentifier(option.ID) || strings.TrimSpace(option.Label) == "" ||
			len(option.Label) > codingtask.MaxQuestionLabelBytes {
			return fmt.Errorf("coding task %q has invalid question option", taskID)
		}
		if _, duplicate := seen[option.ID]; duplicate {
			return fmt.Errorf("coding task %q has duplicate question option", taskID)
		}
		seen[option.ID] = struct{}{}
	}
	return nil
}

func validCodingDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validCodingOwnedIdentity(value string, maximum int) bool {
	return value == strings.TrimSpace(value) && value != "" && len(value) <= maximum
}
