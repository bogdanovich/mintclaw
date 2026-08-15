package nodes

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnrollmentOfferIssueAndURIRoundTrip(t *testing.T) {
	now := time.Unix(1000, 0)
	randomBytes := make([]byte, enrollmentOfferIDBytes+enrollmentSecretBytes)
	for index := range randomBytes {
		randomBytes[index] = byte(index + 1)
	}
	manager := NewEnrollmentOfferManager(EnrollmentOfferConfig{
		Random: bytes.NewReader(randomBytes), Now: func() time.Time { return now },
	})
	pin := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	offer, err := manager.Issue("wss://gateway.example/nodes/v1/ws", pin, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if offer.Version != EnrollmentOfferVersion || offer.Platform != "android" ||
		offer.RequestedRole != "companion" || offer.IssuedAt != now.Unix() ||
		offer.ExpiresAt != now.Add(2*time.Minute).Unix() || offer.SPKISHA256 != pin {
		t.Fatalf("offer = %#v", offer)
	}
	uri, err := EncodeEnrollmentOfferURI(offer)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEnrollmentOfferURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != offer {
		t.Fatalf("decoded offer = %#v, want %#v", decoded, offer)
	}
}

func TestEnrollmentOfferRejectsUnsafeEndpointAndPin(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		pin      string
	}{
		{name: "plaintext", endpoint: "ws://gateway.example/nodes/v1/ws"},
		{name: "userinfo", endpoint: "wss://user@gateway.example/nodes/v1/ws"},
		{name: "query", endpoint: "wss://gateway.example/nodes/v1/ws?token=x"},
		{name: "fragment", endpoint: "wss://gateway.example/nodes/v1/ws#fragment"},
		{name: "wrong path", endpoint: "wss://gateway.example/ws"},
		{name: "uppercase pin", endpoint: "wss://gateway.example/nodes/v1/ws", pin: strings.Repeat("A", 64)},
		{name: "short pin", endpoint: "wss://gateway.example/nodes/v1/ws", pin: "abcd"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewEnrollmentOfferManager(EnrollmentOfferConfig{})
			if _, err := manager.Issue(test.endpoint, test.pin, time.Minute); !errors.Is(
				err,
				ErrEnrollmentOfferInvalid,
			) {
				t.Fatalf("Issue() error = %v", err)
			}
		})
	}
}

func TestEnrollmentOfferCapacityPrunesExpiredOffers(t *testing.T) {
	now := time.Unix(1000, 0)
	manager := NewEnrollmentOfferManager(EnrollmentOfferConfig{
		MaxOffers: 1, Random: newIncrementingReader(), Now: func() time.Time { return now },
	})
	if _, err := manager.Issue("wss://gateway.example/nodes/v1/ws", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Issue(
		"wss://gateway.example/nodes/v1/ws",
		"",
		time.Minute,
	); !errors.Is(err, ErrEnrollmentOfferBusy) {
		t.Fatalf("capacity Issue() error = %v", err)
	}
	now = now.Add(time.Minute)
	if _, err := manager.Issue("wss://gateway.example/nodes/v1/ws", "", time.Minute); err != nil {
		t.Fatalf("Issue() after expiry error = %v", err)
	}
}

func TestEnrollmentOfferBoundsConfigurationAndLifetime(t *testing.T) {
	manager := NewEnrollmentOfferManager(EnrollmentOfferConfig{MaxOffers: MaxEnrollmentOffers + 1})
	if manager.maxOffers != MaxEnrollmentOffers {
		t.Fatalf("max offers = %d, want %d", manager.maxOffers, MaxEnrollmentOffers)
	}
	for _, ttl := range []time.Duration{-time.Second, time.Millisecond, time.Second + time.Millisecond} {
		if _, err := manager.Issue("wss://gateway.example/nodes/v1/ws", "", ttl); !errors.Is(
			err,
			ErrEnrollmentOfferInvalid,
		) {
			t.Fatalf("Issue(ttl=%s) error = %v", ttl, err)
		}
	}
}

func TestEnrollmentOfferConsumeIsSingleUseAndCommitAtomic(t *testing.T) {
	manager := NewEnrollmentOfferManager(EnrollmentOfferConfig{})
	privateKey := testP256PrivateKey(t)
	offer, proof := newTestEnrollmentProof(t, manager, privateKey)
	commitErr := errors.New("registry unavailable")
	if err := manager.Consume(proof, func() error { return commitErr }); !errors.Is(err, commitErr) {
		t.Fatalf("failed commit Consume() error = %v", err)
	}
	commits := 0
	if err := manager.Consume(proof, func() error {
		commits++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if commits != 1 {
		t.Fatalf("commits = %d", commits)
	}
	if err := manager.Consume(proof, func() error { return nil }); !errors.Is(err, ErrEnrollmentOfferUnknown) {
		t.Fatalf("replayed offer %q error = %v", offer.OfferID, err)
	}
}

func TestEnrollmentOfferInvalidProofDoesNotConsume(t *testing.T) {
	manager := NewEnrollmentOfferManager(EnrollmentOfferConfig{})
	offer, proof := newTestEnrollmentProof(t, manager, testP256PrivateKey(t))
	invalid := proof
	invalid.EnrollmentProof = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, 32))
	if err := manager.Consume(invalid, func() error { return nil }); !errors.Is(err, ErrEnrollmentOfferInvalid) {
		t.Fatalf("invalid Consume() error = %v", err)
	}
	if err := manager.Consume(proof, func() error { return nil }); err != nil {
		t.Fatalf("valid offer %q was consumed by invalid proof: %v", offer.OfferID, err)
	}
}

func TestEnrollmentOfferIsInvalidatedByManagerRestart(t *testing.T) {
	manager := NewEnrollmentOfferManager(EnrollmentOfferConfig{})
	_, proof := newTestEnrollmentProof(t, manager, testP256PrivateKey(t))
	restarted := NewEnrollmentOfferManager(EnrollmentOfferConfig{})
	if err := restarted.Consume(proof, func() error { return nil }); !errors.Is(err, ErrEnrollmentOfferUnknown) {
		t.Fatalf("restarted Consume() error = %v", err)
	}
}

func TestEnrollmentOfferRejectsWrongPlatformAndKeyAlgorithm(t *testing.T) {
	manager := NewEnrollmentOfferManager(EnrollmentOfferConfig{})
	_, proof := newTestEnrollmentProof(t, manager, testP256PrivateKey(t))
	for _, mutate := range []func(*IdentityProof){
		func(candidate *IdentityProof) { candidate.Platform = "linux" },
		func(candidate *IdentityProof) { candidate.KeyAlgorithm = KeyAlgorithmEd25519 },
	} {
		candidate := proof
		mutate(&candidate)
		if err := manager.Consume(candidate, func() error { return nil }); !errors.Is(err, ErrEnrollmentRequired) {
			t.Fatalf("wrong identity Consume() error = %v", err)
		}
	}
	if err := manager.Consume(proof, func() error { return nil }); err != nil {
		t.Fatalf("wrong identity consumed valid offer: %v", err)
	}
}

func TestEnrollmentOfferConcurrentConsumeCommitsExactlyOnce(t *testing.T) {
	manager := NewEnrollmentOfferManager(EnrollmentOfferConfig{})
	_, proof := newTestEnrollmentProof(t, manager, testP256PrivateKey(t))
	start := make(chan struct{})
	var commits atomic.Int32
	errorsSeen := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsSeen <- manager.Consume(proof, func() error {
				commits.Add(1)
				return nil
			})
		}()
	}
	close(start)
	wait.Wait()
	close(errorsSeen)
	var succeeded, rejected int
	for err := range errorsSeen {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrEnrollmentOfferUnknown):
			rejected++
		default:
			t.Fatalf("Consume() error = %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 || commits.Load() != 1 {
		t.Fatalf("succeeded = %d, rejected = %d, commits = %d", succeeded, rejected, commits.Load())
	}
}

func TestEnrollmentOfferExpiresDeterministically(t *testing.T) {
	now := time.Unix(1000, 0)
	manager := NewEnrollmentOfferManager(EnrollmentOfferConfig{Now: func() time.Time { return now }})
	_, proof := newTestEnrollmentProof(t, manager, testP256PrivateKey(t))
	now = now.Add(DefaultEnrollmentOfferTTL)
	if err := manager.Consume(proof, func() error { return nil }); !errors.Is(err, ErrEnrollmentOfferExpired) {
		t.Fatalf("expired Consume() error = %v", err)
	}
}

func TestDecodeEnrollmentOfferURIRejectsMalformedPayloads(t *testing.T) {
	manager := NewEnrollmentOfferManager(EnrollmentOfferConfig{})
	offer, err := manager.Issue("wss://gateway.example/nodes/v1/ws", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := EncodeEnrollmentOfferURI(offer)
	if err != nil {
		t.Fatal(err)
	}
	for _, malformed := range []string{
		"", "https://enroll/v1?data=x", uri + "&extra=x", uri + "#fragment", strings.Repeat("x", 4097),
	} {
		if _, err := DecodeEnrollmentOfferURI(malformed); !errors.Is(err, ErrEnrollmentOfferInvalid) {
			t.Fatalf("DecodeEnrollmentOfferURI(%q) error = %v", malformed, err)
		}
	}
}

func TestAndroidEnrollmentLanguageNeutralFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "enrollment-android.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SchemaVersion int             `json:"schema_version"`
		Offer         EnrollmentOffer `json:"offer"`
		URI           string          `json:"uri"`
		Proof         IdentityProof   `json:"proof"`
		Transcript    string          `json:"transcript_base64url"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 {
		t.Fatalf("schema version = %d", fixture.SchemaVersion)
	}
	uri, err := EncodeEnrollmentOfferURI(fixture.Offer)
	if err != nil || uri != fixture.URI {
		t.Fatalf("encoded URI = %q, error = %v", uri, err)
	}
	if decoded, err := DecodeEnrollmentOfferURI(fixture.URI); err != nil || decoded != fixture.Offer {
		t.Fatalf("decoded offer = %#v, error = %v", decoded, err)
	}
	transcript, err := fixture.Proof.transcript()
	if err != nil {
		t.Fatal(err)
	}
	if base64.RawURLEncoding.EncodeToString(transcript) != fixture.Transcript {
		t.Fatal("fixture transcript changed")
	}
	if _, err := fixture.Proof.VerifyIdentity(); err != nil {
		t.Fatalf("fixture identity proof: %v", err)
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(fixture.Offer.Secret)
	if err != nil {
		t.Fatal(err)
	}
	wantProof := base64.RawURLEncoding.EncodeToString(
		enrollmentOfferProof(secret, fixture.Offer.OfferID, transcript),
	)
	if fixture.Proof.EnrollmentProof != wantProof {
		t.Fatalf("enrollment proof = %q, want %q", fixture.Proof.EnrollmentProof, wantProof)
	}
}

func newTestEnrollmentProof(
	t *testing.T,
	manager *EnrollmentOfferManager,
	privateKey *ecdsa.PrivateKey,
) (EnrollmentOffer, IdentityProof) {
	t.Helper()
	offer, err := manager.Issue("wss://gateway.example/nodes/v1/ws", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	proof := newTestP256IdentityProof(t, privateKey, "challenge")
	proof.EnrollmentOfferID = offer.OfferID
	signTestP256IdentityProof(t, privateKey, &proof)
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
	return offer, proof
}

type incrementingReader struct {
	mu   sync.Mutex
	next byte
}

func newIncrementingReader() *incrementingReader {
	return &incrementingReader{next: 1}
}

func (reader *incrementingReader) Read(buffer []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	for index := range buffer {
		buffer[index] = reader.next
		reader.next++
	}
	return len(buffer), nil
}
