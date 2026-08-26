package nodes

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

const (
	MaxClientVersionLength     = 128
	MaxPlatformLength          = 64
	MaxArchitectureLength      = 64
	MaxRoleLength              = 32
	MaxEnrollmentOfferIDLength = 64

	KeyAlgorithmEd25519         KeyAlgorithm = "ed25519"
	KeyAlgorithmECDSAP256SHA256 KeyAlgorithm = "ecdsa-p256-sha256"
)

var ErrInvalidIdentityProof = errors.New("invalid node identity proof")

// KeyAlgorithm is the bounded node-authentication signing algorithm.
type KeyAlgorithm string

func (algorithm KeyAlgorithm) validate() error {
	switch algorithm {
	case KeyAlgorithmEd25519, KeyAlgorithmECDSAP256SHA256:
		return nil
	default:
		return fmt.Errorf("%w: unsupported key algorithm", ErrInvalidIdentityProof)
	}
}

// IdentityPublicKey is a canonical, algorithm-bound node identity key.
type IdentityPublicKey struct {
	Algorithm KeyAlgorithm
	Bytes     []byte
}

type IdentityProof struct {
	Nonce             string            `json:"nonce"`
	NodeID            ID                `json:"node_id"`
	PublicKey         string            `json:"public_key"`
	KeyAlgorithm      KeyAlgorithm      `json:"key_algorithm"`
	EnrollmentOfferID string            `json:"enrollment_offer_id,omitempty"`
	EnrollmentProof   string            `json:"enrollment_proof,omitempty"`
	Signature         string            `json:"signature"`
	MinProtocol       int               `json:"min_protocol"`
	MaxProtocol       int               `json:"max_protocol"`
	ClientVersion     string            `json:"client_version"`
	Platform          string            `json:"platform"`
	Architecture      string            `json:"architecture"`
	RequestedRole     string            `json:"requested_role"`
	CatalogHash       string            `json:"catalog_hash"`
	Catalog           CapabilityCatalog `json:"catalog"`
	Executor          string            `json:"executor,omitempty"`
	PolicyRevision    string            `json:"policy_revision,omitempty"`
}

type identityTranscript struct {
	Nonce             string       `json:"nonce"`
	NodeID            ID           `json:"node_id"`
	PublicKey         string       `json:"public_key"`
	KeyAlgorithm      KeyAlgorithm `json:"key_algorithm"`
	EnrollmentOfferID string       `json:"enrollment_offer_id,omitempty"`
	MinProtocol       int          `json:"min_protocol"`
	MaxProtocol       int          `json:"max_protocol"`
	ClientVersion     string       `json:"client_version"`
	Platform          string       `json:"platform"`
	Architecture      string       `json:"architecture"`
	RequestedRole     string       `json:"requested_role"`
	CatalogHash       string       `json:"catalog_hash"`
	Executor          string       `json:"executor"`
	PolicyRevision    string       `json:"policy_revision"`
}

func DeriveID(publicKey ed25519.PublicKey) (ID, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return "", fmt.Errorf("%w: malformed public key", ErrInvalidIdentityProof)
	}
	sum := sha256.Sum256(publicKey)
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return ID("node_" + strings.ToLower(encoded)), nil
}

// DeriveIDForAlgorithm derives an ID from a canonical public-key encoding.
// Ed25519 IDs retain their established byte representation.
func DeriveIDForAlgorithm(algorithm KeyAlgorithm, publicKey []byte) (ID, error) {
	if err := algorithm.validate(); err != nil {
		return "", err
	}
	if algorithm == KeyAlgorithmEd25519 {
		return DeriveID(ed25519.PublicKey(publicKey))
	}
	if _, err := parseP256PublicKey(publicKey); err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("mintclaw-node-id-v1\x00"))
	_, _ = hash.Write([]byte(algorithm))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(publicKey)
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash.Sum(nil))
	return ID("node_" + strings.ToLower(encoded)), nil
}

func NewIdentityProof(
	privateKey ed25519.PrivateKey,
	nonce string,
	minProtocol, maxProtocol int,
	clientVersion, platform, architecture string,
	catalog CapabilityCatalog,
	profile ExecutionProfile,
) (IdentityProof, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return IdentityProof{}, fmt.Errorf("%w: malformed private key", ErrInvalidIdentityProof)
	}
	if err := profile.ValidateOptional(); err != nil {
		return IdentityProof{}, err
	}
	if err := validateCompanionCatalog(catalog); err != nil {
		return IdentityProof{}, err
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	nodeID, err := DeriveID(publicKey)
	if err != nil {
		return IdentityProof{}, err
	}
	catalogHash, err := catalog.Hash()
	if err != nil {
		return IdentityProof{}, err
	}
	if catalog.Commands == nil {
		catalog.Commands = make([]CommandDescriptor, 0)
	}
	proof := IdentityProof{
		Nonce:          nonce,
		NodeID:         nodeID,
		PublicKey:      base64.RawURLEncoding.EncodeToString(publicKey),
		KeyAlgorithm:   KeyAlgorithmEd25519,
		MinProtocol:    minProtocol,
		MaxProtocol:    maxProtocol,
		ClientVersion:  clientVersion,
		Platform:       platform,
		Architecture:   architecture,
		RequestedRole:  "companion",
		CatalogHash:    catalogHash,
		Catalog:        catalog,
		Executor:       profile.Executor,
		PolicyRevision: profile.PolicyRevision,
	}
	transcript, err := proof.transcript()
	if err != nil {
		return IdentityProof{}, err
	}
	proof.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, transcript))
	return proof, nil
}

// VerifyIdentity verifies either admitted node-authentication algorithm.
func (proof IdentityProof) VerifyIdentity() (IdentityPublicKey, error) {
	if err := proof.validateClaims(); err != nil {
		return IdentityPublicKey{}, err
	}
	if err := proof.KeyAlgorithm.validate(); err != nil {
		return IdentityPublicKey{}, err
	}
	algorithm := proof.KeyAlgorithm
	publicKey, err := base64.RawURLEncoding.Strict().DecodeString(proof.PublicKey)
	if err != nil {
		return IdentityPublicKey{}, fmt.Errorf("%w: malformed public key", ErrInvalidIdentityProof)
	}
	derivedID, err := DeriveIDForAlgorithm(algorithm, publicKey)
	if err != nil {
		return IdentityPublicKey{}, err
	}
	if derivedID != proof.NodeID {
		return IdentityPublicKey{}, fmt.Errorf("%w: node id does not match public key", ErrInvalidIdentityProof)
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(proof.Signature)
	if err != nil || len(signature) != 64 {
		return IdentityPublicKey{}, fmt.Errorf("%w: malformed signature", ErrInvalidIdentityProof)
	}
	transcript, err := proof.transcript()
	if err != nil {
		return IdentityPublicKey{}, err
	}
	switch algorithm {
	case KeyAlgorithmEd25519:
		if len(publicKey) != ed25519.PublicKeySize || !ed25519.Verify(publicKey, transcript, signature) {
			return IdentityPublicKey{}, fmt.Errorf("%w: signature verification failed", ErrInvalidIdentityProof)
		}
	case KeyAlgorithmECDSAP256SHA256:
		parsed, parseErr := parseP256PublicKey(publicKey)
		if parseErr != nil {
			return IdentityPublicKey{}, parseErr
		}
		r := new(big.Int).SetBytes(signature[:32])
		s := new(big.Int).SetBytes(signature[32:])
		halfOrder := new(big.Int).Rsh(new(big.Int).Set(elliptic.P256().Params().N), 1)
		if r.Sign() <= 0 || r.Cmp(elliptic.P256().Params().N) >= 0 || s.Sign() <= 0 || s.Cmp(halfOrder) > 0 {
			return IdentityPublicKey{}, fmt.Errorf("%w: non-canonical signature", ErrInvalidIdentityProof)
		}
		digest := sha256.Sum256(transcript)
		if !ecdsa.Verify(parsed, digest[:], r, s) {
			return IdentityPublicKey{}, fmt.Errorf("%w: signature verification failed", ErrInvalidIdentityProof)
		}
	}
	return IdentityPublicKey{Algorithm: algorithm, Bytes: append([]byte(nil), publicKey...)}, nil
}

func parseP256PublicKey(encoded []byte) (*ecdsa.PublicKey, error) {
	if len(encoded) != 65 || encoded[0] != 4 {
		return nil, fmt.Errorf("%w: malformed public key", ErrInvalidIdentityProof)
	}
	if _, err := ecdh.P256().NewPublicKey(encoded); err != nil {
		return nil, fmt.Errorf("%w: malformed public key", ErrInvalidIdentityProof)
	}
	x := new(big.Int).SetBytes(encoded[1:33])
	y := new(big.Int).SetBytes(encoded[33:])
	return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, nil
}

func (proof IdentityProof) validateClaims() error {
	if proof.Nonce == "" || len(proof.Nonce) > MaxIDLength {
		return fmt.Errorf("%w: malformed nonce", ErrInvalidIdentityProof)
	}
	if err := proof.NodeID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidIdentityProof, err)
	}
	if (proof.EnrollmentOfferID == "") != (proof.EnrollmentProof == "") ||
		len(proof.EnrollmentOfferID) > MaxEnrollmentOfferIDLength {
		return fmt.Errorf("%w: malformed enrollment proof", ErrInvalidIdentityProof)
	}
	if proof.EnrollmentProof != "" {
		decoded, decodeErr := base64.RawURLEncoding.Strict().DecodeString(proof.EnrollmentProof)
		if decodeErr != nil || len(decoded) != sha256.Size {
			return fmt.Errorf("%w: malformed enrollment proof", ErrInvalidIdentityProof)
		}
	}
	if proof.MinProtocol <= 0 || proof.MaxProtocol < proof.MinProtocol ||
		proof.MinProtocol > ProtocolV1 || proof.MaxProtocol < ProtocolV1 {
		return fmt.Errorf("%w: incompatible protocol range", ErrInvalidIdentityProof)
	}
	if len(proof.ClientVersion) == 0 || len(proof.ClientVersion) > MaxClientVersionLength ||
		len(proof.Platform) == 0 || len(proof.Platform) > MaxPlatformLength ||
		len(proof.Architecture) == 0 || len(proof.Architecture) > MaxArchitectureLength ||
		proof.RequestedRole != "companion" || len(proof.RequestedRole) > MaxRoleLength {
		return fmt.Errorf("%w: malformed client claims", ErrInvalidIdentityProof)
	}
	if err := proof.Catalog.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidIdentityProof, err)
	}
	if err := validateCompanionCatalog(proof.Catalog); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidIdentityProof, err)
	}
	catalogHash, err := proof.Catalog.Hash()
	if err != nil || catalogHash != proof.CatalogHash {
		return fmt.Errorf("%w: catalog hash does not match catalog", ErrInvalidIdentityProof)
	}
	if err := (ExecutionProfile{
		Executor:       proof.Executor,
		PolicyRevision: proof.PolicyRevision,
	}).ValidateOptional(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidIdentityProof, err)
	}
	return nil
}

func validateCompanionCatalog(catalog CapabilityCatalog) error {
	for _, descriptor := range catalog.Commands {
		for _, profile := range descriptor.ServiceProfiles {
			if profile.ActionApproval != "required" {
				return fmt.Errorf(
					"%w: companion supplied gateway-only service approval state",
					ErrInvalidCapability,
				)
			}
		}
	}
	return nil
}

func (proof IdentityProof) transcript() ([]byte, error) {
	if err := proof.KeyAlgorithm.validate(); err != nil {
		return nil, err
	}
	algorithm := proof.KeyAlgorithm
	prefix := "mintclaw-node-auth-v1:" + string(algorithm) + "\x00"
	data, err := json.Marshal(identityTranscript{
		Nonce:             proof.Nonce,
		NodeID:            proof.NodeID,
		PublicKey:         proof.PublicKey,
		KeyAlgorithm:      algorithm,
		EnrollmentOfferID: proof.EnrollmentOfferID,
		MinProtocol:       proof.MinProtocol,
		MaxProtocol:       proof.MaxProtocol,
		ClientVersion:     proof.ClientVersion,
		Platform:          proof.Platform,
		Architecture:      proof.Architecture,
		RequestedRole:     proof.RequestedRole,
		CatalogHash:       proof.CatalogHash,
		Executor:          proof.Executor,
		PolicyRevision:    proof.PolicyRevision,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: encode signature transcript: %w", ErrInvalidIdentityProof, err)
	}
	return append([]byte(prefix), data...), nil
}
