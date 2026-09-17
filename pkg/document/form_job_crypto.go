package document

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const formJobKeyFileName = "document_form_jobs.v1.key"

type formJobEnvelope struct {
	Version       string `json:"version"`
	Kind          string `json:"kind"`
	JobID         string `json:"job_id"`
	FieldID       string `json:"field_id,omitempty"`
	EventID       string `json:"event_id,omitempty"`
	Revision      int64  `json:"revision"`
	BindingDigest string `json:"binding_digest,omitempty"`
	Nonce         string `json:"nonce"`
	Ciphertext    string `json:"ciphertext"`
}

type formJobWrappedKey struct {
	Version     string `json:"version"`
	JobID       string `json:"job_id"`
	OwnerDigest string `json:"owner_digest"`
	Nonce       string `json:"nonce"`
	Ciphertext  string `json:"ciphertext"`
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func keyedDigest(key []byte, values ...string) string {
	digest := hmac.New(sha256.New, key)
	for _, value := range values {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func keyedDigestBytes(key []byte, label string, value []byte) string {
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write([]byte(label))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(value)
	return hex.EncodeToString(digest.Sum(nil))
}

func loadOrCreateFormJobKey(keyRoot string, random io.Reader) ([]byte, error) {
	keyPath := filepath.Join(keyRoot, formJobKeyFileName)
	data, err := readProtectedRegularFile(keyPath, chacha20poly1305.KeySize)
	if err == nil {
		if len(data) != chacha20poly1305.KeySize {
			return nil, fmt.Errorf("%w: protected form key has invalid length", ErrFormJobKeyUnavailable)
		}
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %w", ErrFormJobKeyUnavailable, err)
	}
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(random, key); err != nil {
		return nil, fmt.Errorf("%w: generate protected form key: %w", ErrFormJobKeyUnavailable, err)
	}
	if err := fileutil.WriteFileAtomic(keyPath, key, 0o600); err != nil {
		clear(key)
		return nil, fmt.Errorf("%w: persist protected form key: %w", ErrFormJobKeyUnavailable, err)
	}
	return key, nil
}

func readProtectedRegularFile(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("protected file is not regular")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("protected file permissions are too broad: %04o", info.Mode().Perm())
	}
	if info.Size() < 0 || info.Size() > maxBytes {
		return nil, fmt.Errorf("protected file exceeds size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("protected file exceeds size limit")
	}
	return data, nil
}

func wrapFormJobKey(profileKey, jobKey []byte, jobID, ownerDigest string, random io.Reader) (formJobWrappedKey, error) {
	aead, err := chacha20poly1305.NewX(profileKey)
	if err != nil {
		return formJobWrappedKey{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return formJobWrappedKey{}, err
	}
	wrapper := formJobWrappedKey{
		Version:     FormJobEnvelopeVersion,
		JobID:       jobID,
		OwnerDigest: ownerDigest,
		Nonce:       base64.RawStdEncoding.EncodeToString(nonce),
	}
	wrapper.Ciphertext = base64.RawStdEncoding.EncodeToString(
		aead.Seal(nil, nonce, jobKey, wrappedFormJobKeyAAD(wrapper)),
	)
	return wrapper, nil
}

func unwrapFormJobKey(profileKey []byte, wrapped formJobWrappedKey) ([]byte, error) {
	if wrapped.Version != FormJobEnvelopeVersion || strings.TrimSpace(wrapped.JobID) == "" ||
		strings.TrimSpace(wrapped.OwnerDigest) == "" {
		return nil, ErrFormJobRecordCorrupt
	}
	aead, err := chacha20poly1305.NewX(profileKey)
	if err != nil {
		return nil, fmt.Errorf("%w: initialize key wrapper", ErrFormJobKeyUnavailable)
	}
	nonce, err := base64.RawStdEncoding.DecodeString(wrapped.Nonce)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, ErrFormJobRecordCorrupt
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(wrapped.Ciphertext)
	if err != nil {
		return nil, ErrFormJobRecordCorrupt
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, wrappedFormJobKeyAAD(wrapped))
	if err != nil || len(plaintext) != chacha20poly1305.KeySize {
		clear(plaintext)
		return nil, ErrFormJobRecordCorrupt
	}
	return plaintext, nil
}

func wrappedFormJobKeyAAD(wrapped formJobWrappedKey) []byte {
	return []byte(strings.Join([]string{
		wrapped.Version,
		"job_key",
		wrapped.JobID,
		wrapped.OwnerDigest,
	}, "\x00"))
}

func sealFormJobEnvelope(
	jobKey []byte,
	envelope formJobEnvelope,
	payload any,
	random io.Reader,
) (formJobEnvelope, error) {
	aead, err := chacha20poly1305.NewX(jobKey)
	if err != nil {
		return formJobEnvelope{}, err
	}
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return formJobEnvelope{}, err
	}
	defer clear(plaintext)
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return formJobEnvelope{}, err
	}
	envelope.Version = FormJobEnvelopeVersion
	envelope.Nonce = base64.RawStdEncoding.EncodeToString(nonce)
	envelope.Ciphertext = base64.RawStdEncoding.EncodeToString(
		aead.Seal(nil, nonce, plaintext, formJobEnvelopeAAD(envelope)),
	)
	return envelope, nil
}

func openFormJobEnvelope(jobKey []byte, envelope formJobEnvelope, target any) error {
	if envelope.Version != FormJobEnvelopeVersion || strings.TrimSpace(envelope.Kind) == "" ||
		strings.TrimSpace(envelope.JobID) == "" || envelope.Revision <= 0 {
		return ErrFormJobRecordCorrupt
	}
	aead, err := chacha20poly1305.NewX(jobKey)
	if err != nil {
		return fmt.Errorf("%w: initialize protected form envelope", ErrFormJobKeyUnavailable)
	}
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != aead.NonceSize() {
		return ErrFormJobRecordCorrupt
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return ErrFormJobRecordCorrupt
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, formJobEnvelopeAAD(envelope))
	if err != nil {
		return ErrFormJobRecordCorrupt
	}
	defer clear(plaintext)
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrFormJobRecordCorrupt
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrFormJobRecordCorrupt
	}
	return nil
}

func formJobEnvelopeAAD(envelope formJobEnvelope) []byte {
	return []byte(strings.Join([]string{
		envelope.Version,
		envelope.Kind,
		envelope.JobID,
		envelope.FieldID,
		envelope.EventID,
		fmt.Sprintf("%d", envelope.Revision),
		envelope.BindingDigest,
	}, "\x00"))
}

func randomFormJobID(random io.Reader) (string, error) {
	data := make([]byte, 16)
	if _, err := io.ReadFull(random, data); err != nil {
		return "", err
	}
	return "form_job_" + hex.EncodeToString(data), nil
}

func newFormJobDataKey(random io.Reader) ([]byte, error) {
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(random, key); err != nil {
		return nil, err
	}
	return key, nil
}
