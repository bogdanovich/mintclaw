package browser

import (
	"context"
	"time"

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
	Principal          credentialbroker.Principal
	PolicyRevision     string
	Fields             CredentialFields
}

type CredentialLoginResult struct {
	Alias   string `json:"alias"`
	Outcome string `json:"outcome"`
}

// PrivateLoginConsumer lends credentials to the synchronous callback. The
// runtime implementation is the exact-item vault adapter, after approval.
type PrivateLoginConsumer func(context.Context, func([]byte, []byte) error) error

// CredentialGrant is the live authority supplied by the trusted runtime.
// Both the policy authority lease and integrated phone approval lease can
// implement it without exporting an issuer or private provider binding.
type CredentialGrant interface {
	Check(context.Context, credentialbroker.Request) error
	Done() <-chan struct{}
}

type credentialSession struct {
	lease   CredentialGrant
	request credentialbroker.Request
	release []func()
	cookies map[string]struct{}
	closed  bool
	done    chan struct{}
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
	if err != nil || slot.credentials != nil || session.DryRun || session.FrameID != "" ||
		session.SnapshotID != request.SnapshotID || session.SnapshotGeneration != request.SnapshotGeneration {
		return result
	}
	profile, ok := broker.browserProfile(session)
	if !ok || profile.Mode != config.BrowserProfileEphemeral ||
		broker.capabilityMode(session) != browserpolicy.CapabilityFullAccess {
		return result
	}
	fields := request.Fields
	if fields.UsernameRef == fields.PasswordRef || fields.UsernameRef == fields.SubmitRef ||
		fields.PasswordRef == fields.SubmitRef {
		return result
	}
	for ref, destination := range map[string]*string{
		fields.UsernameRef: &fields.UsernameRef,
		fields.PasswordRef: &fields.PasswordRef,
		fields.SubmitRef:   &fields.SubmitRef,
	} {
		element, found := slot.refs[ref]
		// Native Playwright may prefix main-document refs after navigation too.
		// Frame authority remains excluded by session.FrameID above and the
		// private driver's exact main-document/form validation before retrieval.
		if !found || !playwrightTargetPattern.MatchString(element.Target) {
			return result
		}
		*destination = element.Target
	}
	fields.Origin = session.SnapshotOrigin
	scope := credentialbroker.Request{
		Principal: request.Principal, Account: request.Account,
		Origin: fields.Origin, PolicyRevision: request.PolicyRevision,
	}
	if lease.Check(ctx, scope) != nil {
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
	bound := &credentialSession{lease: lease, request: scope, done: make(chan struct{})}
	slot.credentials = bound
	operationCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-lease.Done():
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
		err = consume(operationCtx, consumeValues)
	}
	if err != nil || bound.check(ctx, fields.Origin) != nil ||
		(outcome.code != "logged_in" && outcome.code != "handoff_required") {
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
			} else {
				bound.close()
				slot.credentials = nil
			}
		}
		return result
	}
	if bound.protectCookies(outcome.cookies) != nil || driver.bindCredentialOutputs(bound.protectCookies) != nil {
		private.lost = true
		_ = private.client.Close()
		broker.retireCredentialSession(request.Owner, request.SessionID)
		result.Outcome = "outcome_unknown"
		return result
	}
	slot.refs, slot.refPositions, slot.inputs = nil, nil, nil
	slot.navigationID = ""
	result.Alias, result.Outcome = request.Account, outcome.code
	closed := bound.done
	go func() {
		select {
		case <-lease.Done():
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
