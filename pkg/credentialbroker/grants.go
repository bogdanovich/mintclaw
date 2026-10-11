package credentialbroker

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"slices"
	"sync"
	"time"
)

const (
	MaxGrantLifetime = 20 * time.Hour
	MaxActiveGrants  = 64
	grantRandomBytes = 32
)

// Approval is the exact scope verified by the trusted operator approval flow.
// Its lifetime must be positive and at most MaxGrantLifetime, without clamping.
type Approval struct {
	Principal      Principal
	Accounts       []string
	PolicyRevision string
	Lifetime       time.Duration
}

// Grant is a metadata snapshot, not an authorization capability by itself.
// Accounts is defensively copied; callers cannot change the live grant.
type Grant struct {
	ID             string
	Principal      Principal
	Accounts       []string
	PolicyRevision string
	IssuedAt       time.Time
	ExpiresAt      time.Time
}

// Authority owns a bounded set of non-persistent grants. All control and check
// operations share one lock. Its zero value denies access; use NewAuthority.
type Authority struct {
	mu     sync.Mutex
	policy *Policy
	grants map[string]Grant
	now    func() time.Time
	random io.Reader
}

func NewAuthority(policy *Policy) (*Authority, error) {
	if policy.Revision() == "" {
		return nil, ErrInvalidPolicy
	}
	return &Authority{
		policy: policy, grants: make(map[string]Grant), now: time.Now, random: rand.Reader,
	}, nil
}

// Approve issues a new grant after an authenticated, single-use, scope-bound
// operator approval. It never extends an existing grant or partially approves
// a list. The CA1 foundation does not implement approval authentication.
func (authority *Authority) Approve(approval Approval) (Grant, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.policy.Revision() == "" || authority.now == nil || authority.random == nil {
		return Grant{}, ErrUnavailable
	}
	if !validPrincipal(approval.Principal) || approval.PolicyRevision != authority.policy.Revision() ||
		approval.Lifetime <= 0 || approval.Lifetime > MaxGrantLifetime ||
		len(approval.Accounts) == 0 || len(approval.Accounts) > MaxAccounts {
		return Grant{}, ErrInvalidApproval
	}
	accounts := slices.Clone(approval.Accounts)
	slices.Sort(accounts)
	for index, alias := range accounts {
		account, exists := authority.policy.accounts[alias]
		if !validIdentifier(alias) || !exists || (index > 0 && alias == accounts[index-1]) {
			return Grant{}, ErrInvalidApproval
		}
		if _, allowed := account.principals[approval.Principal]; !allowed {
			return Grant{}, ErrDenied
		}
	}
	now := authority.now()
	authority.prune(now)
	if len(authority.grants) >= MaxActiveGrants {
		return Grant{}, ErrUnavailable
	}
	id, err := authority.newGrantID()
	if err != nil {
		return Grant{}, err
	}
	grant := Grant{
		ID: id, Principal: approval.Principal, Accounts: accounts,
		PolicyRevision: approval.PolicyRevision, IssuedAt: now, ExpiresAt: now.Add(approval.Lifetime),
	}
	authority.grants[id] = grant
	grant.Accounts = slices.Clone(grant.Accounts)
	return grant, nil
}

// Authorize checks current policy and grant state. Consumers must authenticate
// Request.Principal and obtain Origin from the live execution context.
func (authority *Authority) Authorize(grantID string, request Request) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if !authority.policy.permits(request) {
		return ErrDenied
	}
	if authority.now == nil || !validGrantID(grantID) {
		return ErrApprovalRequired
	}
	authority.prune(authority.now())
	grant, exists := authority.grants[grantID]
	if !exists {
		return ErrApprovalRequired
	}
	if grant.Principal != request.Principal || grant.PolicyRevision != request.PolicyRevision ||
		!slices.Contains(grant.Accounts, request.Account) {
		return ErrDenied
	}
	return nil
}

// Revoke requires an authenticated owner identity from the trusted control
// plane. Once it returns, subsequent Authorize calls cannot use this grant.
// In-flight execution cancellation is the consumer's separate responsibility.
func (authority *Authority) Revoke(ownerID, grantID string) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if !validIdentifier(ownerID) || !validGrantID(grantID) {
		return ErrDenied
	}
	grant, exists := authority.grants[grantID]
	if !exists || grant.Principal.OwnerID != ownerID {
		return ErrDenied
	}
	delete(authority.grants, grantID)
	return nil
}

// ReplacePolicy invalidates all grants when the effective authority changes.
// Equivalent normalized policies preserve grants. Old approval challenges
// cannot issue new grants while their revision differs from the current one.
// The approval transport must also discard pending challenges on a policy
// transition, including a later return to a previously configured policy.
func (authority *Authority) ReplacePolicy(policy *Policy) error {
	if policy.Revision() == "" {
		return ErrInvalidPolicy
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.policy.Revision() != policy.Revision() {
		authority.policy = policy
		authority.grants = make(map[string]Grant)
	}
	return nil
}

func (authority *Authority) prune(now time.Time) {
	for id, grant := range authority.grants {
		// time.Now retains a monotonic component in production. The lower bound
		// also fails closed if an injected clock unexpectedly moves backwards.
		if now.Before(grant.IssuedAt) || !now.Before(grant.ExpiresAt) {
			delete(authority.grants, id)
		}
	}
}

func (authority *Authority) newGrantID() (string, error) {
	for range 3 {
		var randomBytes [grantRandomBytes]byte
		if _, err := io.ReadFull(authority.random, randomBytes[:]); err != nil {
			return "", ErrUnavailable
		}
		id := hex.EncodeToString(randomBytes[:])
		if _, exists := authority.grants[id]; !exists {
			return id, nil
		}
	}
	return "", ErrUnavailable
}

func validGrantID(id string) bool {
	if len(id) != grantRandomBytes*2 {
		return false
	}
	for _, char := range id {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
