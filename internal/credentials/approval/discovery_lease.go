package approval

import (
	"context"
	"net"
	"net/url"

	"golang.org/x/net/publicsuffix"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

// BindDiscovered narrows a signed vault-wide lease to one privately discovered
// item and the live website's registrable domain. The trusted caller must first
// match that exact item to this origin using fresh provider metadata, and must
// recheck the item's websites when retrieving values. No binding is persisted.
func (lease LoginLease) BindDiscovered(ctx context.Context, request credentialbroker.Request,
	itemID string,
) (LoginLease, error) {
	if lease.Check(ctx, request) != nil || !lease.discovery || lease.binding.ItemID != "" ||
		!intent.ValidAlias(itemID) {
		return LoginLease{}, ErrDenied
	}
	domain, err := discoveryDomain(request.Origin)
	if err != nil {
		return LoginLease{}, ErrDenied
	}
	lease.binding.ItemID = itemID
	lease.domain = domain
	return lease, nil
}

func discoveryDomain(origin string) (string, error) {
	normalized, err := browserpolicy.NormalizePublicOrigin(origin)
	if err != nil || normalized != origin {
		return "", ErrDenied
	}
	parsed, err := url.Parse(normalized)
	if err != nil || parsed.Scheme != "https" || net.ParseIP(parsed.Hostname()) != nil {
		return "", ErrDenied
	}
	domain, err := publicsuffix.EffectiveTLDPlusOne(parsed.Hostname())
	if err != nil {
		return "", ErrDenied
	}
	return domain, nil
}
