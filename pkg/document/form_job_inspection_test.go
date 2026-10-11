package document

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestInspectFormJobDoesNotExpireOrRewriteState(t *testing.T) {
	store, options := newTestFormJobStore(t)
	store.now = func() time.Time { return time.Now().Add(-2 * options.Retention) }
	owner := testFormJobOwner()
	record, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
	if err != nil {
		t.Fatal(err)
	}
	record, _, err = appendTestFormValue(t, store, record, owner, "PRIVATE_INSPECTION_CANARY")
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	paths := []string{formJobStatePath(options), filepath.Join(options.KeyRoot, formJobKeyFileName)}
	contents := make([][]byte, len(paths))
	infos := make([]os.FileInfo, len(paths))
	for index, path := range paths {
		contents[index], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		infos[index], err = os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path + ".lock"); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	got, err := InspectFormJob(options, record.JobID, owner)
	if err != nil || !reflect.DeepEqual(got, record) {
		t.Fatalf("native public record not preserved: %v", err)
	}
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), "PRIVATE_INSPECTION_CANARY") ||
		got.State == FormJobExpired || got.ExpiresAt >= time.Now().UnixMilli() {
		t.Fatal("inspection leaked a value, expired a job, or changed its expiry")
	}
	got.Fields[0].FieldID = "changed"
	again, err := InspectFormJob(options, record.JobID, owner)
	if err != nil || !reflect.DeepEqual(again, record) {
		t.Fatal("public result aliases later inspection")
	}
	for index, path := range paths {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, contents[index]) {
			t.Fatal("inspection rewrote state or key bytes")
		}
		info, err := os.Stat(path)
		if err != nil || !info.ModTime().Equal(infos[index].ModTime()) || info.Mode() != infos[index].Mode() {
			t.Fatal("inspection changed state/key metadata")
		}
		if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
			t.Fatal("inspection created a lock file")
		}
	}
}

func TestInspectFormJobUsesNativeOwnerAndIntegrityChecks(t *testing.T) {
	for _, name := range []string{"owner", "missing key", "wrong key", "tampered state", "missing state", "budget"} {
		t.Run(name, func(t *testing.T) {
			store, options := newTestFormJobStore(t)
			owner := testFormJobOwner()
			record, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			keyPath := filepath.Join(options.KeyRoot, formJobKeyFileName)
			want := ErrFormJobRecordCorrupt
			switch name {
			case "owner":
				owner.SenderID = "foreign"
				want = ErrFormJobUnauthorized
			case "missing key":
				err = os.Remove(keyPath)
				want = ErrFormJobKeyUnavailable
			case "wrong key":
				err = os.WriteFile(keyPath, bytes.Repeat([]byte("x"), 32), 0o600)
			case "tampered state":
				data, readErr := os.ReadFile(formJobStatePath(options))
				if readErr != nil {
					t.Fatal(readErr)
				}
				data = bytes.ReplaceAll(data, []byte(record.SourceDigest), []byte(strings.Repeat("b", 64)))
				err = os.WriteFile(formJobStatePath(options), data, 0o600)
			case "missing state":
				err = os.Remove(formJobStatePath(options))
				want = ErrFormJobNotFound
			case "budget":
				options.MaxBytes = 4096
				err = os.WriteFile(formJobStatePath(options), bytes.Repeat([]byte(" "), 4097), 0o600)
				want = ErrFormJobStoreUnavailable
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := InspectFormJob(options, record.JobID, owner)
			if !errors.Is(err, want) || !reflect.DeepEqual(got, FormJobRecord{}) {
				t.Fatalf("invalid observation error = %v, want %v; no partial record", err, want)
			}
			if name == "missing key" || name == "missing state" {
				path := keyPath
				if name == "missing state" {
					path = formJobStatePath(options)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("inspection created missing state/key material")
				}
			}
		})
	}
}

func TestInspectFormJobRefusesUnsafeDirectoryWithoutRepair(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission and symlink admission")
	}
	for _, name := range []string{"broad root", "symlink store"} {
		t.Run(name, func(t *testing.T) {
			store, options := newTestFormJobStore(t)
			owner := testFormJobOwner()
			record, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			if name == "broad root" {
				err = os.Chmod(options.KeyRoot, 0o755)
			} else {
				directory := filepath.Join(options.StateRoot, "document_form_jobs")
				if err = os.Rename(directory, directory+"-original"); err == nil {
					err = os.Symlink(directory+"-original", directory)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, err := InspectFormJob(options, record.JobID, owner); err == nil ||
				!reflect.DeepEqual(got, FormJobRecord{}) {
				t.Fatal("unsafe root was admitted")
			}
			if name == "broad root" {
				info, err := os.Stat(options.KeyRoot)
				if err != nil || info.Mode().Perm() != 0o755 {
					t.Fatal("inspection repaired root permissions")
				}
			}
		})
	}
}

func TestFormJobOwnerFromProtectedRouteMatchesNativeOwner(t *testing.T) {
	owner := testFormJobOwner()
	for _, fallback := range []bool{false, true} {
		route := testProtectedAnswerRoute(owner)
		if fallback {
			route.SessionKey = owner.RouteSessionKey
			route.RouteSessionKey = ""
		}
		got, err := FormJobOwnerFromProtectedRoute(owner.WorkspaceID, route)
		if err != nil || !reflect.DeepEqual(got, owner) {
			t.Fatal("inspection and native protected ingress owner mapping differ")
		}
	}
	route := testProtectedAnswerRoute(owner)
	route.SenderID = ""
	if got, err := FormJobOwnerFromProtectedRoute(owner.WorkspaceID, route); err == nil ||
		!reflect.DeepEqual(got, FormJobOwner{}) {
		t.Fatal("invalid owner mapping was admitted")
	}
}
