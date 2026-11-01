package approval

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

// Bounded passkey state plus at most 64 grants with bounded account aliases.
const maxOwnerRecordBytes = 1 << 20

// ownerStore is private. Only the protected filesystem implementation is wired
// by New. Tests use an in-memory double; there is no production dependency
// injection path that can claim a write succeeded without durable persistence.
type ownerStore interface {
	load() (ownerRecord, error)
	commit(ownerRecord) error
	close() error
}

type ownerRecord struct {
	Version     int                   `json:"version"`
	OwnerID     string                `json:"owner_id"`
	Origin      string                `json:"origin"`
	UserID      []byte                `json:"user_id"`
	Credentials []webauthn.Credential `json:"credentials"`
	// Registration is stored inactive before its final admission check. The
	// first assertion after an accepted registration persists activation. A
	// restart before that assertion requires fresh protected owner setup.
	EnrollmentPending bool `json:"enrollment_pending,omitempty"`
	// Approval metadata shares the private, atomic owner transaction. No vault
	// values, browser cookies, pending links or WebAuthn challenges are stored.
	GrantPolicyRevision string                   `json:"grant_policy_revision,omitempty"`
	Grants              []credentialbroker.Grant `json:"grants,omitempty"`
}

func (owner ownerRecord) WebAuthnID() []byte                         { return owner.UserID }
func (owner ownerRecord) WebAuthnName() string                       { return owner.OwnerID }
func (owner ownerRecord) WebAuthnDisplayName() string                { return "MintClaw owner" }
func (owner ownerRecord) WebAuthnCredentials() []webauthn.Credential { return owner.Credentials }

func (owner ownerRecord) valid(ownerID, origin string) bool {
	if owner.Version != 1 || owner.OwnerID != ownerID || owner.Origin != origin || len(owner.UserID) != 64 ||
		len(owner.Credentials) != 1 {
		return false
	}
	credential := owner.Credentials[0]
	return len(credential.ID) > 0 && len(credential.ID) <= 1024 && len(credential.PublicKey) > 0 &&
		len(credential.PublicKey) <= 1024 && credential.Flags.UserPresent && credential.Flags.UserVerified &&
		!credential.Authenticator.CloneWarning && (!credential.Flags.BackupState || credential.Flags.BackupEligible)
}

// encoding uses a separate alias because generic formatting/JSON of the live
// owner record must redact private passkey state. The filesystem encoder is the
// only production caller that serializes the complete library credential.
type persistedOwner ownerRecord

func encodeOwner(owner ownerRecord) ([]byte, error) {
	encoded, err := json.Marshal(persistedOwner(owner))
	if err != nil || len(encoded) > maxOwnerRecordBytes {
		return nil, ErrUnavailable
	}
	return encoded, nil
}

func (ownerRecord) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private owner record]")
}
func (ownerRecord) MarshalJSON() ([]byte, error) { return []byte(`"[private owner record]"`), nil }
