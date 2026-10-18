package eval

func (domain *pdfConversation) concludeWire(trial *Trial, complete bool) {
	if domain.wire == nil || trial.PDF == nil {
		return
	}
	defer clear(domain.wire.messages)
	message, delivery, ref := "", "", ""
	if settled := trial.PDF.Settlement; settled != nil && len(settled.PlatformMessageIDs) == 1 {
		message, delivery, ref = settled.PlatformMessageIDs[0], settled.OutboxDeliveryID, settled.ArtifactRef
	}
	evidence := domain.wire.bind(*trial, message, delivery, ref, "application/pdf", complete)
	trial.PDF.Wire = evidence
	switch evidence.Code {
	case "multiple_wire_deliveries", "wire_media_conflict", "wire_media_binding_mismatch":
		failPDFArtifact(trial, Fail, evidence.Code)
	}
}
