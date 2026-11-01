package diagnostictrace

import (
	"crypto/sha256"
	"encoding/hex"
)

// FilteredHash is the native diagnostic hash boundary. It is not a privacy
// certificate: callers still own protected-content projection and raw scanning.
func FilteredHash(value string, filter func(string) string) string {
	if value == "" {
		return ""
	}
	if filter != nil {
		value = filter(value)
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
