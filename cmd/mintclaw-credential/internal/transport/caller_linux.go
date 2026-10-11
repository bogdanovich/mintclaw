//go:build linux

// Package transport implements private credential intent transport. It does not
// create listeners, load tokens, retrieve credentials, or qualify deployment.
package transport

import (
	"errors"

	"github.com/bogdanovich/mintclaw/internal/credentialipc"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

var ErrUnavailable = errors.New("private credential transport is unavailable")

// CallerConfig is trusted startup authority. PID must identify an already
// supervised/qualified gateway process, not a saved PID file, model argument,
// network field, or arbitrary same-UID process. Startup must prove that role
// before pinning it and prevent agent process-control/impersonation access.
type CallerConfig struct {
	PID       int32
	UID       uint32
	GID       uint32
	Principal credentialbroker.Principal
	Accounts  []string
}

type Caller struct {
	pid       int32
	uid       uint32
	gid       uint32
	principal credentialbroker.Principal
	accounts  map[string]struct{}
	lifetime  credentialipc.Lifetime
}

func PinCaller(config CallerConfig) (*Caller, error) {
	if config.PID <= 0 || config.UID == 0 || config.GID == 0 || len(config.Accounts) > credentialbroker.MaxAccounts {
		return nil, ErrUnavailable
	}
	for _, part := range []string{
		config.Principal.OwnerID, config.Principal.ActorID, config.Principal.AgentID, config.Principal.HostID,
	} {
		if !intent.ValidAlias(part) {
			return nil, ErrUnavailable
		}
	}
	accounts := make(map[string]struct{}, len(config.Accounts))
	for _, alias := range config.Accounts {
		if !intent.ValidAlias(alias) {
			return nil, ErrUnavailable
		}
		if _, duplicate := accounts[alias]; duplicate {
			return nil, ErrUnavailable
		}
		accounts[alias] = struct{}{}
	}
	lifetime, err := credentialipc.OpenProcess(config.PID)
	if err != nil || !lifetime.Alive() {
		lifetime.Close()
		return nil, ErrUnavailable
	}
	return &Caller{config.PID, config.UID, config.GID, config.Principal, accounts, lifetime}, nil
}

func (caller *Caller) Close() {
	if caller != nil {
		caller.lifetime.Close()
	}
}

func (caller *Caller) permits(request intent.Request) bool {
	if request.Account != "" {
		if _, allowed := caller.accounts[request.Account]; !allowed {
			return false
		}
	}
	for _, account := range request.Accounts {
		if _, allowed := caller.accounts[account]; !allowed {
			return false
		}
	}
	return true
}
