package outbox

import (
	"encoding/json"
	"errors"
)

const MaxInspectionBytes = 2 * 1024 * 1024

var ErrInvalidInspection = errors.New("outbox: invalid bounded intent observation")

// InspectIntent validates caller-admitted bytes with the native load contract,
// without opening/creating a store, locks, directories or delivery ownership.
// Callers own filesystem admission, freshness and raw privacy checks. Returned
// payloads are sensitive evidence, not a safe model or transport projection.
func InspectIntent(data []byte, expectedID string) (Intent, error) {
	if len(data) == 0 || len(data) > MaxInspectionBytes || validateID(expectedID) != nil {
		return Intent{}, ErrInvalidInspection
	}
	var intent Intent
	if json.Unmarshal(data, &intent) != nil || validateIntent(intent) != nil || intent.ID != expectedID {
		return Intent{}, ErrInvalidInspection
	}
	return intent, nil
}
