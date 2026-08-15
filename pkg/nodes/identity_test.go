package nodes

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityProofRoundTripAndTamperDetection(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewIdentityProof(
		privateKey, "challenge", ProtocolV1, ProtocolV1,
		"v0.1.0", "linux", "amd64", CapabilityCatalog{},
		ExecutionProfile{},
	)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := proof.Verify()
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !publicKey.Equal(privateKey.Public()) {
		t.Fatal("verified public key does not match signer")
	}

	proof.Platform = "darwin"
	if _, err := proof.Verify(); !errors.Is(err, ErrInvalidIdentityProof) {
		t.Fatalf("tampered Verify() error = %v", err)
	}
}

func TestIdentityProofExecutionProfileRoundTripAndTamperDetection(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewIdentityProof(
		privateKey,
		"challenge",
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
	if _, err := proof.Verify(); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	proof.PolicyRevision = "policy-2"
	if _, err := proof.Verify(); !errors.Is(err, ErrInvalidIdentityProof) {
		t.Fatalf("tampered execution profile Verify() error = %v", err)
	}
}

func TestIdentityProofRejectsIncompleteExecutionProfile(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewIdentityProof(
		privateKey,
		"challenge",
		ProtocolV1,
		ProtocolV1,
		"v0.1.0",
		"linux",
		"amd64",
		CapabilityCatalog{},
		ExecutionProfile{Executor: "local"},
	); !errors.Is(err, ErrInvalidInvocation) {
		t.Fatalf("incomplete execution profile error = %v", err)
	}
}

func TestIdentityProofRejectsGatewayOnlyServiceApprovalState(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := serviceActionDescriptorFixture()
	descriptor.ServiceProfiles[0].ActionApproval = "operator_bypass_configured"
	if _, err := NewIdentityProof(
		privateKey,
		"challenge",
		ProtocolV1,
		ProtocolV1,
		"v0.1.0",
		"linux",
		"amd64",
		CapabilityCatalog{Commands: []CommandDescriptor{descriptor}},
		ExecutionProfile{},
	); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("gateway-only companion approval state error = %v", err)
	}
}

func TestDeriveIDIsStableAndKeyBound(t *testing.T) {
	firstPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secondPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := DeriveID(firstPublic)
	if err != nil {
		t.Fatal(err)
	}
	repeatedID, err := DeriveID(firstPublic)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := DeriveID(secondPublic)
	if err != nil {
		t.Fatal(err)
	}
	if firstID != repeatedID || firstID == secondID {
		t.Fatalf("derived ids = %q, %q, %q", firstID, repeatedID, secondID)
	}
}

func TestIdentityProofRejectsCatalogHashMismatch(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewIdentityProof(
		privateKey, "challenge", ProtocolV1, ProtocolV1,
		"v0.1.0", "linux", "amd64", CapabilityCatalog{},
		ExecutionProfile{},
	)
	if err != nil {
		t.Fatal(err)
	}
	proof.CatalogHash = "not-the-catalog-hash"
	if _, err := proof.Verify(); !errors.Is(err, ErrInvalidIdentityProof) {
		t.Fatalf("Verify() error = %v", err)
	}
}

func TestLegacyIdentityProofTranscriptRemainsByteCompatible(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	proof, err := NewIdentityProof(
		privateKey, "challenge", ProtocolV1, ProtocolV1,
		"v0.1.0", "linux", "amd64", CapabilityCatalog{}, ExecutionProfile{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if proof.KeyAlgorithm != "" {
		t.Fatalf("legacy key algorithm = %q, want omitted", proof.KeyAlgorithm)
	}
	transcript, err := proof.transcript()
	if err != nil {
		t.Fatal(err)
	}
	want := "mintclaw-node-auth-v1\x00" +
		`{"nonce":"challenge","node_id":"` + string(proof.NodeID) +
		`","public_key":"` + proof.PublicKey +
		`","min_protocol":1,"max_protocol":1,"client_version":"v0.1.0",` +
		`"platform":"linux","architecture":"amd64","requested_role":"companion",` +
		`"catalog_hash":"` + proof.CatalogHash + `","executor":"","policy_revision":""}`
	if string(transcript) != want {
		t.Fatalf("legacy transcript changed:\n got %q\nwant %q", transcript, want)
	}
	if !ed25519.Verify(
		privateKey.Public().(ed25519.PublicKey),
		transcript,
		mustDecodeIdentityValue(t, proof.Signature),
	) {
		t.Fatal("legacy signature does not verify over the compatibility transcript")
	}
}

func TestExplicitEd25519AlgorithmIsTranscriptBound(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewIdentityProof(
		privateKey, "challenge", ProtocolV1, ProtocolV1,
		"v0.1.0", "linux", "amd64", CapabilityCatalog{}, ExecutionProfile{},
	)
	if err != nil {
		t.Fatal(err)
	}
	legacyTranscript, err := proof.transcript()
	if err != nil {
		t.Fatal(err)
	}
	proof.KeyAlgorithm = KeyAlgorithmEd25519
	algorithmTranscript, err := proof.transcript()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(legacyTranscript, algorithmTranscript) {
		t.Fatal("explicit algorithm reused the legacy transcript domain")
	}
	proof.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, algorithmTranscript))
	if _, err := proof.Verify(); err != nil {
		t.Fatalf("explicit Ed25519 Verify() error = %v", err)
	}
}

func TestP256IdentityProofRoundTripAndAlgorithmBinding(t *testing.T) {
	privateKey := testP256PrivateKey(t)
	proof := newTestP256IdentityProof(t, privateKey, "challenge")
	if proof.KeyAlgorithm != KeyAlgorithmECDSAP256SHA256 {
		t.Fatalf("key algorithm = %q", proof.KeyAlgorithm)
	}
	verified, err := proof.VerifyIdentity()
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	wantPublicKey := testP256PublicKeyBytes(t, privateKey)
	if verified.Algorithm != KeyAlgorithmECDSAP256SHA256 || !bytes.Equal(verified.Bytes, wantPublicKey) {
		t.Fatal("verified P-256 public key does not match signer")
	}
	if _, err := proof.Verify(); !errors.Is(err, ErrInvalidIdentityProof) {
		t.Fatalf("legacy Verify() accepted P-256 identity: %v", err)
	}

	proof.KeyAlgorithm = KeyAlgorithmEd25519
	if _, err := proof.VerifyIdentity(); !errors.Is(err, ErrInvalidIdentityProof) {
		t.Fatalf("algorithm-confused Verify() error = %v", err)
	}
}

func TestP256IdentityProofRejectsHighSSignature(t *testing.T) {
	proof := newTestP256IdentityProof(t, testP256PrivateKey(t), "challenge")
	signature := mustDecodeIdentityValue(t, proof.Signature)
	s := new(big.Int).SetBytes(signature[32:])
	highS := new(big.Int).Sub(elliptic.P256().Params().N, s)
	highS.FillBytes(signature[32:])
	proof.Signature = base64.RawURLEncoding.EncodeToString(signature)
	if _, err := proof.VerifyIdentity(); !errors.Is(err, ErrInvalidIdentityProof) {
		t.Fatalf("high-S Verify() error = %v", err)
	}
}

func TestP256IdentityProofBindsEnrollmentOfferID(t *testing.T) {
	privateKey := testP256PrivateKey(t)
	proof := newTestP256IdentityProof(t, privateKey, "challenge")
	proof.EnrollmentOfferID = strings.Repeat("a", 22)
	proof.EnrollmentProof = base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size))
	signTestP256IdentityProof(t, privateKey, &proof)
	if _, err := proof.VerifyIdentity(); err != nil {
		t.Fatalf("enrollment-bound VerifyIdentity() error = %v", err)
	}
	proof.EnrollmentOfferID = strings.Repeat("b", 22)
	if _, err := proof.VerifyIdentity(); !errors.Is(err, ErrInvalidIdentityProof) {
		t.Fatalf("tampered enrollment offer VerifyIdentity() error = %v", err)
	}
}

func TestIdentityProofRejectsIncompleteEnrollmentProof(t *testing.T) {
	proof := newTestP256IdentityProof(t, testP256PrivateKey(t), "challenge")
	proof.EnrollmentOfferID = strings.Repeat("a", 22)
	if _, err := proof.VerifyIdentity(); !errors.Is(err, ErrInvalidIdentityProof) {
		t.Fatalf("incomplete enrollment VerifyIdentity() error = %v", err)
	}
}

func TestP256IdentityProofRejectsMalformedPublicKeysAndSignatures(t *testing.T) {
	proof := newTestP256IdentityProof(t, testP256PrivateKey(t), "challenge")
	tests := []struct {
		name   string
		mutate func(*IdentityProof)
	}{
		{name: "compressed public key", mutate: func(candidate *IdentityProof) {
			candidate.PublicKey = base64.RawURLEncoding.EncodeToString(make([]byte, 33))
		}},
		{name: "padded public key", mutate: func(candidate *IdentityProof) {
			candidate.PublicKey += "="
		}},
		{name: "off-curve public key", mutate: func(candidate *IdentityProof) {
			candidate.PublicKey = base64.RawURLEncoding.EncodeToString(append([]byte{4}, make([]byte, 64)...))
		}},
		{name: "zero r", mutate: func(candidate *IdentityProof) {
			signature := mustDecodeIdentityValue(t, candidate.Signature)
			clear(signature[:32])
			candidate.Signature = base64.RawURLEncoding.EncodeToString(signature)
		}},
		{name: "oversized signature", mutate: func(candidate *IdentityProof) {
			candidate.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, 65))
		}},
		{name: "unknown algorithm", mutate: func(candidate *IdentityProof) {
			candidate.KeyAlgorithm = "p256"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := proof
			test.mutate(&candidate)
			if _, verifyErr := candidate.VerifyIdentity(); !errors.Is(verifyErr, ErrInvalidIdentityProof) {
				t.Fatalf("Verify() error = %v", verifyErr)
			}
		})
	}
}

func TestP256NodeIDIsDomainSeparated(t *testing.T) {
	privateKey := testP256PrivateKey(t)
	publicKey := testP256PublicKeyBytes(t, privateKey)
	id, err := DeriveIDForAlgorithm(KeyAlgorithmECDSAP256SHA256, publicKey)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("mintclaw-node-id-v1\x00ecdsa-p256-sha256\x00"))
	_, _ = hash.Write(publicKey)
	want := ID("node_" + strings.ToLower(
		base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash.Sum(nil)),
	))
	if id != want {
		t.Fatalf("P-256 node ID = %q, want %q", id, want)
	}
	legacyDigest := sha256.Sum256(publicKey)
	legacyLikeID := ID("node_" + strings.ToLower(
		base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(legacyDigest[:]),
	))
	if id == legacyLikeID {
		t.Fatal("P-256 node ID collided with legacy derivation")
	}
}

func TestP256LanguageNeutralFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "identity-p256.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SchemaVersion int           `json:"schema_version"`
		Proof         IdentityProof `json:"proof"`
		Transcript    string        `json:"transcript_base64url"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 {
		t.Fatalf("fixture schema version = %d", fixture.SchemaVersion)
	}
	transcript, err := fixture.Proof.transcript()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(transcript, mustDecodeIdentityValue(t, fixture.Transcript)) {
		t.Fatal("fixture transcript does not match canonical proof transcript")
	}
	if _, err := fixture.Proof.VerifyIdentity(); err != nil {
		t.Fatalf("fixture Verify() error = %v", err)
	}
}

func testP256PrivateKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey
}

func newTestP256IdentityProof(t *testing.T, privateKey *ecdsa.PrivateKey, nonce string) IdentityProof {
	t.Helper()
	publicKey := testP256PublicKeyBytes(t, privateKey)
	nodeID, err := DeriveIDForAlgorithm(KeyAlgorithmECDSAP256SHA256, publicKey)
	if err != nil {
		t.Fatal(err)
	}
	catalog := CapabilityCatalog{Commands: []CommandDescriptor{}}
	catalogHash, err := catalog.Hash()
	if err != nil {
		t.Fatal(err)
	}
	proof := IdentityProof{
		Nonce: nonce, NodeID: nodeID, PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		KeyAlgorithm: KeyAlgorithmECDSAP256SHA256, MinProtocol: ProtocolV1, MaxProtocol: ProtocolV1,
		ClientVersion: "android-test", Platform: "android", Architecture: "arm64-v8a",
		RequestedRole: "companion", CatalogHash: catalogHash, Catalog: catalog,
	}
	signTestP256IdentityProof(t, privateKey, &proof)
	return proof
}

func signTestP256IdentityProof(t *testing.T, privateKey *ecdsa.PrivateKey, proof *IdentityProof) {
	t.Helper()
	transcript, err := proof.transcript()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(transcript)
	r, s, err := ecdsa.Sign(rand.Reader, privateKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	halfOrder := new(big.Int).Rsh(new(big.Int).Set(elliptic.P256().Params().N), 1)
	if s.Cmp(halfOrder) > 0 {
		s.Sub(elliptic.P256().Params().N, s)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	proof.Signature = base64.RawURLEncoding.EncodeToString(signature)
}

func testP256PublicKeyBytes(t *testing.T, privateKey *ecdsa.PrivateKey) []byte {
	t.Helper()
	publicKey, err := privateKey.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	return publicKey.Bytes()
}

func mustDecodeIdentityValue(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
