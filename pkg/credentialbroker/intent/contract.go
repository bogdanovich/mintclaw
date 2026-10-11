package intent

import "errors"

const (
	Version        = 1
	MaxPacketBytes = 32768
	MaxAccounts    = 16
)

var (
	ErrInvalid     = errors.New("credential intent is invalid")
	ErrUnavailable = errors.New("credential transport is unavailable")
)

type Operation string

const (
	Status          Operation = "status"
	RequestApproval Operation = "request_approval"
	ApprovalStatus  Operation = "approval_status"
	GrantStatus     Operation = "grant_status"
	Login           Operation = "login"
)

// Request excludes identity, provider references, URLs, selectors, and secret
// values. Identity comes from the authenticated service caller. A login still
// needs a qualified consumer with live grant/origin/frame/element checks.
type Request struct {
	Operation       Operation `json:"operation"`
	Account         string    `json:"account,omitempty"`
	Accounts        []string  `json:"accounts,omitempty"`
	ApprovalID      string    `json:"approval_id,omitempty"`
	GrantID         string    `json:"grant_id,omitempty"`
	DurationSeconds int       `json:"duration_seconds,omitempty"`
}

type Code string

const (
	Ready            Code = "ready"
	NotReady         Code = "not_ready"
	Denied           Code = "denied"
	ApprovalRequired Code = "approval_required"
	ApprovalPending  Code = "approval_pending"
	ApprovalExpired  Code = "approval_expired"
	GrantActive      Code = "grant_active"
	GrantInactive    Code = "grant_inactive"
	LoggedIn         Code = "logged_in"
	HandoffRequired  Code = "handoff_required"
	Failed           Code = "failed"
	OutcomeUnknown   Code = "outcome_unknown"
)

// Response admits only fixed outcomes and service-owned references. References
// locate pending requests or scoped grants/sessions; they are not bearer
// authorization. Trusted handlers must source them from their own controllers,
// never SDK fields, browser output, verifier diagnostics, or caller text.
type Response struct {
	Code            Code   `json:"code"`
	ApprovalID      string `json:"approval_id,omitempty"`
	GrantID         string `json:"grant_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	ValidForSeconds int    `json:"valid_for_seconds,omitempty"`
}

func (request Request) Validate() error {
	if request.DurationSeconds < 0 || request.DurationSeconds > 20*60*60 {
		return ErrInvalid
	}
	switch request.Operation {
	case Status:
		if request.Account != "" || len(request.Accounts) != 0 || request.ApprovalID != "" ||
			request.GrantID != "" || request.DurationSeconds != 0 {
			return ErrInvalid
		}
	case RequestApproval:
		if request.Account != "" || request.ApprovalID != "" || request.GrantID != "" ||
			request.DurationSeconds == 0 || len(request.Accounts) == 0 || len(request.Accounts) > MaxAccounts {
			return ErrInvalid
		}
		seen := make(map[string]struct{}, len(request.Accounts))
		for _, alias := range request.Accounts {
			if !ValidAlias(alias) {
				return ErrInvalid
			}
			if _, duplicate := seen[alias]; duplicate {
				return ErrInvalid
			}
			seen[alias] = struct{}{}
		}
	case ApprovalStatus:
		if !ValidReference(request.ApprovalID) || request.GrantID != "" || request.Account != "" ||
			len(request.Accounts) != 0 || request.DurationSeconds != 0 {
			return ErrInvalid
		}
	case GrantStatus:
		if !ValidReference(request.GrantID) || request.ApprovalID != "" || request.Account != "" ||
			len(request.Accounts) != 0 || request.DurationSeconds != 0 {
			return ErrInvalid
		}
	case Login:
		if !ValidAlias(request.Account) || !ValidReference(request.GrantID) || request.ApprovalID != "" ||
			len(request.Accounts) != 0 || request.DurationSeconds != 0 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func (response Response) Validate(operation Operation) error {
	if response.ValidForSeconds < 0 || response.ValidForSeconds > 20*60*60 {
		return ErrInvalid
	}
	noReferences := response.ApprovalID == "" && response.GrantID == "" && response.SessionID == ""
	switch response.Code {
	case NotReady, Denied, Failed, OutcomeUnknown:
		if !noReferences || response.ValidForSeconds != 0 {
			return ErrInvalid
		}
	case Ready:
		if operation != Status || !noReferences || response.ValidForSeconds != 0 {
			return ErrInvalid
		}
	case ApprovalRequired:
		if operation != RequestApproval || !ValidReference(response.ApprovalID) || response.GrantID != "" ||
			response.SessionID != "" || response.ValidForSeconds == 0 || response.ValidForSeconds > 5*60 {
			return ErrInvalid
		}
	case ApprovalPending:
		if operation != ApprovalStatus || !noReferences || response.ValidForSeconds == 0 ||
			response.ValidForSeconds > 5*60 {
			return ErrInvalid
		}
	case ApprovalExpired:
		if operation != ApprovalStatus || !noReferences || response.ValidForSeconds != 0 {
			return ErrInvalid
		}
	case GrantActive:
		if response.ApprovalID != "" || response.SessionID != "" || response.ValidForSeconds == 0 ||
			(operation == ApprovalStatus && !ValidReference(response.GrantID)) ||
			(operation == GrantStatus && response.GrantID != "") ||
			(operation != ApprovalStatus && operation != GrantStatus) {
			return ErrInvalid
		}
	case GrantInactive:
		if operation != GrantStatus || !noReferences || response.ValidForSeconds != 0 {
			return ErrInvalid
		}
	case LoggedIn, HandoffRequired:
		if operation != Login || !ValidReference(response.SessionID) || response.ApprovalID != "" ||
			response.GrantID != "" || response.ValidForSeconds != 0 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

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
	if len(value) != 32 {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
