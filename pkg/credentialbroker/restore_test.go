package credentialbroker

import (
	"strings"
	"testing"
	"time"
)

func TestRestoreKeepsDeadlineAndScopedLeaseLifetime(t *testing.T) {
	fixture := newGrantFixture(t)
	grant := mustApprove(t, fixture.authority, fixture.approval())
	fixture.elapsed.Store(int64(3 * time.Hour))
	restored, err := NewAuthority(fixture.policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	restored.now = fixture.authority.now
	if restored.Restore([]Grant{grant}) != nil {
		t.Fatal("valid private snapshot was not restored")
	}
	lease, err := restored.Lease(t.Context(), grant.ID, testRequest(fixture.policy))
	if err != nil {
		t.Fatal(err)
	}
	if remaining, remainErr := lease.Remaining(
		t.Context(),
		testRequest(fixture.policy),
	); remainErr != nil ||
		remaining != 17*time.Hour {
		t.Fatal("restore renewed the approval window")
	}
	fixture.elapsed.Store(int64(MaxGrantLifetime))
	if lease.Check(t.Context(), testRequest(fixture.policy)) == nil {
		t.Fatal("restored grant did not expire at its original deadline")
	}
	select {
	case <-lease.Done():
	default:
		t.Fatal("restored expiry did not cancel its consumer")
	}
}

func TestRestoreValidatesWholeSnapshotBeforeAdmission(t *testing.T) {
	for _, mode := range []string{
		"expired", "future", "overlong", "bad-id", "duplicate", "revision", "principal", "account", "capacity",
	} {
		t.Run(mode, func(t *testing.T) {
			fixture := newGrantFixture(t)
			grant := mustApprove(t, fixture.authority, fixture.approval())
			bad := grant
			bad.ID = strings.Repeat("a", 64)
			bad.Accounts = append([]string(nil), grant.Accounts...)
			snapshot := []Grant{grant, bad}
			switch mode {
			case "expired":
				bad.IssuedAt = bad.IssuedAt.Add(-MaxGrantLifetime)
				bad.ExpiresAt = grant.IssuedAt
			case "future":
				bad.IssuedAt = bad.IssuedAt.Add(time.Hour)
				bad.ExpiresAt = bad.ExpiresAt.Add(time.Hour)
			case "overlong":
				bad.ExpiresAt = bad.ExpiresAt.Add(time.Second)
			case "bad-id":
				bad.ID = "invalid"
			case "duplicate":
				bad.ID = grant.ID
			case "revision":
				bad.PolicyRevision = "other"
			case "principal":
				bad.Principal.ActorID = "other"
			case "account":
				bad.Accounts = []string{"missing"}
			case "capacity":
				snapshot = make([]Grant, MaxActiveGrants+1)
			}
			if mode != "capacity" {
				snapshot[1] = bad
			}
			restored, err := NewAuthority(fixture.policy)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(restored.Close)
			restored.now = fixture.authority.now
			err = restored.Restore(snapshot)
			if mode == "expired" || mode == "future" {
				if err != nil || !restored.Active(grant.ID) || restored.Active(bad.ID) {
					t.Fatal("snapshot lifetime admission was incorrect")
				}
			} else if err == nil || restored.Active(grant.ID) {
				t.Fatal("invalid snapshot partially admitted a valid grant")
			}
		})
	}
}
