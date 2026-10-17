package intent

const (
	MaxAccounts    = 16
	ReferenceBytes = 32
)

// Code is bounded approval-status metadata. It contains no provider values.
type Code string

const (
	NotReady        Code = "not_ready"
	Denied          Code = "denied"
	ApprovalPending Code = "approval_pending"
	GrantActive     Code = "grant_active"
)

func ValidAlias(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, character := range []byte(value) {
		letterOrDigit := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9'
		if !letterOrDigit && (index == 0 || character != '_' && character != '-') {
			return false
		}
	}
	return true
}

func ValidReference(value string) bool {
	if len(value) != ReferenceBytes*2 {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
