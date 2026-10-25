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

// Authority owns a bounded set of live grants. All control and check
// operations share one lock. Its zero value denies access; use NewAuthority.
type Authority struct {
	mu     sync.Mutex
	policy *Policy
	grants map[string]*grantLifetime
	now    func() time.Time
	random io.Reader
	closed bool
}

func NewAuthority(policy *Policy) (*Authority, error) {
	if policy.Revision() == "" {
		return nil, ErrInvalidPolicy
	}
	return &Authority{
		policy: policy, grants: make(map[string]*grantLifetime), now: time.Now, random: rand.Reader,
	}, nil
}

// Approve issues a new grant after an authenticated, single-use, scope-bound
// operator approval. It never extends an existing grant or partially approves
// a list. The CA1 foundation does not implement approval authentication.
func (authority *Authority) Approve(approval Approval) (Grant, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.closed || authority.policy.Revision() == "" || authority.now == nil || authority.random == nil {
		return Grant{}, ErrUnavailable
	}
	accounts, err := authority.approvalAccounts(approval)
	if err != nil {
		return Grant{}, err
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
	authority.install(grant)
	grant.Accounts = slices.Clone(grant.Accounts)
	return grant, nil
}

func (authority *Authority) approvalAccounts(approval Approval) ([]string, error) {
	if !validPrincipal(approval.Principal) || approval.PolicyRevision != authority.policy.Revision() ||
		approval.Lifetime <= 0 || approval.Lifetime > MaxGrantLifetime ||
		len(approval.Accounts) == 0 || len(approval.Accounts) > MaxAccounts {
		return nil, ErrInvalidApproval
	}
	accounts := slices.Clone(approval.Accounts)
	slices.Sort(accounts)
	for index, alias := range accounts {
		account, exists := authority.policy.accounts[alias]
		if !validIdentifier(alias) || !exists || (index > 0 && alias == accounts[index-1]) {
			return nil, ErrInvalidApproval
		}
		if _, allowed := account.principals[approval.Principal]; !allowed {
			return nil, ErrDenied
		}
	}
	return accounts, nil
}

// Restore admits only previously verified approval metadata loaded by the
// trusted control plane from its private durable store. It is not a model or
// network API. A fresh authority is required, timestamps never renew, and all
// records are validated before any live lease is installed.
func (authority *Authority) Restore(grants []Grant) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.closed || authority.now == nil || len(authority.grants) != 0 || len(grants) > MaxActiveGrants {
		return ErrUnavailable
	}
	validated := make([]Grant, 0, len(grants))
	seen := make(map[string]bool, len(grants))
	now := authority.now()
	for _, grant := range grants {
		accounts, err := authority.approvalAccounts(Approval{
			Principal: grant.Principal, Accounts: grant.Accounts, PolicyRevision: grant.PolicyRevision,
			Lifetime: grant.ExpiresAt.Sub(grant.IssuedAt),
		})
		if err != nil || !validGrantID(grant.ID) || seen[grant.ID] || grant.IssuedAt.IsZero() {
			return ErrInvalidApproval
		}
		seen[grant.ID] = true
		if now.Before(grant.IssuedAt) || !now.Before(grant.ExpiresAt) {
			continue
		}
		grant.Accounts = accounts
		validated = append(validated, grant)
	}
	for _, grant := range validated {
		authority.install(grant)
	}
	return nil
}

// install requires the authority lock and an admitted, independently owned grant.
func (authority *Authority) install(grant Grant) {
	id := grant.ID
	lifetime := &grantLifetime{grant: grant, done: make(chan struct{})}
	authority.grants[id] = lifetime
	current := authority.now()
	delay := grant.ExpiresAt.Sub(current)
	if current.Before(grant.IssuedAt) {
		delay = 0
	}
	// One timer/channel per bounded grant, shared by all scoped consumers.
	// The callback retains the generation, so it cannot revoke a replacement
	// even if a future grant happens to reuse the same random reference.
	lifetime.timer = time.AfterFunc(delay, func() { authority.expire(id, lifetime) })
}

// Authorize checks current policy and grant state. Consumers must authenticate
// Request.Principal and obtain Origin from the live execution context.
func (authority *Authority) Authorize(grantID string, request Request) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return authority.authorize(grantID, request)
}

// Active reports only the current lifetime of an already-issued grant. It is
// used by the approval page, not to authorize a consumer or retrieve a value.
func (authority *Authority) Active(grantID string) bool {
	if authority == nil {
		return false
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.closed || authority.now == nil || !validGrantID(grantID) {
		return false
	}
	authority.prune(authority.now())
	_, active := authority.grants[grantID]
	return active
}

// authorize requires the authority lock.
func (authority *Authority) authorize(grantID string, request Request) error {
	if authority.closed {
		return ErrUnavailable
	}
	if !authority.policy.permits(request) {
		return ErrDenied
	}
	if authority.now == nil || !validGrantID(grantID) {
		return ErrApprovalRequired
	}
	authority.prune(authority.now())
	lifetime, exists := authority.grants[grantID]
	if !exists {
		return ErrApprovalRequired
	}
	grant := lifetime.grant
	if grant.Principal != request.Principal || grant.PolicyRevision != request.PolicyRevision ||
		!slices.Contains(grant.Accounts, request.Account) {
		return ErrDenied
	}
	return nil
}

// Revoke requires an authenticated owner identity from the trusted control
// plane. Once it returns, subsequent Authorize calls cannot use this grant.
// It also closes the shared consumer lease signal. Consumers must cancel
// in-flight work and quarantine sessions whose outcome cannot be acknowledged.
func (authority *Authority) Revoke(ownerID, grantID string) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.closed {
		return ErrUnavailable
	}
	if !validIdentifier(ownerID) || !validGrantID(grantID) {
		return ErrDenied
	}
	lifetime, exists := authority.grants[grantID]
	if !exists || lifetime.grant.Principal.OwnerID != ownerID {
		return ErrDenied
	}
	authority.invalidate(grantID)
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
	if authority.closed {
		return ErrUnavailable
	}
	if authority.policy.Revision() != policy.Revision() {
		authority.policy = policy
		for id := range authority.grants {
			authority.invalidate(id)
		}
	}
	return nil
}

func (authority *Authority) prune(now time.Time) {
	for id, lifetime := range authority.grants {
		grant := lifetime.grant
		// time.Now retains a monotonic component in production. The lower bound
		// also fails closed if an injected clock unexpectedly moves backwards.
		if now.Before(grant.IssuedAt) || !now.Before(grant.ExpiresAt) {
			authority.invalidate(id)
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
