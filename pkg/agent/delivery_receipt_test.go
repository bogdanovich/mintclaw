package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestSettleFinalHandledDeliveryConfirmsDeliveredReceipt(t *testing.T) {
	receipt, coordinator, deliveryID := testOutboundReceipt(t)
	if err := coordinator.MarkDelivered(
		deliveryID,
		outbox.Outcome{PlatformMessageIDs: []string{"telegram-1"}},
	); err != nil {
		t.Fatalf("MarkDelivered() error = %v", err)
	}
	confirmed := false
	result := (&toolshared.ToolResult{ResponseHandled: true}).WithDeliveryIntent(
		toolshared.DeliveryFinalHandled,
	)
	result.ConfirmOutbound = func() { confirmed = true }

	if err := settleFinalHandledDelivery(context.Background(), receipt, result, 1); err != nil {
		t.Fatalf("settleFinalHandledDelivery() error = %v", err)
	}
	if !confirmed || !result.ResponseHandled || !strings.Contains(result.ForLLM, "delivered") {
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
	result := (&toolshared.ToolResult{}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)

	err := settleFinalHandledDelivery(context.Background(), receipt, result, 1)
	if err == nil || !strings.Contains(err.Error(), "definitely failed") ||
		!strings.Contains(err.Error(), "request entity too large") {
		t.Fatalf("settleFinalHandledDelivery() error = %v", err)
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
	result := (&toolshared.ToolResult{}).WithDeliveryIntent(toolshared.DeliveryFinalHandled)

	err := settleFinalHandledDelivery(context.Background(), receipt, result, 0)
	if err == nil || !strings.Contains(err.Error(), "must not be retried blindly") {
		t.Fatalf("settleFinalHandledDelivery() error = %v", err)
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
	if result.ResponseHandled || !strings.Contains(result.ForLLM, "still pending") ||
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
	if result.ResponseHandled || !strings.Contains(result.ForLLM, "still pending") ||
		!strings.Contains(result.ForLLM, "Do not claim") {
		t.Fatalf("pending result = %+v", result)
	}
}

func TestFinalHandledPublishedCommitFailureRemainsPending(t *testing.T) {
	commitErr := errors.New("commit durable admission")
	result := (&toolshared.ToolResult{ResponseHandled: true}).WithDeliveryIntent(
		toolshared.DeliveryFinalHandled,
	)

	err := classifyFinalHandledPublicationError(
		outboundPublication{published: true},
		result,
		commitErr,
	)
	if !errors.Is(err, errFinalHandledDeliveryPending) || !errors.Is(err, commitErr) {
		t.Fatalf("classification error = %v, want pending and commit causes", err)
	}
	if result.ResponseHandled || !strings.Contains(result.ForLLM, "state is uncertain") ||
		!strings.Contains(result.ForLLM, "Do not claim") {
		t.Fatalf("pending result = %+v", result)
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
	}, coordinator, admission.Intent.ID
}
