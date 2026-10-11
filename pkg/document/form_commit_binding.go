package document

// ApprovalArguments is the value-free native tool approval shape. Keeping it
// with the binding lets trusted observers compare the same arguments the tool
// uses without copying its schema. It does not authorize or consume a grant.
func (binding FormCommitBinding) ApprovalArguments() map[string]any {
	arguments := map[string]any{
		"schema_version":         "mintclaw.document_form_approval.v1",
		"job_id":                 binding.JobID,
		"owner_digest":           binding.OwnerDigest,
		"approval_revision":      binding.ApprovalRevision,
		"review_revision":        binding.ReviewRevision,
		"source_digest":          binding.SourceDigest,
		"field_schema_digest":    binding.FieldSchemaDigest,
		"backend_revision":       binding.BackendRevision,
		"audit_policy_revision":  binding.AuditPolicyRevision,
		"assignment_digest":      binding.AssignmentDigest,
		"review_digest":          binding.ReviewDigest,
		"output_policy_revision": binding.OutputPolicyRevision,
		"operation_id":           binding.OperationID,
		"expires_at":             binding.ExpiresAt,
	}
	if binding.OutputScope != "" {
		arguments["output_scope"] = binding.OutputScope
	}
	if binding.OutputMode != "" {
		arguments["output_mode"] = binding.OutputMode
	}
	return arguments
}
