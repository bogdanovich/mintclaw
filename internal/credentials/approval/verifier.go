package approval

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func newVerifier(origin string) (*webauthn.WebAuthn, error) {
	if len(origin) == 0 || len(origin) > 512 {
		return nil, ErrUnavailable
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Path != "" ||
		parsed.RawPath != "" || parsed.Hostname() == "" || net.ParseIP(parsed.Hostname()) != nil ||
		strings.ToLower(parsed.Host) != parsed.Host || strings.HasSuffix(parsed.Hostname(), ".") ||
		parsed.String() != origin || parsed.Port() == "443" {
		return nil, ErrUnavailable
	}
	if port := parsed.Port(); port != "" {
		number, portErr := strconv.Atoi(port)
		if portErr != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
			return nil, ErrUnavailable
		}
	}
	for _, character := range []byte(parsed.Hostname()) {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') &&
			character != '.' && character != '-' {
			return nil, ErrUnavailable
		}
	}
	return webauthn.New(&webauthn.Config{
		RPID: parsed.Hostname(), RPDisplayName: "MintClaw credential approval", RPOrigins: []string{origin},
		RPTopOriginVerificationMode: protocol.TopOriginExplicitVerificationMode,
		AttestationPreference:       protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: PendingLifetime, TimeoutUVD: PendingLifetime},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: PendingLifetime, TimeoutUVD: PendingLifetime},
		},
	})
}

func (setup *OwnerSetup) Begin(ctx context.Context) (Ceremony, error) {
	return setup.begin(ctx, time.Time{})
}

func (setup *OwnerSetup) begin(ctx context.Context, issued time.Time) (Ceremony, error) {
	if setup == nil || setup.controller == nil {
		return Ceremony{}, ErrDenied
	}
	controller := setup.controller
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.usable(ctx) || !controller.enrollmentAllowed() ||
		(!issued.IsZero() && !withinLifetime(controller.now(), issued)) {
		return Ceremony{}, ErrDenied
	}
	controller.prune()
	// Only one initial enrollment may be pending. A failed ceremony is consumed;
	// the trusted owner can deliberately request another through this capability.
	for _, ceremony := range controller.ceremonies {
		if ceremony.kind == enroll {
			return Ceremony{}, ErrDenied
		}
	}
	ceremony, err := controller.begin(
		enroll,
		Scope{
			Principal:      credentialbroker.Principal{OwnerID: controller.ownerID},
			PolicyRevision: controller.policy.Revision(),
		},
		"",
	)
	if err != nil {
		return Ceremony{}, err
	}
	pending := controller.ceremonies[ceremony.ID]
	pending.enrollmentIssued = issued
	controller.ceremonies[ceremony.ID] = pending
	return ceremony, nil
}

func (setup *OwnerSetup) Finish(ctx context.Context, id string, response []byte) (resultErr error) {
	if setup == nil || setup.controller == nil {
		return ErrDenied
	}
	controller := setup.controller
	controller.mu.Lock()
	defer controller.mu.Unlock()
	defer func() {
		if recover() != nil {
			controller.fail()
			resultErr = ErrUnavailable
		}
	}()
	ceremony, err := controller.claim(ctx, id, enroll)
	if err != nil {
		return err
	}
	if !controller.enrollmentAllowed() || len(response) == 0 || len(response) > maxResponseBytes {
		return ErrDenied
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return ErrDenied
	}
	credential, err := controller.verifier.CreateCredential(controller.owner, *ceremony.session, parsed)
	if err != nil || credential == nil || credential.Authenticator.CloneWarning ||
		!credential.Flags.UserPresent || !credential.Flags.UserVerified {
		return ErrDenied
	}
	owner := controller.owner
	owner.Credentials = []webauthn.Credential{*credential}
	owner.EnrollmentPending = true
	if err = controller.persist(ctx, ceremony, owner); err != nil {
		return err
	}
	// This decision and the final original-invite/context check occur under
	// the controller lock. A rejected or uncertain write remains inactive on
	// disk without relying on a second rollback write that could also fail.
	// Only a later assertion from this accepted in-memory owner promotes the
	// durable record. Restart before then requires a fresh protected setup.
	controller.owner.EnrollmentPending = false
	return nil
}

func (controller *Controller) begin(
	kind ceremonyKind,
	scope Scope,
	requestID string,
) (result Ceremony, resultErr error) {
	defer func() {
		if recover() != nil {
			controller.fail()
			result, resultErr = Ceremony{}, ErrUnavailable
		}
	}()
	if len(controller.ceremonies) >= MaxPending {
		return Ceremony{}, ErrUnavailable
	}
	id, err := controller.newReference()
	if err != nil {
		return Ceremony{}, err
	}
	var options any
	var session *webauthn.SessionData
	if kind == enroll {
		options, session, err = controller.verifier.BeginRegistration(controller.owner,
			webauthn.WithRegistrationOrigin(controller.origin),
			webauthn.WithCredentialParameters([]protocol.CredentialParameter{
				{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgES256},
			}),
		)
	} else {
		options, session, err = controller.verifier.BeginLogin(controller.owner,
			webauthn.WithLoginOrigin(controller.origin), webauthn.WithUserVerification(protocol.VerificationRequired))
	}
	if err != nil || session == nil || session.UserVerification != protocol.VerificationRequired ||
		session.Expires.IsZero() {
		return Ceremony{}, ErrUnavailable
	}
	encoded, err := json.Marshal(options)
	if err != nil || len(encoded) > maxResponseBytes {
		return Ceremony{}, ErrUnavailable
	}
	controller.ceremonies[id] = pendingCeremony{
		kind: kind, session: session, scope: cloneScope(scope), request: requestID, issued: controller.now(),
		options: encoded,
	}
	return publicCeremony(id, controller.ceremonies[id]), nil
}

func publicCeremony(id string, pending pendingCeremony) Ceremony {
	return Ceremony{ID: id, Options: append([]byte(nil), pending.options...), Scope: cloneScope(pending.scope)}
}

func (controller *Controller) verifyAssertion(
	ctx context.Context,
	ceremony pendingCeremony,
	response []byte,
) (resultErr error) {
	defer func() {
		if recover() != nil {
			controller.fail()
			resultErr = ErrUnavailable
		}
	}()
	if len(response) == 0 || len(response) > maxResponseBytes {
		return ErrDenied
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return ErrDenied
	}
	credential, err := controller.verifier.ValidateLogin(controller.owner, *ceremony.session, parsed)
	if err != nil || credential == nil {
		return ErrDenied
	}
	if credential.Authenticator.CloneWarning {
		// Latch the warning durably so restarting cannot silently re-enable this
		// passkey. Recovery requires a separate protected operator procedure.
		owner := controller.owner
		owner.Credentials = []webauthn.Credential{*credential}
		err = controller.store.commit(owner)
		controller.fail()
		if err != nil {
			return ErrUnavailable
		}
		return ErrDenied
	}
	owner := controller.owner
	owner.Credentials = []webauthn.Credential{*credential}
	return controller.persist(ctx, ceremony, owner)
}

func (controller *Controller) persist(ctx context.Context, ceremony pendingCeremony, owner ownerRecord) error {
	if ctx.Err() != nil || !controller.ceremonyLive(ceremony) ||
		!owner.valid(controller.ownerID, controller.origin) {
		return ErrDenied
	}
	if err := controller.store.commit(owner); err != nil {
		controller.fail()
		return ErrUnavailable
	}
	controller.owner = owner
	if ctx.Err() != nil || !controller.ceremonyLive(ceremony) {
		return ErrDenied
	}
	return nil
}
