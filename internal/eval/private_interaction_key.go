package eval

import (
	"os"
	"path/filepath"
)

// This exact native key already verified a fresh prepared-action HMAC. It is
// cryptographic state, not a generic authorization for opaque profile files.
type interactionKeyWitness struct {
	home, name, sha256 string
}

func (witness *interactionKeyWitness) matches(home, name string, data []byte, mode os.FileMode) bool {
	return witness != nil && witness.home == filepath.Clean(home) && witness.name == name &&
		safeRelativePath(witness.name) && validDigest(witness.sha256) && mode.IsRegular() && mode.Perm() == 0o600 &&
		len(data) == 32 && digest(data) == witness.sha256
}

func (run *browserRun) approvalKeyWitness(home string) *interactionKeyWitness {
	var result *interactionKeyWitness
	// Invalid or conflicting observations must not fall back to no-key mode.
	for _, pending := range run.trials {
		domain := pending.conversation
		if domain == nil || domain.approval == nil {
			continue
		}
		if !validDigest(domain.approval.keySHA256) {
			return &interactionKeyWitness{}
		}
		relative, err := filepath.Rel(home, filepath.Join(domain.questions.workspace, "state", "interaction_hmac.key"))
		name := filepath.ToSlash(relative)
		if err != nil || !safeRelativePath(name) {
			return &interactionKeyWitness{}
		}
		witness := interactionKeyWitness{home: filepath.Clean(home), name: name, sha256: domain.approval.keySHA256}
		if result != nil && *result != witness {
			return &interactionKeyWitness{}
		}
		result = &witness
	}
	return result
}
