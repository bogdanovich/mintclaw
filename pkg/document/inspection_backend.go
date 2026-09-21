package document

import "io"

const (
	PDFCPUBackendName    = "pdfcpu"
	PDFCPUBackendVersion = "v0.15.0"
)

type inspectionBackend interface {
	Inspect(io.ReadSeeker, Limits) backendInspection
}

type backendInspection struct {
	State   State
	Facts   *InspectionFacts
	Failure *Failure
}

func failedInspection(code FailureCode, message string) backendInspection {
	return backendInspection{
		State:   StateFailed,
		Failure: &Failure{Code: code, Message: message},
	}
}

func defaultInspectionFacts() *InspectionFacts {
	unknownString := StringFact{State: FactUnknown}
	unknownInteger := IntegerFact{State: FactUnknown}
	return &InspectionFacts{
		Backend:    BackendIdentity{Name: PDFCPUBackendName, Version: PDFCPUBackendVersion, Role: "production"},
		PDFVersion: unknownString,
		PageCount:  unknownInteger,
		Encryption: EncryptionFacts{
			State:                FactUnknown,
			PasswordRequired:     FactUnknown,
			Permissions:          unknownString,
			OperationPermissions: unknownOperationPermissions(),
		},
		Signatures: SignatureFacts{
			State:       FactUnknown,
			Count:       unknownInteger,
			Content:     SignatureClassFacts{State: FactUnknown, Count: unknownInteger},
			UsageRights: SignatureClassFacts{State: FactUnknown, Count: unknownInteger},
			Certified:   FactUnknown,
			Timestamped: FactUnknown,
		},
		Restrictions: RestrictionFacts{
			State:                FactUnknown,
			EncryptedPermissions: FactUnknown,
			DocMDP:               FactUnknown,
			FieldMDP:             FactUnknown,
			UsageRights:          FactUnknown,
			ReaderExtensions:     FactUnknown,
		},
		AcroForm: AcroFormFacts{State: FactUnknown, FieldCount: unknownInteger},
		XFA: XFAFacts{
			State:          FactUnknown,
			Representation: unknownString,
			Rendering:      unknownString,
		},
		Actions: ActionFacts{
			State: FactUnknown, JavaScript: FactUnknown, SubmitForm: FactUnknown, Launch: FactUnknown,
			ExternalNavigation: FactUnknown, OpenAction: FactUnknown, AdditionalActions: FactUnknown,
			CalculationOrder: FactUnknown,
		},
		HybridForm: HybridFormFacts{
			State: FactUnknown, Authority: unknownString, NeedsRendering: FactUnknown, XMLParsed: FactUnknown,
			Scripts: FactUnknown, DataConnections: FactUnknown, RepeatingSubforms: FactUnknown,
			PageGrowth: FactUnknown,
		},
		ExtractableText: TextFacts{State: FactUnknown},
	}
}

func unknownOperationPermissions() OperationPermissionFacts {
	return OperationPermissionFacts{
		Print: PermissionUnknown, FormFill: PermissionUnknown, Modify: PermissionUnknown,
		Assemble: PermissionUnknown,
	}
}

func absentHybridFormFacts() HybridFormFacts {
	return HybridFormFacts{
		State: FactAbsent, Authority: StringFact{State: FactAbsent}, NeedsRendering: FactAbsent,
		XMLParsed: FactAbsent, Scripts: FactAbsent, DataConnections: FactAbsent,
		RepeatingSubforms: FactAbsent, PageGrowth: FactAbsent,
	}
}

func textFactState(facts TextFacts) FactState {
	if facts.PagesUnknown > 0 {
		if facts.PagesWithText > 0 || facts.PagesWithoutText > 0 {
			return FactMixed
		}
		return FactUnknown
	}
	if facts.PagesWithText > 0 && facts.PagesWithoutText > 0 {
		return FactMixed
	}
	if facts.PagesWithText > 0 {
		return FactPresent
	}
	return FactAbsent
}

func aggregatePresence(states ...FactState) FactState {
	hasUnknown := false
	for _, state := range states {
		if state == FactPresent {
			return FactPresent
		}
		hasUnknown = hasUnknown || state == FactUnknown || state == FactMixed
	}
	if hasUnknown {
		return FactUnknown
	}
	return FactAbsent
}
