package gateway

import (
	"context"

	"github.com/bogdanovich/mintclaw/internal/credentials/approval"
	"github.com/bogdanovich/mintclaw/internal/credentials/vault"
	"github.com/bogdanovich/mintclaw/internal/secretvalues"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

type credentialVaultFactory func(context.Context, []byte) (vault.Provider, error)

func newCredentialVault(directory string, create credentialVaultFactory) (vault.Provider, error) {
	var provider vault.Provider
	ctx, cancel := context.WithTimeout(context.Background(), vault.InitializationTimeout)
	defer cancel()
	err := vault.UseServiceAccountToken(directory, func(token []byte) error {
		// The SDK has no explicit client-destruction API and retains its own
		// token copy. Keep this guard until process exit, including init errors.
		if _, err := secretvalues.Register(token); err != nil {
			return errCredentialsUnavailable
		}
		var err error
		provider, err = create(ctx, token)
		return err
	})
	if err != nil {
		return vault.Provider{}, errCredentialsUnavailable
	}
	return provider, nil
}

// prepareLogin is private runtime admission, not a model-facing secret API.
// Retrieval remains lazy: the browser must establish live form readiness before
// invoking consume. The returned lease owns revocation and session retirement.
func (runtime *credentialRuntime) prepareLogin(ctx context.Context, request credentialbroker.Request) (
	approval.LoginLease, func(context.Context, func([]byte, []byte) error) error, error,
) {
	if runtime == nil || runtime.consumeLogin == nil {
		return approval.LoginLease{}, nil, errCredentialsUnavailable
	}
	lease, err := runtime.controller.ActiveLogin(ctx, request)
	if err != nil {
		return approval.LoginLease{}, nil, errCredentialsUnavailable
	}
	consume := func(useCtx context.Context, use func([]byte, []byte) error) error {
		return consumeCredentialLogin(useCtx, request, lease, runtime.consumeLogin, use)
	}
	return lease, consume, nil
}

type credentialLoginAuthority interface {
	Binding(context.Context, credentialbroker.Request) (approval.LoginBinding, error)
	Check(context.Context, credentialbroker.Request) error
}

func consumeCredentialLogin(ctx context.Context, request credentialbroker.Request, lease credentialLoginAuthority,
	retrieve func(context.Context, vault.Binding, func([]byte, []byte) error) error,
	use func([]byte, []byte) error,
) (resultErr error) {
	defer func() {
		if recover() != nil {
			resultErr = errCredentialsUnavailable
		}
	}()
	if lease == nil || retrieve == nil || use == nil {
		return errCredentialsUnavailable
	}
	binding, err := lease.Binding(ctx, request)
	if err != nil {
		return errCredentialsUnavailable
	}
	err = retrieve(ctx, vault.Binding{VaultID: binding.VaultID, ItemID: binding.ItemID},
		func(username, password []byte) error {
			if lease.Check(ctx, request) != nil {
				return errCredentialsUnavailable
			}
			for _, value := range [][]byte{username, password} {
				if len(value) == 0 {
					continue
				}
				release, registerErr := secretvalues.Register(value)
				if registerErr != nil {
					return errCredentialsUnavailable
				}
				defer release()
			}
			if lease.Check(ctx, request) != nil {
				return errCredentialsUnavailable
			}
			return use(username, password)
		})
	if err != nil || lease.Check(ctx, request) != nil {
		return errCredentialsUnavailable
	}
	return nil
}
