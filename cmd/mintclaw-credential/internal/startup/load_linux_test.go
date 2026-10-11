//go:build linux

package startup

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func canonicalACL(permission uint16, uid uint32) []byte {
	acl := make([]byte, 44)
	binary.LittleEndian.PutUint32(acl, 2)
	for index, entry := range []struct {
		tag, permission uint16
		id              uint32
	}{
		{1, permission, ^uint32(0)},
		{2, permission, uid},
		{4, 0, ^uint32(0)},
		{16, permission, ^uint32(0)},
		{32, 0, ^uint32(0)},
	} {
		offset := 4 + index*8
		binary.LittleEndian.PutUint16(acl[offset:], entry.tag)
		binary.LittleEndian.PutUint16(acl[offset+2:], entry.permission)
		binary.LittleEndian.PutUint32(acl[offset+4:], entry.id)
	}
	return acl
}

func TestEffectiveCredentialAccessRejectsBroadOrAmbiguousACLs(t *testing.T) {
	identity := serviceIdentity{65532, 65532}
	for _, directory := range []bool{false, true} {
		permission, mode := uint16(4), uint32(0o440)
		if directory {
			permission, mode = 5, 0o550
		}
		stat := unix.Stat_t{Uid: 0, Gid: 0, Mode: mode}
		acl := canonicalACL(permission, identity.uid)
		if !validCredentialAccess(stat, acl, identity, directory) {
			t.Fatal("actual systemd service-only ACL shape was rejected")
		}
		for _, modify := range []func(*unix.Stat_t, []byte) []byte{
			func(stat *unix.Stat_t, acl []byte) []byte { stat.Mode |= 0o004; return acl },
			func(stat *unix.Stat_t, acl []byte) []byte { stat.Mode |= 0o200; return acl },
			func(stat *unix.Stat_t, acl []byte) []byte { stat.Mode |= 0o4000; return acl },
			func(stat *unix.Stat_t, acl []byte) []byte { stat.Uid++; return acl },
			func(stat *unix.Stat_t, acl []byte) []byte { stat.Gid++; return acl },
			func(_ *unix.Stat_t, acl []byte) []byte { acl[0]++; return acl },
			func(_ *unix.Stat_t, acl []byte) []byte {
				binary.LittleEndian.PutUint32(acl[16:], identity.uid+1)
				return acl
			},
			func(_ *unix.Stat_t, acl []byte) []byte { binary.LittleEndian.PutUint16(acl[22:], 4); return acl },
			func(_ *unix.Stat_t, acl []byte) []byte { binary.LittleEndian.PutUint16(acl[38:], 4); return acl },
			func(_ *unix.Stat_t, acl []byte) []byte { binary.LittleEndian.PutUint16(acl[12:], 8); return acl },
			func(_ *unix.Stat_t, acl []byte) []byte { return append(acl, make([]byte, 8)...) },
			func(_ *unix.Stat_t, _ []byte) []byte { return nil },
		} {
			changed := stat
			changedACL := modify(&changed, bytes.Clone(acl))
			if validCredentialAccess(changed, changedACL, identity, directory) {
				t.Fatal("broader, ambiguous, or unauthenticated ACL authority was admitted")
			}
		}
		fallback := unix.Stat_t{Uid: identity.uid, Gid: identity.gid, Mode: uint32(permission) << 6}
		if !validCredentialAccess(fallback, nil, identity, directory) ||
			validCredentialAccess(fallback, acl, identity, directory) {
			t.Fatal("service-owned no-ACL fallback was confused with ACL delivery")
		}
	}
}

func TestCancelledAndUntrustedStartupNeverUseEnvironmentToken(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", tokenCanary)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, scenario := range []struct {
		ctx       context.Context
		directory string
	}{
		{nil, "/run/credentials/synthetic"},
		{ctx, "/run/credentials/synthetic"},
		{context.Background(), "relative/" + tokenCanary},
		{context.Background(), "/run/credentials/../" + tokenCanary},
		{context.Background(), "/run//" + tokenCanary},
		{context.Background(), "/run/" + strings.Repeat("a", 4096)},
	} {
		token, err := loadServiceToken(scenario.ctx, scenario.directory)
		if !errors.Is(err, errUnavailable) || token.state != nil || strings.Contains(err.Error(), tokenCanary) {
			t.Fatal("untrusted startup retained or disclosed an environment credential")
		}
	}
}

// Only the disposable qualification script enables this fixture. A production
// credential directory cannot be selected through its environment.
func TestCredentialDeliveryFixture(t *testing.T) {
	mode := os.Getenv("MINTCLAW_CREDENTIAL_DELIVERY_FIXTURE")
	if mode == "" {
		t.Skip("requires disposable synthetic credential-delivery qualification")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("synthetic fixture requires its disposable container")
	}
	marker, err := os.ReadFile("/qual-credentials/synthetic-only")
	if err != nil || string(marker) != "mintclaw-synthetic-loader-v1" {
		t.Fatal("synthetic fixture marker is missing")
	}
	if mode != "readonly" && mode != "writable" && mode != "unhardened" && mode != "root" &&
		mode != "root-group" && mode != "supplementary" {
		t.Fatal("synthetic fixture mode is invalid")
	}
	if mode == "unhardened" {
		privileges, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
		if err != nil || privileges != 0 {
			t.Fatal("unhardened fixture did not establish its intended process state")
		}
	}
	if mode == "root" && os.Geteuid() != 0 || mode != "root" && os.Geteuid() != 65532 {
		t.Fatal("synthetic fixture did not establish its intended identity")
	}
	if mode == "root-group" && os.Getegid() != 0 {
		t.Fatal("root-group fixture did not establish its intended primary group")
	}
	if mode == "supplementary" {
		groups, err := os.Getgroups()
		found := false
		for _, group := range groups {
			found = found || group == 1
		}
		if err != nil || !found {
			t.Fatal("supplementary-group fixture did not establish its intended membership")
		}
	}
	for _, name := range []string{
		"canonical", "fallback", "extra-user", "group-readable", "other-readable", "owner-writable",
		"multiple-links", "symlink", "fifo", "empty", "oversize", "newline", "non-ascii", "unsafe-parent",
	} {
		t.Run(name, func(t *testing.T) {
			directory := "/qual-credentials/" + name
			if name == "unsafe-parent" {
				directory += "/source"
			}
			token, err := loadServiceToken(context.Background(), directory)
			defer token.destroy()
			admitted := mode == "readonly" && (name == "canonical" || name == "fallback")
			if !admitted {
				if !errors.Is(err, errUnavailable) || token.state != nil {
					t.Fatal("unsafe synthetic credential source was admitted")
				}
				return
			}
			if err != nil {
				t.Fatal("qualified synthetic credential source was rejected")
			}
			if token.consume(func(value []byte) error {
				if !bytes.Equal(value, []byte(tokenCanary)) {
					return errUnavailable
				}
				return nil
			}) != nil {
				t.Fatal("qualified source did not deliver the expected synthetic input")
			}
		})
	}
}
