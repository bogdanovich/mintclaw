package identity

import (
	"strings"
	"testing"
)

func TestLocalActorIDIsStableOpaqueAndMachineScoped(t *testing.T) {
	identity := LocalOperatorIdentity{UID: "501", Username: "operator", Hostname: "workstation"}
	first, err := LocalActorID(identity)
	if err != nil {
		t.Fatalf("LocalActorID() error = %v", err)
	}
	second, err := LocalActorID(identity)
	if err != nil {
		t.Fatalf("LocalActorID() repeat error = %v", err)
	}
	if first != second || !strings.HasPrefix(first, "local:") {
		t.Fatalf("local actor IDs = %q and %q", first, second)
	}
	if strings.Contains(first, identity.Username) || strings.Contains(first, identity.Hostname) {
		t.Fatalf("local actor ID exposes identity material: %q", first)
	}
	other, err := LocalActorID(LocalOperatorIdentity{UID: "501", Username: "operator", Hostname: "other"})
	if err != nil {
		t.Fatalf("LocalActorID(other host) error = %v", err)
	}
	if other == first {
		t.Fatal("local actor ID did not distinguish hosts")
	}
	renamed, err := LocalActorID(LocalOperatorIdentity{UID: "501", Username: "renamed", Hostname: "workstation"})
	if err != nil {
		t.Fatalf("LocalActorID(renamed account) error = %v", err)
	}
	if renamed != first {
		t.Fatal("stable operating-system account ID changed after a username rename")
	}
}

func TestLocalActorIDRejectsIncompleteOrUnsafeIdentity(t *testing.T) {
	for _, identity := range []LocalOperatorIdentity{
		{Hostname: "workstation"},
		{UID: "501"},
		{UID: "501\n", Hostname: "workstation"},
	} {
		if _, err := LocalActorID(identity); err == nil {
			t.Fatalf("LocalActorID(%+v) succeeded", identity)
		}
	}
}
