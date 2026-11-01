//go:build linux || darwin

package approval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
)

func runtimeStoreConfig(t *testing.T) Config {
	t.Helper()
	config := testConfig()
	config.StateDirectory = t.TempDir()
	if err := os.Chmod(config.StateDirectory, 0o700); err != nil {
		t.Fatal("private test directory setup failed")
	}
	return config
}

func TestRuntimeOwnerStorePersistsPasskeyAndOriginalGrantDeadline(t *testing.T) {
	config := runtimeStoreConfig(t)
	controller, setup, err := New(config)
	if err != nil || setup == nil {
		t.Fatal("ordinary private owner store unavailable")
	}
	t.Cleanup(func() { _ = controller.Close() })
	if _, _, err = New(config); !errors.Is(err, ErrUnavailable) {
		t.Fatal("second runtime bypassed the owner store lock")
	}
	fixture := newFixture(t)
	fixture.controller, fixture.setup = controller, setup
	fixture.enroll(t)
	_, ceremony := fixture.approval(t, time.Hour)
	grant, err := controller.FinishApproval(context.Background(), ceremony.ID,
		fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false))
	if err != nil {
		t.Fatal("filesystem-backed passkey approval failed")
	}
	if controller.Close() != nil {
		t.Fatal("runtime owner store close failed")
	}
	restarted, setup, err := New(config)
	if err != nil || setup != nil {
		t.Fatal("durable owner passkey was not loaded")
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if restarted.owner.Credentials[0].Authenticator.SignCount != 1 {
		t.Fatal("authenticator update was not durable")
	}
	request := checkedRequest(grant.PolicyRevision)
	if restarted.Check(context.Background(), grant.ID, request) != nil {
		t.Fatal("restart lost the verified credential grant")
	}
	restored := restarted.grants[grant.ID]
	if !restored.IssuedAt.Equal(grant.IssuedAt) || !restored.ExpiresAt.Equal(grant.ExpiresAt) {
		t.Fatal("restart renewed the original approval window")
	}
	lease, err := restarted.ActiveLogin(t.Context(), request)
	if err != nil || lease.Check(t.Context(), request) != nil {
		t.Fatal("restored grant could not supply a scoped private lease")
	}
	request.Principal.ActorID = "other"
	if restarted.Check(t.Context(), grant.ID, request) == nil {
		t.Fatal("restored approval admitted a different principal")
	}
	stat, err := os.Stat(filepath.Join(config.StateDirectory, ownerFile))
	if err != nil || stat.Mode().Perm() != 0o600 {
		t.Fatal("passkey record permissions are not private")
	}
}

func TestRuntimeOwnerStoreRejectsSharedOrIndirectRecords(t *testing.T) {
	for _, kind := range []string{"shared-directory", "shared-record", "symlink-directory", "symlink-record", "hardlink-record"} {
		t.Run(kind, func(t *testing.T) {
			config := runtimeStoreConfig(t)
			filename := filepath.Join(config.StateDirectory, ownerFile)
			switch kind {
			case "shared-directory":
				if os.Chmod(config.StateDirectory, 0o750) != nil {
					t.Fatal("shared directory fixture failed")
				}
			case "shared-record":
				if os.WriteFile(filename, []byte(`{"version":1}`), 0o644) != nil {
					t.Fatal("shared record fixture failed")
				}
			case "symlink-directory":
				link := filepath.Join(t.TempDir(), "linked")
				if os.Symlink(config.StateDirectory, link) != nil {
					t.Fatal("directory symlink fixture failed")
				}
				config.StateDirectory = link
			case "symlink-record":
				if os.Symlink(filepath.Join(t.TempDir(), "missing"), filename) != nil {
					t.Fatal("record symlink fixture failed")
				}
			case "hardlink-record":
				if os.WriteFile(filename, []byte(`{"version":1}`), 0o600) != nil ||
					os.Link(filename, filepath.Join(config.StateDirectory, "linked")) != nil {
					t.Fatal("record hardlink fixture failed")
				}
			}
			if _, _, err := New(config); !errors.Is(err, ErrUnavailable) {
				t.Fatal("unsafe runtime owner store accepted")
			}
		})
	}
}
