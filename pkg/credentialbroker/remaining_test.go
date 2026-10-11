package credentialbroker

import (
	"context"
	"testing"
	"time"
)

func TestLeaseRemainingUsesOriginalGenerationAndCurrentScope(t *testing.T) {
	fixture := newGrantFixture(t)
	grant := mustApprove(t, fixture.authority, fixture.approval())
	request := testRequest(fixture.policy)
	lease, err := fixture.authority.Lease(context.Background(), grant.ID, request)
	if err != nil {
		t.Fatal("lease unavailable")
	}
	fixture.elapsed.Store(int64(7 * time.Hour))
	remaining, err := lease.Remaining(context.Background(), request)
	if err != nil || remaining != 13*time.Hour {
		t.Fatal("remaining lifetime renewed the grant")
	}
	changed := request
	changed.Principal.ActorID = "other"
	if _, err = lease.Remaining(context.Background(), changed); err == nil {
		t.Fatal("remaining lifetime admitted another actor")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = lease.Remaining(canceled, request); err == nil {
		t.Fatal("remaining lifetime ignored cancellation")
	}
	fixture.elapsed.Store(int64(MaxGrantLifetime))
	if _, err = lease.Remaining(context.Background(), request); err == nil {
		t.Fatal("expired lease returned a new lifetime")
	}
	if _, err = (GrantLease{}).Remaining(context.Background(), request); err == nil {
		t.Fatal("zero lease returned a lifetime")
	}
}
