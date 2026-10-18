package approval

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

const testOrigin = "https://approval.example.org"

var testPrincipal = credentialbroker.Principal{OwnerID: "owner", ActorID: "actor", AgentID: "agent", HostID: "host"}

func testRules() []credentialbroker.AccountRule {
	return []credentialbroker.AccountRule{{
		Alias: "facebook", VaultID: "private-vault", ItemID: "private-item",
		Origins: []string{"https://www.facebook.com"}, Principals: []credentialbroker.Principal{testPrincipal},
	}}
}

func testConfig() Config { return Config{OwnerID: "owner", Origin: testOrigin, Accounts: testRules()} }

type memoryStore struct {
	encoded  []byte
	commits  int
	fail     bool
	onCommit func()
}

func (store *memoryStore) load() (ownerRecord, error) {
	if len(store.encoded) == 0 {
		return ownerRecord{}, nil
	}
	var owner persistedOwner
	err := json.Unmarshal(store.encoded, &owner)
	return ownerRecord(owner), err
}

func (store *memoryStore) commit(owner ownerRecord) error {
	var err error
	store.encoded, err = encodeOwner(owner)
	store.commits++
	if store.onCommit != nil {
		store.onCommit()
	}
	if store.fail {
		return errors.New("SYNTHETIC_PRIVATE_STORAGE_DIAGNOSTIC")
	}
	return err
}
func (*memoryStore) close() error { return nil }

type fixture struct {
	controller *Controller
	setup      *OwnerSetup
	store      *memoryStore
	key        *ecdsa.PrivateKey
	id         []byte
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	store := &memoryStore{}
	controller, setup, err := newController(testConfig(), store)
	if err != nil || setup == nil {
		t.Fatal("controller setup failed", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("test authenticator key failed")
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Error("controller close failed")
		}
	})
	return fixture{
		controller: controller,
		setup:      setup,
		store:      store,
		key:        key,
		id:         []byte("synthetic-passkey-credential-v1"),
	}
}

func (fixture fixture) enroll(t *testing.T) {
	t.Helper()
	ceremony, err := fixture.setup.Begin(context.Background())
	if err != nil {
		t.Fatal("enrollment begin failed", err)
	}
	if err = fixture.setup.Finish(context.Background(), ceremony.ID,
		fixture.registration(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified)); err != nil {
		t.Fatal("enrollment finish failed", err)
	}
}

func (fixture fixture) approval(t *testing.T, lifetime time.Duration) (string, Ceremony) {
	t.Helper()
	id, err := fixture.controller.Request(context.Background(), testPrincipal, []string{"facebook"}, lifetime)
	if err != nil {
		t.Fatal("approval request failed", err)
	}
	ceremony, err := fixture.controller.BeginApproval(context.Background(), id)
	if err != nil {
		t.Fatal("approval begin failed", err)
	}
	return id, ceremony
}

func challenge(t *testing.T, ceremony Ceremony) string {
	t.Helper()
	var options struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if json.Unmarshal(ceremony.Options, &options) != nil || options.PublicKey.Challenge == "" {
		t.Fatal("missing public challenge")
	}
	return options.PublicKey.Challenge
}

func marshalFixture(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal("fixture JSON failed")
	}
	return encoded
}

func authData(origin string, flags protocol.AuthenticatorFlags, counter uint32) []byte {
	rpID := strings.TrimPrefix(origin, "https://")
	hash := sha256.Sum256([]byte(rpID))
	data := append(slices.Clone(hash[:]), byte(flags))
	return binary.BigEndian.AppendUint32(data, counter)
}

func clientData(t *testing.T, ceremony Ceremony, origin, kind string, crossOrigin bool) []byte {
	t.Helper()
	return marshalFixture(t, map[string]any{
		"type": kind, "challenge": challenge(t, ceremony),
		"origin": origin, "crossOrigin": crossOrigin,
	})
}

func (fixture fixture) registration(t *testing.T, ceremony Ceremony, origin string,
	flags protocol.AuthenticatorFlags,
) []byte {
	t.Helper()
	publicKey, err := webauthncbor.Marshal(map[int]any{
		1: 2, 3: -7, -1: 1,
		-2: fixture.key.X.FillBytes(make([]byte, 32)), -3: fixture.key.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatal("test COSE key failed")
	}
	data := authData(testOrigin, flags|protocol.FlagAttestedCredentialData, 0)
	data = append(data, make([]byte, 16)...)
	data = binary.BigEndian.AppendUint16(data, uint16(len(fixture.id)))
	data = append(data, fixture.id...)
	data = append(data, publicKey...)
	attestation, err := webauthncbor.Marshal(
		map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": data},
	)
	if err != nil {
		t.Fatal("test attestation failed")
	}
	id := base64.RawURLEncoding.EncodeToString(fixture.id)
	return marshalFixture(t, map[string]any{"id": id, "rawId": id, "type": "public-key", "response": map[string]any{
		"attestationObject": base64.RawURLEncoding.EncodeToString(attestation),
		"clientDataJSON": base64.RawURLEncoding.EncodeToString(
			clientData(t, ceremony, origin, "webauthn.create", false),
		),
	}})
}

func (fixture fixture) assertion(t *testing.T, ceremony Ceremony, origin string,
	flags protocol.AuthenticatorFlags, counter uint32, crossOrigin bool,
) []byte {
	t.Helper()
	data := authData(testOrigin, flags, counter)
	client := clientData(t, ceremony, origin, "webauthn.get", crossOrigin)
	hash := sha256.Sum256(client)
	signed := append(slices.Clone(data), hash[:]...)
	digest := sha256.Sum256(signed)
	signature, err := ecdsa.SignASN1(rand.Reader, fixture.key, digest[:])
	if err != nil {
		t.Fatal("test signature failed")
	}
	id := base64.RawURLEncoding.EncodeToString(fixture.id)
	return marshalFixture(t, map[string]any{"id": id, "rawId": id, "type": "public-key", "response": map[string]any{
		"authenticatorData": base64.RawURLEncoding.EncodeToString(data),
		"clientDataJSON":    base64.RawURLEncoding.EncodeToString(client),
		"signature":         base64.RawURLEncoding.EncodeToString(signature),
	}})
}

func checkedRequest(revision string) credentialbroker.Request {
	return credentialbroker.Request{
		Principal: testPrincipal, Account: "facebook",
		Origin: "https://www.facebook.com", PolicyRevision: revision,
	}
}

func TestRealPasskeyApprovalLifecycle(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.controller.Request(
		context.Background(),
		testPrincipal,
		[]string{"facebook"},
		time.Hour,
	); !errors.Is(
		err,
		ErrUnavailable,
	) {
		t.Fatal("unenrolled controller admitted a request")
	}
	fixture.enroll(t)
	if fixture.store.commits != 1 {
		t.Fatal("enrollment was not persisted")
	}
	if _, err := fixture.setup.Begin(context.Background()); !errors.Is(err, ErrDenied) {
		t.Fatal("initial setup capability replaced enrolled owner")
	}
	id, ceremony := fixture.approval(t, credentialbroker.MaxGrantLifetime)
	if ceremony.Scope.Principal != testPrincipal || ceremony.Scope.DurationSeconds != 20*60*60 ||
		ceremony.Scope.PolicyRevision == "" || !slices.Equal(ceremony.Scope.Accounts, []string{"facebook"}) {
		t.Fatal("phone scope omitted requested authority")
	}
	ceremony.Scope.Accounts[0] = "ebay"
	response := fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false)
	grant, err := fixture.controller.FinishApproval(context.Background(), ceremony.ID, response)
	if err != nil || fixture.store.commits != 2 || !slices.Equal(grant.Accounts, []string{"facebook"}) ||
		grant.ExpiresAt.Sub(grant.IssuedAt) != 20*time.Hour {
		t.Fatal("verified scope did not issue a persisted bounded grant", err)
	}
	stored, err := fixture.store.load()
	if err != nil || stored.Credentials[0].Authenticator.SignCount != 1 ||
		len(stored.Credentials[0].Attestation.ClientDataJSON) == 0 {
		t.Fatal("complete credential state was not persisted")
	}
	if code, issued := fixture.controller.Status(
		context.Background(),
		testPrincipal,
		id,
	); code != intent.GrantActive ||
		issued != grant.ID {
		t.Fatal("initiator cannot obtain issued grant")
	}
	other := testPrincipal
	other.AgentID = "other"
	if code, issued := fixture.controller.Status(
		context.Background(),
		other,
		id,
	); code != intent.Denied ||
		issued != "" {
		t.Fatal("locator substituted initiating principal")
	}
	grant.Accounts[0] = "ebay"
	if fixture.controller.Check(context.Background(), grant.ID, checkedRequest(grant.PolicyRevision)) != nil {
		t.Fatal("caller modified live grant scope")
	}
	providerBinding, err := fixture.controller.AuthorizedBinding(
		context.Background(),
		grant.ID,
		checkedRequest(grant.PolicyRevision),
	)
	if err != nil || providerBinding.VaultID != "private-vault" || providerBinding.ItemID != "private-item" ||
		providerBinding.PolicyRevision != grant.PolicyRevision {
		t.Fatal("authorized binding did not match granted policy")
	}
	if _, err = fixture.controller.FinishApproval(
		context.Background(),
		ceremony.ID,
		response,
	); !errors.Is(
		err,
		ErrDenied,
	) {
		t.Fatal("assertion replay issued another grant")
	}
	revocation, err := fixture.controller.BeginRevocation(context.Background(), grant.ID)
	if err != nil || revocation.Scope.GrantID != grant.ID {
		t.Fatal("revocation scope is not bound to grant")
	}
	if err = fixture.controller.FinishRevocation(
		context.Background(),
		revocation.ID,
		fixture.assertion(
			t,
			revocation,
			testOrigin,
			protocol.FlagUserPresent|protocol.FlagUserVerified,
			2,
			false,
		),
	); err != nil {
		t.Fatal("authenticated revocation failed", err)
	}
	if !errors.Is(
		fixture.controller.Check(context.Background(), grant.ID, checkedRequest(grant.PolicyRevision)),
		ErrDenied,
	) {
		t.Fatal("revoked grant remains usable")
	}
	if fixture.store.commits != 3 {
		t.Fatal("revocation skipped counter persistence")
	}
}

func TestRejectedAssertionsAreConsumed(t *testing.T) {
	cases := []struct {
		name, origin string
		flags        protocol.AuthenticatorFlags
		cross        bool
		invalid      bool
	}{
		{name: "missing-user-verification", origin: testOrigin, flags: protocol.FlagUserPresent},
		{name: "missing-presence", origin: testOrigin, flags: protocol.FlagUserVerified},
		{
			name:   "different-origin",
			origin: "https://other.example.org",
			flags:  protocol.FlagUserPresent | protocol.FlagUserVerified,
		},
		{
			name:   "opaque-android-origin",
			origin: "android:apk-key-hash:synthetic",
			flags:  protocol.FlagUserPresent | protocol.FlagUserVerified,
		},
		{
			name:   "cross-origin",
			origin: testOrigin,
			flags:  protocol.FlagUserPresent | protocol.FlagUserVerified,
			cross:  true,
		},
		{name: "invalid-body", invalid: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			fixture.enroll(t)
			id, ceremony := fixture.approval(t, time.Hour)
			response := []byte("SYNTHETIC_PRIVATE_ASSERTION_DIAGNOSTIC")
			if !test.invalid {
				response = fixture.assertion(t, ceremony, test.origin, test.flags, 1, test.cross)
			}
			grant, err := fixture.controller.FinishApproval(context.Background(), ceremony.ID, response)
			if !errors.Is(err, ErrDenied) || grant.ID != "" || fixture.store.commits != 1 {
				t.Fatal("hostile assertion granted access")
			}
			valid := fixture.assertion(
				t,
				ceremony,
				testOrigin,
				protocol.FlagUserPresent|protocol.FlagUserVerified,
				1,
				false,
			)
			if _, err = fixture.controller.FinishApproval(
				context.Background(),
				ceremony.ID,
				valid,
			); !errors.Is(
				err,
				ErrDenied,
			) {
				t.Fatal("failed ceremony was retried")
			}
			if code, _ := fixture.controller.Status(context.Background(), testPrincipal, id); code != intent.Denied {
				t.Fatal("failed request remains pending")
			}
		})
	}
}

func TestEnrollmentRequiresPrivateCapabilityAndVerifiedOwner(t *testing.T) {
	if _, err := (*OwnerSetup)(nil).Begin(context.Background()); !errors.Is(err, ErrDenied) {
		t.Fatal("nil setup admitted")
	}
	if err := (&OwnerSetup{}).Finish(
		context.Background(),
		strings.Repeat("a", 64),
		[]byte("{}"),
	); !errors.Is(
		err,
		ErrDenied,
	) {
		t.Fatal("zero setup admitted enrollment")
	}
	for _, test := range []struct {
		name, origin string
		flags        protocol.AuthenticatorFlags
	}{
		{"missing-uv", testOrigin, protocol.FlagUserPresent},
		{"wrong-origin", "https://other.example.org", protocol.FlagUserPresent | protocol.FlagUserVerified},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			ceremony, err := fixture.setup.Begin(context.Background())
			if err != nil {
				t.Fatal("setup begin failed")
			}
			if err = fixture.setup.Finish(
				context.Background(),
				ceremony.ID,
				fixture.registration(t, ceremony, test.origin, test.flags),
			); !errors.Is(err, ErrDenied) ||
				fixture.store.commits != 0 {
				t.Fatal("unverified owner enrollment persisted")
			}
			if err = fixture.setup.Finish(
				context.Background(),
				ceremony.ID,
				fixture.registration(
					t,
					ceremony,
					testOrigin,
					protocol.FlagUserPresent|protocol.FlagUserVerified,
				),
			); !errors.Is(err, ErrDenied) {
				t.Fatal("failed enrollment replayed")
			}
		})
	}
}

func TestChallengeExpirationCapacityAndPolicyABA(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	now := time.Now()
	fixture.controller.now = func() time.Time { return now }
	_, ceremony := fixture.approval(t, time.Hour)
	response := fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false)
	now = now.Add(PendingLifetime)
	if _, err := fixture.controller.FinishApproval(
		context.Background(),
		ceremony.ID,
		response,
	); !errors.Is(
		err,
		ErrDenied,
	) {
		t.Fatal("challenge accepted at exact expiry")
	}
	_, ceremony = fixture.approval(t, time.Hour)
	response = fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false)
	changed := testRules()
	changed[0].ItemID = "different-item"
	if fixture.controller.ReplacePolicy(context.Background(), changed) != nil ||
		fixture.controller.ReplacePolicy(context.Background(), testRules()) != nil {
		t.Fatal("policy transition failed")
	}
	if _, err := fixture.controller.FinishApproval(
		context.Background(),
		ceremony.ID,
		response,
	); !errors.Is(
		err,
		ErrDenied,
	) {
		t.Fatal("policy A-B-A revived an old challenge")
	}
	for range MaxPending {
		if _, err := fixture.controller.Request(
			context.Background(),
			testPrincipal,
			[]string{"facebook"},
			time.Hour,
		); err != nil {
			t.Fatal("bounded request unexpectedly failed")
		}
	}
	if _, err := fixture.controller.Request(
		context.Background(),
		testPrincipal,
		[]string{"facebook"},
		time.Hour,
	); !errors.Is(
		err,
		ErrUnavailable,
	) {
		t.Fatal("pending request capacity was unbounded")
	}
	now = now.Add(RequestLifetime)
	if _, err := fixture.controller.Request(
		context.Background(),
		testPrincipal,
		[]string{"facebook"},
		time.Hour,
	); err != nil {
		t.Fatal("expired capacity was not reclaimed")
	}
}

func TestConcurrentAssertionIssuesOnce(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	_, ceremony := fixture.approval(t, time.Hour)
	response := fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false)
	var wait sync.WaitGroup
	results := make(chan bool, 16)
	for range 16 {
		wait.Go(func() {
			grant, err := fixture.controller.FinishApproval(context.Background(), ceremony.ID, response)
			results <- err == nil && grant.ID != ""
		})
	}
	wait.Wait()
	close(results)
	issued := 0
	for result := range results {
		if result {
			issued++
		}
	}
	if issued != 1 || fixture.store.commits != 2 {
		t.Fatal("concurrent assertion was not consumed once")
	}
}

func TestPersistenceFailureQuarantinesAuthority(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	_, ceremony := fixture.approval(t, time.Hour)
	fixture.store.fail = true
	grant, err := fixture.controller.FinishApproval(context.Background(), ceremony.ID,
		fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false))
	if !errors.Is(err, ErrUnavailable) || grant.ID != "" || !fixture.controller.failed {
		t.Fatal("uncertain durable write issued a grant")
	}
	fixture.store.fail = false
	if _, err = fixture.controller.Request(
		context.Background(),
		testPrincipal,
		[]string{"facebook"},
		time.Hour,
	); !errors.Is(
		err,
		ErrUnavailable,
	) {
		t.Fatal("quarantined controller silently recovered")
	}
	restarted, setup, err := newController(testConfig(), fixture.store)
	if err != nil || setup != nil || len(restarted.grants) != 0 ||
		restarted.owner.Credentials[0].Authenticator.SignCount != 1 {
		t.Fatal("restart replayed grants or lost acknowledged durable counter")
	}
	if err = restarted.Close(); err != nil {
		t.Fatal("restart close failed")
	}
}

func TestCloneWarningDisablesExistingGrants(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	_, ceremony := fixture.approval(t, time.Hour)
	grant, err := fixture.controller.FinishApproval(context.Background(), ceremony.ID,
		fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false))
	if err != nil {
		t.Fatal("first approval failed")
	}
	_, ceremony = fixture.approval(t, time.Hour)
	if _, err = fixture.controller.FinishApproval(
		context.Background(),
		ceremony.ID,
		fixture.assertion(
			t,
			ceremony,
			testOrigin,
			protocol.FlagUserPresent|protocol.FlagUserVerified,
			1,
			false,
		),
	); !errors.Is(err, ErrDenied) {
		t.Fatal("counter reuse issued grant")
	}
	if !errors.Is(
		fixture.controller.Check(context.Background(), grant.ID, checkedRequest(grant.PolicyRevision)),
		ErrUnavailable,
	) {
		t.Fatal("clone warning left existing authority usable")
	}
	if _, _, err = newController(testConfig(), fixture.store); !errors.Is(err, ErrUnavailable) {
		t.Fatal("restart silently re-enabled a passkey with a durable clone warning")
	}
}

func TestPolicyAndVerifierConfigurationFailClosed(t *testing.T) {
	for _, origin := range []string{
		"http://approval.example.org", testOrigin + "/", testOrigin + "?x=1", testOrigin + "#x",
		"https://user@approval.example.org", "https://approval.example.org.", "https://127.0.0.1", "https://APPROVAL.example.org",
		"android:apk-key-hash:synthetic", "https://approval.example.org:0", "https://*.example.org",
		"https://approval.example.org:65536", "https://approval.example.org:08443",
		"https://approval.example.org:443", // Browser origin serialization omits the default port.
	} {
		config := testConfig()
		config.Origin = origin
		if _, _, err := newController(config, &memoryStore{}); !errors.Is(err, ErrUnavailable) {
			t.Fatal("unsafe RP origin admitted")
		}
	}
	fixture := newFixture(t)
	fixture.enroll(t)
	for _, test := range []struct {
		principal credentialbroker.Principal
		aliases   []string
		lifetime  time.Duration
	}{
		{testPrincipal, []string{"facebook"}, 0},
		{testPrincipal, []string{"facebook"}, 20*time.Hour + time.Second},
		{testPrincipal, []string{"facebook"}, time.Millisecond},
		{testPrincipal, []string{"facebook", "facebook"}, time.Hour},
		{testPrincipal, []string{"ebay"}, time.Hour},
		{credentialbroker.Principal{}, []string{"facebook"}, time.Hour},
	} {
		if _, err := fixture.controller.Request(
			context.Background(),
			test.principal,
			test.aliases,
			test.lifetime,
		); !errors.Is(
			err,
			ErrDenied,
		) {
			t.Fatal("invalid scope admitted")
		}
	}
	config := testConfig()
	other := testPrincipal
	other.OwnerID = "other"
	config.Accounts[0].Principals[0] = other
	if _, _, err := newController(config, &memoryStore{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("another owner adopted")
	}
}

func TestPrivateStateFormattingAndResponseBounds(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	for _, value := range []any{fixture.controller, fixture.setup, fixture.controller.owner} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal("redacted JSON failed")
		}
		for _, output := range []string{fmt.Sprintf("%+v", value), fmt.Sprintf("%#v", value), string(encoded)} {
			if strings.Contains(output, "private-vault") || strings.Contains(output, "private-item") ||
				strings.Contains(output, base64.StdEncoding.EncodeToString(fixture.id)) {
				t.Fatal("private state formatting leaked")
			}
		}
	}
	_, ceremony := fixture.approval(t, time.Hour)
	if _, err := fixture.controller.FinishApproval(
		context.Background(),
		ceremony.ID,
		bytes.Repeat([]byte("a"), maxResponseBytes+1),
	); !errors.Is(
		err,
		ErrDenied,
	) {
		t.Fatal("oversized response admitted")
	}
}

func TestCancellationAndExpiryDuringPersistenceDoNotIssue(t *testing.T) {
	for _, mode := range []string{"already-canceled", "canceled-during-save", "request-expired-during-save", "clock-backwards"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newFixture(t)
			fixture.enroll(t)
			now := time.Now()
			fixture.controller.now = func() time.Time { return now }
			id, err := fixture.controller.Request(context.Background(), testPrincipal, []string{"facebook"}, time.Hour)
			if err != nil {
				t.Fatal("request failed")
			}
			if mode == "request-expired-during-save" {
				now = now.Add(RequestLifetime - time.Second)
			}
			ceremony, err := fixture.controller.BeginApproval(context.Background(), id)
			if err != nil {
				t.Fatal("begin failed")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "already-canceled":
				cancel()
			case "canceled-during-save":
				fixture.store.onCommit = cancel
			case "request-expired-during-save":
				fixture.store.onCommit = func() { now = now.Add(time.Second) }
			case "clock-backwards":
				now = now.Add(-time.Second)
			}
			response := fixture.assertion(
				t,
				ceremony,
				testOrigin,
				protocol.FlagUserPresent|protocol.FlagUserVerified,
				1,
				false,
			)
			grant, err := fixture.controller.FinishApproval(ctx, ceremony.ID, response)
			if !errors.Is(err, ErrDenied) || grant.ID != "" || len(fixture.controller.grants) != 0 {
				t.Fatal("canceled or expired ceremony issued grant")
			}
			if _, err = fixture.controller.FinishApproval(
				context.Background(),
				ceremony.ID,
				response,
			); !errors.Is(
				err,
				ErrDenied,
			) {
				t.Fatal("canceled ceremony remained replayable")
			}
		})
	}
}

func TestEquivalentPolicyPreservesAndChangedBindingRevokes(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	_, ceremony := fixture.approval(t, time.Hour)
	if fixture.controller.ReplacePolicy(context.Background(), testRules()) != nil {
		t.Fatal("equivalent policy failed")
	}
	grant, err := fixture.controller.FinishApproval(context.Background(), ceremony.ID,
		fixture.assertion(t, ceremony, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false))
	if err != nil {
		t.Fatal("equivalent policy invalidated pending approval")
	}
	rules := testRules()
	rules[0].ItemID = "replacement-item"
	if fixture.controller.ReplacePolicy(context.Background(), rules) != nil {
		t.Fatal("binding replacement failed")
	}
	rules[0].ItemID = "caller-mutated-item"
	rules[0].Origins[0] = "https://other.example.org"
	if fixture.controller.bindings["facebook"].itemID != "replacement-item" ||
		fixture.controller.bindings["facebook"].origins[0] != "https://www.facebook.com" {
		t.Fatal("operator input changed frozen provider binding")
	}
	if !errors.Is(
		fixture.controller.Check(context.Background(), grant.ID, checkedRequest(grant.PolicyRevision)),
		ErrDenied,
	) {
		t.Fatal("changed provider binding left old grant usable")
	}
}

func TestSignedWrongKeyAndOwnerHandleAreDenied(t *testing.T) {
	for _, mode := range []string{"wrong-key", "wrong-owner-handle", "revocation-as-approval"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newFixture(t)
			fixture.enroll(t)
			_, ceremony := fixture.approval(t, time.Hour)
			if mode == "wrong-key" {
				key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal("other test key failed")
				}
				fixture.key = key
			}
			response := fixture.assertion(
				t,
				ceremony,
				testOrigin,
				protocol.FlagUserPresent|protocol.FlagUserVerified,
				1,
				false,
			)
			if mode == "wrong-owner-handle" {
				var body map[string]any
				if json.Unmarshal(response, &body) != nil {
					t.Fatal("fixture decode failed")
				}
				body["response"].(map[string]any)["userHandle"] = base64.RawURLEncoding.EncodeToString(
					[]byte("other-owner"),
				)
				response = marshalFixture(t, body)
			}
			if mode == "revocation-as-approval" {
				if !errors.Is(
					fixture.controller.FinishRevocation(context.Background(), ceremony.ID, response),
					ErrDenied,
				) {
					t.Fatal("approval assertion authorized a different operation")
				}
			}
			if _, err := fixture.controller.FinishApproval(
				context.Background(),
				ceremony.ID,
				response,
			); !errors.Is(
				err,
				ErrDenied,
			) {
				t.Fatal("substituted authenticator, owner, or ceremony issued grant")
			}
		})
	}
}
