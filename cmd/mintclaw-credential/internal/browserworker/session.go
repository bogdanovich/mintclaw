package browserworker

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/approval"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/egress"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/vault"
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

// Config is protected startup configuration, never a model-selected executable,
// proxy, command, or browser profile. The launcher's checks are additional gates;
// startup must prove the complete role, file, network and agent isolation first.
type (
	Config       struct{ ExecutablePath string }
	launchConfig struct{ executablePath, proxyServer, proxyPin string }
)

// CurrentScope must derive the current owner/actor/agent/host/account/revision
// from qualified runtime identity and policy, honoring ctx and caller lifetime.
// Only live origin is supplied by the private worker, never by model arguments.
type (
	CurrentScope    func(context.Context) (credentialbroker.Request, error)
	AdmitSubmission func(context.Context, credentialbroker.LoginFlow) error
)

type loginSource interface {
	UseLogin(context.Context, vault.Binding, func([]byte, []byte) error) error
}

type networkBoundary interface {
	Match(context.Context, credentialbroker.Request) error
	Endpoint(context.Context) (string, string, error)
	LockForInjection(context.Context) error
	PermitSubmit(context.Context) error
	Done() <-chan struct{}
	Close() error
}

type proxyBoundary struct {
	proxy *egress.LoginProxy
	lease approval.LoginLease
}

func (boundary proxyBoundary) Match(ctx context.Context, request credentialbroker.Request) error {
	return boundary.proxy.Match(ctx, boundary.lease, request)
}

func (boundary proxyBoundary) Endpoint(ctx context.Context) (string, string, error) {
	return boundary.proxy.Endpoint(ctx)
}

func (boundary proxyBoundary) LockForInjection(ctx context.Context) error {
	return boundary.proxy.LockForInjection(ctx)
}

func (boundary proxyBoundary) PermitSubmit(ctx context.Context) error {
	return boundary.proxy.PermitSubmit(ctx)
}
func (boundary proxyBoundary) Done() <-chan struct{} { return boundary.proxy.Done() }
func (boundary proxyBoundary) Close() error          { return boundary.proxy.Close() }

// The production implementation is approval.LoginLease. This private contract
// also lets synthetic consumer tests exercise the real CA1 lifetime authority
// without enrolling an operator passkey or loading a service-account token.
type loginLease interface {
	Binding(context.Context, credentialbroker.Request) (approval.LoginBinding, error)
	Flow(context.Context, credentialbroker.Request) (credentialbroker.LoginFlow, error)
	Origins(context.Context, credentialbroker.Request) ([]string, error)
	Remaining(context.Context, credentialbroker.Request) (time.Duration, error)
	Done() <-chan struct{}
}

// Session owns one private worker and one immutable grant generation. It cannot
// export page/DOM/screenshot/cookie/driver handles or retry a login. A service
// registry must bind any later opaque session ID to this principal and lease.
type Session struct {
	worker  *channel
	lease   loginLease
	flow    credentialbroker.LoginFlow
	current CurrentScope
	network networkBoundary
	used    atomic.Bool
}

// Connect takes ownership of an inherited, supervisor-qualified endpoint and
// its verified termination operation. It never creates a listener or launches
// an arbitrary subprocess. Main does not call this until startup is qualified.
func Connect(ctx context.Context, connection net.Conn, config Config, lease approval.LoginLease,
	current CurrentScope, submit AdmitSubmission, network *egress.LoginProxy, stop func(context.Context) error,
) (*Session, error) {
	return connect(ctx, connection, config, lease, current, submit, proxyBoundary{network, lease}, stop)
}

func connect(ctx context.Context, connection net.Conn, config Config, lease loginLease,
	current CurrentScope, submit AdmitSubmission, network networkBoundary, stop func(context.Context) error,
) (result *Session, resultErr error) {
	var worker *channel
	defer func() {
		if recover() != nil {
			result, resultErr = nil, errUnavailable
		}
		if resultErr != nil {
			if network != nil {
				_ = network.Close()
			}
			if worker != nil {
				worker.retire()
			} else {
				if connection != nil {
					_ = connection.Close()
				}
				if stop != nil {
					_ = terminate(stop)
				}
			}
		}
	}()
	if ctx == nil || ctx.Err() != nil || connection == nil || lease == nil || current == nil || submit == nil ||
		stop == nil || network == nil {
		return nil, errUnavailable
	}
	request, err := current(ctx)
	if err != nil || ctx.Err() != nil {
		return nil, errUnavailable
	}
	if network.Match(ctx, request) != nil {
		return nil, errUnavailable
	}
	proxyServer, proxyPin, err := network.Endpoint(ctx)
	if err != nil {
		return nil, errUnavailable
	}
	flow, err := lease.Flow(ctx, request)
	if err != nil {
		return nil, errUnavailable
	}
	origins, err := lease.Origins(ctx, request)
	if err != nil {
		return nil, errUnavailable
	}
	session := &Session{lease: lease, flow: flow, current: current, network: network}
	check := func(ctx context.Context, operation, origin string) error {
		normalized, originErr := browserpolicy.NormalizePublicOrigin(origin)
		if originErr != nil || normalized != origin || !strings.HasPrefix(origin, "https://") {
			return errUnavailable
		}
		switch operation {
		case "navigate", "prepare", "retrieve", "inject", "submit", "lease":
			if origin != flow.EntryOrigin {
				return errUnavailable
			}
		case "outcome":
			if !strings.HasPrefix(flow.SuccessURL, origin+"/") &&
				!strings.HasPrefix(flow.HandoffURL, origin+"/") {
				return errUnavailable
			}
		case "network":
		default:
			return errUnavailable
		}
		if _, bindingErr := session.binding(ctx, origin); bindingErr != nil {
			return errUnavailable
		}
		if operation == "submit" {
			if submit(ctx, flow) != nil {
				return errUnavailable
			}
			if network.PermitSubmit(ctx) != nil {
				return errUnavailable
			}
		}
		_, bindingErr := session.binding(ctx, origin)
		return bindingErr
	}
	worker, err = newChannel(ctx, connection, check, stop)
	if err != nil {
		return nil, errUnavailable
	}
	session.worker = worker
	go func() {
		select {
		case <-lease.Done():
		case <-network.Done():
		case <-worker.done:
		}
		_ = network.Close()
		worker.retire()
	}()
	remaining, err := lease.Remaining(ctx, request)
	if err != nil || remaining < time.Millisecond {
		return nil, errUnavailable
	}
	body, err := encodeConfiguration(launchConfig{config.ExecutablePath, proxyServer, proxyPin}, flow, origins,
		uint32(remaining/time.Millisecond))
	if err != nil {
		return nil, errUnavailable
	}
	defer clear(body)
	setupCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	code, err := worker.rpc(setupCtx, 1, body)
	if err != nil || code != 1 {
		return nil, errUnavailable
	}
	if _, err = session.binding(ctx, flow.EntryOrigin); err != nil {
		return nil, errUnavailable
	}
	return session, nil
}

func (session *Session) binding(ctx context.Context, origin string) (approval.LoginBinding, error) {
	if ctx == nil || ctx.Err() != nil || session == nil || session.current == nil {
		return approval.LoginBinding{}, errUnavailable
	}
	request, err := session.current(ctx)
	if err != nil || ctx.Err() != nil {
		return approval.LoginBinding{}, errUnavailable
	}
	request.Origin = origin
	binding, err := session.lease.Binding(ctx, request)
	if err != nil {
		return approval.LoginBinding{}, errUnavailable
	}
	return binding, nil
}

// Login checks current DOM readiness before provider retrieval and again after
// it. Plaintext is lent only to one bounded private injection frame. All raw
// worker/provider diagnostics are discarded; uncertainty retires the worker.
func (session *Session) Login(ctx context.Context, provider vault.Provider) intent.Code {
	return session.login(ctx, provider)
}

func (session *Session) login(ctx context.Context, provider loginSource) (result intent.Code) {
	result = intent.OutcomeUnknown
	defer func() {
		if recover() != nil {
			result = intent.OutcomeUnknown
		}
		if result != intent.LoggedIn && result != intent.HandoffRequired && session != nil && session.worker != nil {
			if session.network != nil {
				_ = session.network.Close()
			}
			session.worker.retire()
		}
	}()
	if ctx == nil || ctx.Err() != nil || session == nil || session.worker == nil || provider == nil ||
		!session.used.CompareAndSwap(false, true) {
		return intent.Denied
	}
	operationCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	unwatch := context.AfterFunc(session.worker.ctx, cancel)
	defer unwatch()
	for _, kind := range []byte{2, 3} {
		code, err := session.worker.rpc(operationCtx, kind, nil)
		if err != nil || code != 1 {
			return intent.OutcomeUnknown
		}
	}
	if session.network.LockForInjection(operationCtx) != nil {
		return intent.Denied
	}
	binding, err := session.binding(operationCtx, session.flow.EntryOrigin)
	if err != nil {
		return intent.Denied
	}
	err = provider.UseLogin(operationCtx, vault.Binding{VaultID: binding.VaultID, ItemID: binding.ItemID},
		func(username, password []byte) error {
			if _, checkErr := session.binding(operationCtx, session.flow.EntryOrigin); checkErr != nil {
				return errUnavailable
			}
			code, readyErr := session.worker.rpc(operationCtx, 3, nil)
			if readyErr != nil || code != 1 {
				return errUnavailable
			}
			if len(username) == 0 || len(username) > 1024 || len(password) == 0 || len(password) > 4096 {
				return errUnavailable
			}
			body := make([]byte, 0, 4+len(username)+len(password))
			defer func() { clear(body) }()
			body, encodeErr := addBytes(body, username, 1024)
			if encodeErr != nil {
				return errUnavailable
			}
			body, encodeErr = addBytes(body, password, 4096)
			if encodeErr != nil {
				return errUnavailable
			}
			code, injectErr := session.worker.rpc(operationCtx, 4, body)
			if injectErr != nil || code != 2 {
				return errUnavailable
			}
			return nil
		})
	if err != nil || operationCtx.Err() != nil {
		return intent.OutcomeUnknown
	}
	code, err := session.worker.rpc(operationCtx, 5, nil)
	if err != nil || (code != 3 && code != 4) {
		return intent.OutcomeUnknown
	}
	if _, err = session.binding(operationCtx, session.flow.EntryOrigin); err != nil {
		return intent.Denied
	}
	select {
	case <-session.worker.done:
		return intent.Denied
	default:
	}
	if code == 4 {
		return intent.HandoffRequired
	}
	return intent.LoggedIn
}

func (session *Session) Close(ctx context.Context) error {
	if session == nil || session.worker == nil {
		return errUnavailable
	}
	_ = session.network.Close()
	return session.worker.close(ctx)
}

func (*Session) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private credential session]")
}
func (*Session) MarshalJSON() ([]byte, error) { return []byte(`"[private credential session]"`), nil }
func (*Session) MarshalText() ([]byte, error) { return []byte("[private credential session]"), nil }
