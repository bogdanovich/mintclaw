// Package provisioning accepts private operator input for OS-encrypted credential
// storage. It never decrypts a credential, contacts a vault, or enables agents.
package provisioning

import "errors"

var (
	ErrUnsupported     = errors.New("credential provisioning requires Linux")
	ErrIdentity        = errors.New("credential provisioning requires an operator root process")
	ErrIsolation       = errors.New("credential setup process protection failed")
	ErrInput           = errors.New("private terminal token input failed")
	ErrToken           = errors.New("service-account token must be nonempty bounded printable ASCII")
	ErrStorage         = errors.New("protected credential storage is unavailable")
	ErrExists          = errors.New("encrypted service-account credential already exists")
	ErrBusy            = errors.New("credential setup is already running")
	ErrEncryption      = errors.New("OS credential encryption failed")
	ErrCommitUncertain = errors.New("credential storage outcome is uncertain; inspect protected setup before retrying")
)

const (
	credentialName     = "op-service-account"
	maxTokenBytes      = 32768
	maxCiphertextBytes = 128 * 1024
)
