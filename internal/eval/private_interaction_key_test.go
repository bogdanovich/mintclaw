package eval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateInteractionKeyRequiresExactVerifiedNativeWitness(t *testing.T) {
	for _, mode := range []string{
		"complete", "unverified", "foreign-home", "foreign-path", "digest", "changed-text", "missing", "mode",
		"short", "alias", "extra-opaque", "literal", "escaped", "credential",
	} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			name := "workspace/state/interaction_hmac.key"
			file := filepath.Join(home, filepath.FromSlash(name))
			if os.MkdirAll(filepath.Dir(file), 0o700) != nil {
				t.Fatal("native key root unavailable")
			}
			data := bytes.Repeat([]byte{0xff}, 32)
			facts := map[string]string{"sentinel": "KEY_PRIVATE_CANARY"}
			var filter *strings.Replacer
			if mode == "literal" || mode == "credential" {
				copy(data, []byte(facts["sentinel"]))
			}
			if mode == "escaped" {
				copy(data, []byte(`\u004bEY_PRIVATE_CANARY`))
			}
			if mode == "credential" {
				filter = strings.NewReplacer(facts["sentinel"], "[redacted]")
				facts = nil
			}
			witness := &interactionKeyWitness{home: home, name: name, sha256: digest(data)}
			permission := os.FileMode(0o600)
			switch mode {
			case "unverified":
				witness = nil
			case "foreign-home":
				witness.home = t.TempDir()
			case "foreign-path":
				witness.name = "other.key"
			case "digest":
				witness.sha256 = strings.Repeat("a", 64)
			case "changed-text":
				data = bytes.Repeat([]byte{'x'}, 32)
			case "mode":
				permission = 0o644
			case "short":
				data = data[:31]
			}
			if mode != "missing" && os.WriteFile(file, data, permission) != nil {
				t.Fatal("native key unavailable")
			}
			if mode == "alias" {
				target := filepath.Join(t.TempDir(), "foreign.key")
				if os.WriteFile(target, data, 0o600) != nil || os.Remove(file) != nil ||
					os.Symlink(target, file) != nil {
					t.Fatal("alias control unavailable")
				}
			}
			if mode == "extra-opaque" && os.WriteFile(filepath.Join(home, "other.bin"), data, 0o600) != nil {
				t.Fatal("opaque control unavailable")
			}
			result := observePrivateSurfaces(t.Context(), home, facts, filter, nil, witness)
			if !result.valid() {
				t.Fatal("invalid profile observation", result)
			}
			switch mode {
			case "complete":
				if result.Code != "observed" || result.AuthorizedInteractionKeys != 1 || result.OpaqueFiles != 0 {
					t.Fatal(result)
				}
			case "literal", "escaped", "credential":
				if result.Code != "leak_observed" || !result.LeakObserved || result.AuthorizedInteractionKeys != 0 {
					t.Fatal("key witness bypassed a raw leak guard", result)
				}
			default:
				if result.Code != "unavailable" || result.LeakObserved {
					t.Fatal("unknown key became privacy proof", result)
				}
			}
			encoded, err := json.Marshal(result)
			if err != nil || bytes.Contains(encoded, []byte(name)) ||
				bytes.Contains(encoded, []byte("KEY_PRIVATE_CANARY")) {
				t.Fatal("profile receipt exposed private key identity or content")
			}
		})
	}
}

func TestBrowserApprovalKeyWitnessDoesNotAcceptConflictingCohorts(t *testing.T) {
	home := t.TempDir()
	domain := &browserConversation{
		questions: questionConversation{workspace: filepath.Join(home, "workspace")},
		approval:  &browserApprovalObservation{keySHA256: strings.Repeat("a", 64)},
	}
	run := browserRun{trials: map[string]browserPendingTrial{"first": {conversation: domain}}}
	if witness := run.approvalKeyWitness(home); witness == nil || !validDigest(witness.sha256) {
		t.Fatal("verified key unavailable")
	}
	other := *domain
	other.approval = &browserApprovalObservation{keySHA256: strings.Repeat("b", 64)}
	run.trials["second"] = browserPendingTrial{conversation: &other}
	if witness := run.approvalKeyWitness(home); witness == nil || validDigest(witness.sha256) {
		t.Fatal("conflicting keys became a positive or absent witness")
	}
	delete(run.trials, "second")
	domain.questions.workspace = t.TempDir()
	if witness := run.approvalKeyWitness(home); witness == nil || validDigest(witness.sha256) {
		t.Fatal("foreign native key escaped the owned profile")
	}
}
