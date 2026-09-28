package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strings"
)

// LocalOperatorIdentity is the trusted operating-system identity material used
// by a local coding composition root. It is hashed before entering runtime
// principal state so usernames and hostnames are not exposed to feature tools.
type LocalOperatorIdentity struct {
	UID      string
	Username string
	Hostname string
}

func CurrentLocalActorID() (string, error) {
	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolve local operator: %w", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("resolve local hostname: %w", err)
	}
	return LocalActorID(LocalOperatorIdentity{
		UID:      current.Uid,
		Username: current.Username,
		Hostname: hostname,
	})
}

func LocalActorID(identity LocalOperatorIdentity) (string, error) {
	for _, value := range []string{identity.UID, identity.Username, identity.Hostname} {
		if containsIdentityControl(value) {
			return "", errors.New("local operator identity contains control characters")
		}
	}
	uid := strings.TrimSpace(identity.UID)
	username := strings.TrimSpace(identity.Username)
	hostname := strings.TrimSpace(identity.Hostname)
	if (uid == "" && username == "") || hostname == "" {
		return "", errors.New("local operator identity requires an account and hostname")
	}
	accountID := uid
	if accountID == "" {
		accountID = username
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("mintclaw:local-operator:v1\x00"))
	for _, value := range []string{accountID, hostname} {
		_, _ = fmt.Fprintf(digest, "%d:", len(value))
		_, _ = digest.Write([]byte(value))
	}
	return BuildCanonicalID("local", hex.EncodeToString(digest.Sum(nil)[:16])), nil
}

func containsIdentityControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}
