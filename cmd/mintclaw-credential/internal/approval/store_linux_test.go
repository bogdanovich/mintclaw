//go:build linux

package approval

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"golang.org/x/sys/unix"
)

// This fixture accepts only an explicit synthetic marker at a fixed disposable
// container path. It has no path switch that can select an operator's key store.
func TestProtectedOwnerStoreFixture(t *testing.T) {
	if os.Getenv("MINTCLAW_SYNTHETIC_APPROVAL_FIXTURE") != "1" {
		t.Skip("disposable Linux fixture only")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("fixture requires Docker")
	}
	marker, err := os.ReadFile("/qual-approval/synthetic-only")
	if err != nil || string(marker) != "mintclaw-synthetic-approval-v1\n" || os.Geteuid() != 65532 ||
		os.Getegid() != 65532 {
		t.Fatal("fixture identity or synthetic marker invalid")
	}
	config := testConfig()
	config.StateDirectory = "/qual-approval/state"
	fixture := newFixture(t)
	controller, setup, err := New(config)
	if err != nil || setup == nil {
		t.Fatal("protected store open failed")
	}
	fixture.controller, fixture.setup = controller, setup
	t.Cleanup(func() { _ = controller.Close() })
	if _, _, err = New(config); !errors.Is(err, ErrUnavailable) {
		t.Fatal("second controller bypassed lifetime store lock")
	}
	fixture.enroll(t)
	_, ceremony := fixture.approval(t, time.Hour)
	grant, err := controller.FinishApproval(context.Background(), ceremony.ID,
		fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false))
	if err != nil {
		t.Fatal("filesystem-backed approval failed")
	}
	if err = controller.Close(); err != nil {
		t.Fatal("store close failed")
	}
	restarted, setup, err := New(config)
	if err != nil || setup != nil || restarted.owner.Credentials[0].Authenticator.SignCount != 1 ||
		len(restarted.owner.Credentials[0].Attestation.ClientDataJSON) == 0 {
		t.Fatal("complete enrollment/counter record did not survive restart")
	}
	if !errors.Is(restarted.Check(context.Background(), grant.ID, checkedRequest(grant.PolicyRevision)), ErrDenied) {
		t.Fatal("restart restored a grant")
	}
	if err = restarted.Close(); err != nil {
		t.Fatal("restart close failed")
	}
	filename := filepath.Join(config.StateDirectory, ownerFile)
	stat, err := os.Stat(filename)
	if err != nil || stat.Mode().Perm() != 0o600 {
		t.Fatal("owner key file is not private")
	}
	for _, change := range []string{"owner", "origin"} {
		other := config
		if change == "owner" {
			other.OwnerID = "other-owner"
			other.Accounts = nil
		} else {
			other.Origin = "https://other.example.org"
		}
		if _, _, err = New(other); !errors.Is(err, ErrUnavailable) {
			t.Fatal("store adopted another owner or relying party")
		}
	}
	for _, mode := range []os.FileMode{0o644, 0o640, 0o660} {
		if os.Chmod(filename, mode) != nil {
			t.Fatal("fixture file mode failed")
		}
		if _, _, err = New(config); !errors.Is(err, ErrUnavailable) {
			t.Fatal("unsafe record permissions accepted")
		}
	}
	if os.Chmod(filename, 0o600) != nil {
		t.Fatal("fixture permission restore failed")
	}
	acl := binary.LittleEndian.AppendUint32(nil, 2)
	for _, entry := range []struct {
		tag, permission uint16
		id              uint32
	}{
		{1, 6, ^uint32(0)}, {2, 0, 65531}, {4, 0, ^uint32(0)}, {16, 0, ^uint32(0)}, {32, 0, ^uint32(0)},
	} {
		acl = binary.LittleEndian.AppendUint16(acl, entry.tag)
		acl = binary.LittleEndian.AppendUint16(acl, entry.permission)
		acl = binary.LittleEndian.AppendUint32(acl, entry.id)
	}
	if unix.Setxattr(filename, "system.posix_acl_access", acl, 0) != nil {
		t.Fatal("fixture ACL setup failed")
	}
	if _, _, err = New(config); !errors.Is(err, ErrUnavailable) {
		t.Fatal("noncanonical owner record ACL accepted")
	}
	if unix.Removexattr(filename, "system.posix_acl_access") != nil {
		t.Fatal("fixture ACL cleanup failed")
	}
	link := filepath.Join(config.StateDirectory, "second-link")
	if os.Link(filename, link) != nil {
		t.Fatal("fixture hard link failed")
	}
	if _, _, err = New(config); !errors.Is(err, ErrUnavailable) {
		t.Fatal("multiply linked record accepted")
	}
	if os.Remove(link) != nil || os.Remove(filename) != nil {
		t.Fatal("fixture file removal failed")
	}
	if os.Symlink("/dev/null", filename) != nil {
		t.Fatal("fixture symlink failed")
	}
	if _, _, err = New(config); !errors.Is(err, ErrUnavailable) {
		t.Fatal("record symlink accepted")
	}
	if os.Remove(filename) != nil || unix.Mkfifo(filename, 0o600) != nil {
		t.Fatal("fixture FIFO failed")
	}
	if _, _, err = New(config); !errors.Is(err, ErrUnavailable) {
		t.Fatal("record FIFO accepted")
	}
	if os.Remove(filename) != nil {
		t.Fatal("fixture FIFO removal failed")
	}
	for _, body := range []string{"", "{}", `{"version":0}`, strings.Repeat("a", maxOwnerRecordBytes+1)} {
		if os.WriteFile(filename, []byte(body), 0o600) != nil {
			t.Fatal("invalid fixture write failed")
		}
		if _, _, err = New(config); !errors.Is(err, ErrUnavailable) {
			t.Fatal("malformed owner record accepted")
		}
		if os.Remove(filename) != nil {
			t.Fatal("invalid fixture removal failed")
		}
	}
	if os.Chmod(config.StateDirectory, 0o750) != nil {
		t.Fatal("fixture directory mode failed")
	}
	if _, _, err = New(config); !errors.Is(err, ErrUnavailable) {
		t.Fatal("shared owner directory accepted")
	}
}
