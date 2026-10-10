package browser

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

type navigationEvidence struct {
	HTTPStatus   int    `json:"http_status"`
	AntiBotBlock string `json:"anti_bot_block"`
}

func (evidence navigationEvidence) valid() bool {
	if evidence.HTTPStatus != 0 && (evidence.HTTPStatus < 100 || evidence.HTTPStatus > 599) {
		return false
	}
	switch evidence.AntiBotBlock {
	case "", "cloudflare", "datadome", "perimeterx", "imperva":
		return true
	default:
		return false
	}
}

type NavigationRecovery struct {
	Status        string       `json:"status"`
	Reason        string       `json:"reason,omitempty"`
	FromSessionID string       `json:"from_browser_session_id,omitempty"`
	SessionID     string       `json:"browser_session_id,omitempty"`
	Target        string       `json:"target,omitempty"`
	Profile       string       `json:"profile,omitempty"`
	InvocationID  string       `json:"invocation_id,omitempty"`
	Observation   *Observation `json:"-"`
}

// RecoverNavigation admits at most one new navigation after a completed GET.
// It never redispatches the original invocation or transfers cookies, approvals,
// element refs, or controller authority between targets.
func (broker *Broker) RecoverNavigation(
	ctx context.Context, owner Owner, invocationID, snapshotID string,
) (NavigationRecovery, error) {
	if owner.Validate() != nil || !validIdentifier(invocationID) || !validIdentifier(snapshotID) {
		return NavigationRecovery{}, ErrInvalid
	}
	broker.mu.Lock()
	recovery, destination, rawURL, err := broker.prepareNavigationRecoveryLocked(ctx, owner, invocationID, snapshotID)
	broker.mu.Unlock()
	if err != nil || destination.Target == "" {
		return recovery, err
	}
	session, err := broker.Open(ctx, destination)
	if err != nil {
		recovery.Status, recovery.Reason = "unavailable", navigationRecoveryFailure(err)
		return recovery, nil
	}
	recovery.SessionID, recovery.Target, recovery.Profile = session.ID, session.Target, session.Profile
	fail := func(cause error) (NavigationRecovery, error) {
		recovery.Status, recovery.Reason = "unavailable", navigationRecoveryFailure(cause)
		_, closeErr := broker.Close(context.WithoutCancel(ctx), owner, session.ID)
		return recovery, closeErr
	}
	observation, err := broker.Observe(ctx, owner, session.ID, session.TabID)
	if err != nil {
		return fail(err)
	}
	requestID, err := randomOpaqueID("recovery")
	if err != nil {
		return fail(err)
	}
	preparation, err := broker.PrepareAction(ctx, PrepareActionRequest{
		Owner: owner, RequestID: requestID, SessionID: session.ID, TabID: observation.TabID,
		FrameID: observation.FrameID, ContextCatalogID: observation.ContextCatalogID,
		ContextGeneration: observation.ContextGeneration, SnapshotID: observation.SnapshotID,
		SnapshotGeneration: observation.SnapshotGeneration,
		Action:             Action{Kind: ActionNavigate, URL: rawURL}, DeclaredEffect: EffectNavigation,
	})
	if err != nil {
		return fail(err)
	}
	if preparation.RequiresApproval {
		return fail(ErrApprovalRequired)
	}
	invocation, err := broker.ExecuteAction(ctx, owner, preparation.Action.ID, nil)
	recovery.InvocationID = invocation.ID
	if err != nil || invocation.State != InvocationSucceeded {
		return fail(errors.Join(err, ErrNavigationFailed))
	}
	observation, err = broker.Observe(ctx, owner, session.ID, session.TabID)
	if err != nil {
		return fail(err)
	}
	// Give supported automatic solvers one bounded passive wait. No challenge
	// interaction or original action is redispatched during this interval.
	if broker.config.Targets[session.Target].Steel.SolveCaptcha && observation.AntiBotBlock == "cloudflare" {
		deadline := time.Now().Add(15 * time.Second)
		for observation.AntiBotBlock == "cloudflare" && time.Now().Before(deadline) {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fail(ctx.Err())
			case <-timer.C:
			}
			observation, err = broker.Observe(ctx, owner, session.ID, session.TabID)
			if err != nil {
				return fail(err)
			}
		}
	}
	recovery.Observation = &observation
	recovery.Status = "recovered"
	if observation.AntiBotBlock != "" {
		recovery.Status, recovery.Reason = "blocked", observation.AntiBotBlock
		return recovery, nil
	}
	if observation.HTTPStatus >= 400 {
		recovery.Status, recovery.Reason = "unavailable", "http_error"
		return recovery, nil
	}
	broker.mu.Lock()
	err = broker.rememberSuccessfulNavigationLocked(ctx, owner, session.ID, observation, rawURL)
	broker.mu.Unlock()
	return recovery, err
}

func (broker *Broker) prepareNavigationRecoveryLocked(
	ctx context.Context, owner Owner, invocationID, snapshotID string,
) (NavigationRecovery, OpenRequest, string, error) {
	invocation, err := broker.store.GetInvocation(ctx, invocationID)
	if err != nil {
		return NavigationRecovery{}, OpenRequest{}, "", err
	}
	if invocation.Owner != owner || invocation.State != InvocationSucceeded ||
		invocation.Effect != EffectNavigation || invocation.PreparedActionID == "" {
		return NavigationRecovery{}, OpenRequest{}, "", nil
	}
	prepared, err := broker.store.GetPreparedAction(ctx, invocation.PreparedActionID)
	if err != nil {
		return NavigationRecovery{}, OpenRequest{}, "", err
	}
	if prepared.Action.Kind != ActionNavigate || prepared.FrameID != "" || prepared.DryRun {
		return NavigationRecovery{}, OpenRequest{}, "", nil
	}
	session, slot, _, err := broker.actionSessionLocked(ctx, owner, invocation.SessionID, prepared.TabID)
	if err != nil {
		return NavigationRecovery{}, OpenRequest{}, "", err
	}
	if session.SnapshotID != snapshotID || session.RecoveryAttempted ||
		!slot.navigationEvidence.valid() || session.EffectiveController() != ControllerAgent {
		return NavigationRecovery{}, OpenRequest{}, "", ErrStale
	}
	observation := Observation{
		URL: slot.observedURL, HTTPStatus: slot.navigationEvidence.HTTPStatus,
		AntiBotBlock: slot.navigationEvidence.AntiBotBlock,
	}
	target := broker.config.Targets[session.Target]
	if target.EffectiveProvider() == config.BrowserProviderSteel {
		return NavigationRecovery{}, OpenRequest{}, "", broker.rememberSuccessfulNavigationLocked(
			ctx, owner, session.ID, observation, prepared.Action.URL,
		)
	}
	fallback := target.Fallback
	if fallback.Target == "" {
		return NavigationRecovery{}, OpenRequest{}, "", nil
	}
	host := navigationHostname(prepared.Action.URL)
	// A redirected challenge must not authorize replaying a different origin.
	if host == "" || host != navigationHostname(observation.URL) {
		return NavigationRecovery{}, OpenRequest{}, "", nil
	}
	// A remembered provider preference cannot turn an ordinary HTTP failure
	// or missing response evidence into another billable navigation.
	if observation.AntiBotBlock == "" && (observation.HTTPStatus < 200 || observation.HTTPStatus >= 400) {
		return NavigationRecovery{}, OpenRequest{}, "", nil
	}
	profileRevision := broker.config.Targets[fallback.Target].Profiles[fallback.Profile].Revision
	remembered, err := broker.rememberedNavigationLocked(ctx, owner, host, fallback, profileRevision)
	if err != nil {
		return NavigationRecovery{}, OpenRequest{}, "", err
	}
	if observation.AntiBotBlock == "" && !remembered {
		return NavigationRecovery{}, OpenRequest{}, "", nil
	}
	destination := OpenRequest{Owner: owner, Target: fallback.Target, Profile: fallback.Profile}
	_, profile, grantErr := broker.authorize(destination)
	granted := grantErr == nil
	if !granted || profile.CapabilityMode != config.BrowserCapabilityFullAccess ||
		target.Profiles[session.Profile].CapabilityMode != config.BrowserCapabilityFullAccess ||
		target.Profiles[session.Profile].Mode == config.BrowserProfileAttachedUser ||
		!broker.originAllowed(
			Session{Target: fallback.Target, Profile: fallback.Profile},
			prepared.DestinationOrigin,
		) ||
		!broker.originNetworkAllowed(
			ctx,
			Session{Target: fallback.Target, Profile: fallback.Profile},
			prepared.DestinationOrigin,
		) {
		return NavigationRecovery{Status: "unavailable", Reason: "not_granted"}, OpenRequest{}, "", nil
	}
	reason := observation.AntiBotBlock
	if reason == "" {
		reason = "remembered_domain"
	}
	recovery := NavigationRecovery{Status: "switching", Reason: reason, FromSessionID: session.ID}
	// Persist before cleanup or any cloud call. A crash cannot retry this source
	// navigation and create another paid session.
	session.RecoveryAttempted = true
	session.Revision++
	session.UpdatedAt = timestampAtLeast(broker.now().UTC().UnixNano(), session.UpdatedAt)
	if err = broker.store.UpdateSession(ctx, session.Revision-1, session); err != nil {
		return recovery, OpenRequest{}, "", err
	}
	if _, err = broker.finishSessionLocked(ctx, session, SessionClosed, ""); err != nil {
		return recovery, OpenRequest{}, "", err
	}
	return recovery, destination, prepared.Action.URL, nil
}

func navigationHostname(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func (broker *Broker) rememberedNavigationLocked(
	ctx context.Context, owner Owner, host string, fallback config.BrowserFallbackConfig, profileRevision string,
) (bool, error) {
	if fallback.RememberSeconds == 0 {
		return false, nil
	}
	sessions, err := broker.store.ListSessions(ctx)
	if err != nil {
		return false, err
	}
	now := broker.now().UTC().UnixNano()
	cutoff := broker.now().UTC().Add(-time.Duration(fallback.RememberSeconds) * time.Second).UnixNano()
	for _, session := range sessions {
		at := int64(0)
		for _, success := range session.SuccessfulDomains {
			if success.Host == host {
				at = success.At
			}
		}
		if session.Target == fallback.Target && session.Profile == fallback.Profile &&
			session.Owner.ActorID == owner.ActorID && session.Owner.AgentID == owner.AgentID &&
			session.PolicyRevision == broker.policyRevision && session.ProfileRevision == profileRevision &&
			at > 0 && at >= cutoff && at <= now {
			return true, nil
		}
	}
	return false, nil
}

func (broker *Broker) rememberSuccessfulNavigationLocked(
	ctx context.Context, owner Owner, sessionID string, observation Observation, requestedURL string,
) error {
	host := navigationHostname(requestedURL)
	if host == "" || host != navigationHostname(observation.URL) || observation.AntiBotBlock != "" ||
		observation.HTTPStatus < 200 || observation.HTTPStatus >= 400 {
		return nil
	}
	session, err := broker.store.GetSession(ctx, sessionID)
	if err != nil || session.Owner != owner || session.State != SessionReady {
		return errors.Join(err, ErrStale)
	}
	index := 0
	for candidate, success := range session.SuccessfulDomains {
		if success.Host == host || success.Host == "" {
			index = candidate
			break
		}
		if success.At < session.SuccessfulDomains[index].At {
			index = candidate
		}
	}
	session.SuccessfulDomains[index] = BrowserDomainSuccess{Host: host, At: broker.now().UTC().UnixNano()}
	session.Revision++
	session.UpdatedAt = timestampAtLeast(broker.now().UTC().UnixNano(), session.UpdatedAt)
	return broker.store.UpdateSession(ctx, session.Revision-1, session)
}

func navigationRecoveryFailure(err error) string {
	switch {
	case errors.Is(err, ErrProviderQuota), errors.Is(err, ErrCapacity):
		return "quota_exhausted"
	case errors.Is(err, ErrProviderAuthentication):
		return "provider_authentication"
	case errors.Is(err, ErrApprovalRequired):
		return "approval_required"
	case errors.Is(err, ErrDenied):
		return "not_granted"
	default:
		return "provider_unavailable"
	}
}
