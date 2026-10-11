package credentialbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func assertLeaseEnded(t *testing.T, lease GrantLease, origin string) {
	t.Helper()
	select {
	case <-lease.Done():
	default:
		t.Fatal("retired grant left its consumer signal live")
	}
	request := lease.request
	request.Origin = origin
	if lease.Check(context.Background(), request) == nil {
		t.Fatal("retired lease admitted a cookie-backed operation")
	}
}

func TestLeaseScopeAndOriginalExpiry(t *testing.T) {
	fixture := newGrantFixture(t)
	grant := mustApprove(t, fixture.authority, fixture.approval())
	request := testRequest(fixture.policy)
	lease, err := fixture.authority.Lease(context.Background(), grant.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	origin := request.Origin
	bound := request
	request.Account = "craigslist"
	request.Principal.ActorID = "changed-actor"
	request.PolicyRevision = "changed-revision"
	if lease.Check(context.Background(), bound) != nil {
		t.Fatal("caller mutation changed the bound lease")
	}
	for _, other := range []string{"https://accounts.craigslist.org", "http://signin.ebay.com", "", origin + "."} {
		current := bound
		current.Origin = other
		if lease.Check(context.Background(), current) == nil {
			t.Fatal("lease admitted an origin outside its account")
		}
	}
	fixture.elapsed.Store(int64(MaxGrantLifetime - time.Nanosecond))
	other, err := fixture.authority.Lease(context.Background(), grant.ID, testRequest(fixture.policy))
	if err != nil || other.Done() != lease.Done() || lease.Check(context.Background(), bound) != nil {
		t.Fatal("late borrow did not retain the original lifetime")
	}
	fixture.elapsed.Store(int64(MaxGrantLifetime))
	if lease.Check(context.Background(), bound) == nil {
		t.Fatal("late borrow renewed the grant")
	}
	assertLeaseEnded(t, lease, origin)
	assertLeaseEnded(t, other, origin)
}

func TestLeaseIdleExpiry(t *testing.T) {
	policy := mustPolicy(t, testRules())
	authority, err := NewAuthority(policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(authority.Close)
	grant := mustApprove(t, authority, Approval{
		Principal: testPrincipal(), Accounts: []string{"ebay"}, PolicyRevision: policy.Revision(),
		Lifetime: 500 * time.Millisecond,
	})
	request := testRequest(policy)
	lease, err := authority.Lease(context.Background(), grant.ID, request)
	if err != nil {
		t.Fatal("idle fixture expired before borrowing")
	}
	select {
	case <-lease.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("idle authenticated session received no expiry signal")
	}
	if time.Now().Before(grant.ExpiresAt) {
		t.Fatal("expiry timer shortened the approved lifetime")
	}
	assertLeaseEnded(t, lease, request.Origin)
}

func TestLeaseRevocationPolicyAndShutdown(t *testing.T) {
	for _, mode := range []string{"revoke", "binding-change", "shutdown", "backwards-clock"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newGrantFixture(t)
			grant := mustApprove(t, fixture.authority, fixture.approval())
			request := testRequest(fixture.policy)
			lease, err := fixture.authority.Lease(context.Background(), grant.ID, request)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "revoke":
				if fixture.authority.Revoke("other-owner", grant.ID) == nil ||
					lease.Check(context.Background(), request) != nil {
					t.Fatal("unverified owner invalidated a lease")
				}
				if fixture.authority.Revoke(testPrincipal().OwnerID, grant.ID) != nil {
					t.Fatal("owner revocation failed")
				}
			case "binding-change":
				if fixture.authority.ReplacePolicy(mustPolicy(t, testRules())) != nil ||
					lease.Check(context.Background(), request) != nil {
					t.Fatal("equivalent policy invalidated a lease")
				}
				rules := testRules()
				rules[0].ItemID = "replacement-item"
				if fixture.authority.ReplacePolicy(mustPolicy(t, rules)) != nil ||
					fixture.authority.ReplacePolicy(fixture.policy) != nil {
					t.Fatal("A-B-A policy transition failed")
				}
			case "shutdown":
				fixture.authority.Close()
				fixture.authority.Close()
				if fixture.authority.ReplacePolicy(fixture.policy) == nil {
					t.Fatal("policy replacement reopened a closed authority")
				}
				if _, err = fixture.authority.Approve(fixture.approval()); err == nil {
					t.Fatal("closed authority issued another grant")
				}
			case "backwards-clock":
				fixture.elapsed.Store(-1)
				if lease.Check(context.Background(), request) == nil {
					t.Fatal("backwards clock kept a lease active")
				}
			}
			assertLeaseEnded(t, lease, request.Origin)
		})
	}
}

func TestRetiredLeaseAndTimerCannotBindReusedReference(t *testing.T) {
	fixture := newGrantFixture(t)
	fixture.authority.random = bytes.NewReader(make([]byte, grantRandomBytes*2))
	grant := mustApprove(t, fixture.authority, fixture.approval())
	request := testRequest(fixture.policy)
	old, err := fixture.authority.Lease(context.Background(), grant.ID, request)
	if err != nil || fixture.authority.Revoke(testPrincipal().OwnerID, grant.ID) != nil {
		t.Fatal("retired generation setup failed")
	}
	replacement := mustApprove(t, fixture.authority, fixture.approval())
	if replacement.ID != grant.ID {
		t.Fatal("synthetic random collision was not exercised")
	}
	active, err := fixture.authority.Lease(context.Background(), replacement.ID, request)
	if err != nil || active.Done() == old.Done() {
		t.Fatal("replacement retained the old lifetime signal")
	}
	fixture.authority.expire(grant.ID, old.lifetime)
	assertLeaseEnded(t, old, request.Origin)
	if active.Check(context.Background(), request) != nil {
		t.Fatal("old expiry callback revoked its replacement")
	}
	select {
	case <-active.Done():
		t.Fatal("old expiry callback canceled its replacement")
	default:
	}
}

func TestLeaseDeniedScopeCancellationAndRedaction(t *testing.T) {
	fixture := newGrantFixture(t)
	grant := mustApprove(t, fixture.authority, fixture.approval())
	request := testRequest(fixture.policy)
	lease, err := fixture.authority.Lease(context.Background(), grant.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, dimension := range []string{"owner", "actor", "agent", "host", "account", "revision", "origin"} {
		other := request
		switch dimension {
		case "owner":
			other.Principal.OwnerID = "different"
		case "actor":
			other.Principal.ActorID = "different"
		case "agent":
			other.Principal.AgentID = "different"
		case "host":
			other.Principal.HostID = "different"
		case "account":
			other.Account = "craigslist"
		case "revision":
			other.PolicyRevision = "different"
		case "origin":
			other.Origin = "https://accounts.craigslist.org"
		}
		if _, borrowErr := fixture.authority.Lease(context.Background(), grant.ID, other); borrowErr == nil ||
			lease.Check(context.Background(), other) == nil {
			t.Fatalf("lease admitted changed %s", dimension)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, denied := range []context.Context{nil, ctx} {
		if _, err = fixture.authority.Lease(denied, grant.ID, request); err == nil ||
			lease.Check(denied, request) == nil {
			t.Fatal("missing/canceled context admitted lease use")
		}
	}
	if lease.Check(context.Background(), request) != nil {
		t.Fatal("one canceled operation revoked the owner's grant")
	}
	assertLeaseEnded(t, GrantLease{}, request.Origin)
	if _, err = (*Authority)(nil).Lease(context.Background(), grant.ID, request); err == nil {
		t.Fatal("nil authority created a lease")
	}
	(*Authority)(nil).Close()
	for _, value := range []any{lease, &lease, map[string]any{"lease": lease}} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if strings.Contains(string(encoded), grant.ID) || strings.Contains(fmt.Sprintf("%#v", value), grant.ID) {
			t.Fatal("generic serialization exposed private lease state")
		}
	}
}

func TestConcurrentLeaseChecksAndRetirement(t *testing.T) {
	fixture := newGrantFixture(t)
	grant := mustApprove(t, fixture.authority, fixture.approval())
	request := testRequest(fixture.policy)
	lease, err := fixture.authority.Lease(context.Background(), grant.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	var started, finished sync.WaitGroup
	for range 16 {
		started.Add(1)
		finished.Add(1)
		go func() {
			defer finished.Done()
			started.Done()
			for range 100 {
				if checkErr := lease.Check(context.Background(), request); checkErr != nil &&
					!errors.Is(checkErr, ErrApprovalRequired) {
					t.Error("concurrent lease check returned an unexpected result")
				}
			}
			<-lease.Done()
			if lease.Check(context.Background(), request) == nil {
				t.Error("observed retirement was followed by authorization")
			}
		}()
	}
	started.Wait()
	if fixture.authority.Revoke(testPrincipal().OwnerID, grant.ID) != nil {
		t.Fatal("concurrent revocation failed")
	}
	finished.Wait()
}

// Signal only after capturing the first Err result. This lets cancellation
// happen while an admitted operation waits on the real authority mutex.
type observedLeaseContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

func (ctx *observedLeaseContext) Err() error {
	err := ctx.Context.Err()
	ctx.once.Do(func() { close(ctx.checked) })
	return err
}

func TestLeaseCancellationWhileWaitingForAuthority(t *testing.T) {
	for _, operation := range []string{"borrow", "use"} {
		t.Run(operation, func(t *testing.T) {
			fixture := newGrantFixture(t)
			grant := mustApprove(t, fixture.authority, fixture.approval())
			request := testRequest(fixture.policy)
			lease, err := fixture.authority.Lease(context.Background(), grant.ID, request)
			if err != nil {
				t.Fatal(err)
			}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &observedLeaseContext{Context: parent, checked: make(chan struct{})}
			result := make(chan error, 1)
			fixture.authority.mu.Lock()
			go func() {
				if operation == "borrow" {
					_, borrowErr := fixture.authority.Lease(ctx, grant.ID, request)
					result <- borrowErr
					return
				}
				result <- lease.Check(ctx, request)
			}()
			<-ctx.checked
			cancel()
			fixture.authority.mu.Unlock()
			if checkErr := <-result; !errors.Is(checkErr, ErrDenied) {
				t.Fatal("context cancellation while waiting admitted credential use")
			}
			if lease.Check(context.Background(), request) != nil {
				t.Fatal("one canceled operation invalidated the grant")
			}
		})
	}
}
