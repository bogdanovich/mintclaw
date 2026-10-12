package toolshared

import "testing"

func TestDocumentIntakeProcessingIdentityIsReceiptBoundAndKeepsExecutionIdentity(t *testing.T) {
	base := WithToolExecutionIdentity(t.Context(), "workspace", "turn-execution")
	bound := WithToolDocumentIntakeProcessingIdentity(base, "receipt-a", "inbound-a")
	if ToolDocumentIntakeProcessingIdentity(bound, "receipt-a") != "inbound-a" ||
		ToolDocumentIntakeProcessingIdentity(bound, "receipt-b") != "" ||
		ToolDocumentIntakeProcessingIdentity(bound, "") != "" ||
		ToolDocumentIntakeProcessingIdentity(base, "receipt-a") != "" ||
		ToolExecutionID(bound) != ToolExecutionID(base) {
		t.Fatal("processing context changed its receipt or ambient execution identity")
	}
	replayed := WithToolDocumentIntakeProcessingIdentity(base, "receipt-a", "inbound-a")
	retried := WithToolDocumentIntakeProcessingIdentity(base, "receipt-a", "inbound-b")
	if ToolDocumentIntakeProcessingIdentity(replayed, "receipt-a") != "inbound-a" ||
		ToolDocumentIntakeProcessingIdentity(retried, "receipt-a") != "inbound-b" {
		t.Fatal("explicit retry or replay lost its own ingress identity")
	}
}
