package credentialbroker

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
)

const (
	MaxAccounts             = 64
	MaxOriginsPerAccount    = 16
	MaxPrincipalsPerAccount = 32
)

var (
	ErrInvalidPolicy    = errors.New("credential policy is invalid")
	ErrDenied           = errors.New("credential access denied")
	ErrApprovalRequired = errors.New("credential approval required")
	ErrInvalidApproval  = errors.New("credential approval is invalid")
	ErrUnavailable      = errors.New("credential authorization unavailable")

	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

// Principal is populated from authenticated, runtime-owned identity, never
// from model arguments. HostID identifies the trusted execution host.
type Principal struct {
	OwnerID string `json:"owner_id"`
	ActorID string `json:"actor_id"`
	AgentID string `json:"agent_id"`
	HostID  string `json:"host_id"`
}

// AccountRule is operator configuration. Provider IDs are immutable bindings,
// not model-visible secret references. All origins must be exact public HTTPS
// origins; live DNS and browser/frame authority still require separate checks.
type AccountRule struct {
	Alias      string      `json:"alias"`
	VaultID    string      `json:"vault_id"`
	ItemID     string      `json:"item_id"`
	Origins    []string    `json:"origins"`
	Principals []Principal `json:"principals"`
}

// Request describes one credential use or authenticated browser operation.
// Origin must come from trusted live browser state, not a requested URL alone.
type Request struct {
	Principal      Principal
	Account        string
	Origin         string
	PolicyRevision string
}

// Policy is immutable after construction. Its unexported data is deliberately
// absent from generic serialization; consumers use its canonical revision.
type Policy struct {
	revision string
	accounts map[string]accountPolicy
}

type accountPolicy struct {
	origins    []string
	principals map[Principal]struct{}
}

// NewPolicy constructs a defensively copied, default-deny policy. Empty policy
// is valid and grants nothing. A revision covers every normalized authority
// field, including the vault and item binding, independent of input ordering.
func NewPolicy(rules []AccountRule) (*Policy, error) {
	if len(rules) > MaxAccounts {
		return nil, ErrInvalidPolicy
	}
	policy := &Policy{accounts: make(map[string]accountPolicy, len(rules))}
	normalized := make([]AccountRule, 0, len(rules))
	for _, rule := range rules {
		if !validIdentifier(rule.Alias) || !validIdentifier(rule.VaultID) || !validIdentifier(rule.ItemID) ||
			len(rule.Origins) == 0 || len(rule.Origins) > MaxOriginsPerAccount ||
			len(rule.Principals) == 0 || len(rule.Principals) > MaxPrincipalsPerAccount {
			return nil, ErrInvalidPolicy
		}
		if _, exists := policy.accounts[rule.Alias]; exists {
			return nil, ErrInvalidPolicy
		}
		origins := make([]string, 0, len(rule.Origins))
		for _, raw := range rule.Origins {
			origin, err := credentialOrigin(raw)
			if err != nil || slices.Contains(origins, origin) {
				return nil, ErrInvalidPolicy
			}
			origins = append(origins, origin)
		}
		slices.Sort(origins)
		principals := make(map[Principal]struct{}, len(rule.Principals))
		for _, principal := range rule.Principals {
			if !validPrincipal(principal) {
				return nil, ErrInvalidPolicy
			}
			if _, exists := principals[principal]; exists {
				return nil, ErrInvalidPolicy
			}
			principals[principal] = struct{}{}
		}
		canonical := rule
		canonical.Origins = origins
		canonical.Principals = slices.Clone(rule.Principals)
		slices.SortFunc(canonical.Principals, comparePrincipal)
		normalized = append(normalized, canonical)
		policy.accounts[rule.Alias] = accountPolicy{origins: origins, principals: principals}
	}
	slices.SortFunc(normalized, func(left, right AccountRule) int { return cmp.Compare(left.Alias, right.Alias) })
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, ErrInvalidPolicy
	}
	digest := sha256.Sum256(append([]byte("mintclaw-credential-policy-v1\x00"), encoded...))
	policy.revision = hex.EncodeToString(digest[:])
	return policy, nil
}

func (policy *Policy) Revision() string {
	if policy == nil {
		return ""
	}
	return policy.revision
}

func (policy *Policy) permits(request Request) bool {
	if policy == nil || policy.revision == "" || request.PolicyRevision != policy.revision ||
		!validPrincipal(request.Principal) || !validIdentifier(request.Account) {
		return false
	}
	account, exists := policy.accounts[request.Account]
	if !exists {
		return false
	}
	if _, allowed := account.principals[request.Principal]; !allowed {
		return false
	}
	origin, err := credentialOrigin(request.Origin)
	return err == nil && slices.Contains(account.origins, origin)
}

func credentialOrigin(raw string) (string, error) {
	origin, err := browserpolicy.NormalizePublicOrigin(raw)
	if err != nil || !strings.HasPrefix(origin, "https://") {
		return "", ErrDenied
	}
	return origin, nil
}

func validIdentifier(value string) bool {
	return identifierPattern.MatchString(value)
}

func validPrincipal(principal Principal) bool {
	return validIdentifier(principal.OwnerID) && validIdentifier(principal.ActorID) &&
		validIdentifier(principal.AgentID) && validIdentifier(principal.HostID)
}

func comparePrincipal(left, right Principal) int {
	return cmp.Or(
		cmp.Compare(left.OwnerID, right.OwnerID),
		cmp.Compare(left.ActorID, right.ActorID),
		cmp.Compare(left.AgentID, right.AgentID),
		cmp.Compare(left.HostID, right.HostID),
	)
}
