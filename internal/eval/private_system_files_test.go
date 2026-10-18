package eval

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/skills"
)

func privateSystemFileFixture(t *testing.T) (string, string, []byte, os.FileMode) {
	t.Helper()
	home := t.TempDir()
	bundle, err := skills.EnsureSystemBundle(home)
	if err != nil {
		t.Fatal(err)
	}
	var name string
	var contents []byte
	var mode os.FileMode
	err = filepath.WalkDir(bundle.Root, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || name != "" {
			return walkErr
		}
		data, readErr := os.ReadFile(file)
		if readErr != nil {
			return readErr
		}
		if entry.Name() == ".manifest.json" || !bytes.ContainsRune(data, '\\') {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		name, contents, mode = file, data, info.Mode()
		return nil
	})
	if err != nil || name == "" {
		t.Fatal("native escaped-text fixture unavailable", err)
	}
	return home, name, contents, mode
}

func TestPrivateSystemFilesUseExactNativePublicByteWitness(t *testing.T) {
	home, _, _, _ := privateSystemFileFixture(t)
	result := observePrivateSurfaces(t.Context(), home, map[string]string{"name": "private-system-file-canary"},
		strings.NewReplacer("private-system-file-credential", "[redacted]"), nil)
	if !result.valid() || result.Code != "observed" || result.AuthorizedSystemFiles == 0 ||
		result.Files != result.AuthorizedSystemFiles+2 || result.OpaqueFiles != 0 || result.LeakObserved {
		t.Fatal("unchanged compiled public files became opaque", result)
	}
}

func TestPrivateSystemFilesNeverOverrideKnownLiteralGuards(t *testing.T) {
	for _, kind := range []string{"fact", "credential"} {
		home, _, contents, _ := privateSystemFileFixture(t)
		literal := string(contents[:min(48, len(contents))])
		var facts map[string]string
		var filter *strings.Replacer
		if kind == "fact" {
			facts = map[string]string{"name": literal}
		} else {
			filter = strings.NewReplacer(literal, "[redacted]")
		}
		result := observePrivateSurfaces(t.Context(), home, facts, filter, nil)
		if !result.valid() || !result.LeakObserved || result.Code != "leak_observed" || result.verdict() != Fail {
			t.Fatal("compiled public identity bypassed a known literal guard", kind, result)
		}
	}
}

func TestPrivateSystemFilesDoNotExemptRuntimeChangesOrOtherLocations(t *testing.T) {
	for _, kind := range []string{
		"literal", "credential", "escaped", "opaque", "extra", "other-generation", "workspace", "mode", "symlink",
	} {
		t.Run(kind, func(t *testing.T) {
			if kind == "mode" && runtime.GOOS == "windows" {
				t.Skip("native Windows bundle identity does not require POSIX modes")
			}
			home, name, contents, mode := privateSystemFileFixture(t)
			originalName := name
			before := observePrivateSurfaces(t.Context(), home, map[string]string{"name": "PRIVATE_SYSTEM_FILE_CANARY"},
				strings.NewReplacer("private-system-file-credential", "[redacted]"), nil)
			if before.Code != "observed" {
				t.Fatal("native baseline unavailable")
			}
			switch kind {
			case "literal":
				contents = append(contents, []byte("PRIVATE_SYSTEM_FILE_CANARY")...)
			case "credential":
				contents = append(contents, []byte("private-system-file-credential")...)
			case "escaped":
				contents = []byte(`{"value":"\u0050RIVATE_SYSTEM_FILE_CANARY"}`)
			case "opaque":
				contents = append(contents, []byte("\\opaque-runtime-change")...)
			case "extra":
				name = filepath.Join(filepath.Dir(name), "unexpected.txt")
			case "other-generation":
				name = filepath.Join(home, "skills", ".system", "generations", strings.Repeat("0", 64), "unknown.md")
			case "workspace":
				name = filepath.Join(home, "workspace", "skills", "copy.md")
			case "symlink":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(home, "skills", ".system", "active.json"), name); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "symlink" {
				if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
					t.Fatal(err)
				}
				if name == originalName {
					if err := os.Chmod(name, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(name, contents, 0o600); err != nil {
					t.Fatal(err)
				}
				if kind != "mode" {
					if err := os.Chmod(name, mode); err != nil {
						t.Fatal(err)
					}
				}
			}
			result := observePrivateSurfaces(t.Context(), home, map[string]string{"name": "PRIVATE_SYSTEM_FILE_CANARY"},
				strings.NewReplacer("private-system-file-credential", "[redacted]"), nil)
			wantLeak := kind == "literal" || kind == "credential" || kind == "escaped"
			if !result.valid() || result.LeakObserved != wantLeak ||
				wantLeak && result.Code != "leak_observed" || !wantLeak && result.Code != "unavailable" {
				t.Fatal("unknown or changed file acquired compiled public identity", kind, result)
			}
			wantAuthorized := before.AuthorizedSystemFiles
			if name == originalName {
				wantAuthorized--
			}
			if result.AuthorizedSystemFiles != wantAuthorized {
				t.Fatal("compiled file count does not reflect exact byte identities", kind, result)
			}
		})
	}
}

func TestPrivateSystemFileReceiptBoundsAndLegacyUnknowns(t *testing.T) {
	for _, observation := range []PrivateSurfaceObservation{
		{Scope: "owned_profile_files_v1", Code: "observed", Files: 1, AuthorizedSystemFiles: -1},
		{Scope: "owned_profile_files_v1", Code: "observed", Files: 1, AuthorizedSystemFiles: 2},
		{Scope: "owned_profile_files_v1", Code: "unavailable", Files: 1, OpaqueFiles: 1, AuthorizedSystemFiles: 1},
		{Scope: "owned_profile_files_v1", Code: "observed", Files: 1, AuthorizedDocuments: 1, AuthorizedSystemFiles: 1},
	} {
		if observation.valid() {
			t.Fatal("inconsistent compiled-file counts admitted")
		}
	}
	var legacy PrivateSurfaceObservation
	if json.Unmarshal([]byte(`{"scope":"owned_profile_files_v1","code":"unavailable","files":7,"opaque_files":5}`),
		&legacy) != nil || !legacy.valid() || legacy.AuthorizedSystemFiles != 0 || legacy.verdict() != Inconclusive {
		t.Fatal("old receipt gained compiled-file coverage")
	}
}
