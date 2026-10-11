package credentialbroker

import (
	"bytes"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type grantFixture struct {
	policy    *Policy
	authority *Authority
	elapsed   atomic.Int64
}

func newGrantFixture(t *testing.T) *grantFixture {
	t.Helper()
	fixture := &grantFixture{policy: mustPolicy(t, testRules())}
	authority, err := NewAuthority(fixture.policy)
	if err != nil {
		t.Fatal(err)
	}
	fixture.authority = authority
	start := time.Unix(1800000000, 0)
	authority.now = func() time.Time { return start.Add(time.Duration(fixture.elapsed.Load())) }
	return fixture
}

func (fixture *grantFixture) approval() Approval {
	return Approval{
		Principal: testPrincipal(), Accounts: []string{"ebay"},
		PolicyRevision: fixture.policy.Revision(), Lifetime: MaxGrantLifetime,
	}
}

func mustApprove(t *testing.T, authority *Authority, approval Approval) Grant {
	t.Helper()
	grant, err := authority.Approve(approval)
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

func TestGrantScopeAndDefensiveCopies(t *testing.T) {
	fixture := newGrantFixture(t)
	approval := fixture.approval()
	grant := mustApprove(t, fixture.authority, approval)
	request := testRequest(fixture.policy)
	if err := fixture.authority.Authorize(grant.ID, request); err != nil {
		t.Fatal(err)
	}
	approval.Accounts[0] = "craigslist"
	grant.Accounts[0] = "craigslist"
	if err := fixture.authority.Authorize(grant.ID, request); err != nil {
		t.Fatal("caller mutation changed approved scope")
	}
	request.Account = "craigslist"
	request.Origin = "https://accounts.craigslist.org"
	if err := fixture.authority.Authorize(grant.ID, request); !errors.Is(err, ErrDenied) {
		t.Fatalf("unapproved account error = %v", err)
	}
	if err := fixture.authority.Authorize(
		"missing",
		testRequest(fixture.policy),
	); !errors.Is(
		err,
		ErrApprovalRequired,
	) {
		t.Fatalf("missing grant error = %v", err)
	}
	if decoded, err := hex.DecodeString(grant.ID); err != nil || len(decoded) != grantRandomBytes {
		t.Fatal("grant ID does not have 256 bits of random material")
	}

	approval = fixture.approval()
	approval.Accounts = []string{"ebay", "craigslist"}
	other := mustApprove(t, fixture.authority, approval)
	if other.ID == grant.ID || !slices.Equal(other.Accounts, []string{"craigslist", "ebay"}) {
		t.Fatal("new grant was not independent and canonical")
	}
	if err := fixture.authority.Authorize(other.ID, request); err != nil {
		t.Fatal(err)
	}
}

func TestGrantBindsEveryPrincipalDimension(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Principal)
	}{
		{name: "owner", mutate: func(principal *Principal) { principal.OwnerID = "owner-2" }},
		{name: "actor", mutate: func(principal *Principal) { principal.ActorID = "actor-2" }},
		{name: "agent", mutate: func(principal *Principal) { principal.AgentID = "agent-2" }},
		{name: "host", mutate: func(principal *Principal) { principal.HostID = "host-2" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			other := testPrincipal()
			test.mutate(&other)
			rules := testRules()
			rules[0].Principals = append(rules[0].Principals, other)
			policy := mustPolicy(t, rules)
			authority, err := NewAuthority(policy)
			if err != nil {
				t.Fatal(err)
			}
			grant := mustApprove(t, authority, Approval{
				Principal: testPrincipal(), Accounts: []string{"ebay"},
				PolicyRevision: policy.Revision(), Lifetime: time.Hour,
			})
			request := testRequest(policy)
			request.Principal = other
			if !policy.permits(request) {
				t.Fatal("test requires the other principal to be policy-allowed")
			}
			if err = authority.Authorize(grant.ID, request); !errors.Is(err, ErrDenied) {
				t.Fatalf("grant with changed principal error = %v", err)
			}
		})
	}
}

func TestGrantExpiresAtTwentyHoursWithoutSlidingRenewal(t *testing.T) {
	fixture := newGrantFixture(t)
	grant := mustApprove(t, fixture.authority, fixture.approval())
	if grant.ExpiresAt.Sub(grant.IssuedAt) != MaxGrantLifetime {
		t.Fatal("grant lifetime differs from approved duration")
	}
	request := testRequest(fixture.policy)
	fixture.elapsed.Store(int64(MaxGrantLifetime - time.Nanosecond))
	for range 10 {
		if err := fixture.authority.Authorize(grant.ID, request); err != nil {
			t.Fatal("grant expired before its boundary")
		}
	}
	fixture.elapsed.Store(int64(MaxGrantLifetime))
	if err := fixture.authority.Authorize(grant.ID, request); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("exact expiry error = %v", err)
	}
	fixture.elapsed.Store(int64(MaxGrantLifetime + time.Second))
	if err := fixture.authority.Authorize(grant.ID, request); !errors.Is(err, ErrApprovalRequired) {
		t.Fatal("expired grant regained access")
	}
	fresh := mustApprove(t, fixture.authority, fixture.approval())
	if fresh.ID == grant.ID || !fresh.IssuedAt.After(grant.ExpiresAt) {
		t.Fatal("fresh approval reused old grant")
	}
}

func TestShortGrantAndBackwardsClockFailClosed(t *testing.T) {
	fixture := newGrantFixture(t)
	approval := fixture.approval()
	approval.Lifetime = time.Minute
	grant := mustApprove(t, fixture.authority, approval)
	fixture.elapsed.Store(int64(time.Minute))
	if err := fixture.authority.Authorize(grant.ID, testRequest(fixture.policy)); !errors.Is(err, ErrApprovalRequired) {
		t.Fatal("short grant did not expire")
	}
	grant = mustApprove(t, fixture.authority, approval)
	fixture.elapsed.Store(0)
	if err := fixture.authority.Authorize(grant.ID, testRequest(fixture.policy)); !errors.Is(err, ErrApprovalRequired) {
		t.Fatal("backwards clock retained grant")
	}
	fixture.elapsed.Store(int64(time.Minute))
	if err := fixture.authority.Authorize(grant.ID, testRequest(fixture.policy)); !errors.Is(err, ErrApprovalRequired) {
		t.Fatal("clock recovery revived invalidated grant")
	}
}

func TestApprovalRejectsInvalidScopeAtomically(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Approval)
		want   error
	}{
		{
			name:   "missing owner",
			mutate: func(approval *Approval) { approval.Principal.OwnerID = "" },
			want:   ErrInvalidApproval,
		},
		{
			name:   "unknown principal",
			mutate: func(approval *Approval) { approval.Principal.ActorID = "other" },
			want:   ErrDenied,
		},
		{
			name:   "stale policy",
			mutate: func(approval *Approval) { approval.PolicyRevision = "old" },
			want:   ErrInvalidApproval,
		},
		{name: "zero lifetime", mutate: func(approval *Approval) { approval.Lifetime = 0 }, want: ErrInvalidApproval},
		{
			name:   "negative lifetime",
			mutate: func(approval *Approval) { approval.Lifetime = -time.Second },
			want:   ErrInvalidApproval,
		},
		{name: "over 20 hours", mutate: func(approval *Approval) { approval.Lifetime++ }, want: ErrInvalidApproval},
		{name: "no accounts", mutate: func(approval *Approval) { approval.Accounts = nil }, want: ErrInvalidApproval},
		{
			name:   "duplicate account",
			mutate: func(approval *Approval) { approval.Accounts = []string{"ebay", "ebay"} },
			want:   ErrInvalidApproval,
		},
		{
			name:   "unknown account in list",
			mutate: func(approval *Approval) { approval.Accounts = []string{"ebay", "missing"} },
			want:   ErrInvalidApproval,
		},
		{
			name:   "too many accounts",
			mutate: func(approval *Approval) { approval.Accounts = make([]string, MaxAccounts+1) },
			want:   ErrInvalidApproval,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGrantFixture(t)
			approval := fixture.approval()
			test.mutate(&approval)
			grant, err := fixture.authority.Approve(approval)
			if !errors.Is(err, test.want) || grant.ID != "" || len(fixture.authority.grants) != 0 {
				t.Fatalf("invalid approval result = %v, %v", grant, err)
			}
		})
	}
}

func TestRevocationAndRestart(t *testing.T) {
	fixture := newGrantFixture(t)
	grant := mustApprove(t, fixture.authority, fixture.approval())
	if err := fixture.authority.Revoke("other-owner", grant.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("other owner revoked grant")
	}
	request := testRequest(fixture.policy)
	if err := fixture.authority.Authorize(grant.ID, request); err != nil {
		t.Fatal("failed revocation changed grant")
	}
	restarted, err := NewAuthority(fixture.policy)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Authorize(grant.ID, request); !errors.Is(err, ErrApprovalRequired) {
		t.Fatal("grant survived new authority instance")
	}
	if err = fixture.authority.Revoke(testPrincipal().OwnerID, grant.ID); err != nil {
		t.Fatal(err)
	}
	if err = fixture.authority.Authorize(grant.ID, request); !errors.Is(err, ErrApprovalRequired) {
		t.Fatal("revoked grant authorized access")
	}
}

func TestPolicyReplacementInvalidatesGrantsAndPendingApprovals(t *testing.T) {
	fixture := newGrantFixture(t)
	grant := mustApprove(t, fixture.authority, fixture.approval())
	equivalent := testRules()
	slices.Reverse(equivalent)
	if err := fixture.authority.ReplacePolicy(mustPolicy(t, equivalent)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.authority.Authorize(grant.ID, testRequest(fixture.policy)); err != nil {
		t.Fatal("equivalent replacement revoked grant")
	}
	rules := testRules()
	rules[0].ItemID = "replacement-item"
	changed := mustPolicy(t, rules)
	if err := fixture.authority.ReplacePolicy(changed); err != nil {
		t.Fatal(err)
	}
	if err := fixture.authority.Authorize(grant.ID, testRequest(fixture.policy)); !errors.Is(err, ErrDenied) {
		t.Fatal("old policy revision authorized access")
	}
	if err := fixture.authority.Authorize(grant.ID, testRequest(changed)); !errors.Is(err, ErrApprovalRequired) {
		t.Fatal("policy update retained grant under new revision")
	}
	if _, err := fixture.authority.Approve(fixture.approval()); !errors.Is(err, ErrInvalidApproval) {
		t.Fatal("stale approval issued a grant")
	}
	approval := fixture.approval()
	approval.PolicyRevision = changed.Revision()
	fresh := mustApprove(t, fixture.authority, approval)
	if err := fixture.authority.ReplacePolicy(nil); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal("invalid policy replacement succeeded")
	}
	if err := fixture.authority.Authorize(fresh.ID, testRequest(changed)); err != nil {
		t.Fatal("failed replacement changed effective policy")
	}
	if err := fixture.authority.ReplacePolicy(mustPolicy(t, nil)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.authority.Authorize(fresh.ID, testRequest(changed)); !errors.Is(err, ErrDenied) {
		t.Fatal("default-deny replacement retained access")
	}
}

func TestCapacityIsBoundedAndExpiredSlotsAreReclaimed(t *testing.T) {
	fixture := newGrantFixture(t)
	var first Grant
	for index := range MaxActiveGrants {
		grant := mustApprove(t, fixture.authority, fixture.approval())
		if index == 0 {
			first = grant
		}
	}
	if _, err := fixture.authority.Approve(fixture.approval()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("capacity limit was exceeded")
	}
	if err := fixture.authority.Revoke(testPrincipal().OwnerID, first.ID); err != nil {
		t.Fatal(err)
	}
	mustApprove(t, fixture.authority, fixture.approval())
	fixture.elapsed.Store(int64(MaxGrantLifetime))
	mustApprove(t, fixture.authority, fixture.approval())
	if len(fixture.authority.grants) != 1 {
		t.Fatal("expired grants consumed capacity")
	}
}

func TestConcurrentApprovalRespectsCapacity(t *testing.T) {
	fixture := newGrantFixture(t)
	var finished sync.WaitGroup
	ids := make(chan string, MaxActiveGrants*2)
	for range MaxActiveGrants * 2 {
		finished.Add(1)
		go func() {
			defer finished.Done()
			grant, err := fixture.authority.Approve(fixture.approval())
			if err == nil {
				ids <- grant.ID
			} else if !errors.Is(err, ErrUnavailable) {
				t.Errorf("concurrent approval error = %v", err)
			}
		}()
	}
	finished.Wait()
	close(ids)
	unique := make(map[string]struct{})
	for id := range ids {
		if _, exists := unique[id]; exists {
			t.Error("concurrent approvals reused a grant")
		}
		unique[id] = struct{}{}
	}
	if len(unique) != MaxActiveGrants {
		t.Fatalf("concurrent approved grants = %d, want %d", len(unique), MaxActiveGrants)
	}
}

type failingRandom struct{}

func (failingRandom) Read([]byte) (int, error) {
	return 0, errors.New("synthetic-private-provider-diagnostic")
}

func TestRandomFailuresAndCollisionsAreSafe(t *testing.T) {
	fixture := newGrantFixture(t)
	fixture.authority.random = failingRandom{}
	grant, err := fixture.authority.Approve(fixture.approval())
	if grant.ID != "" || !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "synthetic") {
		t.Fatalf("private failure projection = %v, %v", grant, err)
	}
	fixture.authority.random = bytes.NewReader(make([]byte, grantRandomBytes*4))
	grant = mustApprove(t, fixture.authority, fixture.approval())
	if _, err = fixture.authority.Approve(fixture.approval()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("repeated random ID overwrote an existing grant")
	}
	if err = fixture.authority.Authorize(grant.ID, testRequest(fixture.policy)); err != nil {
		t.Fatal("collision changed live grant")
	}
}

func TestZeroValueAuthorityDenies(t *testing.T) {
	var authority Authority
	fixture := newGrantFixture(t)
	if _, err := authority.Approve(fixture.approval()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("zero authority issued a grant")
	}
	if err := authority.Authorize("anything", testRequest(fixture.policy)); !errors.Is(err, ErrDenied) {
		t.Fatal("zero authority authorized access")
	}
	if _, err := NewAuthority(nil); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal("authority accepted missing policy")
	}
}

func TestConcurrentChecksAndRevocation(t *testing.T) {
	fixture := newGrantFixture(t)
	grant := mustApprove(t, fixture.authority, fixture.approval())
	request := testRequest(fixture.policy)
	const workers = 16
	var started, finished sync.WaitGroup
	started.Add(workers)
	finished.Add(workers)
	revoked := make(chan struct{})
	for range workers {
		go func() {
			defer finished.Done()
			started.Done()
			for range 100 {
				if err := fixture.authority.Authorize(
					grant.ID,
					request,
				); err != nil &&
					!errors.Is(err, ErrApprovalRequired) {
					t.Errorf("concurrent authorization error = %v", err)
				}
			}
			<-revoked
			for range 100 {
				if err := fixture.authority.Authorize(grant.ID, request); !errors.Is(err, ErrApprovalRequired) {
					t.Error("authorization succeeded after revocation returned")
				}
			}
		}()
	}
	started.Wait()
	if err := fixture.authority.Revoke(testPrincipal().OwnerID, grant.ID); err != nil {
		t.Error(err)
	}
	close(revoked)
	finished.Wait()
}
