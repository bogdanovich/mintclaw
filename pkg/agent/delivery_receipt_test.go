package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestSettleImmediateDeliveryAcknowledgesFormRecovery(t *testing.T) {
	coordinator, err := outbox.OpenCoordinator(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })
	identity := outbox.Identity{
		SourceID: "pdf3-form-delivery", Channel: "telegram", ChatID: "pdf3-chat", SessionKey: "pdf3-session",
	}
	mediaRef := "media://filled-document"
	admission, err := coordinator.AdmitMedia("/agents/main", identity, bus.OutboundMediaMessage{
		Context:    bus.InboundContext{Channel: identity.Channel, ChatID: identity.ChatID},
		SessionKey: identity.SessionKey,
		Parts:      []bus.MediaPart{{Type: "file", Ref: mediaRef}},
		Recovery: &bus.OutboundRecovery{
			Kind: bus.OutboundRecoveryDocumentFill, MediaRef: mediaRef,
			WorkspaceID: "workspace", AgentID: "main", ActorID: "actor", RouteID: "route",
			SessionID: "session", AuthorityKind: "inbound_media", OperationID: "document_write_1",
			DomainDeliveryID: "delivery_1", DomainJobID: "form_job_0123456789abcdef",
			DomainOwnerDigest: strings.Repeat("a", 64),
		},
	})
	if err != nil || !admission.Dispatch {
		t.Fatalf("AdmitMedia() = %#v, %v", admission, err)
	}
	if err = coordinator.PrepareAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.CommitAdmission(admission.Lease); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.BeginAttempt(admission.Intent.ID); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.MarkDelivered(admission.Intent.ID, outbox.Outcome{}); err != nil {
		t.Fatal(err)
	}
	result := (&toolshared.ToolResult{}).WithDeliveryIntent(toolshared.DeliveryImmediateContinue)
	settled := false
	result.Delivery.Settle = func(context.Context, toolshared.DeliverySettlement) error {
		settled = true
		return nil
	}
	receipt := outboundPublication{
		published: true, deliveryID: admission.Intent.ID, coordinator: coordinator, admission: admission,
	}
	if err = settleImmediateDelivery(t.Context(), receipt, result); err != nil {
		t.Fatal(err)
	}
	intent, err := coordinator.Get(admission.Intent.ID)
	if err != nil || !settled || !intent.RecoverySettled {
		t.Fatalf("settled intent = %#v, callback=%t, err=%v", intent, settled, err)
	}
	if recovered, recoverErr := coordinator.Recover(); recoverErr != nil || len(recovered) != 0 {
		t.Fatalf("Recover() = %#v, %v", recovered, recoverErr)
	}
}

func TestSettleFinalHandledDeliveryConfirmsDeliveredReceipt(t *testing.T) {
	receipt, coordinator, deliveryID := testOutboundReceipt(t)
	if err := coordinator.MarkDelivered(
		deliveryID,
		outbox.Outcome{PlatformMessageIDs: []string{"telegram-1"}},
	); err != nil {
		t.Fatalf("MarkDelivered() error = %v", err)
	}
	confirmed := false
	var settlement toolshared.DeliverySettlement
	result := (&toolshared.ToolResult{}).WithDeliveryIntent(
		toolshared.DeliveryFinalHandled,
	)
	result.Delivery.Confirm = func() { confirmed = true }
	result.Delivery.Settle = func(_ context.Context, got toolshared.DeliverySettlement) error {
		settlement = got
		return nil
	}

	if err := settleFinalHandledDelivery(context.Background(), receipt, result, 1); err != nil {
		t.Fatalf("settleFinalHandledDelivery() error = %v", err)
	}
	if !confirmed || settlement.Status != toolshared.DeliverySettlementDelivered ||
		settlement.DeliveryID != deliveryID || !result.Delivery.IsFinalHandled() ||
		!strings.Contains(result.ForLLM, "delivered") {
		t.Fatalf("settled result = %+v, confirmed = %v", result, confirmed)
	}
}

func TestSettleFinalHandledDeliverySurfacesDefinitiveFailure(t *testing.T) {
	receipt, coordinator, deliveryID := testOutboundReceipt(t)
	if err := coordinator.MarkDefinitelyFailed(
		deliveryID,
		outbox.Outcome{Error: "request entity too large"},
	); err != nil {
		t.Fatalf("MarkDefinitelyFailed() error = %v", err)
	}
	var settlement toolshared.DeliverySettlement
	result := (&toolshared.ToolResult{}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)
	result.Delivery.Settle = func(_ context.Context, got toolshared.DeliverySettlement) error {
		settlement = got
		return nil
	}

	err := settleFinalHandledDelivery(context.Background(), receipt, result, 1)
	if err == nil || !strings.Contains(err.Error(), "definitely failed") ||
		!strings.Contains(err.Error(), "request entity too large") {
		t.Fatalf("settleFinalHandledDelivery() error = %v", err)
	}
	if settlement.Status != toolshared.DeliverySettlementDefinitelyFailed ||
		settlement.DeliveryID != deliveryID {
		t.Fatalf("settlement = %#v", settlement)
	}
}

func TestSettleFinalHandledDeliveryPreservesAmbiguousSafety(t *testing.T) {
	receipt, coordinator, deliveryID := testOutboundReceipt(t)
	if err := coordinator.MarkAmbiguous(
		deliveryID,
		outbox.Outcome{Error: "acceptance unknown"},
	); err != nil {
		t.Fatalf("MarkAmbiguous() error = %v", err)
	}
	var settlement toolshared.DeliverySettlement
	result := (&toolshared.ToolResult{}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)
	result.Delivery.Settle = func(_ context.Context, got toolshared.DeliverySettlement) error {
		settlement = got
		return nil
	}

	err := settleFinalHandledDelivery(context.Background(), receipt, result, 0)
	if !errors.Is(err, errFinalHandledDeliveryAmbiguous) ||
		!strings.Contains(err.Error(), "must not be retried blindly") {
		t.Fatalf("settleFinalHandledDelivery() error = %v", err)
	}
	if !isNonPublishableTurnError(err) {
		t.Fatalf("ambiguous delivery error must stop user-visible continuation")
	}
	if settlement.Status != toolshared.DeliverySettlementAmbiguous || settlement.DeliveryID != deliveryID {
		t.Fatalf("settlement = %#v", settlement)
	}
}

func TestSettleImmediateDeliverySettlementFailureStopsTheTurn(t *testing.T) {
	settlementErr := errors.New("persist domain settlement")
	for _, scenario := range []struct {
		name string
		mark func(*outbox.Coordinator, string) error
		want error
	}{
		{
			name: "delivered",
			mark: func(coordinator *outbox.Coordinator, deliveryID string) error {
				return coordinator.MarkDelivered(deliveryID, outbox.Outcome{})
			},
			want: errFinalHandledDeliveryAmbiguous,
		},
		{
			name: "definitely failed",
			mark: func(coordinator *outbox.Coordinator, deliveryID string) error {
				return coordinator.MarkDefinitelyFailed(deliveryID, outbox.Outcome{Error: "rejected"})
			},
			want: errFinalHandledDeliveryPending,
		},
		{
			name: "ambiguous",
			mark: func(coordinator *outbox.Coordinator, deliveryID string) error {
				return coordinator.MarkAmbiguous(deliveryID, outbox.Outcome{Error: "response lost"})
			},
			want: errFinalHandledDeliveryAmbiguous,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			receipt, coordinator, deliveryID := testOutboundReceipt(t)
			if err := scenario.mark(coordinator, deliveryID); err != nil {
				t.Fatal(err)
			}
			result := (&toolshared.ToolResult{}).WithDeliveryIntent(toolshared.DeliveryImmediateContinue)
			result.Delivery.Settle = func(context.Context, toolshared.DeliverySettlement) error {
				return settlementErr
			}
			err := settleImmediateDelivery(t.Context(), receipt, result)
			if !errors.Is(err, scenario.want) || !errors.Is(err, settlementErr) ||
				!isNonPublishableTurnError(err) {
				t.Fatalf("settleImmediateDelivery() error = %v", err)
			}
		})
	}
}

func TestRecoverableImmediateDeliveryRequiresDurableReceipts(t *testing.T) {
	settled := false
	result := (&toolshared.ToolResult{}).WithDeliveryIntent(toolshared.DeliveryImmediateContinue)
	result.Delivery.Settle = func(context.Context, toolshared.DeliverySettlement) error {
		settled = true
		return nil
	}
	_, outcome, err := (&AgentLoop{}).deliverToolResultToUser(
		withOutboundTransaction(t.Context(), "recoverable-immediate-delivery"),
		&turnState{},
		result,
		"document",
	)
	if err == nil || !strings.Contains(err.Error(), "durable delivery receipts") ||
		outcome != toolResultDeliveryNone || settled {
		t.Fatalf("receiptless recoverable delivery = outcome %d settled=%t err=%v", outcome, settled, err)
	}
}

func TestSettleFinalHandledDeliveryCancellationRemainsPending(t *testing.T) {
	receipt, _, _ := testOutboundReceipt(t)
	result := (&toolshared.ToolResult{}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := settleFinalHandledDelivery(ctx, receipt, result, 1)
	if !errors.Is(err, errFinalHandledDeliveryPending) {
		t.Fatalf("settleFinalHandledDelivery() error = %v, want pending sentinel", err)
	}
	if result.Delivery.IsFinalHandled() || !strings.Contains(result.ForLLM, "still pending") ||
		!strings.Contains(result.ForLLM, "Do not claim") {
		t.Fatalf("pending result = %+v", result)
	}
}

func TestSettleFinalHandledDeliveryWaitErrorRemainsPending(t *testing.T) {
	receipt, coordinator, _ := testOutboundReceipt(t)
	if err := coordinator.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	result := (&toolshared.ToolResult{}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)

	err := settleFinalHandledDelivery(t.Context(), receipt, result, 1)
	if !errors.Is(err, errFinalHandledDeliveryPending) {
		t.Fatalf("settleFinalHandledDelivery() error = %v, want pending sentinel", err)
	}
	if result.Delivery.IsFinalHandled() || !strings.Contains(result.ForLLM, "still pending") ||
		!strings.Contains(result.ForLLM, "Do not claim") {
		t.Fatalf("pending result = %+v", result)
	}
}

func TestFinalHandledPublishedCommitFailureRemainsPending(t *testing.T) {
	commitErr := errors.New("commit durable admission")
	result := (&toolshared.ToolResult{}).WithDeliveryIntent(
		toolshared.DeliveryFinalHandled,
	)

	err := classifyDurablePublicationError(
		outboundPublication{published: true},
		result,
		commitErr,
	)
	if !errors.Is(err, errFinalHandledDeliveryPending) || !errors.Is(err, commitErr) {
		t.Fatalf("classification error = %v, want pending and commit causes", err)
	}
	if result.Delivery.IsFinalHandled() || !strings.Contains(result.ForLLM, "state is uncertain") ||
		!strings.Contains(result.ForLLM, "Do not claim") {
		t.Fatalf("pending result = %+v", result)
	}
}

func TestClassifySynchronousFinalHandledDeliveryError(t *testing.T) {
	newResult := func() *toolshared.ToolResult {
		return (&toolshared.ToolResult{}).WithDeliveryIntent(
			toolshared.DeliveryFinalHandled,
		)
	}

	ambiguous := newResult()
	err := classifySynchronousFinalHandledDeliveryError(
		ambiguous,
		errors.New("transport response was lost"),
	)
	if !errors.Is(err, errFinalHandledDeliveryAmbiguous) || ambiguous.Delivery.IsFinalHandled() ||
		!strings.Contains(ambiguous.ForLLM, "Do not claim delivery") {
		t.Fatalf("ambiguous classification = %v, result = %#v", err, ambiguous)
	}

	definiteCause := errors.New("preflight rejected payload")
	definite := newResult()
	err = classifySynchronousFinalHandledDeliveryError(
		definite,
		channels.DefiniteNotSentDeliveryError(definiteCause),
	)
	if !errors.Is(err, definiteCause) || errors.Is(err, errFinalHandledDeliveryAmbiguous) ||
		!definite.Delivery.IsFinalHandled() {
		t.Fatalf("definite classification = %v, result = %#v", err, definite)
	}
}

func testOutboundReceipt(t *testing.T) (outboundPublication, *outbox.Coordinator, string) {
	t.Helper()
	coordinator, err := outbox.OpenCoordinator(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCoordinator() error = %v", err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })
	admission, err := coordinator.AdmitMessage(
		"/agents/main",
		outbox.Identity{
			SourceID: "receipt-test-" + t.Name(),
			Channel:  "telegram",
			ChatID:   "chat-1",
		},
		bus.OutboundMessage{Content: "deliver me"},
	)
	if err != nil {
		t.Fatalf("AdmitMessage() error = %v", err)
	}
	if err = coordinator.PrepareAdmission(admission.Lease); err != nil {
		t.Fatalf("PrepareAdmission() error = %v", err)
	}
	if err = coordinator.CommitAdmission(admission.Lease); err != nil {
		t.Fatalf("CommitAdmission() error = %v", err)
	}
	if err = coordinator.BeginAttempt(admission.Intent.ID); err != nil {
		t.Fatalf("BeginAttempt() error = %v", err)
	}
	return outboundPublication{
		published:   true,
		deliveryID:  admission.Intent.ID,
		coordinator: coordinator,
		admission:   admission,
	}, coordinator, admission.Intent.ID
}
