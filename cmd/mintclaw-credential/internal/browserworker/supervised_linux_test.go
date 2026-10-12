//go:build linux

package browserworker

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/approval"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

// These composition fixtures use actual CA1 authority and kernel endpoint
// adoption, with synthetic resource/proxy/worker peers. Separate fixtures must
// qualify Root IPC, enforcing TLS, real Chromium and protected deployment.
type compositionProxy struct {
	*syntheticNetwork
	mu       sync.Mutex
	listener net.Listener
	serving  atomic.Bool
}

func (proxy *compositionProxy) Serve(ctx context.Context, listener net.Listener) error {
	proxy.mu.Lock()
	select {
	case <-proxy.Done():
		proxy.mu.Unlock()
		_ = listener.Close()
		return errUnavailable
	default:
	}
	proxy.listener = listener
	proxy.mu.Unlock()
	proxy.serving.Store(true)
	unwatch := context.AfterFunc(ctx, func() { _ = proxy.Close() })
	defer unwatch()
	connection, err := listener.Accept()
	if connection != nil {
		_ = connection.Close()
	}
	return err
}

func (proxy *compositionProxy) Close() error {
	_ = proxy.syntheticNetwork.Close()
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if proxy.listener != nil {
		_ = proxy.listener.Close()
	}
	return nil
}

type compositionAllocation struct {
	ctx                    context.Context
	listener, control      *os.File
	peer                   net.Conn
	listenerID, controlID  unix.Stat_t
	proxy                  *compositionProxy
	mode                   string
	once                   sync.Once
	done                   chan struct{}
	retirements            atomic.Int32
	copiesOpenAtRetirement atomic.Bool
	wrongExecutable        atomic.Bool
}

func (allocation *compositionAllocation) EndpointFiles(context.Context) (*os.File, *os.File, error) {
	return allocation.listener, allocation.control, nil
}

func (allocation *compositionAllocation) Start(context.Context) error {
	if !allocation.proxy.serving.Load() || allocation.mode == "start-failure" {
		return errUnavailable
	}
	go func() {
		for {
			frame, err := readPacket(allocation.peer)
			if err != nil {
				return
			}
			code := byte(1)
			switch frame.kind {
			case 1:
				if len(frame.body) < 6 {
					allocation.wrongExecutable.Store(true)
				} else {
					size := int(binary.BigEndian.Uint16(frame.body[4:6]))
					if size > len(frame.body)-6 || string(frame.body[6:6+size]) != "/bin/chromium" {
						allocation.wrongExecutable.Store(true)
					}
				}
				if allocation.mode == "configuration-failure" {
					code = 8
				}
			case 4:
				code = 2
			case 5:
				code = 3
			case 6:
				code = 7
			}
			clear(frame.body)
			reply, err := encodePacket(0x10, frame.id, []byte{code})
			if err != nil {
				return
			}
			_, err = allocation.peer.Write(reply)
			clear(reply)
			if err != nil || frame.kind == 6 {
				return
			}
		}
	}()
	return nil
}

func (allocation *compositionAllocation) Retire(context.Context) error {
	allocation.retirements.Add(1)
	if compositionReferences(allocation.listenerID) != 0 || compositionReferences(allocation.controlID) != 0 {
		allocation.copiesOpenAtRetirement.Store(true)
	}
	if allocation.ctx != nil && allocation.ctx.Err() != nil {
		return errUnavailable
	}
	select {
	case <-allocation.done:
		return errUnavailable
	default:
	}
	allocation.Close()
	return nil
}

func (allocation *compositionAllocation) Close() {
	allocation.once.Do(func() {
		_ = allocation.listener.Close()
		_ = allocation.control.Close()
		_ = allocation.peer.Close()
		close(allocation.done)
	})
}

func (allocation *compositionAllocation) Done() <-chan struct{} { return allocation.done }

func compositionReferences(original unix.Stat_t) int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	count := 0
	for _, entry := range entries {
		fd, parseErr := strconv.Atoi(entry.Name())
		var current unix.Stat_t
		if parseErr == nil && unix.Fstat(fd, &current) == nil && current.Dev == original.Dev &&
			current.Ino == original.Ino {
			count++
		}
	}
	return count
}

func compositionFixture(t *testing.T, mode string) (syntheticLease, CurrentScope,
	*credentialbroker.Authority, credentialbroker.Grant, *compositionAllocation, *compositionProxy,
) {
	t.Helper()
	flow := credentialbroker.LoginFlow{
		NavigationURL: fixtureOrigin + "/login", EntryOrigin: fixtureOrigin,
		FormID: "login", UsernameID: "username", PasswordID: "password", UsernameType: "email",
		SubmitURL: fixtureOrigin + "/session", SuccessURL: fixtureOrigin + "/home", SuccessMarkerID: "authenticated",
		HandoffURL: fixtureOrigin + "/mfa", HandoffMarkerID: "mfa-required",
	}
	policy, err := credentialbroker.NewPolicy([]credentialbroker.AccountRule{{
		Alias: "account", VaultID: "vault", ItemID: "item", Origins: []string{fixtureOrigin},
		Principals: []credentialbroker.Principal{fixturePrincipal}, Login: &flow,
	}})
	if err != nil {
		t.Fatal("composition policy unavailable")
	}
	authority, err := credentialbroker.NewAuthority(policy)
	if err != nil {
		t.Fatal("composition authority unavailable")
	}
	t.Cleanup(authority.Close)
	grant, err := authority.Approve(credentialbroker.Approval{
		Principal:      fixturePrincipal,
		Accounts:       []string{"account"},
		PolicyRevision: policy.Revision(),
		Lifetime:       time.Minute,
	})
	if err != nil {
		t.Fatal("composition grant unavailable")
	}
	request := credentialbroker.Request{
		Principal: fixturePrincipal, Account: "account", Origin: fixtureOrigin, PolicyRevision: policy.Revision(),
	}
	grantLease, err := authority.Lease(t.Context(), grant.ID, request)
	if err != nil {
		t.Fatal("composition lease unavailable")
	}
	lease := syntheticLease{grantLease, flow, approval.LoginBinding{
		VaultID: "vault", ItemID: "item", PolicyRevision: policy.Revision(),
	}}
	proxy := &compositionProxy{syntheticNetwork: &syntheticNetwork{lease: grantLease, done: make(chan struct{})}}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal("composition listener unavailable")
	}
	listenerFile, err := listener.File()
	_ = listener.Close()
	if err != nil {
		t.Fatal("composition listener export unavailable")
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		_ = listenerFile.Close()
		t.Fatal("composition control unavailable")
	}
	controlFile := os.NewFile(uintptr(fds[0]), "composition control")
	peerFile := os.NewFile(uintptr(fds[1]), "composition peer")
	peer, err := net.FileConn(peerFile)
	_ = peerFile.Close()
	if err != nil {
		_ = listenerFile.Close()
		_ = controlFile.Close()
		t.Fatal("composition peer unavailable")
	}
	allocation := &compositionAllocation{
		listener: listenerFile, control: controlFile, peer: peer, proxy: proxy, mode: mode, done: make(chan struct{}),
	}
	if unix.Fstat(int(listenerFile.Fd()), &allocation.listenerID) != nil ||
		unix.Fstat(int(controlFile.Fd()), &allocation.controlID) != nil {
		t.Fatal("composition endpoint identities unavailable")
	}
	t.Cleanup(allocation.Close)
	return lease, func(context.Context) (credentialbroker.Request, error) { return request, nil },
		authority, grant, allocation, proxy
}

func TestSupervisedCompositionOwnership(t *testing.T) {
	for _, mode := range []string{
		"normal", "adoption-failure", "proxy-failure", "start-failure", "configuration-failure",
		"original-cancellation", "server-loss", "grant-revocation",
	} {
		t.Run(mode, func(t *testing.T) {
			lease, current, authority, grant, allocation, proxy := compositionFixture(t, mode)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "adoption-failure" {
				_ = allocation.control.Close()
			}
			session, err := startSupervised(
				ctx,
				lease,
				current,
				func(context.Context, credentialbroker.LoginFlow) error { return nil },
				func(ctx context.Context) (supervisedAllocation, error) { allocation.ctx = ctx; return allocation, nil },
				func(context.Context) (supervisedProxy, error) {
					if mode == "proxy-failure" {
						return nil, errUnavailable
					}
					return proxy, nil
				},
			)
			failed := mode == "adoption-failure" || mode == "proxy-failure" || mode == "start-failure" ||
				mode == "configuration-failure"
			if failed {
				if session != nil || !errors.Is(err, errUnavailable) {
					t.Fatal("composition failure admitted a session")
				}
			} else {
				if err != nil || session == nil || allocation.wrongExecutable.Load() {
					t.Fatal("qualified composition failed or selected a foreign executable")
				}
				switch mode {
				case "normal":
					provider := &syntheticSource{}
					if session.login(ctx, provider) != intent.LoggedIn || provider.calls.Load() != 1 {
						t.Fatal("composition did not use one scoped synthetic login")
					}
				case "original-cancellation":
					cancel()
				case "server-loss":
					allocation.Close()
				case "grant-revocation":
					if authority.Revoke(fixturePrincipal.OwnerID, grant.ID) != nil {
						t.Fatal("composition grant could not be revoked")
					}
				}
				closing, end := context.WithTimeout(context.Background(), 8*time.Second)
				defer end()
				closeErr := session.Close(closing)
				if (mode == "normal" || mode == "grant-revocation") && closeErr != nil {
					t.Fatal("acknowledged composition retirement failed")
				}
				if (mode == "original-cancellation" || mode == "server-loss") && closeErr == nil {
					t.Fatal("lost resource authority supplied retirement proof")
				}
				provider := &syntheticSource{}
				if session.login(context.Background(), provider) != intent.Denied || provider.calls.Load() != 0 {
					t.Fatal("retired composition allowed a new login or retrieval")
				}
			}
			if compositionReferences(allocation.listenerID) != 0 || compositionReferences(allocation.controlID) != 0 ||
				allocation.copiesOpenAtRetirement.Load() {
				t.Fatal("composition retained adopted endpoints during retirement")
			}
			expectedRetirements := int32(1)
			if mode == "adoption-failure" || mode == "proxy-failure" || mode == "start-failure" {
				expectedRetirements = 0
			}
			if allocation.retirements.Load() != expectedRetirements {
				t.Fatal("composition replayed retirement or requested it before start")
			}
			select {
			case <-allocation.Done():
			default:
				t.Fatal("composition left its resource allocation active")
			}
		})
	}
}

func TestSupervisedCompositionRejectsWithdrawnLeaseBeforeAllocation(t *testing.T) {
	lease, current, authority, _, allocation, proxy := compositionFixture(t, "normal")
	authority.Close()
	var allocations atomic.Int32
	session, err := startSupervised(t.Context(), lease, current,
		func(context.Context, credentialbroker.LoginFlow) error { return nil },
		func(context.Context) (supervisedAllocation, error) { allocations.Add(1); return allocation, nil },
		func(context.Context) (supervisedProxy, error) { return proxy, nil })
	if session != nil || !errors.Is(err, errUnavailable) || allocations.Load() != 0 {
		t.Fatal("withdrawn lease allocated browser resources")
	}
}
