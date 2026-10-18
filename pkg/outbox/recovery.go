package outbox

import (
	"encoding/json"
	"errors"
	"os"
)

type RecoveryIssueCode string

const (
	RecoveryIssueInvalidJSON      RecoveryIssueCode = "invalid_json"
	RecoveryIssuePermissionDenied RecoveryIssueCode = "permission_denied"
)

// RecoveryIssue identifies a blocked record without exposing its payload or
// filesystem error. Recovery never removes or replaces the damaged record.
type RecoveryIssue struct {
	DeliveryID string            `json:"delivery_id"`
	Code       RecoveryIssueCode `json:"code"`
}

// RecoveryReport separates usable work from records that remain blocked by
// their canonical on-disk identities. Intents contain sensitive payloads;
// only Issues are safe for ordinary recovery diagnostics.
type RecoveryReport struct {
	Intents []Intent
	Issues  []RecoveryIssue
}

// AdmissionRecoveryReport transfers independent recovery claims together with
// safe diagnostics for blocked records. A fatal error transfers no claims.
type AdmissionRecoveryReport struct {
	Admissions []Admission
	Issues     []RecoveryIssue
}

func isolatedRecordIssue(id string, err error) (RecoveryIssue, bool) {
	var syntaxErr *json.SyntaxError
	switch {
	case errors.As(err, &syntaxErr):
		return RecoveryIssue{DeliveryID: id, Code: RecoveryIssueInvalidJSON}, true
	case errors.Is(err, os.ErrPermission):
		return RecoveryIssue{DeliveryID: id, Code: RecoveryIssuePermissionDenied}, true
	default:
		// Unsupported schemas, identity conflicts, missing records, and other
		// storage failures cannot be isolated as malformed individual payloads.
		return RecoveryIssue{}, false
	}
}
