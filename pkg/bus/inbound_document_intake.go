package bus

func validateInboundSpoolVersion(rec spooledInboundRecord) error {
	switch rec.Version {
	case inboundSpoolVersion:
		if !rec.Message.Context.DocumentIntake.IsZero() {
			return errInboundDocumentIntakeGuard
		}
	case inboundDocumentSpoolVersion:
		if !rec.Message.Context.DocumentIntake.Valid() {
			return errInboundDocumentIntakeGuard
		}
	default:
		return errInboundSpoolVersionUnsupported
	}
	return nil
}
