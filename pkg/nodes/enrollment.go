package nodes

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	EnrollmentOfferVersion        = 1
	DefaultEnrollmentOfferTTL     = 5 * time.Minute
	MaxEnrollmentOfferTTL         = 5 * time.Minute
	DefaultMaxEnrollmentOffers    = 64
	MaxEnrollmentOffers           = 64
	MaxEnrollmentOfferPayloadSize = 4096

	enrollmentOfferIDBytes = 16
	enrollmentSecretBytes  = 32
	enrollmentProofDomain  = "mintclaw-android-enrollment-v1\x00"
)

var (
	ErrEnrollmentRequired     = errors.New("android node enrollment offer is required")
	ErrEnrollmentOfferInvalid = errors.New("invalid Android node enrollment offer")
	ErrEnrollmentOfferUnknown = errors.New("android node enrollment offer is unknown or already used")
	ErrEnrollmentOfferExpired = errors.New("android node enrollment offer expired")
	ErrEnrollmentOfferBusy    = errors.New("android node enrollment offer capacity reached")
)

// EnrollmentOffer is the bounded, sensitive QR payload returned once to an
// authenticated operator. It grants permission only to request pairing.
type EnrollmentOffer struct {
	Version       int    `json:"version"`
	OfferID       string `json:"offer_id"`
	Secret        string `json:"secret"`
	Endpoint      string `json:"endpoint"`
	SPKISHA256    string `json:"spki_sha256,omitempty"`
	RequestedRole string `json:"requested_role"`
	Platform      string `json:"platform"`
	IssuedAt      int64  `json:"issued_at"`
	ExpiresAt     int64  `json:"expires_at"`
}

type EnrollmentOfferRequest struct {
	Endpoint   string `json:"endpoint"`
	SPKISHA256 string `json:"spki_sha256,omitempty"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

type EnrollmentOfferResponse struct {
	Offer EnrollmentOffer `json:"offer"`
	URI   string          `json:"uri"`
}

type EnrollmentOfferConfig struct {
	MaxOffers int
	Random    io.Reader
	Now       func() time.Time
}

type enrollmentOfferRecord struct {
	offer  EnrollmentOffer
	secret []byte
}

// EnrollmentOfferManager owns only live, unconsumed offers in one gateway
// admission generation. Restart or reconciliation discards the manager.
type EnrollmentOfferManager struct {
	mu        sync.Mutex
	maxOffers int
	random    io.Reader
	now       func() time.Time
	offers    map[string]enrollmentOfferRecord
}

func NewEnrollmentOfferManager(cfg EnrollmentOfferConfig) *EnrollmentOfferManager {
	if cfg.MaxOffers <= 0 {
		cfg.MaxOffers = DefaultMaxEnrollmentOffers
	} else if cfg.MaxOffers > MaxEnrollmentOffers {
		cfg.MaxOffers = MaxEnrollmentOffers
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &EnrollmentOfferManager{
		maxOffers: cfg.MaxOffers,
		random:    cfg.Random,
		now:       cfg.Now,
		offers:    make(map[string]enrollmentOfferRecord),
	}
}

func (manager *EnrollmentOfferManager) Issue(
	endpoint string,
	spkiSHA256 string,
	ttl time.Duration,
) (EnrollmentOffer, error) {
	endpoint, spkiSHA256, err := validateEnrollmentEndpoint(endpoint, spkiSHA256)
	if err != nil {
		return EnrollmentOffer{}, err
	}
	if ttl == 0 {
		ttl = DefaultEnrollmentOfferTTL
	}
	if ttl < time.Second || ttl%time.Second != 0 {
		return EnrollmentOffer{}, fmt.Errorf("%w: lifetime must be positive", ErrEnrollmentOfferInvalid)
	}
	if ttl > MaxEnrollmentOfferTTL {
		return EnrollmentOffer{}, fmt.Errorf("%w: lifetime exceeds five minutes", ErrEnrollmentOfferInvalid)
	}
	now := manager.now()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.pruneExpiredLocked(now)
	if len(manager.offers) >= manager.maxOffers {
		return EnrollmentOffer{}, ErrEnrollmentOfferBusy
	}
	randomBytes := make([]byte, enrollmentOfferIDBytes+enrollmentSecretBytes)
	if _, err := io.ReadFull(manager.random, randomBytes); err != nil {
		return EnrollmentOffer{}, fmt.Errorf("generate Android enrollment offer: %w", err)
	}
	offerID := base64.RawURLEncoding.EncodeToString(randomBytes[:enrollmentOfferIDBytes])
	if _, exists := manager.offers[offerID]; exists {
		return EnrollmentOffer{}, errors.New("generated duplicate Android enrollment offer ID")
	}
	secret := append([]byte(nil), randomBytes[enrollmentOfferIDBytes:]...)
	offer := EnrollmentOffer{
		Version: EnrollmentOfferVersion, OfferID: offerID,
		Secret: base64.RawURLEncoding.EncodeToString(secret), Endpoint: endpoint, SPKISHA256: spkiSHA256,
		RequestedRole: "companion", Platform: "android", IssuedAt: now.Unix(), ExpiresAt: now.Add(ttl).Unix(),
	}
	if _, err := EncodeEnrollmentOfferURI(offer); err != nil {
		return EnrollmentOffer{}, err
	}
	manager.offers[offerID] = enrollmentOfferRecord{offer: offer, secret: secret}
	return offer, nil
}

// Consume verifies an Android proof and commits the pending pairing while the
// offer is exclusively held. The offer is deleted only after commit succeeds.
func (manager *EnrollmentOfferManager) Consume(proof IdentityProof, commit func() error) error {
	if manager == nil || proof.Platform != "android" || proof.KeyAlgorithm != KeyAlgorithmECDSAP256SHA256 ||
		proof.EnrollmentOfferID == "" || proof.EnrollmentProof == "" {
		return ErrEnrollmentRequired
	}
	if commit == nil {
		return errors.New("android enrollment commit is required")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	now := manager.now()
	record, exists := manager.offers[proof.EnrollmentOfferID]
	if !exists {
		return ErrEnrollmentOfferUnknown
	}
	if now.Unix() >= record.offer.ExpiresAt {
		delete(manager.offers, proof.EnrollmentOfferID)
		return ErrEnrollmentOfferExpired
	}
	transcript, err := proof.transcript()
	if err != nil {
		return err
	}
	provided, err := base64.RawURLEncoding.Strict().DecodeString(proof.EnrollmentProof)
	if err != nil || len(provided) != sha256.Size {
		return ErrEnrollmentOfferInvalid
	}
	expected := enrollmentOfferProof(record.secret, proof.EnrollmentOfferID, transcript)
	if !hmac.Equal(provided, expected) {
		return ErrEnrollmentOfferInvalid
	}
	if err := commit(); err != nil {
		return err
	}
	delete(manager.offers, proof.EnrollmentOfferID)
	return nil
}

func EncodeEnrollmentOfferURI(offer EnrollmentOffer) (string, error) {
	if err := validateEnrollmentOffer(offer); err != nil {
		return "", err
	}
	data, err := json.Marshal(offer)
	if err != nil {
		return "", fmt.Errorf("%w: encode payload: %w", ErrEnrollmentOfferInvalid, err)
	}
	if len(data) > MaxEnrollmentOfferPayloadSize {
		return "", fmt.Errorf("%w: payload exceeds limit", ErrEnrollmentOfferInvalid)
	}
	uri := (&url.URL{
		Scheme: "mintclaw", Host: "enroll", Path: "/v1",
		RawQuery: "data=" + base64.RawURLEncoding.EncodeToString(data),
	}).String()
	if len(uri) > MaxEnrollmentOfferPayloadSize {
		return "", fmt.Errorf("%w: URI exceeds limit", ErrEnrollmentOfferInvalid)
	}
	return uri, nil
}

func DecodeEnrollmentOfferURI(uri string) (EnrollmentOffer, error) {
	if len(uri) == 0 || len(uri) > MaxEnrollmentOfferPayloadSize {
		return EnrollmentOffer{}, ErrEnrollmentOfferInvalid
	}
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "mintclaw" || parsed.Host != "enroll" || parsed.Path != "/v1" ||
		parsed.User != nil || parsed.Fragment != "" {
		return EnrollmentOffer{}, ErrEnrollmentOfferInvalid
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return EnrollmentOffer{}, ErrEnrollmentOfferInvalid
	}
	values, exists := query["data"]
	if !exists || len(values) != 1 || len(query) != 1 {
		return EnrollmentOffer{}, ErrEnrollmentOfferInvalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(values[0])
	if err != nil || len(data) == 0 || len(data) > MaxEnrollmentOfferPayloadSize {
		return EnrollmentOffer{}, ErrEnrollmentOfferInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var offer EnrollmentOffer
	if err := decoder.Decode(&offer); err != nil {
		return EnrollmentOffer{}, ErrEnrollmentOfferInvalid
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return EnrollmentOffer{}, ErrEnrollmentOfferInvalid
	}
	if err := validateEnrollmentOffer(offer); err != nil {
		return EnrollmentOffer{}, err
	}
	return offer, nil
}

func validateEnrollmentOffer(offer EnrollmentOffer) error {
	if offer.Version != EnrollmentOfferVersion || offer.RequestedRole != "companion" || offer.Platform != "android" ||
		offer.IssuedAt <= 0 || offer.ExpiresAt <= offer.IssuedAt ||
		time.Duration(offer.ExpiresAt-offer.IssuedAt)*time.Second > MaxEnrollmentOfferTTL {
		return ErrEnrollmentOfferInvalid
	}
	id, err := base64.RawURLEncoding.Strict().DecodeString(offer.OfferID)
	if err != nil || len(id) != enrollmentOfferIDBytes {
		return ErrEnrollmentOfferInvalid
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(offer.Secret)
	if err != nil || len(secret) != enrollmentSecretBytes {
		return ErrEnrollmentOfferInvalid
	}
	_, _, err = validateEnrollmentEndpoint(offer.Endpoint, offer.SPKISHA256)
	return err
}

func validateEnrollmentEndpoint(endpoint, spkiSHA256 string) (string, string, error) {
	endpoint = strings.TrimSpace(endpoint)
	spkiSHA256 = strings.TrimSpace(spkiSHA256)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "wss" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Path != "/nodes/v1/ws" {
		return "", "", fmt.Errorf("%w: endpoint must be the exact secure node WebSocket URL", ErrEnrollmentOfferInvalid)
	}
	if spkiSHA256 != "" {
		decoded, decodeErr := hex.DecodeString(spkiSHA256)
		if decodeErr != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != spkiSHA256 {
			return "", "", fmt.Errorf("%w: malformed SPKI SHA-256", ErrEnrollmentOfferInvalid)
		}
	}
	return parsed.String(), spkiSHA256, nil
}

func enrollmentOfferProof(secret []byte, offerID string, transcript []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(enrollmentProofDomain))
	_, _ = mac.Write([]byte(offerID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(transcript)
	return mac.Sum(nil)
}

func (manager *EnrollmentOfferManager) pruneExpiredLocked(now time.Time) {
	for id, record := range manager.offers {
		if now.Unix() >= record.offer.ExpiresAt {
			delete(manager.offers, id)
		}
	}
}
