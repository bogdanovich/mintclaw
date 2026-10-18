package browser

import (
	"context"
	"net/url"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/bogdanovich/mintclaw/internal/secretvalues"
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

// CredentialLoginRequest is runtime-owned admission metadata. Model tools may
// select an alias and current field references, but cannot supply the principal,
// policy revision, grant lease, vault consumer, or outcome evidence.
type CredentialLoginRequest struct {
	Owner              Owner
	SessionID          string
	TabID              string
	SnapshotID         string
	SnapshotGeneration uint64
	Account            string
	CredentialID       string
	LoginName          string
	Principal          credentialbroker.Principal
	PolicyRevision     string
	Fields             CredentialFields
}

type CredentialLoginResult struct {
	Alias        string                `json:"alias"`
	Outcome      string                `json:"outcome"`
	CredentialID string                `json:"credential_id,omitempty"`
	Candidates   []CredentialCandidate `json:"candidates,omitempty"`
}

// CredentialCandidate projects overview metadata only; never login values.
type CredentialCandidate struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}

// PrivateLoginConsumer lends credentials to the synchronous callback. The
// runtime implementation is the exact-item vault adapter, after approval.
// The second buffer is a password or computed TOTP according to Fields.Step.
type PrivateLoginConsumer func(context.Context, func([]byte, []byte) error) error

// CredentialGrant is the live authority supplied by the trusted runtime.
// Both the policy authority lease and integrated phone approval lease can
// implement it without exporting an issuer or private provider binding.
type CredentialGrant interface {
	Check(context.Context, credentialbroker.Request) error
	Done() <-chan struct{}
}

type credentialSession struct {
	lease        CredentialGrant
	request      credentialbroker.Request
	release      []func()
	cookies      map[string]struct{}
	closed       bool
	done         chan struct{}
	credentialID string
}

func (bound *credentialSession) check(ctx context.Context, origin string) error {
	request := bound.request
	request.Origin = origin
	return bound.lease.Check(ctx, request)
}

func (bound *credentialSession) close() {
	bound.closed = true
	for _, release := range bound.release {
		release()
	}
	bound.release = nil
	clear(bound.cookies)
	if bound.done != nil {
		close(bound.done)
		bound.done = nil
	}
}

func (bound *credentialSession) protectCookies(values []string) error {
	if bound.closed || len(values) > 128 {
		return errCredentialDriver
	}
	if bound.cookies == nil {
		bound.cookies = make(map[string]struct{})
	}
	for _, value := range values {
		if value == "" || len(value) > 4096 {
			return errCredentialDriver
		}
		// Cookie inventories include public boolean flags. Registering these
		// known literals as secrets would redact structural numbers and opaque
		// browser refs process-wide. The private cookie envelope still suppresses
		// them at source; all other cookie values retain echo protection.
		switch value {
		case "0", "1", "true", "false":
			continue
		}
		if _, exists := bound.cookies[value]; exists {
			continue
		}
		if len(bound.cookies) >= 128 {
			return errCredentialDriver
		}
		release, err := secretvalues.Register([]byte(value))
		if err != nil {
			return errCredentialDriver
		}
		bound.cookies[value] = struct{}{}
		bound.release = append(bound.release, release)
	}
	return nil
}

// LoginCredential serializes observation, retrieval, private fill and submit
// under the existing broker lock. Values never enter ordinary actions, durable
// invocations or returned results. Unknown injection outcomes retire the worker.
func (broker *Broker) LoginCredential(
	ctx context.Context,
	request CredentialLoginRequest,
	lease CredentialGrant,
	consume PrivateLoginConsumer,
) CredentialLoginResult {
	result := CredentialLoginResult{Outcome: "denied"}
	if ctx == nil || ctx.Err() != nil || lease == nil || consume == nil || request.Owner.Validate() != nil ||
		!intent.ValidAlias(request.Account) || request.Principal.ActorID != request.Owner.ActorID ||
		OpaqueAgentID(request.Principal.AgentID) != request.Owner.AgentID {
		return result
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	session, slot, worker, err := broker.actionSessionLocked(ctx, request.Owner, request.SessionID, request.TabID)
	if err != nil || session.DryRun || session.FrameID != "" ||
		session.SnapshotID != request.SnapshotID || session.SnapshotGeneration != request.SnapshotGeneration {
		return result
	}
	profile, ok := broker.browserProfile(session)
	if !ok || profile.Mode != config.BrowserProfileEphemeral ||
		broker.capabilityMode(session) != browserpolicy.CapabilityFullAccess {
		return result
	}
	fields := request.Fields
	if !resolveCredentialStepRefs(&fields, slot.refs) {
		return result
	}
	fields.Origin = session.SnapshotOrigin
	if fields.Step != "" {
		entry, parseErr := url.Parse(fields.Origin)
		if parseErr != nil {
			return result
		}
		fields.WebsiteDomain, err = publicsuffix.EffectiveTLDPlusOne(entry.Hostname())
		if err != nil {
			return result
		}
	}
	scope := credentialbroker.Request{
		Principal: request.Principal, Account: request.Account,
		Origin: fields.Origin, PolicyRevision: request.PolicyRevision,
	}
	if lease.Check(ctx, scope) != nil {
		return result
	}
	fresh := slot.credentials == nil
	bound := slot.credentials
	if fresh {
		if fields.Step == "totp" || fields.Step == "verify" ||
			(fields.Step != "" && !intent.ValidAlias(request.CredentialID)) {
			return result
		}
		bound = &credentialSession{
			lease: lease, request: scope, done: make(chan struct{}), credentialID: request.CredentialID,
		}
	} else if fields.Step == "" || request.CredentialID != bound.credentialID ||
		bound.request.Principal != scope.Principal || bound.request.Account != scope.Account ||
		bound.request.PolicyRevision != scope.PolicyRevision || bound.check(ctx, fields.Origin) != nil {
		return result
	}
	private, ok := worker.(*playwrightWorker)
	if !ok {
		return result
	}
	private.mu.Lock()
	defer private.mu.Unlock()
	driver, ok := private.client.(privateCredentialDriver)
	if !ok || private.attached || private.humanControl || private.pendingDialog != nil || private.closed ||
		private.lost {
		return result
	}
	navigation, err := private.navigationIdentityLocked(ctx)
	if err != nil || navigation != slot.navigationID {
		return result
	}
	id, err := driver.prepareCredential(ctx, fields)
	if err != nil {
		return result
	}
	slot.credentials = bound
	operationCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-bound.lease.Done():
			cancel()
		case <-operationCtx.Done():
		}
	}()
	defer func() { cancel(); <-watchDone }()
	attempted := false
	outcome := credentialDriverResult{}
	consumeValues := func(username, password []byte) error {
		if bound.check(operationCtx, fields.Origin) != nil {
			return errCredentialDriver
		}
		for _, value := range [][]byte{username, password} {
			if len(value) == 0 {
				continue
			}
			release, registerErr := secretvalues.Register(value)
			if registerErr != nil {
				return errCredentialDriver
			}
			bound.release = append(bound.release, release)
		}
		if bound.check(operationCtx, fields.Origin) != nil {
			return errCredentialDriver
		}
		attempted = true
		var submitErr error
		outcome, submitErr = driver.submitCredential(operationCtx, id, username, password)
		return submitErr
	}
	// Preparation may await browser work while approval changes. This check
	// must precede retrieval itself, not only the callback after retrieval.
	err = bound.check(operationCtx, fields.Origin)
	if err == nil {
		if fields.Step == "verify" {
			outcome, err = driver.submitCredential(operationCtx, id, nil, nil)
		} else {
			err = consume(operationCtx, consumeValues)
		}
	}
	validOutcome := outcome.code == "logged_in" || outcome.code == "handoff_required"
	if fields.Step == "verify" {
		validOutcome = outcome.code == "logged_in" || outcome.code == "unverified"
	} else if fields.Step != "" {
		validOutcome = outcome.code == "submitted"
	}
	if err != nil || bound.check(ctx, fields.Origin) != nil ||
		!validOutcome {
		if attempted {
			private.lost = true
			// The caller's context may be canceled. Abort the private transport so
			// the driver cannot keep executing with filled inputs.
			_ = private.client.Close()
			broker.retireCredentialSession(request.Owner, request.SessionID)
			result.Outcome = "outcome_unknown"
		} else {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			if driver.discardCredential(cleanupCtx, id) != nil {
				private.lost = true
				_ = private.client.Close()
				broker.retireCredentialSession(request.Owner, request.SessionID)
			} else if fresh {
				bound.close()
				slot.credentials = nil
			}
		}
		return result
	}
	if bound.protectCookies(outcome.cookies) != nil ||
		(fresh && driver.bindCredentialOutputs(bound.protectCookies) != nil) {
		private.lost = true
		_ = private.client.Close()
		broker.retireCredentialSession(request.Owner, request.SessionID)
		result.Outcome = "outcome_unknown"
		return result
	}
	slot.refs, slot.refPositions, slot.inputs = nil, nil, nil
	slot.navigationID = ""
	result.Alias, result.Outcome = request.Account, outcome.code
	result.CredentialID = request.CredentialID
	if !fresh {
		return result
	}
	closed := bound.done
	go func() {
		select {
		case <-bound.lease.Done():
		case <-closed:
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = broker.Close(cleanupCtx, request.Owner, request.SessionID)
	}()
	return result
}

func (broker *Broker) retireCredentialSession(owner Owner, id string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = broker.Close(ctx, owner, id)
	}()
}
