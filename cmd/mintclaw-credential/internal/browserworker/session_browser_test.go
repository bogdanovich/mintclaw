//go:build linux || darwin

package browserworker

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/approval"
	"github.com/bogdanovich/mintclaw/internal/credentials/vault"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

const fixtureOrigin = "https://login.example.org"

var fixturePrincipal = credentialbroker.Principal{OwnerID: "owner", ActorID: "actor", AgentID: "agent", HostID: "host"}

type syntheticLease struct {
	grant   credentialbroker.GrantLease
	flow    credentialbroker.LoginFlow
	binding approval.LoginBinding
}

func (lease syntheticLease) Binding(
	ctx context.Context,
	request credentialbroker.Request,
) (approval.LoginBinding, error) {
	if lease.grant.Check(ctx, request) != nil {
		return approval.LoginBinding{}, errUnavailable
	}
	return lease.binding, nil
}

func (lease syntheticLease) Flow(
	ctx context.Context,
	request credentialbroker.Request,
) (credentialbroker.LoginFlow, error) {
	if lease.grant.Check(ctx, request) != nil {
		return credentialbroker.LoginFlow{}, errUnavailable
	}
	return lease.flow, nil
}

func (lease syntheticLease) Origins(ctx context.Context, request credentialbroker.Request) ([]string, error) {
	if lease.grant.Check(ctx, request) != nil {
		return nil, errUnavailable
	}
	return []string{fixtureOrigin}, nil
}

func (lease syntheticLease) Remaining(ctx context.Context, request credentialbroker.Request) (time.Duration, error) {
	return lease.grant.Remaining(ctx, request)
}
func (lease syntheticLease) Done() <-chan struct{} { return lease.grant.Done() }

type syntheticSource struct {
	calls  atomic.Int32
	before func(context.Context) error
}

// Only the private test helper permits this synthetic network boundary. The
// public Connect function requires the actual enforcing proxy and signed lease.
type syntheticNetwork struct {
	lease     credentialbroker.GrantLease
	done      chan struct{}
	once      sync.Once
	locked    atomic.Bool
	submitted atomic.Bool
	lockErr   error
}

func (network *syntheticNetwork) Match(ctx context.Context, request credentialbroker.Request) error {
	return network.lease.Check(ctx, request)
}

func (network *syntheticNetwork) Endpoint(context.Context) (string, string, error) {
	return "http://127.0.0.1:18080", base64.StdEncoding.EncodeToString(make([]byte, 32)), nil
}

func (network *syntheticNetwork) LockForInjection(context.Context) error {
	if network.lockErr != nil {
		return network.lockErr
	}
	if !network.locked.CompareAndSwap(false, true) {
		return errUnavailable
	}
	return nil
}

func (network *syntheticNetwork) PermitSubmit(context.Context) error {
	if !network.locked.Load() || !network.submitted.CompareAndSwap(false, true) {
		return errUnavailable
	}
	return nil
}
func (network *syntheticNetwork) Done() <-chan struct{} { return network.done }
func (network *syntheticNetwork) Close() error {
	network.once.Do(func() { close(network.done) })
	return nil
}

func (source *syntheticSource) UseLogin(
	ctx context.Context,
	binding vault.Binding,
	inject func([]byte, []byte) error,
) error {
	source.calls.Add(1)
	if binding.VaultID != "vault" || binding.ItemID != "item" {
		return errUnavailable
	}
	if source.before != nil && source.before(ctx) != nil {
		return errUnavailable
	}
	username, password := []byte("synthetic-channel-user@example.org"), []byte("SYNTHETIC_CHANNEL_PASSWORD_v1")
	defer clear(username)
	defer clear(password)
	return inject(username, password)
}

// This fixture qualifies the actual Go/Node channel and DOM consumer with real
// CA1 leases and a synthetic source. It does not enroll a physical passkey,
// initialize the SDK, qualify network transport or prove host role isolation.
func browserFixture(t *testing.T, mode string, lifetime time.Duration) (*Session, *credentialbroker.Authority,
	credentialbroker.Grant, *atomic.Int32, *atomic.Pointer[credentialbroker.Request],
) {
	t.Helper()
	if os.Getenv("MINTCLAW_SYNTHETIC_CREDENTIAL_BROWSER_FIXTURE") != "1" {
		t.Skip("synthetic browser fixture only")
	}
	flow := credentialbroker.LoginFlow{
		NavigationURL: fixtureOrigin + "/login", EntryOrigin: fixtureOrigin,
		FormID: "login", UsernameID: "username", PasswordID: "password", UsernameType: "email",
		SubmitURL: fixtureOrigin + "/session", SuccessURL: fixtureOrigin + "/home", SuccessMarkerID: "authenticated",
		HandoffURL: fixtureOrigin + "/mfa", HandoffMarkerID: "mfa-required",
	}
	policy, err := credentialbroker.NewPolicy([]credentialbroker.AccountRule{
		{
			Alias:      "account",
			VaultID:    "vault",
			ItemID:     "item",
			Origins:    []string{fixtureOrigin},
			Principals: []credentialbroker.Principal{fixturePrincipal},
			Login:      &flow,
		},
	})
	if err != nil {
		t.Fatal("synthetic policy unavailable")
	}
	authority, err := credentialbroker.NewAuthority(policy)
	if err != nil {
		t.Fatal("synthetic authority unavailable")
	}
	t.Cleanup(authority.Close)
	grant, err := authority.Approve(credentialbroker.Approval{
		Principal: fixturePrincipal, Accounts: []string{"account"},
		PolicyRevision: policy.Revision(), Lifetime: lifetime,
	})
	if err != nil {
		t.Fatal("synthetic grant unavailable")
	}
	request := credentialbroker.Request{
		Principal:      fixturePrincipal,
		Account:        "account",
		Origin:         fixtureOrigin,
		PolicyRevision: policy.Revision(),
	}
	lease, err := authority.Lease(context.Background(), grant.ID, request)
	if err != nil {
		t.Fatal("synthetic lease unavailable")
	}
	current := &atomic.Pointer[credentialbroker.Request]{}
	current.Store(&request)
	descriptors, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal("synthetic endpoint unavailable")
	}
	parent, child := os.NewFile(
		uintptr(descriptors[0]),
		"synthetic parent",
	), os.NewFile(
		uintptr(descriptors[1]),
		"synthetic child",
	)
	connection, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		_ = child.Close()
		t.Fatal("synthetic endpoint unavailable")
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		_ = connection.Close()
		_ = child.Close()
		t.Fatal("synthetic fixture path unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	path := filepath.Join(
		filepath.Dir(source),
		"../../../../runtime/browser/credential-login/channel-browser-fixture.cjs",
	)
	command := exec.CommandContext(ctx, "node", path)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	if cache := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); cache != "" {
		command.Env = append(command.Env, "PLAYWRIGHT_BROWSERS_PATH="+cache)
	}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	command.ExtraFiles = []*os.File{child}
	if command.Start() != nil {
		_ = connection.Close()
		_ = child.Close()
		t.Fatal("synthetic worker unavailable")
	}
	_ = child.Close()
	finished := make(chan struct{})
	go func() { _ = command.Wait(); close(finished) }()
	var stopOnce sync.Once
	stops := &atomic.Int32{}
	stop := func(ctx context.Context) error {
		stopOnce.Do(func() { stops.Add(1); _ = command.Process.Kill() })
		select {
		case <-finished:
			return nil
		case <-ctx.Done():
			return errUnavailable
		}
	}
	t.Cleanup(func() { _ = connection.Close(); _ = stop(context.Background()) })
	session, err := connect(
		ctx,
		connection,
		Config{ExecutablePath: "/synthetic/" + mode},
		syntheticLease{
			lease,
			flow,
			approval.LoginBinding{VaultID: "vault", ItemID: "item", PolicyRevision: policy.Revision()},
		},
		func(context.Context) (credentialbroker.Request, error) { return *current.Load(), nil },
		func(context.Context, credentialbroker.LoginFlow) error {
			if mode == "submit-denied" {
				return errUnavailable
			}
			return nil
		},
		&syntheticNetwork{lease: lease, done: make(chan struct{})},
		stop,
	)
	if err != nil {
		t.Fatal("synthetic channel configuration unavailable")
	}
	t.Cleanup(func() {
		closing, end := context.WithTimeout(context.Background(), 8*time.Second)
		defer end()
		_ = session.Close(closing)
	})
	return session, authority, grant, stops, current
}

func TestBrowserChannelLogin(t *testing.T) {
	for _, mode := range []string{"normal", "mfa", "stale-before", "stale-after", "submit-denied"} {
		t.Run(mode, func(t *testing.T) {
			session, _, _, _, _ := browserFixture(t, mode, time.Minute)
			provider := &syntheticSource{}
			code := session.login(context.Background(), provider)
			expected := intent.OutcomeUnknown
			if mode == "normal" {
				expected = intent.LoggedIn
			}
			if mode == "mfa" {
				expected = intent.HandoffRequired
			}
			if code != expected {
				t.Fatal("unexpected fixed login outcome")
			}
			expectedCalls := int32(1)
			if mode == "stale-before" {
				expectedCalls = 0
			}
			if provider.calls.Load() != expectedCalls {
				t.Fatal("retrieval occurred outside admitted readiness")
			}
			if session.network.(*syntheticNetwork).submitted.Load() != (mode == "normal" || mode == "mfa") {
				t.Fatal("network submission permission escaped the trusted effect gate")
			}
			projection, err := json.Marshal(session)
			if err != nil {
				t.Fatal("safe session projection unavailable")
			}
			output := string(projection) + fmt.Sprintf("%+v %#v", session, session) + string(code)
			for _, value := range []string{"synthetic-channel-user@example.org", "SYNTHETIC_CHANNEL_PASSWORD_v1", "SYNTHETIC_CHANNEL_COOKIE_v1"} {
				for _, encoded := range []string{value, base64.StdEncoding.EncodeToString([]byte(value)), hex.EncodeToString([]byte(value))} {
					if strings.Contains(output, encoded) {
						t.Fatal("private authentication material reached projection")
					}
				}
			}
			if session.login(context.Background(), provider) != intent.Denied ||
				provider.calls.Load() != expectedCalls {
				t.Fatal("private login was replayed")
			}
		})
	}
}

func TestBrowserChannelLockFailurePreventsRetrieval(t *testing.T) {
	session, _, _, stops, _ := browserFixture(t, "normal", time.Minute)
	session.network.(*syntheticNetwork).lockErr = errUnavailable
	provider := &syntheticSource{}
	if session.login(context.Background(), provider) != intent.Denied || provider.calls.Load() != 0 {
		t.Fatal("provider retrieval occurred without a sealed preparation network")
	}
	closing, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if session.Close(closing) != nil || stops.Load() != 1 {
		t.Fatal("failed injection lock retained an unverified worker")
	}
}

func TestBrowserChannelNetworkRetiresBlockedRetrieval(t *testing.T) {
	session, _, _, stops, _ := browserFixture(t, "normal", time.Minute)
	started := make(chan struct{})
	provider := &syntheticSource{
		before: func(ctx context.Context) error { close(started); <-ctx.Done(); return errUnavailable },
	}
	result := make(chan intent.Code, 1)
	go func() { result <- session.login(context.Background(), provider) }()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("retrieval did not start")
	}
	_ = session.network.Close()
	select {
	case code := <-result:
		if code != intent.OutcomeUnknown {
			t.Fatal("retired network retained login authority")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("network failure did not cancel provider retrieval")
	}
	closing, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if session.Close(closing) != nil || stops.Load() != 1 {
		t.Fatal("network failure retained an unverified worker")
	}
}

func TestBrowserChannelRevokesBlockedRetrieval(t *testing.T) {
	session, authority, grant, stops, _ := browserFixture(t, "normal", time.Minute)
	started := make(chan struct{})
	provider := &syntheticSource{
		before: func(ctx context.Context) error { close(started); <-ctx.Done(); return errUnavailable },
	}
	result := make(chan intent.Code, 1)
	go func() { result <- session.login(context.Background(), provider) }()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("retrieval did not start")
	}
	if authority.Revoke(fixturePrincipal.OwnerID, grant.ID) != nil {
		t.Fatal("synthetic revocation failed")
	}
	select {
	case code := <-result:
		if code != intent.OutcomeUnknown {
			t.Fatal("revoked retrieval returned success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revocation did not cancel retrieval")
	}
	closing, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if session.Close(closing) != nil || stops.Load() != 1 {
		t.Fatal("revoked worker shutdown was not verified")
	}
}

func TestBrowserChannelRejectsChangedIdentityAfterRetrieval(t *testing.T) {
	session, _, _, _, current := browserFixture(t, "normal", time.Minute)
	provider := &syntheticSource{before: func(context.Context) error {
		changed := *current.Load()
		changed.Principal.HostID = "other-host"
		current.Store(&changed)
		return nil
	}}
	if session.login(context.Background(), provider) != intent.OutcomeUnknown || provider.calls.Load() != 1 {
		t.Fatal("changed execution host retained injection authority")
	}
}
