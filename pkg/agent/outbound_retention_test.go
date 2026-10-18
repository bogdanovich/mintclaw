package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func deliveredPlainPublication(t *testing.T) (*AgentLoop, *bus.MessageBus, string, string, string) {
	t.Helper()
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	t.Cleanup(cleanup)
	root := t.TempDir()
	installTestOutboundCoordinator(t, al, root)
	spool, err := bus.NewInboundSpool(bus.InboundSpoolPath(root))
	if err != nil {
		t.Fatal(err)
	}
	msgBus.SetInboundSpool(spool)
	input, err := spool.Prepare(t.Context(), bus.NormalizeInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "telegram",
			ChatID:   "original-chat",
			ChatType: "direct",
			SenderID: "owner",
		},
		Content:    "original source",
		SessionKey: "original-session",
	}))
	if err != nil {
		t.Fatal(err)
	}
	al.GetConfig().Channels = config.ChannelsConfig{
		"telegram": &config.Channel{Type: config.ChannelTelegram, Enabled: true},
	}
	source := input.SpoolID
	ctx := al.withNativeFinalPublication(withOutboundTransaction(t.Context(), source), input, &continuationTarget{
		Workspace: "/agents/main", Channel: "telegram", ChatID: "original-chat", SessionKey: "original-session",
	})
	receipt, err := al.publishTransactionMessageReceiptAtBoundary(
		ctx,
		"/agents/main",
		bus.OutboundMessage{
			Channel:    "telegram",
			ChatID:     "original-chat",
			SessionKey: "original-session",
			Content:    "original private body",
		},
		nil,
	)
	if err != nil || !receipt.published {
		t.Fatalf("original publication = %#v, %v", receipt, err)
	}
	select {
	case <-msgBus.OutboundChan():
	default:
		t.Fatal("original message did not reach the bus")
	}
	coordinator := al.outboundCoordinator()
	if err = coordinator.BeginAttempt(receipt.deliveryID); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.MarkDelivered(
		receipt.deliveryID,
		outbox.Outcome{PlatformMessageIDs: []string{"original-remote-id"}},
	); err != nil {
		t.Fatal(err)
	}
	proof, err := msgBus.AckInbound(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := coordinator.SealNativeFinalMessage(receipt.deliveryID, proof)
	if err != nil || !sealed {
		t.Fatalf("fixture source seal=%v,%v", sealed, err)
	}
	if err = al.closeOutboundOutbox(); err != nil {
		t.Fatal(err)
	}
	return al, msgBus, root, source, receipt.deliveryID
}

func retiredPlainPublication(t *testing.T) (*AgentLoop, *bus.MessageBus, string, string, string) {
	t.Helper()
	al, msgBus, root, source, deliveryID := deliveredPlainPublication(t)
	report, err := outbox.RetirePayloadsOffline(t.Context(), root, outbox.RetirementPolicy{
		DeliveryIDs: []string{deliveryID}, MinAge: time.Nanosecond, Apply: true,
	})
	if err != nil || len(report.Items) != 1 || report.Items[0].Outcome != "retired" {
		t.Fatalf("retirement = %#v, %v", report, err)
	}
	installTestOutboundCoordinator(t, al, root)
	return al, msgBus, root, source, deliveryID
}

func TestRetiredPlainPublicationPreservesTerminalTruthWithoutCandidatePayload(t *testing.T) {
	al, msgBus, _, source, deliveryID := retiredPlainPublication(t)
	ctx := withOutboundTransaction(t.Context(), source)
	receipt, err := al.publishTransactionMessageReceiptAtBoundary(
		ctx,
		"/changed-owner",
		bus.OutboundMessage{
			Channel:    "web",
			ChatID:     "changed-chat",
			SessionKey: "changed-session",
			Content:    "replacement candidate must never be sent",
		},
		nil,
	)
	if err != nil || receipt.published || receipt.deliveryID != deliveryID {
		t.Fatalf("retired replay = %#v, %v", receipt, err)
	}
	if err = outboundTransactionFromContext(ctx).awaitDelivered(t.Context()); err != nil {
		t.Fatalf("delivered truth changed to failure: %v", err)
	}
	terminal, err := receipt.awaitTerminal(t.Context())
	if err != nil || terminal.Status != outbox.StatusDelivered || !terminal.PayloadRetired || terminal.Message != nil ||
		terminal.Identity.ChatID != "original-chat" {
		t.Fatalf("terminal replay = %#v, %v", terminal, err)
	}
	select {
	case message := <-msgBus.OutboundChan():
		t.Fatalf("retired replay published a candidate: %#v", message)
	default:
	}
}

func TestRetiredPlainPublicationRejectsNewCommitWithoutCallingIt(t *testing.T) {
	al, msgBus, _, source, _ := retiredPlainPublication(t)
	calls := 0
	receipt, err := al.publishTransactionMessageReceiptAtBoundary(
		withOutboundTransaction(t.Context(), source), "/agents/main",
		bus.OutboundMessage{Channel: "telegram", ChatID: "original-chat", Content: "new candidate"},
		func(context.Context) error { calls++; return nil })
	var retired *outbox.RetiredPayloadError
	if !errors.As(err, &retired) || calls != 0 || receipt.published ||
		retired.Receipt.Status != outbox.StatusDelivered {
		t.Fatalf("new callback = %#v, %v, calls %d", receipt, err, calls)
	}
	select {
	case message := <-msgBus.OutboundChan():
		t.Fatalf("callback replay published: %#v", message)
	default:
	}
}

func TestRetiredPlainPublicationRejectsNewBindingBeforeDomainMutation(t *testing.T) {
	al, _, _, source, _ := retiredPlainPublication(t)
	binds, validations := 0, 0
	ctx := withBoundOutboundTransaction(t.Context(), source,
		func(string) error { binds++; return nil }, func(string) error { validations++; return nil })
	_, err := al.publishTransactionMessageReceiptAtBoundary(ctx, "/agents/main",
		bus.OutboundMessage{Channel: "telegram", ChatID: "original-chat", Content: "new candidate"}, nil)
	var retired *outbox.RetiredPayloadError
	if !errors.As(err, &retired) || binds != 0 || validations != 0 || retired.Receipt.Status != outbox.StatusDelivered {
		t.Fatalf("domain binding replay = %v, binds %d, validations %d", err, binds, validations)
	}
}

func TestPublicationWithCommitRemainsProtectedFromRetirement(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	t.Cleanup(cleanup)
	root := t.TempDir()
	installTestOutboundCoordinator(t, al, root)
	calls := 0
	receipt, err := al.publishTransactionMessageReceiptAtBoundary(
		withOutboundTransaction(t.Context(), "protected-commit"), "/agents/main",
		bus.OutboundMessage{Channel: "telegram", ChatID: "chat", Content: "domain-bearing body"},
		func(context.Context) error { calls++; return nil })
	if err != nil || !receipt.published || calls != 1 {
		t.Fatalf("protected publication = %#v, %v", receipt, err)
	}
	<-msgBus.OutboundChan()
	coordinator := al.outboundCoordinator()
	if err = coordinator.BeginAttempt(receipt.deliveryID); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.MarkDelivered(receipt.deliveryID, outbox.Outcome{}); err != nil {
		t.Fatal(err)
	}
	if err = al.closeOutboundOutbox(); err != nil {
		t.Fatal(err)
	}
	report, err := outbox.RetirePayloadsOffline(t.Context(), root, outbox.RetirementPolicy{
		DeliveryIDs: []string{receipt.deliveryID}, MinAge: time.Nanosecond, Apply: true,
	})
	if err != nil || report.Items[0].Outcome != "protected_class" {
		t.Fatalf("protected cleanup = %#v, %v", report, err)
	}
}

func TestPlainEligibilityIsRevokedBeforeNewDomainCallback(t *testing.T) {
	for _, mode := range []string{"commit", "bind", "validation"} {
		t.Run(mode, func(t *testing.T) {
			al, _, root, source, deliveryID := deliveredPlainPublication(t)
			installTestOutboundCoordinator(t, al, root)
			coordinator := al.outboundCoordinator()
			calls := 0
			checkProtection := func() error {
				current, err := coordinator.Get(deliveryID)
				if err != nil || current.RetentionClass != "" {
					t.Errorf("callback began before durable protection: %#v, %v", current, err)
				}
				calls++
				return nil
			}
			ctx := withOutboundTransaction(t.Context(), source)
			var commit func(context.Context) error
			if mode == "commit" {
				commit = func(context.Context) error { return checkProtection() }
			}
			if mode == "bind" {
				ctx = withBoundOutboundTransaction(
					t.Context(),
					source,
					func(string) error { return checkProtection() },
					nil,
				)
			}
			if mode == "validation" {
				ctx = withBoundOutboundTransaction(
					t.Context(),
					source,
					nil,
					func(string) error { return checkProtection() },
				)
			}
			receipt, err := al.publishTransactionMessageReceiptAtBoundary(ctx, "/agent",
				bus.OutboundMessage{Channel: "telegram", ChatID: "original-chat", Content: "changed candidate"}, commit)
			if err != nil || receipt.published {
				t.Fatalf("protected replay = %#v, %v", receipt, err)
			}
			if mode != "validation" && calls != 1 {
				t.Fatalf("callback calls = %d", calls)
			}
			current, err := coordinator.Get(deliveryID)
			if err != nil || current.RetentionClass != "" || current.Message == nil ||
				current.Message.Content != "original private body" {
				t.Fatalf("canonical protected payload = %#v, %v", current, err)
			}
			if err = al.closeOutboundOutbox(); err != nil {
				t.Fatal(err)
			}
			report, err := outbox.RetirePayloadsOffline(t.Context(), root, outbox.RetirementPolicy{
				DeliveryIDs: []string{deliveryID}, MinAge: time.Nanosecond, Apply: true,
			})
			if err != nil || report.Items[0].Outcome != "protected_class" {
				t.Fatalf("new domain obligation lost protection: %#v, %v", report, err)
			}
		})
	}
}
