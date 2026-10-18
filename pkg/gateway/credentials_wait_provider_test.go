//go:build linux || darwin

package gateway

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/internal/credentials/approval"
	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

type credentialProviderHarness struct {
	perform       func() (browser.CredentialLoginResult, error)
	finalize      func()
	cancel        func()
	notifications <-chan bus.OutboundMessage
	waiting       <-chan struct{}
}

// A temporary delivery outage must not occupy the two-hour pending pool.
// The real synthetic phone enrolls first; no grant or vault values exist yet.
func credentialFixtureRejectNotifications(
	ctx context.Context, runtime *credentialRuntime, cfg *config.Config, origin string,
) error {
	account := cfg.Credentials.Accounts[0]
	scope, _, err := runtime.controller.LoginRequest(ctx, account.Principals[0], account.Alias, origin)
	if err != nil {
		return errors.New("notification capacity fixture scope unavailable")
	}
	original := runtime.notify
	defer func() { runtime.notify = original }()
	notified := 0
	for _, canceled := range []bool{false, true} {
		for range approval.MaxPending + 1 {
			callCtx, cancel := context.WithCancel(ctx)
			runtime.notify = func(context.Context, string) error {
				notified++
				if canceled {
					cancel()
					return callCtx.Err()
				}
				return errors.New("synthetic notification admission failure")
			}
			_, waiting, requestErr := runtime.credentialLeaseOrApproval(callCtx, scope)
			cancel()
			if !errors.Is(requestErr, errCredentialsUnavailable) || waiting.Reference != "" {
				return errors.New("failed notification provided a usable approval wait")
			}
		}
	}
	if notified != 2*(approval.MaxPending+1) {
		return errors.New("failed notifications exhausted the pending approval pool")
	}
	return nil
}

type credentialWaitDelivery struct {
	coordinator   *outbox.Coordinator
	notifications chan bus.OutboundMessage
	phoneRequests atomic.Int32
}

func (*credentialWaitDelivery) GetChannel(string) (channels.Channel, bool) { return nil, false }
func (*credentialWaitDelivery) GetEnabledChannels() []string               { return []string{"telegram"} }
func (*credentialWaitDelivery) InvokeTypingStop(string, string)            {}
func (*credentialWaitDelivery) SendMedia(context.Context, bus.OutboundMediaMessage) error {
	return errors.New("unexpected fixture media")
}
func (*credentialWaitDelivery) SendPlaceholder(context.Context, string, string) bool     { return false }
func (*credentialWaitDelivery) DismissToolFeedback(context.Context, bus.OutboundMessage) {}
func (*credentialWaitDelivery) PauseToolFeedback(context.Context, bus.OutboundMessage)   {}
func (*credentialWaitDelivery) SupportsDurableDeliveryReceipts() bool                    { return true }
func (delivery *credentialWaitDelivery) SendMessage(ctx context.Context, message bus.OutboundMessage) error {
	if message.DeliveryID != "" {
		if err := delivery.coordinator.BeginAttempt(message.DeliveryID); err != nil {
			return err
		}
	}
	if strings.Contains(message.Content, "#request=") {
		delivery.phoneRequests.Add(1)
		select {
		case delivery.notifications <- message:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if message.DeliveryID != "" {
		return delivery.coordinator.MarkDelivered(message.DeliveryID, outbox.Outcome{})
	}
	return nil
}

func installCredentialWaitDelivery(
	t *testing.T,
	loop *agent.AgentLoop,
	messageBus *bus.MessageBus,
) *credentialWaitDelivery {
	t.Helper()
	coordinator, err := outbox.OpenCoordinator(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	delivery := &credentialWaitDelivery{coordinator: coordinator, notifications: make(chan bus.OutboundMessage, 32)}
	loop.SetOutboundOutbox(coordinator)
	loop.SetChannelManager(delivery)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case message := <-messageBus.OutboundChan():
				if err := delivery.SendMessage(ctx, message); err != nil && ctx.Err() == nil {
					t.Error("fixture durable message delivery failed")
				}
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done; loop.SetOutboundOutbox(nil); _ = coordinator.Close() })
	return delivery
}

// Existing transaction/restart/cancel/expiry cases start after a synthetic
// phone grant. The dedicated delayed-provider matrix owns the full new wait
// composition, including the initial browser and its native cleanup receipt.
func awaitCredentialFixturePhoneGrant(
	ctx context.Context,
	runtime *credentialRuntime,
	cfg *config.Config,
	origin string,
) error {
	principal := cfg.Credentials.Accounts[0].Principals[0]
	alias := cfg.Credentials.Accounts[0].Alias
	scope, _, err := runtime.controller.LoginRequest(ctx, principal, alias, origin)
	if cfg.Credentials.Accounts[0].DiscoverLogins {
		scope, _, err = runtime.controller.DiscoveryRequest(ctx, principal, alias, origin)
	}
	if err != nil {
		return errors.New("fixture grant scope unavailable")
	}
	if _, err = runtime.controller.ActiveLogin(ctx, scope); err == nil {
		return nil
	}
	reference, err := runtime.controller.Request(ctx, principal, []string{alias}, 20*time.Hour)
	if err != nil || runtime.notify(ctx, runtime.origin+credentialPagePath+"#request="+reference) != nil {
		return errors.New("fixture phone request unavailable")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err = runtime.controller.ActiveLogin(ctx, scope); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func credentialFixtureSuspendedWait(loop *agent.AgentLoop, running *services) (interactions.Record, error) {
	owner := loop.GetRegistry().GetDefaultAgent()
	registry := interactions.NewRegistry(interactions.WorkspaceStorePath(owner.Workspace))
	records := registry.ListNonterminal()
	if registry.LastLoadError() != nil || len(records) != 1 {
		return interactions.Record{}, errors.New("fixture durable wait unavailable")
	}
	record := records[0]
	remaining := time.Until(time.UnixMilli(record.ExpiresAt))
	if record.Kind != interactions.KindExternalWait || record.Status != interactions.StatusWaiting ||
		record.Origin.ToolName != "browser_login" || remaining < 118*time.Minute || remaining > 2*time.Hour {
		return interactions.Record{}, errors.New("fixture wait did not retain its two-hour deadline")
	}
	sessions, err := running.Browser.store.ListSessions(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].State != browser.SessionClosed {
		return interactions.Record{}, errors.New("fixture idle browser was not closed before waiting")
	}
	verifiedClose := false
	for _, receipt := range record.OutcomeReceipts {
		if receipt.Kind == taskresult.ReceiptKindResourceCleanup && receipt.Tool == "browser_session" &&
			receipt.Metadata["state"] == "closed" {
			verifiedClose = true
		}
	}
	if !verifiedClose {
		return interactions.Record{}, errors.New("fixture wait lost its native close receipt")
	}
	return record, nil
}

func resumeCredentialFixtureWait(
	ctx context.Context,
	loop *agent.AgentLoop,
	record interactions.Record,
	pendingCheck func() error,
) (string, error) {
	owner := loop.GetRegistry().GetDefaultAgent()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		loop.RecoverHumanInteractions(ctx)
		registry := interactions.NewRegistry(interactions.WorkspaceStorePath(owner.Workspace))
		current, ok := registry.Get(record.ID)
		if !ok || registry.LastLoadError() != nil {
			return "", errors.New("fixture wait state lost")
		}
		if current.Status == interactions.StatusResolved {
			key := current.Origin.ContinuationSessionKey
			if key == "" {
				key = current.Route.SessionKey
			}
			history, err := owner.Sessions.ReadTurnHistory(ctx, key)
			if err != nil {
				return "", err
			}
			for i := len(history) - 1; i >= 0; i-- {
				if history[i].Role == "assistant" && len(history[i].ToolCalls) == 0 &&
					strings.Contains(history[i].Content, "fixture complete") {
					return history[i].Content, nil
				}
			}
			return "", errors.New("fixture resumed result missing")
		}
		if current.Status == interactions.StatusWaiting && pendingCheck != nil {
			if err := pendingCheck(); err != nil {
				return "", err
			}
		}
		if current.Status != interactions.StatusWaiting && current.Status != interactions.StatusClaimed &&
			current.Status != interactions.StatusResuming {
			return "", errors.New("fixture approval wait failed")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
