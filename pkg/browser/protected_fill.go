package browser

import (
	"bytes"
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxProtectedFillBindingBytes = 4096
	protectedFillBindingDomain   = "mintclaw.browser.protected-fill.v1\x00"
)

type protectedFillBinding struct {
	Version              int                  `json:"version"`
	Request              PrepareActionRequest `json:"request"`
	ControllerGeneration uint64               `json:"controller_generation"`
	ProfileRevision      string               `json:"profile_revision"`
	PolicyRevision       string               `json:"policy_revision"`
	CatalogRevision      string               `json:"catalog_revision"`
	IssuedAt             int64                `json:"issued_at"`
	ExpiresAt            int64                `json:"expires_at"`
}

// ProtectedFillTarget is an authenticated value-free intent, not proof that its
// target is still usable or that a native interaction accepted an answer.
type ProtectedFillTarget struct {
	Request   PrepareActionRequest
	ExpiresAt int64
}

func (broker *Broker) InspectProtectedFillBinding(
	ctx context.Context, owner Owner, token string,
) (ProtectedFillTarget, error) {
	if err := ctx.Err(); err != nil {
		return ProtectedFillTarget{}, err
	}
	binding, err := broker.openProtectedFillBinding(token)
	if err != nil {
		return ProtectedFillTarget{}, err
	}
	if owner.Validate() != nil || binding.Request.Owner != owner {
		return ProtectedFillTarget{}, ErrNotFound
	}
	return ProtectedFillTarget{Request: binding.Request, ExpiresAt: binding.ExpiresAt}, nil
}

// ProtectedFillPreparation reads existing native authority for an accepted
// consumer continuation. It never reconstructs an answer. Active preparations
// require the same live input slot; terminal records remain replay-safe reads.
func (broker *Broker) ProtectedFillPreparation(
	ctx context.Context, owner Owner, preparedID string,
) (Preparation, error) {
	if err := ctx.Err(); err != nil {
		return Preparation{}, err
	}
	if owner.Validate() != nil || !validIdentifier(preparedID) {
		return Preparation{}, ErrInvalid
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	prepared, err := broker.store.GetPreparedAction(ctx, preparedID)
	if err != nil {
		return Preparation{}, err
	}
	if prepared.Owner != owner || prepared.Action.Kind != ActionFill || prepared.Action.Value != "" ||
		!validDigest(prepared.InputDigest) {
		return Preparation{}, ErrNotFound
	}
	invocation, err := broker.store.GetInvocation(ctx,
		derivedIdentifier("invocation", owner, prepared.SessionID, prepared.RequestID))
	if err != nil {
		return Preparation{}, err
	}
	if invocation.State.Terminal() {
		return preparationView(prepared), nil
	}
	session, slot, worker, err := broker.actionSessionLocked(ctx, owner, prepared.SessionID, prepared.TabID)
	if err != nil {
		return Preparation{}, err
	}
	if err = broker.validateActionInput(slot, prepared); err != nil {
		return Preparation{}, err
	}
	if err = broker.revalidatePreparedLocked(ctx, session, slot, worker, prepared); err != nil {
		return Preparation{}, err
	}
	return preparationView(prepared), nil
}

// CreateProtectedFillBinding binds a value-free fill intent to this broker
// generation and one fresh native target. It does not prepare or execute an
// action, retain an answer, create an interaction, or authorize input ingress.
func (broker *Broker) CreateProtectedFillBinding(
	ctx context.Context, request PrepareActionRequest,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !broker.validProtectedFillRequest(request) {
		return "", ErrInvalid
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	session, slot, worker, err := broker.actionPreparationContextLocked(ctx, request)
	if err != nil {
		return "", err
	}
	if slot.credentials != nil {
		return "", ErrDenied
	}
	draft, err := broker.resolvePreparedActionLocked(ctx, session, slot, worker, request, request.Action, "", 0)
	if err != nil {
		return "", broker.handleWorkerBoundaryErrorLocked(ctx, session, err)
	}
	now := broker.now().UTC()
	expires := now.Add(time.Duration(broker.config.Limits.Effective().PreparedSeconds) * time.Second).UnixNano()
	if session.ExpiresAt < expires {
		expires = session.ExpiresAt
	}
	binding := protectedFillBinding{
		Version: 1, Request: request, ControllerGeneration: session.ControllerGeneration,
		ProfileRevision: session.ProfileRevision, PolicyRevision: session.PolicyRevision,
		CatalogRevision: draft.CatalogRevision, IssuedAt: now.UnixNano(), ExpiresAt: expires,
	}
	return broker.sealProtectedFillBinding(binding)
}

// PrepareProtectedFill stages an authorized consumer's transient answer in the
// existing preparation/input slot. The consumer still owns prompt/route/claim
// authorization; normal ExecuteAction policy, approval and freshness checks
// remain mandatory. A binding cannot survive broker restart or retarget input.
func (broker *Broker) PrepareProtectedFill(
	ctx context.Context, owner Owner, token, value string,
) (Preparation, error) {
	if err := ctx.Err(); err != nil {
		return Preparation{}, err
	}
	binding, err := broker.openProtectedFillBinding(token)
	if err != nil {
		return Preparation{}, err
	}
	if binding.Request.Owner != owner || owner.Validate() != nil {
		return Preparation{}, ErrNotFound
	}
	request := binding.Request
	request.Action.Value = value
	if !utf8.ValidString(value) || broker.validateActionPreparation(request) != nil {
		return Preparation{}, ErrInvalid
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return Preparation{}, err
	}
	now := broker.now().UTC().UnixNano()
	if binding.IssuedAt > now || binding.ExpiresAt <= now {
		return Preparation{}, ErrStale
	}
	session, slot, worker, err := broker.actionPreparationContextLocked(ctx, request)
	if err != nil {
		return Preparation{}, err
	}
	if slot.credentials != nil || session.ControllerGeneration != binding.ControllerGeneration ||
		session.ProfileRevision != binding.ProfileRevision || session.PolicyRevision != binding.PolicyRevision ||
		worker.CatalogRevision() != binding.CatalogRevision {
		return Preparation{}, ErrStale
	}
	return broker.prepareActionInContextLocked(ctx, request, session, slot, worker)
}

func (broker *Broker) validProtectedFillRequest(request PrepareActionRequest) bool {
	if request.Action.Kind != ActionFill || request.Action.Ref == "" ||
		request.Action != (Action{Kind: ActionFill, Ref: request.Action.Ref}) ||
		request.DeclaredEffect != "" || request.Confirmation != "" || request.Upload != nil {
		return false
	}
	// This sentinel is used only by the pure request-shape validator. It never
	// enters a preparation, an input slot, or a worker request.
	request.Action.Value = "?"
	return broker.validateActionPreparation(request) == nil
}

func (broker *Broker) sealProtectedFillBinding(binding protectedFillBinding) (string, error) {
	if binding.ExpiresAt <= binding.IssuedAt {
		return "", ErrInvalid
	}
	key, err := broker.protectedFillSigningKey()
	if err != nil {
		return "", err
	}
	defer clear(key)
	data, err := json.Marshal(binding)
	if err != nil {
		return "", ErrInvalid
	}
	defer clear(data)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	token := base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(token) > maxProtectedFillBindingBytes {
		return "", ErrInvalid
	}
	return token, nil
}

func (broker *Broker) openProtectedFillBinding(token string) (protectedFillBinding, error) {
	if len(token) == 0 || len(token) > maxProtectedFillBindingBytes || len(broker.bindingKey) != sha256.Size {
		return protectedFillBinding{}, ErrInvalid
	}
	payload, signature, found := strings.Cut(token, ".")
	if !found || len(signature) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
		return protectedFillBinding{}, ErrInvalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(payload)
	if err != nil {
		return protectedFillBinding{}, ErrInvalid
	}
	defer clear(data)
	actual, err := base64.RawURLEncoding.Strict().DecodeString(signature)
	if err != nil {
		return protectedFillBinding{}, ErrInvalid
	}
	defer clear(actual)
	key, err := broker.protectedFillSigningKey()
	if err != nil {
		return protectedFillBinding{}, err
	}
	defer clear(key)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	if !hmac.Equal(actual, mac.Sum(nil)) {
		return protectedFillBinding{}, ErrInvalid
	}
	var binding protectedFillBinding
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&binding) != nil || decoder.Decode(new(any)) != io.EOF || binding.Version != 1 ||
		binding.ControllerGeneration == 0 || binding.IssuedAt <= 0 || binding.ExpiresAt <= binding.IssuedAt ||
		!broker.validProtectedFillRequest(binding.Request) {
		return protectedFillBinding{}, ErrInvalid
	}
	return binding, nil
}

func (broker *Broker) protectedFillSigningKey() ([]byte, error) {
	if len(broker.bindingKey) != sha256.Size {
		return nil, ErrInvalid
	}
	// Native InputDigest authenticates arbitrary input with bindingKey. Derive
	// a separate key, not a MAC prefix that the input API could reproduce.
	key, err := hkdf.Key(sha256.New, broker.bindingKey, nil, protectedFillBindingDomain, sha256.Size)
	if err != nil {
		return nil, ErrInvalid
	}
	return key, nil
}
