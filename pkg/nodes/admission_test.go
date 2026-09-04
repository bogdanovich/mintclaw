package nodes

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuthenticatorPersistsPendingPairingAndRejectsReplay(t *testing.T) {
	registry, err := NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	authenticator, err := NewAuthenticator(registry, AdmissionConfig{
		Random: bytes.NewReader(bytes.Repeat([]byte{1}, 64)),
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := authenticator.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	if challenge.MinProtocol != ProtocolV1 || challenge.MaxProtocol != ProtocolV2 {
		t.Fatalf("challenge protocol range = %d..%d", challenge.MinProtocol, challenge.MaxProtocol)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewIdentityProof(
		privateKey, challenge.Nonce, ProtocolV1, ProtocolV1,
		"v0.1.0", "linux", "amd64", CapabilityCatalog{},
		currentTestExecutionProfile(),
	)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := authenticator.Authenticate(proof)
	if err != nil {
		t.Fatal(err)
	}
	result := admission.Result
	if result.State != StatePendingPairing {
		t.Fatalf("state = %q", result.State)
	}
	pending, exists, err := registry.Pending(proof.NodeID)
	if err != nil || !exists {
		t.Fatalf("Pending() = exists %v, error %v", exists, err)
	}
	if !bytes.Equal(pending.PublicKey, privateKey.Public().(ed25519.PublicKey)) {
		t.Fatal("pending public key does not match signer")
	}
	if pending.Node.ProtocolVersion != ProtocolV1 ||
		pending.Node.Executor != "local" ||
		pending.Node.PolicyRevision != "policy-1" {
		t.Fatalf("pending node = %#v", pending.Node)
	}
	if _, err := authenticator.Authenticate(proof); !errors.Is(err, ErrChallengeUnknown) {
		t.Fatalf("replayed Admit() error = %v", err)
	}
}

func TestAuthenticatorNegotiatesV2AndPersistsProtocolHash(t *testing.T) {
	registry, err := NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewAuthenticator(registry, AdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := authenticator.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	catalog := CapabilityCatalog{Commands: []CommandDescriptor{
		descriptor("node.info.v1", `{"type":"integer","maximum":60}`),
	}}
	proof, err := NewIdentityProof(
		privateKey,
		challenge.Nonce,
		ProtocolV2,
		ProtocolV2,
		"v0.2.0",
		"linux",
		"amd64",
		catalog,
		currentTestExecutionProfile(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authenticator.Authenticate(proof); err != nil {
		t.Fatal(err)
	}
	pending, found, err := registry.Pending(proof.NodeID)
	if err != nil || !found {
		t.Fatalf("Pending() = found %v, error %v", found, err)
	}
	wantHash, err := catalog.HashForProtocol(ProtocolV2)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Node.ProtocolVersion != ProtocolV2 || pending.Node.CatalogHash != wantHash {
		t.Fatalf("pending v2 node = %#v", pending.Node)
	}
}

func TestAuthenticatorAdmitsAndReconnectsP256Identity(t *testing.T) {
	registry, err := NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	offers := NewEnrollmentOfferManager(EnrollmentOfferConfig{})
	privateKey := testP256PrivateKey(t)
	authenticator, err := NewAuthenticator(registry, AdmissionConfig{EnrollmentOffers: offers})
	if err != nil {
		t.Fatal(err)
	}
	newProof := func() IdentityProof {
		challenge, challengeErr := authenticator.IssueChallenge()
		if challengeErr != nil {
			t.Fatal(challengeErr)
		}
		return newTestP256IdentityProof(t, privateKey, challenge.Nonce)
	}

	proof := newProof()
	attachTestEnrollmentOffer(t, offers, privateKey, &proof)
	admission, err := authenticator.Authenticate(proof)
	if err != nil {
		t.Fatal(err)
	}
	if admission.Result.State != StatePendingPairing {
		t.Fatalf("initial state = %q", admission.Result.State)
	}
	pending, exists, err := registry.Pending(proof.NodeID)
	if err != nil || !exists {
		t.Fatalf("Pending() = exists %v, error %v", exists, err)
	}
	if pending.KeyAlgorithm != KeyAlgorithmECDSAP256SHA256 {
		t.Fatalf("pending key algorithm = %q", pending.KeyAlgorithm)
	}
	if _, err := registry.Approve(
		proof.NodeID,
		PairingApproval{Aliases: []Alias{"android-test"}, At: time.Now().Unix()},
	); err != nil {
		t.Fatal(err)
	}

	inconsistentReconnect := newProof()
	inconsistentReconnect.Platform = "linux"
	signTestP256IdentityProof(t, privateKey, &inconsistentReconnect)
	if _, err := authenticator.Authenticate(inconsistentReconnect); !errors.Is(err, ErrEnrollmentOfferInvalid) {
		t.Fatalf("inconsistent reconnect error = %v", err)
	}

	reconnect := newProof()
	admission, err = authenticator.Authenticate(reconnect)
	if err != nil {
		t.Fatal(err)
	}
	if admission.Result.State != StateConnected {
		t.Fatalf("reconnect state = %q", admission.Result.State)
	}
}

func TestAuthenticatorRequiresOfferBeforeUnknownAndroidPairing(t *testing.T) {
	registry, err := NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewAuthenticator(registry, AdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := authenticator.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	proof := newTestP256IdentityProof(t, testP256PrivateKey(t), challenge.Nonce)
	if _, err := authenticator.Authenticate(proof); !errors.Is(err, ErrEnrollmentRequired) {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if _, exists, err := registry.Pending(proof.NodeID); err != nil || exists {
		t.Fatalf("unauthorized Pending() = exists %v, error %v", exists, err)
	}
}

func TestAuthenticatorRejectsP256IdentityOutsideAndroidPlatform(t *testing.T) {
	registry, err := NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewAuthenticator(registry, AdmissionConfig{
		EnrollmentOffers: NewEnrollmentOfferManager(EnrollmentOfferConfig{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := authenticator.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	privateKey := testP256PrivateKey(t)
	proof := newTestP256IdentityProof(t, privateKey, challenge.Nonce)
	proof.Platform = "linux"
	signTestP256IdentityProof(t, privateKey, &proof)
	if _, err := authenticator.Authenticate(proof); !errors.Is(err, ErrEnrollmentOfferInvalid) {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if _, exists, err := registry.Pending(proof.NodeID); err != nil || exists {
		t.Fatalf("unauthorized Pending() = exists %v, error %v", exists, err)
	}
}

func TestAuthenticatorRejectsEnrollmentAuthorityFromNonAndroidIdentity(t *testing.T) {
	registry, err := NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewAuthenticator(registry, AdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := authenticator.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewIdentityProof(
		privateKey,
		challenge.Nonce,
		ProtocolV1,
		ProtocolV1,
		"v0.1.0",
		"linux",
		"amd64",
		CapabilityCatalog{},
		currentTestExecutionProfile(),
	)
	if err != nil {
		t.Fatal(err)
	}
	proof.EnrollmentOfferID = strings.Repeat("a", 22)
	proof.EnrollmentProof = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	transcript, err := proof.transcript()
	if err != nil {
		t.Fatal(err)
	}
	proof.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, transcript))
	if _, err := authenticator.Authenticate(proof); !errors.Is(err, ErrEnrollmentOfferInvalid) {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if _, exists, err := registry.Pending(proof.NodeID); err != nil || exists {
		t.Fatalf("unauthorized Pending() = exists %v, error %v", exists, err)
	}
}

func TestAuthenticatorRejectsEd25519IdentityClaimingAndroidPlatform(t *testing.T) {
	registry, err := NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewAuthenticator(registry, AdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := authenticator.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewIdentityProof(
		privateKey,
		challenge.Nonce,
		ProtocolV1,
		ProtocolV1,
		"v0.1.0",
		"android",
		"arm64-v8a",
		CapabilityCatalog{},
		currentTestExecutionProfile(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authenticator.Authenticate(proof); !errors.Is(err, ErrEnrollmentOfferInvalid) {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if _, exists, err := registry.Pending(proof.NodeID); err != nil || exists {
		t.Fatalf("unauthorized Pending() = exists %v, error %v", exists, err)
	}
}

func attachTestEnrollmentOffer(
	t *testing.T,
	offers *EnrollmentOfferManager,
	privateKey *ecdsa.PrivateKey,
	proof *IdentityProof,
) {
	t.Helper()
	offer, err := offers.Issue("wss://gateway.example/nodes/v1/ws", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	proof.EnrollmentOfferID = offer.OfferID
	signTestP256IdentityProof(t, privateKey, proof)
	secret, err := base64.RawURLEncoding.Strict().DecodeString(offer.Secret)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := proof.transcript()
	if err != nil {
		t.Fatal(err)
	}
	proof.EnrollmentProof = base64.RawURLEncoding.EncodeToString(
		enrollmentOfferProof(secret, offer.OfferID, transcript),
	)
}

func TestFileRegistryRejectsMissingKeyAlgorithm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	registry, err := NewFileRegistry(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewAuthenticator(registry, AdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := authenticator.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewIdentityProof(
		privateKey, challenge.Nonce, ProtocolV1, ProtocolV1,
		"v0.1.0", "linux", "amd64", CapabilityCatalog{}, currentTestExecutionProfile(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authenticator.Authenticate(proof); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Approve(
		proof.NodeID,
		PairingApproval{Aliases: []Alias{"test-node"}, At: time.Now().Unix()},
	); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document registryDocument
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	record := document.Records[string(proof.NodeID)]
	record.KeyAlgorithm = ""
	document.Records[string(proof.NodeID)] = record
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err = NewFileRegistry(path, 4); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("NewFileRegistry() error = %v, want ErrInvalidNode", err)
	}
}

func TestAuthenticatorPersistsAuthenticatedExecutionProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	registry, err := NewFileRegistry(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewAuthenticator(registry, AdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := authenticator.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewIdentityProof(
		privateKey,
		challenge.Nonce,
		ProtocolV1,
		ProtocolV1,
		"v0.1.0",
		"linux",
		"amd64",
		CapabilityCatalog{},
		ExecutionProfile{Executor: "local", PolicyRevision: "policy-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, authenticateErr := authenticator.Authenticate(proof); authenticateErr != nil {
		t.Fatal(authenticateErr)
	}
	pending, found, err := registry.Pending(proof.NodeID)
	if err != nil || !found {
		t.Fatalf("Pending() = found %v, error %v", found, err)
	}
	if pending.Node.Executor != "local" || pending.Node.PolicyRevision != "policy-1" {
		t.Fatalf("pending execution profile = %#v", pending.Node)
	}
	if pending.Node.ProtocolVersion != ProtocolV1 {
		t.Fatalf("pending protocol = %d, want %d", pending.Node.ProtocolVersion, ProtocolV1)
	}

	reloaded, err := NewFileRegistry(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	pending, found, err = reloaded.Pending(proof.NodeID)
	if err != nil || !found {
		t.Fatalf("reloaded Pending() = found %v, error %v", found, err)
	}
	if pending.Node.Executor != "local" || pending.Node.PolicyRevision != "policy-1" {
		t.Fatalf("reloaded execution profile = %#v", pending.Node)
	}
}

func TestAuthenticatorConsumesInvalidProofChallenge(t *testing.T) {
	registry, err := NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewAuthenticator(registry, AdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := authenticator.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewIdentityProof(
		privateKey, challenge.Nonce, ProtocolV1, ProtocolV1,
		"v0.1.0", "linux", "amd64", CapabilityCatalog{},
		currentTestExecutionProfile(),
	)
	if err != nil {
		t.Fatal(err)
	}
	proof.Signature = "invalid"
	if _, err := authenticator.Authenticate(proof); !errors.Is(err, ErrInvalidIdentityProof) {
		t.Fatalf("Admit() error = %v", err)
	}
	if _, err := authenticator.Authenticate(proof); !errors.Is(err, ErrChallengeUnknown) {
		t.Fatalf("second Admit() error = %v", err)
	}
}

func TestAuthenticatorExpiresAndBoundsChallenges(t *testing.T) {
	registry, err := NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	authenticator, err := NewAuthenticator(registry, AdmissionConfig{
		ChallengeTTL:  time.Second,
		MaxChallenges: 1,
		Random:        bytes.NewReader(bytes.Repeat([]byte{2}, 96)),
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := authenticator.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	if _, issueErr := authenticator.IssueChallenge(); !errors.Is(issueErr, ErrAdmissionBusy) {
		t.Fatalf("second IssueChallenge() error = %v", issueErr)
	}
	now = now.Add(time.Second)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewIdentityProof(
		privateKey, challenge.Nonce, ProtocolV1, ProtocolV1,
		"v0.1.0", "linux", "amd64", CapabilityCatalog{},
		currentTestExecutionProfile(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authenticator.Authenticate(proof); !errors.Is(err, ErrChallengeExpired) {
		t.Fatalf("expired Admit() error = %v", err)
	}
	if _, err := authenticator.IssueChallenge(); err != nil {
		t.Fatalf("IssueChallenge() after expiry error = %v", err)
	}
}

func TestAuthenticatorDiscardChallengeReleasesCapacity(t *testing.T) {
	registry, err := NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewAuthenticator(registry, AdmissionConfig{MaxChallenges: 1})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := authenticator.IssueChallenge()
	if err != nil {
		t.Fatal(err)
	}
	authenticator.DiscardChallenge(challenge.Nonce)
	if _, err := authenticator.IssueChallenge(); err != nil {
		t.Fatalf("IssueChallenge() after discard error = %v", err)
	}
}

func TestAuthenticatorReconnectsApprovedIdentityAndTracksLiveness(t *testing.T) {
	registry, err := NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	authenticator, err := NewAuthenticator(registry, AdmissionConfig{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	first := admitTestIdentity(t, authenticator, privateKey)
	if first.State != StatePendingPairing {
		t.Fatalf("first admission state = %q", first.State)
	}
	if _, approveErr := registry.Approve(first.NodeID, PairingApproval{At: now.Unix()}); approveErr != nil {
		t.Fatal(approveErr)
	}
	now = now.Add(time.Second)
	second := admitTestIdentity(t, authenticator, privateKey)
	if second.State != StateConnected {
		t.Fatalf("approved admission state = %q", second.State)
	}

	now = now.Add(time.Second)
	if heartbeatErr := authenticator.Heartbeat(second.NodeID); heartbeatErr != nil {
		t.Fatal(heartbeatErr)
	}
	registration, exists, err := registry.Registration(second.NodeID)
	if err != nil || !exists {
		t.Fatalf("Registration() = exists %v, error %v", exists, err)
	}
	if registration.Snapshot.LastSeenAt != now.Unix() {
		t.Fatalf("last seen = %d, want %d", registration.Snapshot.LastSeenAt, now.Unix())
	}

	now = now.Add(time.Second)
	if disconnectErr := authenticator.Disconnect(second.NodeID, "test disconnect"); disconnectErr != nil {
		t.Fatal(disconnectErr)
	}
	registration, _, err = registry.Registration(second.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if registration.Snapshot.State != StateDisconnected ||
		registration.Snapshot.DisconnectReason != "test disconnect" {
		t.Fatalf("disconnected registration = %#v", registration)
	}
}

func TestAuthenticatorRejectsRevokedIdentityReconnect(t *testing.T) {
	registry, err := NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"), 4)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	authenticator, err := NewAuthenticator(registry, AdmissionConfig{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	first := admitTestIdentity(t, authenticator, privateKey)
	if _, err := registry.Approve(first.NodeID, PairingApproval{At: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Revoke(first.NodeID, Revocation{
		Reason: "test revocation",
		At:     now.Add(time.Second).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := authenticator.Heartbeat(first.NodeID); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("revoked Heartbeat() error = %v", err)
	}
	if _, err := admitTestIdentityResult(authenticator, privateKey); !errors.Is(err, ErrAdmissionRevoked) {
		t.Fatalf("revoked Admit() error = %v", err)
	}
}

func admitTestIdentity(
	t *testing.T,
	authenticator *Authenticator,
	privateKey ed25519.PrivateKey,
) AdmissionResult {
	t.Helper()
	result, err := admitTestIdentityResult(authenticator, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func admitTestIdentityResult(
	authenticator *Authenticator,
	privateKey ed25519.PrivateKey,
) (AdmissionResult, error) {
	challenge, err := authenticator.IssueChallenge()
	if err != nil {
		return AdmissionResult{}, err
	}
	proof, err := NewIdentityProof(
		privateKey, challenge.Nonce, ProtocolV1, ProtocolV1,
		"v0.1.0", "linux", "amd64", CapabilityCatalog{},
		currentTestExecutionProfile(),
	)
	if err != nil {
		return AdmissionResult{}, err
	}
	admission, err := authenticator.Authenticate(proof)
	if err != nil {
		return AdmissionResult{}, err
	}
	if admission.Result.State == StateConnected {
		if err := authenticator.Connect(admission); err != nil {
			return AdmissionResult{}, err
		}
	}
	return admission.Result, nil
}
