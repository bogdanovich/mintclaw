//go:build linux || darwin

package gateway

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/internal/credentials/approval"
	"github.com/bogdanovich/mintclaw/internal/credentials/vault"
	"github.com/bogdanovich/mintclaw/internal/secretvalues"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func TestCredentialVaultStartupGuardsTokenAndClearsReadBuffer(t *testing.T) {
	dir := t.TempDir()
	const value = "synthetic-gateway-service-token-H54"
	if os.Chmod(dir, 0o700) != nil || vault.StoreServiceAccountToken(dir, []byte(value)) != nil {
		t.Fatal("private vault startup fixture unavailable")
	}
	var retained []byte
	_, err := newCredentialVault(dir, func(_ context.Context, token []byte) (vault.Provider, error) {
		retained = token
		if secretvalues.Redact(string(token)) != secretvalues.Marker {
			t.Fatal("SDK initialization began without output protection")
		}
		return vault.Provider{}, errors.New(string(token))
	})
	if !errors.Is(err, errCredentialsUnavailable) || err.Error() != errCredentialsUnavailable.Error() ||
		secretvalues.Redact(value) != secretvalues.Marker {
		t.Fatal("private SDK initialization exposed its error or removed a retained token's guard")
	}
	for _, char := range retained {
		if char != 0 {
			t.Fatal("SDK startup read buffer was not cleared")
		}
	}
}

type testCredentialLoginAuthority struct {
	credentialbroker.GrantLease
	binding approval.LoginBinding
}

func (lease testCredentialLoginAuthority) Binding(ctx context.Context, request credentialbroker.Request) (
	approval.LoginBinding, error,
) {
	if lease.Check(ctx, request) != nil {
		return approval.LoginBinding{}, approval.ErrDenied
	}
	return lease.binding, nil
}

func TestCredentialConsumerRetrievesExactBindingAndChecksRevocationBeforeUse(t *testing.T) {
	for _, mode := range []string{"success", "revoke-before-retrieve", "revoke-after-retrieve", "consumer-error"} {
		t.Run(mode, func(t *testing.T) {
			principal := credentialbroker.Principal{
				OwnerID: "owner",
				ActorID: "actor",
				AgentID: "agent",
				HostID:  "host",
			}
			policy, err := credentialbroker.NewPolicy([]credentialbroker.AccountRule{{
				Alias: "ebay", VaultID: "vault", ItemID: "item", Origins: []string{"https://example.com"},
				Principals: []credentialbroker.Principal{principal},
			}})
			if err != nil {
				t.Fatal("credential consumer policy unavailable")
			}
			authority, err := credentialbroker.NewAuthority(policy)
			if err != nil {
				t.Fatal("credential consumer authority unavailable")
			}
			defer authority.Close()
			grant, err := authority.Approve(credentialbroker.Approval{
				Principal:      principal,
				Accounts:       []string{"ebay"},
				PolicyRevision: policy.Revision(),
				Lifetime:       time.Hour,
			})
			if err != nil {
				t.Fatal("credential consumer grant unavailable")
			}
			request := credentialbroker.Request{
				Principal: principal, Account: "ebay", Origin: "https://example.com", PolicyRevision: policy.Revision(),
			}
			grantLease, err := authority.Lease(t.Context(), grant.ID, request)
			if err != nil {
				t.Fatal("credential consumer lease unavailable")
			}
			lease := testCredentialLoginAuthority{grantLease, approval.LoginBinding{VaultID: "vault", ItemID: "item"}}
			if mode == "revoke-before-retrieve" && authority.Revoke(principal.OwnerID, grant.ID) != nil {
				t.Fatal("consumer revocation fixture failed")
			}
			retrieved, used := false, false
			err = consumeCredentialLogin(t.Context(), request, lease,
				func(_ context.Context, binding vault.Binding, consume func([]byte, []byte) error) error {
					retrieved = true
					if binding.VaultID != "vault" || binding.ItemID != "item" {
						t.Fatal("credential consumer changed the exact authorized item")
					}
					if mode == "revoke-after-retrieve" && authority.Revoke(principal.OwnerID, grant.ID) != nil {
						t.Fatal("consumer late revocation fixture failed")
					}
					return consume([]byte("synthetic-use-account-R33"), []byte("synthetic-use-password-K82"))
				},
				func(username, password []byte) error {
					used = true
					if secretvalues.Redact(string(username)) != secretvalues.Marker ||
						secretvalues.Redact(string(password)) != secretvalues.Marker {
						t.Fatal("credential consumer used values without output protection")
					}
					if mode == "consumer-error" {
						return errors.New(string(password))
					}
					return nil
				})
			if (err == nil) != (mode == "success") || retrieved != (mode != "revoke-before-retrieve") ||
				used != (mode == "success" || mode == "consumer-error") {
				t.Fatal("credential consumer crossed a revoked boundary or exposed an unsafe outcome")
			}
			if err != nil &&
				(!errors.Is(err, errCredentialsUnavailable) || err.Error() != errCredentialsUnavailable.Error()) {
				t.Fatal("credential consumer failure exposed a private error")
			}
		})
	}
}

func TestCredentialPreparationDeniesUnavailableRuntimeWithoutRetrieving(t *testing.T) {
	for _, runtime := range []*credentialRuntime{nil, {}} {
		lease, consume, err := runtime.prepareLogin(t.Context(), credentialbroker.Request{})
		if !errors.Is(err, errCredentialsUnavailable) || consume != nil ||
			lease.Check(t.Context(), credentialbroker.Request{}) == nil {
			t.Fatal("unavailable credential runtime provided a usable private consumer")
		}
	}
}
