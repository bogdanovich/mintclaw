package document

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	FormAuditPromptRevision = "mintclaw.document_form_audit.v1"
	FormReviewSchemaVersion = "mintclaw.document_form_review.v1"
	maxFormAuditCandidates  = 8
)

type FormAuditPolicy struct {
	PrimaryModel                 string
	PrimaryIdentity              string
	EquivalentFallbacks          []string
	EquivalentFallbackIdentities []string
	PromptRevision               string
}

type formAuditCandidate struct {
	Alias    string `json:"alias"`
	Identity string `json:"identity"`
}

func (policy FormAuditPolicy) Revision() (string, error) {
	candidates, promptRevision, err := policy.normalized()
	if err != nil {
		return "", err
	}
	return formJobJSONDigest(struct {
		PromptRevision string               `json:"prompt_revision"`
		Candidates     []formAuditCandidate `json:"equivalent_candidates"`
	}{PromptRevision: promptRevision, Candidates: candidates})
}

func (policy FormAuditPolicy) normalized() ([]formAuditCandidate, string, error) {
	promptRevision := strings.TrimSpace(policy.PromptRevision)
	if promptRevision == "" {
		promptRevision = FormAuditPromptRevision
	}
	if promptRevision != policy.PromptRevision && policy.PromptRevision != "" ||
		len(promptRevision) > maxFormJobRevisionLength || !utf8.ValidString(promptRevision) {
		return nil, "", errors.New("document form audit prompt revision is invalid")
	}
	aliases := append([]string{policy.PrimaryModel}, policy.EquivalentFallbacks...)
	identities := append([]string{policy.PrimaryIdentity}, policy.EquivalentFallbackIdentities...)
	if len(aliases) == 0 || len(aliases) > maxFormAuditCandidates || len(aliases) != len(identities) {
		return nil, "", errors.New("document form audit model policy is invalid")
	}
	candidates := make([]formAuditCandidate, len(aliases))
	seenAliases := make(map[string]struct{}, len(candidates))
	seenIdentities := make(map[string]struct{}, len(candidates))
	for index := range aliases {
		alias := strings.TrimSpace(aliases[index])
		identity := strings.TrimSpace(identities[index])
		if alias == "" || alias != aliases[index] || len(alias) > maxFormJobRevisionLength ||
			!utf8.ValidString(alias) || identity == "" || identity != identities[index] ||
			len(identity) > maxFormJobRevisionLength || !utf8.ValidString(identity) {
			return nil, "", errors.New("document form audit model policy is invalid")
		}
		if _, duplicate := seenAliases[alias]; duplicate {
			return nil, "", errors.New("document form audit model policy contains a duplicate")
		}
		if _, duplicate := seenIdentities[identity]; duplicate {
			return nil, "", errors.New("document form audit model policy contains a duplicate identity")
		}
		seenAliases[alias] = struct{}{}
		seenIdentities[identity] = struct{}{}
		candidates[index] = formAuditCandidate{Alias: alias, Identity: identity}
	}
	return candidates, promptRevision, nil
}

type FormAuditField struct {
	Schema   FormField
	Existing bool
	State    FormJobFieldState
	Value    FormProtectedValue
}

// FormAuditView is an ephemeral protected projection. Callers must not retain,
// log, trace, or place it in canonical history.
type FormAuditView struct {
	PolicyRevision string
	SchemaDigest   string
	Fields         []FormAuditField
}

type FormAuditDecision string

const (
	FormAuditPass  FormAuditDecision = "pass"
	FormAuditBlock FormAuditDecision = "block"
)

type FormAuditFinding struct {
	FieldID string `json:"field_id"`
	Code    string `json:"code"`
}

// FormAuditProposal is deliberately value-free. A model may block or request
// confirmation, but it cannot confirm values or author assignments.
type FormAuditProposal struct {
	Decision FormAuditDecision  `json:"decision"`
	Findings []FormAuditFinding `json:"findings,omitempty"`
}

type FormAuditor interface {
	AuditForm(context.Context, string, FormAuditView) (FormAuditProposal, error)
}

type FormReviewField struct {
	FieldID    string              `json:"field_id"`
	Label      string              `json:"label"`
	Kind       FormFieldKind       `json:"kind"`
	Required   bool                `json:"required"`
	State      FormValueState      `json:"state"`
	Source     FormValueSource     `json:"source,omitempty"`
	Confidence FormValueConfidence `json:"confidence,omitempty"`
	Validation FormValueValidation `json:"validation,omitempty"`
	Summary    string              `json:"summary"`
}

// FormReview is safe for ordinary history: it contains schema labels and
// value-free states, never protected answers or normalized values.
type FormReview struct {
	SchemaVersion       string                 `json:"schema_version"`
	JobID               string                 `json:"job_id"`
	State               FormJobState           `json:"state"`
	Revision            int64                  `json:"revision"`
	ReviewRevision      int64                  `json:"review_revision,omitempty"`
	FieldSchemaDigest   string                 `json:"field_schema_digest"`
	AuditPolicyRevision string                 `json:"audit_policy_revision"`
	AuditModel          string                 `json:"audit_model,omitempty"`
	AssignmentDigest    string                 `json:"assignment_digest,omitempty"`
	ReviewDigest        string                 `json:"review_digest,omitempty"`
	RequestedAction     string                 `json:"requested_action"`
	Fields              []FormReviewField      `json:"fields"`
	Blockers            []FormJobReviewBlocker `json:"blockers,omitempty"`
	Ready               bool                   `json:"ready"`
}

type FormReviewRequest struct {
	JobID            string
	ExpectedRevision int64
	Owner            FormJobOwner
	Schema           FormFieldsFacts
	Policy           FormAuditPolicy
	Auditor          FormAuditor
}

type FormReviewResult struct {
	Job          FormJobRecord
	Review       FormReview
	UsedFallback bool
}

type preparedFormAudit struct {
	record           FormJobRecord
	view             FormAuditView
	assignmentDigest string
}

func (store *FormJobStore) ReviewFormJob(
	ctx context.Context,
	request FormReviewRequest,
) (FormReviewResult, error) {
	if request.Auditor == nil || strings.TrimSpace(request.JobID) == "" || request.ExpectedRevision <= 0 ||
		!validFormFieldsFacts(request.Schema) {
		return FormReviewResult{}, ErrFormAuditUnavailable
	}
	candidates, _, err := request.Policy.normalized()
	if err != nil {
		return FormReviewResult{}, ErrFormAuditUnavailable
	}
	policyRevision, err := request.Policy.Revision()
	if err != nil {
		return FormReviewResult{}, ErrFormAuditUnavailable
	}
	prepared, err := store.prepareFormAudit(ctx, request, policyRevision)
	if err != nil {
		return FormReviewResult{}, err
	}
	defer clearFormAuditView(&prepared.view)

	var proposal FormAuditProposal
	selectedModel := ""
	selectedIndex := -1
	for index, candidate := range candidates {
		view := cloneFormAuditView(prepared.view)
		candidateProposal, auditErr := request.Auditor.AuditForm(ctx, candidate.Alias, view)
		clearFormAuditView(&view)
		if auditErr != nil || validateFormAuditProposal(candidateProposal, request.Schema) != nil {
			if ctx.Err() != nil {
				return FormReviewResult{}, ctx.Err()
			}
			continue
		}
		proposal = candidateProposal
		selectedModel = candidate.Identity
		selectedIndex = index
		break
	}
	if selectedModel == "" {
		return FormReviewResult{}, ErrFormAuditUnavailable
	}
	job, review, err := store.commitFormAudit(
		ctx,
		request,
		policyRevision,
		prepared,
		selectedModel,
		proposal,
	)
	if err != nil {
		return FormReviewResult{}, err
	}
	return FormReviewResult{
		Job:          job,
		Review:       review,
		UsedFallback: selectedIndex > 0,
	}, nil
}

func (store *FormJobStore) CurrentFormReview(
	ctx context.Context,
	jobID string,
	owner FormJobOwner,
	schema FormFieldsFacts,
) (FormReview, error) {
	record, err := store.Get(ctx, jobID, owner)
	if err != nil {
		return FormReview{}, err
	}
	if matchErr := formJobMatchesSchema(record, schema); matchErr != nil {
		return FormReview{}, matchErr
	}
	if record.AuditRevision == 0 {
		return FormReview{}, ErrFormReviewStale
	}
	review, err := formReviewFromRecord(record, schema)
	if err != nil {
		return FormReview{}, err
	}
	if record.ReviewDigest != "" {
		digest, digestErr := formReviewDigest(review)
		if digestErr != nil || digest != record.ReviewDigest {
			return FormReview{}, ErrFormJobRecordCorrupt
		}
	}
	return review, nil
}

func (store *FormJobStore) prepareFormAudit(
	ctx context.Context,
	request FormReviewRequest,
	policyRevision string,
) (preparedFormAudit, error) {
	var prepared preparedFormAudit
	err := store.update(ctx, func(document *formJobStoreDocument, _ time.Time) (bool, error) {
		record, jobKey, err := store.authorizedRecord(document, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		defer clear(jobKey)
		if record.Public.Revision != request.ExpectedRevision {
			return false, ErrFormJobConflict
		}
		if record.Public.AuditRevision != 0 {
			return false, ErrFormReviewStale
		}
		if record.Public.AuditPolicyRevision != policyRevision {
			return false, ErrFormAuditPolicyChanged
		}
		if matchErr := formJobMatchesSchema(record.Public, request.Schema); matchErr != nil {
			return false, matchErr
		}
		fields, assignments, err := protectedFormAuditFields(record, jobKey, request.Schema)
		if err != nil {
			return false, err
		}
		assignmentDigest, err := formJobJSONKeyedDigest(store.profileKey, assignments)
		clearFormAuditAssignments(assignments)
		if err != nil {
			clearFormAuditFields(fields)
			return false, err
		}
		prepared = preparedFormAudit{
			record: cloneFormJobRecord(record.Public),
			view: FormAuditView{
				PolicyRevision: policyRevision,
				SchemaDigest:   record.Public.FieldSchemaDigest,
				Fields:         fields,
			},
			assignmentDigest: assignmentDigest,
		}
		return false, nil
	})
	return prepared, err
}

type formAuditAssignment struct {
	FieldID  string             `json:"field_id"`
	Existing bool               `json:"existing,omitempty"`
	EventID  string             `json:"event_id,omitempty"`
	Value    FormProtectedValue `json:"value,omitempty"`
}

func protectedFormAuditFields(
	record formJobStoredRecord,
	jobKey []byte,
	schema FormFieldsFacts,
) ([]FormAuditField, []formAuditAssignment, error) {
	current := make(map[string]FormJobFieldState, len(record.Public.Fields))
	for _, field := range record.Public.Fields {
		current[field.FieldID] = field
	}
	events := make(map[string]formJobEnvelope, len(record.Events))
	for _, envelope := range record.Events {
		events[envelope.EventID] = envelope
	}
	fields := make([]FormAuditField, 0, len(schema.Fields))
	assignments := make([]formAuditAssignment, 0, len(schema.Fields))
	for _, field := range schema.Fields {
		if field.ReadOnly {
			continue
		}
		state, found := current[field.ID]
		if !found {
			if !field.HasValue {
				clearFormAuditFields(fields)
				clearFormAuditAssignments(assignments)
				return nil, nil, ErrFormReviewStale
			}
			fields = append(fields, FormAuditField{Schema: cloneFormField(field), Existing: true})
			assignments = append(assignments, formAuditAssignment{FieldID: field.ID, Existing: true})
			continue
		}
		if !formFieldStateResolved(state, field) {
			clearFormAuditFields(fields)
			clearFormAuditAssignments(assignments)
			return nil, nil, ErrFormReviewStale
		}
		envelope, found := events[state.EventID]
		if !found {
			clearFormAuditFields(fields)
			clearFormAuditAssignments(assignments)
			return nil, nil, ErrFormJobRecordCorrupt
		}
		payload, err := openFormJobValuePayload(jobKey, envelope)
		if err != nil || payload.FieldID != field.ID {
			clearFormAuditFields(fields)
			clearFormAuditAssignments(assignments)
			return nil, nil, ErrFormJobRecordCorrupt
		}
		value := cloneProtectedValue(payload.Value)
		fields = append(fields, FormAuditField{
			Schema: cloneFormField(field), State: state, Value: cloneProtectedValue(value),
		})
		assignments = append(assignments, formAuditAssignment{
			FieldID: field.ID, EventID: state.EventID, Value: value,
		})
	}
	return fields, assignments, nil
}

func (store *FormJobStore) commitFormAudit(
	ctx context.Context,
	request FormReviewRequest,
	policyRevision string,
	prepared preparedFormAudit,
	selectedModel string,
	proposal FormAuditProposal,
) (FormJobRecord, FormReview, error) {
	var result FormJobRecord
	var review FormReview
	err := store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, err := store.authorizedPublicRecord(document, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		if record.Public.Revision != prepared.record.Revision ||
			record.Public.LedgerDigest != prepared.record.LedgerDigest {
			return false, ErrFormReviewStale
		}
		if record.Public.AuditPolicyRevision != policyRevision {
			return false, ErrFormAuditPolicyChanged
		}
		if matchErr := formJobMatchesSchema(record.Public, request.Schema); matchErr != nil {
			return false, matchErr
		}
		blockers := normalizedFormAuditBlockers(proposal, request.Schema)
		nextRevision := record.Public.Revision + 1
		auditDigest, err := formJobJSONDigest(struct {
			PolicyRevision   string                 `json:"policy_revision"`
			Model            string                 `json:"model"`
			AssignmentDigest string                 `json:"assignment_digest"`
			Blockers         []FormJobReviewBlocker `json:"blockers,omitempty"`
		}{
			PolicyRevision: policyRevision, Model: selectedModel,
			AssignmentDigest: prepared.assignmentDigest, Blockers: blockers,
		})
		if err != nil {
			return false, err
		}
		record.Public.Revision = nextRevision
		record.Public.UpdatedAt = now.UnixMilli()
		record.Public.AuditRevision = nextRevision
		record.Public.AuditDigest = auditDigest
		record.Public.AuditModel = selectedModel
		record.Public.AuditBlockers = blockers
		record.Public.AssignmentDigest = prepared.assignmentDigest
		record.Public.ReviewRevision = 0
		record.Public.ReviewDigest = ""
		if len(blockers) == 0 {
			record.Public.State = FormJobReviewReady
			record.Public.ReviewRevision = nextRevision
			review, err = formReviewFromRecord(record.Public, request.Schema)
			if err != nil {
				return false, err
			}
			record.Public.ReviewDigest, err = formReviewDigest(review)
			if err != nil {
				return false, err
			}
			review.ReviewDigest = record.Public.ReviewDigest
		} else {
			record.Public.State = FormJobCollecting
			review, err = formReviewFromRecord(record.Public, request.Schema)
			if err != nil {
				return false, err
			}
		}
		document.Records[record.Public.JobID] = record
		result = cloneFormJobRecord(record.Public)
		return true, nil
	})
	return result, review, err
}

func validateFormAuditProposal(proposal FormAuditProposal, schema FormFieldsFacts) error {
	if proposal.Decision == FormAuditPass {
		if len(proposal.Findings) != 0 {
			return errors.New("document form audit pass contains findings")
		}
		return nil
	}
	if proposal.Decision != FormAuditBlock || len(proposal.Findings) == 0 ||
		len(proposal.Findings) > len(schema.Fields) {
		return errors.New("document form audit proposal is invalid")
	}
	fields := make(map[string]FormField, len(schema.Fields))
	for _, field := range schema.Fields {
		fields[field.ID] = field
	}
	seen := make(map[string]struct{}, len(proposal.Findings))
	for _, finding := range proposal.Findings {
		field, found := fields[finding.FieldID]
		if !found || field.ReadOnly || !validFormAuditFindingCode(finding.Code) {
			return errors.New("document form audit finding is invalid")
		}
		if _, duplicate := seen[finding.FieldID]; duplicate {
			return errors.New("document form audit finding is duplicated")
		}
		seen[finding.FieldID] = struct{}{}
	}
	return nil
}

func validFormAuditFindingCode(code string) bool {
	switch code {
	case "field_ambiguous", "field_conflicting", "field_confirmation_required", "field_invalid":
		return true
	default:
		return false
	}
}

func normalizedFormAuditBlockers(proposal FormAuditProposal, schema FormFieldsFacts) []FormJobReviewBlocker {
	order := make(map[string]int, len(schema.Fields))
	for index, field := range schema.Fields {
		order[field.ID] = index
	}
	blockers := make([]FormJobReviewBlocker, len(proposal.Findings))
	for index, finding := range proposal.Findings {
		blockers[index] = FormJobReviewBlocker(finding)
	}
	slices.SortFunc(blockers, func(left, right FormJobReviewBlocker) int {
		if compared := cmp.Compare(order[left.FieldID], order[right.FieldID]); compared != 0 {
			return compared
		}
		return cmp.Compare(left.Code, right.Code)
	})
	return blockers
}

func formReviewFromRecord(record FormJobRecord, schema FormFieldsFacts) (FormReview, error) {
	if err := formJobMatchesSchema(record, schema); err != nil {
		return FormReview{}, err
	}
	current := make(map[string]FormJobFieldState, len(record.Fields))
	for _, field := range record.Fields {
		current[field.FieldID] = field
	}
	review := FormReview{
		SchemaVersion: FormReviewSchemaVersion, JobID: record.JobID, State: record.State,
		Revision: record.Revision, ReviewRevision: record.ReviewRevision,
		FieldSchemaDigest: record.FieldSchemaDigest, AuditPolicyRevision: record.AuditPolicyRevision,
		AuditModel: record.AuditModel, AssignmentDigest: record.AssignmentDigest,
		ReviewDigest: record.ReviewDigest, RequestedAction: "fill_and_deliver_verified_pdf",
		Blockers: append([]FormJobReviewBlocker(nil), record.AuditBlockers...),
		Ready: record.State == FormJobReviewReady && record.ReviewRevision == record.Revision &&
			len(record.AuditBlockers) == 0,
	}
	for _, field := range schema.Fields {
		if field.ReadOnly {
			continue
		}
		label := strings.TrimSpace(field.AlternateName)
		if label == "" {
			label = strings.TrimSpace(field.Name)
		}
		if label == "" {
			label = field.ID
		}
		projected := FormReviewField{
			FieldID: field.ID, Label: label, Kind: field.Kind, Required: field.Required,
			Summary: "existing",
		}
		if state, found := current[field.ID]; found {
			projected.State = state.State
			projected.Source = state.Source
			projected.Confidence = state.Confidence
			projected.Validation = state.Validation
			projected.Summary = "provided"
			if state.ValueKind == ProtectedValueBlank {
				projected.Summary = "blank"
			}
		} else if !field.HasValue {
			projected.Summary = "unresolved"
		}
		review.Fields = append(review.Fields, projected)
	}
	return review, nil
}

func formReviewDigest(review FormReview) (string, error) {
	review.ReviewDigest = ""
	return formJobJSONDigest(review)
}

func cloneFormAuditView(view FormAuditView) FormAuditView {
	cloned := view
	cloned.Fields = make([]FormAuditField, len(view.Fields))
	for index, field := range view.Fields {
		cloned.Fields[index] = field
		cloned.Fields[index].Schema = cloneFormField(field.Schema)
		cloned.Fields[index].Value = cloneProtectedValue(field.Value)
	}
	return cloned
}

func cloneFormField(field FormField) FormField {
	cloned := field
	cloned.Options = append([]FormFieldOption(nil), field.Options...)
	cloned.Widgets = append([]FormFieldWidget(nil), field.Widgets...)
	return cloned
}

func clearFormAuditView(view *FormAuditView) {
	if view == nil {
		return
	}
	clearFormAuditFields(view.Fields)
	view.Fields = nil
}

func clearFormAuditFields(fields []FormAuditField) {
	for index := range fields {
		clearProtectedFormValue(&fields[index].Value)
		clear(fields[index].Schema.Options)
		fields[index].Schema.Options = nil
		clear(fields[index].Schema.Widgets)
		fields[index].Schema.Widgets = nil
	}
	clear(fields)
}

func clearFormAuditAssignments(assignments []formAuditAssignment) {
	for index := range assignments {
		clearProtectedFormValue(&assignments[index].Value)
	}
	clear(assignments)
}

func clearProtectedFormValue(value *FormProtectedValue) {
	if value == nil {
		return
	}
	clear(value.Choices)
	*value = FormProtectedValue{}
}
