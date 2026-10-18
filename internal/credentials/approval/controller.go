package approval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

const (
	MaxPending       = 64
	PendingLifetime  = 5 * time.Minute
	maxResponseBytes = 65536
)

var (
	ErrDenied      = errors.New("phone approval denied")
	ErrUnavailable = errors.New("phone approval unavailable")
)

// Config is protected startup configuration, never request or model data.
// One owner and one exact HTTPS relying-party origin are admitted initially.
type Config struct {
	OwnerID        string
	Origin         string
	StateDirectory string
	Accounts       []credentialbroker.AccountRule
	// PagePath is "/" for the legacy standalone handler, or "/credentials/"
	// when mounted in the existing application.
	PagePath              string
	TrustLoopbackTLSProxy bool
}

// LoginBinding is private service-consumer metadata, never a model result. It
// snapshots exact provider IDs from the revision authorized under the lock.
// Consumers must check the lease and live browser authority again before use.
type LoginBinding struct{ VaultID, ItemID, PolicyRevision string }

func (LoginBinding) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private login binding]")
}
func (LoginBinding) MarshalJSON() ([]byte, error) { return []byte(`"[private login binding]"`), nil }

// Scope is the complete, service-owned scope displayed by the phone. It contains
// aliases, not provider bindings. Returned account slices are independent copies.
type Scope struct {
	Principal       credentialbroker.Principal `json:"principal"`
	Accounts        []string                   `json:"accounts"`
	PolicyRevision  string                     `json:"policy_revision"`
	DurationSeconds int                        `json:"duration_seconds"`
	GrantID         string                     `json:"grant_id,omitempty"`
}

// Ceremony contains public WebAuthn options and a locator. The options are a
// defensive JSON copy, not the private verifier SessionData. IDs never approve
// access by themselves. Enrollment locators must use protected owner setup.
type Ceremony struct {
	ID      string `json:"id"`
	Options []byte `json:"-"`
	Scope   Scope  `json:"scope"`
}

type pendingRequest struct {
	scope     Scope
	issued    time.Time
	ceremony  string
	attempted bool
	grant     string
}

type ceremonyKind uint8

const (
	enroll ceremonyKind = iota + 1
	approve
	revoke
	verify
)

type pendingCeremony struct {
	kind    ceremonyKind
	session *webauthn.SessionData
	scope   Scope
	request string
	issued  time.Time
	options []byte
	// Private operator issue time, retained independently of the browser data.
	enrollmentIssued time.Time
}

type binding struct {
	vaultID, itemID string
	origins         []string
}

// Controller exclusively owns mutable approval authority. No Authority pointer
// or direct grant-issuing entry point is exposed. All transitions, verification,
// persistence acknowledgements, and grant issuance share this concurrency boundary.
type Controller struct {
	mu         sync.Mutex
	ownerID    string
	origin     string
	pagePath   string
	tlsProxy   bool
	policy     *credentialbroker.Policy
	bindings   map[string]binding
	authority  *credentialbroker.Authority
	owner      ownerRecord
	store      ownerStore
	verifier   *webauthn.WebAuthn
	requests   map[string]*pendingRequest
	ceremonies map[string]pendingCeremony
	grants     map[string]credentialbroker.Grant
	now        func() time.Time
	random     io.Reader
	failed     bool
}

// OwnerSetup is an unforgeable, controller-bound initial-enrollment capability.
// New returns it only to trusted startup when no owner is active, including an
// interrupted initial enrollment. Startup must retain it outside the public
// approval handler and release ceremonies only after an authenticated protected
// owner action. Its zero value denies access.
// It is neither a model tool nor evidence that the holder is a human operator.
type OwnerSetup struct{ controller *Controller }

// New opens the private owner store for the trusted runtime. Startup must
// retain OwnerSetup outside model tools and expose enrollment only through a
// private operator setup path. No separate host or service role is required.
func New(config Config) (*Controller, *OwnerSetup, error) {
	store, err := openOwnerStore(config.StateDirectory)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	controller, setup, err := newController(config, store)
	if err != nil {
		_ = store.close()
	}
	return controller, setup, err
}

func newController(config Config, store ownerStore) (*Controller, *OwnerSetup, error) {
	if store == nil || !intent.ValidAlias(config.OwnerID) {
		return nil, nil, ErrUnavailable
	}
	if config.PagePath == "" {
		config.PagePath = "/"
	}
	if config.PagePath != "/" && config.PagePath != "/credentials/" {
		return nil, nil, ErrUnavailable
	}
	policy, bindings, err := freezePolicy(config.OwnerID, config.Accounts)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	verifier, err := newVerifier(config.Origin)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	owner, err := store.load()
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	if owner.Version != 0 && !owner.valid(config.OwnerID, config.Origin) {
		return nil, nil, ErrUnavailable
	}
	if owner.Version == 0 {
		owner = ownerRecord{Version: 1, OwnerID: config.OwnerID, Origin: config.Origin, UserID: make([]byte, 64)}
		if _, err = io.ReadFull(rand.Reader, owner.UserID); err != nil {
			return nil, nil, ErrUnavailable
		}
	}
	authority, err := credentialbroker.NewAuthority(policy)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	controller := &Controller{
		ownerID: config.OwnerID, origin: config.Origin, policy: policy, bindings: bindings,
		pagePath: config.PagePath, tlsProxy: config.TrustLoopbackTLSProxy,
		authority: authority, owner: owner, store: store, verifier: verifier,
		requests: make(map[string]*pendingRequest), ceremonies: make(map[string]pendingCeremony),
		grants: make(map[string]credentialbroker.Grant), now: time.Now, random: rand.Reader,
	}
	var setup *OwnerSetup
	if controller.enrollmentAllowed() {
		setup = &OwnerSetup{controller: controller}
	}
	return controller, setup, nil
}

func freezePolicy(
	ownerID string,
	rules []credentialbroker.AccountRule,
) (*credentialbroker.Policy, map[string]binding, error) {
	policy, err := credentialbroker.NewPolicy(rules)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	bindings := make(map[string]binding, len(rules))
	for _, rule := range rules {
		for _, principal := range rule.Principals {
			if principal.OwnerID != ownerID {
				return nil, nil, ErrUnavailable
			}
		}
		bindings[rule.Alias] = binding{rule.VaultID, rule.ItemID, slices.Clone(rule.Origins)}
	}
	return policy, bindings, nil
}

func (controller *Controller) usable(ctx context.Context) bool {
	return controller != nil && ctx != nil && ctx.Err() == nil && !controller.failed &&
		controller.authority != nil && controller.store != nil && controller.verifier != nil
}

func (controller *Controller) ready(ctx context.Context) bool {
	return controller.usable(ctx) && len(controller.owner.Credentials) == 1 && !controller.owner.EnrollmentPending
}

func (controller *Controller) enrollmentAllowed() bool {
	return len(controller.owner.Credentials) == 0 || controller.owner.EnrollmentPending
}

func (controller *Controller) permits(principal credentialbroker.Principal, aliases []string) bool {
	if principal.OwnerID != controller.ownerID || len(aliases) == 0 || len(aliases) > intent.MaxAccounts {
		return false
	}
	seen := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		account, exists := controller.bindings[alias]
		if !exists || len(account.origins) == 0 {
			return false
		}
		if _, duplicate := seen[alias]; duplicate {
			return false
		}
		seen[alias] = struct{}{}
		err := controller.authority.Authorize("", credentialbroker.Request{
			Principal:      principal,
			Account:        alias,
			Origin:         account.origins[0],
			PolicyRevision: controller.policy.Revision(),
		})
		if !errors.Is(err, credentialbroker.ErrApprovalRequired) {
			return false
		}
	}
	return true
}

// Request creates a bounded five-minute locator. Principal must come from the
// qualified authenticated caller, not a request body. It cannot issue a grant.
func (controller *Controller) Request(ctx context.Context, principal credentialbroker.Principal,
	aliases []string, lifetime time.Duration,
) (string, error) {
	if controller == nil {
		return "", ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.ready(ctx) {
		return "", ErrUnavailable
	}
	controller.prune()
	if lifetime <= 0 || lifetime > credentialbroker.MaxGrantLifetime || lifetime%time.Second != 0 ||
		!controller.permits(principal, aliases) {
		return "", ErrDenied
	}
	if len(controller.requests) >= MaxPending {
		return "", ErrUnavailable
	}
	id, err := controller.newReference()
	if err != nil {
		return "", err
	}
	accounts := slices.Clone(aliases)
	slices.Sort(accounts)
	controller.requests[id] = &pendingRequest{scope: Scope{
		Principal: principal, Accounts: accounts, PolicyRevision: controller.policy.Revision(),
		DurationSeconds: int(lifetime / time.Second),
	}, issued: controller.now()}
	return id, nil
}

func (controller *Controller) BeginApproval(ctx context.Context, requestID string) (Ceremony, error) {
	if controller == nil {
		return Ceremony{}, ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.ready(ctx) {
		return Ceremony{}, ErrUnavailable
	}
	controller.prune()
	request := controller.requests[requestID]
	if !intent.ValidReference(requestID) || request == nil || request.attempted {
		return Ceremony{}, ErrDenied
	}
	if request.ceremony != "" {
		pending, exists := controller.ceremonies[request.ceremony]
		if !exists || !controller.ceremonyLive(pending) {
			return Ceremony{}, ErrDenied
		}
		return publicCeremony(request.ceremony, pending), nil
	}
	ceremony, err := controller.begin(approve, request.scope, requestID)
	if err == nil {
		request.ceremony = ceremony.ID
	}
	return ceremony, err
}

// FinishApproval consumes the challenge before parsing/verification. A failed
// attempt cannot be retried. The verified credential's full updated record is
// durably acknowledged before issuing a fresh grant; no approval silently renews.
func (controller *Controller) FinishApproval(ctx context.Context, id string, response []byte) (
	credentialbroker.Grant, error,
) {
	if controller == nil {
		return credentialbroker.Grant{}, ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	ceremony, err := controller.claim(ctx, id, approve)
	if err != nil {
		return credentialbroker.Grant{}, err
	}
	if err = controller.verifyAssertion(ctx, ceremony, response); err != nil {
		return credentialbroker.Grant{}, err
	}
	grant, err := controller.authority.Approve(credentialbroker.Approval{
		Principal:      ceremony.scope.Principal,
		Accounts:       ceremony.scope.Accounts,
		PolicyRevision: ceremony.scope.PolicyRevision,
		Lifetime:       time.Duration(ceremony.scope.DurationSeconds) * time.Second,
	})
	if err != nil {
		return credentialbroker.Grant{}, ErrUnavailable
	}
	controller.grants[grant.ID] = cloneGrant(grant)
	controller.requests[ceremony.request].grant = grant.ID
	return grant, nil
}

// Status binds the locator to its authenticated initiating principal. Pending
// references never let another caller obtain an issued grant.
func (controller *Controller) Status(ctx context.Context, principal credentialbroker.Principal, id string) (
	intent.Code, string,
) {
	if controller == nil {
		return intent.NotReady, ""
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.ready(ctx) {
		return intent.NotReady, ""
	}
	controller.prune()
	request := controller.requests[id]
	if !intent.ValidReference(id) || request == nil || request.scope.Principal != principal {
		return intent.Denied, ""
	}
	if request.grant != "" && controller.grantActive(request.grant) {
		return intent.GrantActive, request.grant
	}
	if request.attempted {
		return intent.Denied, ""
	}
	return intent.ApprovalPending, ""
}

func (controller *Controller) BeginRevocation(ctx context.Context, grantID string) (Ceremony, error) {
	if controller == nil {
		return Ceremony{}, ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.ready(ctx) {
		return Ceremony{}, ErrUnavailable
	}
	controller.prune()
	grant, exists := controller.grants[grantID]
	if !exists || !intent.ValidReference(grantID) || !controller.grantActive(grantID) {
		return Ceremony{}, ErrDenied
	}
	for id, pending := range controller.ceremonies {
		if pending.kind == revoke && pending.scope.GrantID == grantID && controller.ceremonyLive(pending) {
			return publicCeremony(id, pending), nil
		}
	}
	return controller.begin(revoke, Scope{
		Principal: grant.Principal, Accounts: slices.Clone(grant.Accounts),
		PolicyRevision: grant.PolicyRevision, DurationSeconds: int(grant.ExpiresAt.Sub(grant.IssuedAt) / time.Second),
		GrantID: grant.ID,
	}, "")
}

func (controller *Controller) FinishRevocation(ctx context.Context, id string, response []byte) error {
	if controller == nil {
		return ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	ceremony, err := controller.claim(ctx, id, revoke)
	if err != nil {
		return err
	}
	if err = controller.verifyAssertion(ctx, ceremony, response); err != nil {
		return err
	}
	if controller.authority.Revoke(controller.ownerID, ceremony.scope.GrantID) != nil {
		return ErrDenied
	}
	delete(controller.grants, ceremony.scope.GrantID)
	return nil
}

// Check is one lease check, not permission to cache authorization. A qualified
// consumer must supply live origin, enforce frame/element authority, and check
// again immediately before each credential or cookie-backed operation.
func (controller *Controller) Check(ctx context.Context, grantID string, request credentialbroker.Request) error {
	if controller == nil {
		return ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.ready(ctx) {
		return ErrUnavailable
	}
	if controller.authority.Authorize(grantID, request) != nil {
		return ErrDenied
	}
	return nil
}

func (controller *Controller) AuthorizedBinding(ctx context.Context, grantID string,
	request credentialbroker.Request,
) (LoginBinding, error) {
	if controller == nil {
		return LoginBinding{}, ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.ready(ctx) {
		return LoginBinding{}, ErrUnavailable
	}
	if controller.authority.Authorize(grantID, request) != nil {
		return LoginBinding{}, ErrDenied
	}
	account := controller.bindings[request.Account]
	return LoginBinding{account.vaultID, account.itemID, controller.policy.Revision()}, nil
}

// ReplacePolicy atomically replaces bindings and authority. Every effective
// transition discards all requests, challenges, and grants, including A-B-A.
func (controller *Controller) ReplacePolicy(ctx context.Context, rules []credentialbroker.AccountRule) error {
	if controller == nil {
		return ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.usable(ctx) {
		return ErrUnavailable
	}
	policy, bindings, err := freezePolicy(controller.ownerID, rules)
	if err != nil {
		return ErrDenied
	}
	if controller.policy.Revision() != policy.Revision() {
		if controller.authority.ReplacePolicy(policy) != nil {
			return ErrUnavailable
		}
		controller.clearPending()
	}
	controller.policy, controller.bindings = policy, bindings
	return nil
}

func (controller *Controller) claim(ctx context.Context, id string, kind ceremonyKind) (pendingCeremony, error) {
	if !controller.usable(context.Background()) {
		return pendingCeremony{}, ErrUnavailable
	}
	ceremony, exists := controller.ceremonies[id]
	delete(controller.ceremonies, id)
	if request := controller.requests[ceremony.request]; request != nil {
		request.attempted = true
	}
	if ctx == nil || ctx.Err() != nil || !intent.ValidReference(id) || !exists || ceremony.kind != kind ||
		!controller.ceremonyLive(ceremony) || (kind != enroll && !controller.ready(ctx)) {
		return pendingCeremony{}, ErrDenied
	}
	return ceremony, nil
}

func (controller *Controller) ceremonyLive(ceremony pendingCeremony) bool {
	if ceremony.session == nil || ceremony.session.Expires.IsZero() ||
		!time.Now().Before(ceremony.session.Expires) || !withinLifetime(controller.now(), ceremony.issued) ||
		ceremony.scope.PolicyRevision != controller.policy.Revision() {
		return false
	}
	if ceremony.kind == enroll && !ceremony.enrollmentIssued.IsZero() &&
		!withinLifetime(controller.now(), ceremony.enrollmentIssued) {
		return false
	}
	if ceremony.kind == approve {
		request := controller.requests[ceremony.request]
		return request != nil && withinLifetime(controller.now(), request.issued)
	}
	return true
}

func (controller *Controller) grantActive(id string) bool {
	grant, exists := controller.grants[id]
	if !exists || len(grant.Accounts) == 0 {
		return false
	}
	account, exists := controller.bindings[grant.Accounts[0]]
	return exists && len(account.origins) != 0 && controller.authority.Authorize(id, credentialbroker.Request{
		Principal:      grant.Principal,
		Account:        grant.Accounts[0],
		Origin:         account.origins[0],
		PolicyRevision: grant.PolicyRevision,
	}) == nil
}

func withinLifetime(now, issued time.Time) bool {
	return !now.Before(issued) && now.Before(issued.Add(PendingLifetime))
}

func (controller *Controller) prune() {
	now := controller.now()
	for id, request := range controller.requests {
		if !withinLifetime(now, request.issued) {
			delete(controller.ceremonies, request.ceremony)
			delete(controller.requests, id)
		}
	}
	for id, ceremony := range controller.ceremonies {
		if !withinLifetime(now, ceremony.issued) {
			delete(controller.ceremonies, id)
		}
	}
	for id := range controller.grants {
		if !controller.grantActive(id) {
			delete(controller.grants, id)
		}
	}
}

func (controller *Controller) newReference() (string, error) {
	for range 3 {
		var bytes [intent.ReferenceBytes]byte
		if _, err := io.ReadFull(controller.random, bytes[:]); err != nil {
			return "", ErrUnavailable
		}
		id := hex.EncodeToString(bytes[:])
		_, request := controller.requests[id]
		_, ceremony := controller.ceremonies[id]
		_, grant := controller.grants[id]
		if !request && !ceremony && !grant {
			return id, nil
		}
	}
	return "", ErrUnavailable
}

func (controller *Controller) clearPending() {
	clear(controller.requests)
	clear(controller.ceremonies)
	clear(controller.grants)
}

func (controller *Controller) fail() {
	controller.failed = true
	controller.authority.Close()
	controller.authority = nil
	controller.clearPending()
}

func (controller *Controller) Close() error {
	if controller == nil {
		return nil
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.fail()
	if controller.store == nil {
		return nil
	}
	err := controller.store.close()
	controller.store = nil
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func cloneScope(scope Scope) Scope { scope.Accounts = slices.Clone(scope.Accounts); return scope }
func cloneGrant(grant credentialbroker.Grant) credentialbroker.Grant {
	grant.Accounts = slices.Clone(grant.Accounts)
	return grant
}

func (*Controller) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private approval controller]")
}

func (*Controller) MarshalJSON() ([]byte, error) {
	return []byte(`"[private approval controller]"`), nil
}

func (*OwnerSetup) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private owner setup]")
}
func (*OwnerSetup) MarshalJSON() ([]byte, error) { return []byte(`"[private owner setup]"`), nil }
