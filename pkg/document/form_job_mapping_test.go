package document

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFormFieldSchemaDigestSeparatesSourceAndSchemaIdentity(t *testing.T) {
	schema := *successfulTestFormFields()
	digest, err := FormFieldSchemaDigest(schema)
	if err != nil {
		t.Fatal(err)
	}
	otherSource := schema
	otherSource.SourceSHA256 = strings.Repeat("b", 64)
	otherSourceDigest, err := FormFieldSchemaDigest(otherSource)
	if err != nil {
		t.Fatal(err)
	}
	if digest != otherSourceDigest {
		t.Fatalf("source identity changed field schema digest: %q != %q", digest, otherSourceDigest)
	}
	changed := schema
	changed.Fields = append([]FormField(nil), schema.Fields...)
	changed.Fields[0].Required = true
	changedDigest, err := FormFieldSchemaDigest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if digest == changedDigest {
		t.Fatal("field schema mutation did not change digest")
	}
	backend, err := FormFieldsBackendRevision(schema)
	if err != nil || backend != "pdfcpu:v0.15.0:production:one_shot_process" {
		t.Fatalf("backend revision = (%q, %v)", backend, err)
	}
}

func TestFormJobMappingConfirmsAndReusesFactAcrossRestart(t *testing.T) {
	store, options := newTestFormJobStore(t)
	owner := testFormJobOwner()
	schema := *successfulTestFormFields()
	created := createMappedFormJob(t, store, owner, schema)
	sentinel := "MINTCLAW_PDF3_MAPPING_PRIVATE_91f4"
	supplied := appendMappedSource(t, store, created, owner, schema.Fields[0].ID, "answer-1", FormProtectedValue{
		Kind: ProtectedValueText, Text: sentinel,
	}, FormValueSupplied, FormBlankNone, "")
	mapped, err := store.MapFormField(t.Context(), FormFieldMappingRequest{
		JobID: supplied.JobID, ExpectedRevision: supplied.Revision, Owner: owner, Schema: schema,
		FieldID: schema.Fields[0].ID, SourceEventID: supplied.Fields[0].EventID, IdempotencyKey: "map-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mapped.Reused || mapped.Field.State != FormValueConfirmed ||
		mapped.Field.Confidence != FormValueConfidenceExact || mapped.Field.Validation != FormValueValidationValid ||
		mapped.Field.Source != FormValueSourceDeterministic ||
		mapped.Field.SupersedesEventID != supplied.Fields[0].EventID {
		t.Fatalf("mapped field = %#v, reused=%t", mapped.Field, mapped.Reused)
	}
	values, err := store.ReadValues(t.Context(), mapped.Job.JobID, owner, []string{schema.Fields[0].ID})
	if err != nil || values[schema.Fields[0].ID].Value.Text != sentinel {
		t.Fatalf("mapped protected value = %#v, err=%v", values, err)
	}
	replayed, err := store.MapFormField(t.Context(), FormFieldMappingRequest{
		JobID: supplied.JobID, ExpectedRevision: supplied.Revision, Owner: owner, Schema: schema,
		FieldID: schema.Fields[0].ID, SourceEventID: supplied.Fields[0].EventID, IdempotencyKey: "map-1",
	})
	if err != nil || !replayed.Reused || replayed.Job.Revision != mapped.Job.Revision ||
		replayed.Event.EventID != mapped.Event.EventID {
		t.Fatalf("idempotent mapping replay = %#v, err=%v", replayed, err)
	}
	reused, err := store.MapFormField(t.Context(), FormFieldMappingRequest{
		JobID: mapped.Job.JobID, ExpectedRevision: mapped.Job.Revision, Owner: owner, Schema: schema,
		FieldID: schema.Fields[0].ID, SourceEventID: mapped.Event.EventID, IdempotencyKey: "map-reuse",
	})
	if err != nil || !reused.Reused || reused.Job.Revision != mapped.Job.Revision {
		t.Fatalf("confirmed fact reuse = %#v, err=%v", reused, err)
	}
	summary, err := store.FormMappingSummary(t.Context(), mapped.Job.JobID, owner, schema)
	if err != nil || !summary.ReadyForReview || summary.NextUnresolvedID != "" ||
		!slices.Equal(summary.ConfirmedFieldIDs, []string{schema.Fields[0].ID}) {
		t.Fatalf("mapping summary = %#v, err=%v", summary, err)
	}
	store.Close()
	reopened, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	restarted, err := reopened.FormMappingSummary(t.Context(), mapped.Job.JobID, owner, schema)
	if err != nil || !restarted.ReadyForReview || restarted.Revision != mapped.Job.Revision {
		t.Fatalf("restart summary = %#v, err=%v", restarted, err)
	}
	data, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), sentinel) {
		t.Fatal("mapping sentinel leaked into public store bytes")
	}
}

func TestFormJobMappingValidatesTypesAmbiguityConflictAndCorrection(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	owner := testFormJobOwner()
	_, schema := fillNormalizationFixture()
	schema.Fields[3].Options = append(schema.Fields[3].Options,
		FormFieldOption{Export: "USA", Display: "United States"})
	created := createMappedFormJob(t, store, owner, schema)

	tests := []struct {
		fieldIndex int
		value      FormProtectedValue
		state      FormValueState
		validation FormValueValidation
		code       string
	}{
		{
			0,
			FormProtectedValue{Kind: ProtectedValueText, Text: "valid text"},
			FormValueConfirmed,
			FormValueValidationValid, "",
		},
		{
			2,
			FormProtectedValue{Kind: ProtectedValueText, Text: "yes"},
			FormValueConfirmed,
			FormValueValidationValid, "",
		},
		{
			3,
			FormProtectedValue{Kind: ProtectedValueText, Text: "United States"},
			FormValueAmbiguous,
			FormValueValidationAmbiguous, "field_ambiguous",
		},
		{
			4,
			FormProtectedValue{Kind: ProtectedValueChoices, Choices: []string{"CA", "CA"}},
			FormValueConflicting,
			FormValueValidationConflicting, "field_conflicting",
		},
		{
			5,
			FormProtectedValue{Kind: ProtectedValueChoices, Choices: []string{"one", "three"}},
			FormValueConfirmed,
			FormValueValidationValid, "",
		},
		{
			7,
			FormProtectedValue{Kind: ProtectedValueText, Text: "2026-09-17"},
			FormValueInvalid,
			FormValueValidationInvalid, "field_invalid",
		},
	}
	current := created
	results := make(map[int]FormFieldMappingResult)
	for index, test := range tests {
		fieldID := schema.Fields[test.fieldIndex].ID
		current = appendMappedSource(t, store, current, owner, fieldID, "typed-answer-"+string(rune('a'+index)),
			test.value, FormValueSupplied, FormBlankNone, "")
		source := currentField(t, current, fieldID)
		mapped, err := store.MapFormField(t.Context(), FormFieldMappingRequest{
			JobID: current.JobID, ExpectedRevision: current.Revision, Owner: owner, Schema: schema,
			FieldID: fieldID, SourceEventID: source.EventID, IdempotencyKey: "typed-map-" + string(rune('a'+index)),
		})
		if err != nil {
			t.Fatalf("map field %d: %v", test.fieldIndex, err)
		}
		if mapped.Field.State != test.state || mapped.Field.Validation != test.validation ||
			mapped.Field.ValidationCode != test.code || mapped.Field.Confidence != FormValueConfidenceExact {
			t.Fatalf("field %d mapping = %#v", test.fieldIndex, mapped.Field)
		}
		results[test.fieldIndex] = mapped
		current = mapped.Job
	}
	values, err := store.ReadValues(t.Context(), current.JobID, owner, []string{
		schema.Fields[2].ID, schema.Fields[5].ID,
	})
	if err != nil || values[schema.Fields[2].ID].Value.Boolean == nil ||
		!*values[schema.Fields[2].ID].Value.Boolean ||
		!slices.Equal(values[schema.Fields[5].ID].Value.Choices, []string{"one", "three"}) {
		t.Fatalf("typed protected values = %#v, err=%v", values, err)
	}

	invalidDate := results[7]
	correctedRaw := appendMappedSource(
		t,
		store,
		current,
		owner,
		schema.Fields[7].ID,
		"date-correction",
		FormProtectedValue{Kind: ProtectedValueText, Text: "09/17/2026"},
		FormValueSupplied,
		FormBlankNone,
		invalidDate.Field.EventID,
	)
	correctedSource := currentField(t, correctedRaw, schema.Fields[7].ID)
	corrected, err := store.MapFormField(t.Context(), FormFieldMappingRequest{
		JobID: correctedRaw.JobID, ExpectedRevision: correctedRaw.Revision, Owner: owner, Schema: schema,
		FieldID: schema.Fields[7].ID, SourceEventID: correctedSource.EventID, IdempotencyKey: "map-date-correction",
	})
	if err != nil || corrected.Field.State != FormValueConfirmed ||
		corrected.Field.SupersedesEventID != correctedSource.EventID ||
		corrected.Job.LedgerRevision != invalidDate.Job.LedgerRevision+2 {
		t.Fatalf("corrected date = %#v, err=%v", corrected, err)
	}
	historical, err := store.readFormJobValueEvent(
		t.Context(), corrected.Job.JobID, owner, invalidDate.Field.EventID,
	)
	if err != nil || historical.State != FormValueInvalid {
		t.Fatalf("superseded invalid event = %#v, err=%v", historical, err)
	}
	summary, err := store.FormMappingSummary(t.Context(), corrected.Job.JobID, owner, schema)
	if err != nil || summary.ReadyForReview || summary.NextUnresolvedID != schema.Fields[1].ID ||
		!mappingSummaryHasBlocker(summary, schema.Fields[3].ID, "field_ambiguous") ||
		!mappingSummaryHasBlocker(summary, schema.Fields[4].ID, "field_conflicting") {
		t.Fatalf("typed mapping summary = %#v, err=%v", summary, err)
	}
}

func TestFormJobMappingRequiredBlankAndStaleSchemaFailClosed(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	owner := testFormJobOwner()
	schema := *successfulTestFormFields()
	schema.Fields = append([]FormField(nil), schema.Fields...)
	schema.Fields[0].Required = true
	created := createMappedFormJob(t, store, owner, schema)
	blank := appendMappedSource(t, store, created, owner, schema.Fields[0].ID, "skip-required",
		FormProtectedValue{Kind: ProtectedValueBlank}, FormValueBlanked, FormBlankSkipped, "")
	mapped, err := store.MapFormField(t.Context(), FormFieldMappingRequest{
		JobID: blank.JobID, ExpectedRevision: blank.Revision, Owner: owner, Schema: schema,
		FieldID: schema.Fields[0].ID, SourceEventID: blank.Fields[0].EventID, IdempotencyKey: "map-required-blank",
	})
	if err != nil || mapped.Field.State != FormValueBlanked ||
		mapped.Field.Validation != FormValueValidationRequiredBlank ||
		mapped.Field.ValidationCode != "field_required_blank" {
		t.Fatalf("required blank = %#v, err=%v", mapped, err)
	}
	summary, err := store.FormMappingSummary(t.Context(), mapped.Job.JobID, owner, schema)
	if err != nil || summary.ReadyForReview ||
		!mappingSummaryHasBlocker(summary, schema.Fields[0].ID, "field_required_blank") {
		t.Fatalf("required blank summary = %#v, err=%v", summary, err)
	}
	changed := schema
	changed.Fields = append([]FormField(nil), schema.Fields...)
	changed.Fields[0].MaxLength = 20
	if _, err := store.FormMappingSummary(
		t.Context(),
		mapped.Job.JobID,
		owner,
		changed,
	); !errors.Is(
		err,
		ErrFormJobStale,
	) {
		t.Fatalf("changed schema error = %v", err)
	}
	changedSource := schema
	changedSource.SourceSHA256 = strings.Repeat("b", 64)
	if _, err := store.FormMappingSummary(
		t.Context(), mapped.Job.JobID, owner, changedSource,
	); !errors.Is(err, ErrFormJobStale) {
		t.Fatalf("changed source error = %v", err)
	}
}

func TestFormJobMappingDoesNotPromoteModelSuggestion(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	owner := testFormJobOwner()
	schema := *successfulTestFormFields()
	created := createMappedFormJob(t, store, owner, schema)
	suggested, _, err := store.AppendValue(t.Context(), FormJobAppendValueRequest{
		JobID: created.JobID, ExpectedRevision: created.Revision, Owner: owner,
		FieldID: schema.Fields[0].ID, IdempotencyKey: "model-suggestion",
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: "MINTCLAW_PDF3_LOW_CONFIDENCE_712f"},
		State: FormValueModelSuggested, Source: FormValueSourceModel,
		Confidence: FormValueConfidenceLow, Validation: FormValueValidationConfirmationRequired,
		ValidationCode: "field_confirmation_required",
	})
	if err != nil {
		t.Fatal(err)
	}
	source := currentField(t, suggested, schema.Fields[0].ID)
	mapped, err := store.MapFormField(t.Context(), FormFieldMappingRequest{
		JobID: suggested.JobID, ExpectedRevision: suggested.Revision, Owner: owner, Schema: schema,
		FieldID: schema.Fields[0].ID, SourceEventID: source.EventID, IdempotencyKey: "must-not-promote",
	})
	if err != nil || !mapped.Reused || mapped.Job.Revision != suggested.Revision ||
		mapped.Field.State != FormValueModelSuggested || mapped.Field.Confidence != FormValueConfidenceLow ||
		mapped.Field.Validation != FormValueValidationConfirmationRequired {
		t.Fatalf("model suggestion mapping = %#v, err=%v", mapped, err)
	}
	summary, err := store.FormMappingSummary(t.Context(), suggested.JobID, owner, schema)
	if err != nil || summary.ReadyForReview ||
		!mappingSummaryHasBlocker(summary, schema.Fields[0].ID, "field_confirmation_required") {
		t.Fatalf("model suggestion summary = %#v, err=%v", summary, err)
	}
}

func TestFormJobStoreReopensLegacyCorrectionProjection(t *testing.T) {
	store, options := newTestFormJobStore(t)
	owner := testFormJobOwner()
	schema := *successfulTestFormFields()
	base := createMappedFormJob(t, store, owner, schema)
	created, first, err := store.AppendValue(t.Context(), FormJobAppendValueRequest{
		JobID: base.JobID, ExpectedRevision: base.Revision, Owner: owner, FieldID: schema.Fields[0].ID,
		IdempotencyKey: "legacy-first", Value: FormProtectedValue{Kind: ProtectedValueText, Text: "first"},
		State: FormValueConfirmed, Source: FormValueSourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	corrected, correction, err := store.AppendValue(t.Context(), FormJobAppendValueRequest{
		JobID: created.JobID, ExpectedRevision: created.Revision, Owner: owner,
		FieldID: first.FieldID, IdempotencyKey: "legacy-correction",
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: "corrected"},
		State: FormValueConfirmed, Source: FormValueSourceUser, SupersedesEventID: first.EventID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.update(t.Context(), func(document *formJobStoreDocument, _ time.Time) (bool, error) {
		record := document.Records[corrected.JobID]
		record.Public.Fields[0].SupersedesEventID = ""
		document.Records[corrected.JobID] = record
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	reopened, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	values, err := reopened.ReadValues(t.Context(), corrected.JobID, owner, []string{first.FieldID})
	if err != nil || values[first.FieldID].EventID != correction.EventID ||
		values[first.FieldID].Value.Text != "corrected" {
		t.Fatalf("legacy corrected value = %#v, err=%v", values, err)
	}
	if err := reopened.update(t.Context(), func(document *formJobStoreDocument, _ time.Time) (bool, error) {
		record := document.Records[corrected.JobID]
		record.Public.Fields[0].SupersedesEventID = "form_value_conflicting_projection"
		document.Records[corrected.JobID] = record
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ReadValues(
		t.Context(), corrected.JobID, owner, []string{first.FieldID},
	); !errors.Is(err, ErrFormJobRecordCorrupt) {
		t.Fatalf("conflicting projected correction error = %v", err)
	}
}

func createMappedFormJob(
	t *testing.T,
	store *FormJobStore,
	owner FormJobOwner,
	schema FormFieldsFacts,
) FormJobRecord {
	t.Helper()
	digest, err := FormFieldSchemaDigest(schema)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := FormFieldsBackendRevision(schema)
	if err != nil {
		t.Fatal(err)
	}
	request := testFormJobCreateRequest(owner)
	request.SourceDigest = schema.SourceSHA256
	request.FieldSchemaDigest = digest
	request.BackendRevision = backend
	created, err := store.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func appendMappedSource(
	t *testing.T,
	store *FormJobStore,
	record FormJobRecord,
	owner FormJobOwner,
	fieldID string,
	idempotencyKey string,
	value FormProtectedValue,
	state FormValueState,
	blankReason FormBlankReason,
	supersedes string,
) FormJobRecord {
	t.Helper()
	updated, _, err := store.AppendValue(t.Context(), FormJobAppendValueRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		FieldID: fieldID, IdempotencyKey: idempotencyKey, Value: value,
		State: state, Source: FormValueSourceUser, BlankReason: blankReason, SupersedesEventID: supersedes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func currentField(t *testing.T, record FormJobRecord, fieldID string) FormJobFieldState {
	t.Helper()
	index := slices.IndexFunc(record.Fields, func(field FormJobFieldState) bool { return field.FieldID == fieldID })
	if index < 0 {
		t.Fatalf("field %q not found in %#v", fieldID, record.Fields)
	}
	return record.Fields[index]
}

func mappingSummaryHasBlocker(summary FormJobMappingSummary, fieldID, code string) bool {
	return slices.ContainsFunc(summary.Unresolved, func(blocker FormFieldMappingBlocker) bool {
		return blocker.FieldID == fieldID && blocker.Code == code
	})
}
