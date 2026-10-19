package agent

import "github.com/bogdanovich/mintclaw/pkg/diagnostictrace"

func diagnosticDocumentFields(value ToolExecEndPayload) *diagnostictrace.DocumentFieldsPayload {
	if value.Tool != "document" || value.IsError || value.Async || value.Suspended {
		return nil
	}
	observation := value.DocumentFields
	payload := diagnostictrace.DocumentFieldsPayload{
		Operation: observation.Operation, SourceSHA256: observation.SourceSHA256,
		SchemaFields: observation.SchemaFields, ReturnedFields: observation.ReturnedFields,
		RequiredFields: observation.RequiredFields, ValuesPresent: observation.ValuesPresent,
		DefaultsPresent: observation.DefaultsPresent,
	}
	if !payload.Valid() {
		return nil
	}
	return &payload
}
