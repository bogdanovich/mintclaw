package document

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// FormOutputScope describes transaction intent, never a field value or a
// weakened required flag. The empty value preserves legacy complete filling.
type FormOutputScope string

const FormOutputEditableDraft FormOutputScope = "editable_draft"

var ErrFormEditableOutputUnavailable = errors.New("document form editable output is unsupported")

type FormOutputScopeRequest struct {
	JobID            string
	ExpectedRevision int64
	Owner            FormJobOwner
	Schema           FormFieldsFacts
	Scope            FormOutputScope
	DeferredFieldIDs []string
}

// SetFormOutputScope replaces the bounded exact omission set atomically. The
// private interpreter still works in eight-field windows; this native boundary
// accepts the already validated whole scope, bounded by the existing schema.
func (store *FormJobStore) SetFormOutputScope(
	ctx context.Context, request FormOutputScopeRequest,
) (FormJobRecord, error) {
	if request.ExpectedRevision <= 0 || !validFormFieldsFacts(request.Schema) {
		return FormJobRecord{}, ErrFormJobStale
	}
	desired := FormJobRecord{
		OutputMode: FormOutputEditableAcroForm, OutputScope: request.Scope,
		DeferredFieldIDs: append([]string(nil), request.DeferredFieldIDs...),
	}
	slices.Sort(desired.DeferredFieldIDs)
	if err := validateFormOutputScope(desired); err != nil {
		return FormJobRecord{}, err
	}
	for _, id := range desired.DeferredFieldIDs {
		index := slices.IndexFunc(request.Schema.Fields, func(field FormField) bool { return field.ID == id })
		if index < 0 || request.Schema.Fields[index].ReadOnly {
			return FormJobRecord{}, ErrFormJobStale
		}
	}
	var result FormJobRecord
	err := store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, key, err := store.authorizedRecord(document, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		defer clear(key)
		if !formIntakeMutable(record.Public) {
			return false, ErrFormJobConflict
		}
		if err = formJobMatchesSchema(record.Public, request.Schema); err != nil {
			return false, err
		}
		if request.Scope == FormOutputEditableDraft && record.Public.OutputMode != FormOutputEditableAcroForm {
			return false, ErrFormEditableOutputUnavailable
		}
		if record.Public.OutputScope == desired.OutputScope &&
			slices.Equal(record.Public.DeferredFieldIDs, desired.DeferredFieldIDs) {
			result = cloneFormJobRecord(record.Public)
			return false, nil
		}
		if record.Public.Revision != request.ExpectedRevision {
			return false, ErrFormJobConflict
		}
		if record.Public.Revision == math.MaxInt64 {
			return false, ErrFormJobConflict
		}
		if err = requireNoStagedFormIntake(record, key); err != nil {
			return false, err
		}
		record.Public.OutputScope = desired.OutputScope
		record.Public.DeferredFieldIDs = desired.DeferredFieldIDs
		record.Public.Revision++
		record.Public.UpdatedAt = now.UnixMilli()
		invalidateFormIntakeReview(&record.Public)
		document.Records[record.Public.JobID] = record
		result = cloneFormJobRecord(record.Public)
		return true, nil
	})
	if err != nil {
		return FormJobRecord{}, err
	}
	return result, nil
}

func validFormJobOutputMode(mode FormOutputMode) bool {
	return mode == "" || mode == FormOutputEditableAcroForm || mode == FormOutputFlattenedPrint
}

func validateFormOutputScope(record FormJobRecord) error {
	if !validFormJobOutputMode(record.OutputMode) || len(record.DeferredFieldIDs) > DefaultMaxFormFields {
		return errors.New("document form output scope is invalid")
	}
	switch record.OutputScope {
	case "":
		if len(record.DeferredFieldIDs) != 0 {
			return errors.New("document complete output cannot defer fields")
		}
	case FormOutputEditableDraft:
		if record.OutputMode != FormOutputEditableAcroForm {
			return ErrFormEditableOutputUnavailable
		}
	default:
		return errors.New("document form output scope is invalid")
	}
	for index, id := range record.DeferredFieldIDs {
		if id == "" || strings.TrimSpace(id) != id || len(id) > maxFormJobFieldIDLength ||
			!utf8.ValidString(id) || strings.ContainsRune(id, '\x00') ||
			(index > 0 && record.DeferredFieldIDs[index-1] >= id) {
			return errors.New("document form deferred field identity is invalid")
		}
	}
	return nil
}

func formFieldDeferred(record FormJobRecord, field FormField, hasCurrent bool) bool {
	return record.OutputScope == FormOutputEditableDraft && !hasCurrent && !field.HasValue &&
		slices.Contains(record.DeferredFieldIDs, field.ID)
}

// FormOutputPolicy binds complete and draft transactions to different final
// approval policies. Legacy complete jobs retain their existing policy.
func (record FormJobRecord) FormOutputPolicy() string {
	if record.OutputScope == FormOutputEditableDraft {
		return FormCommitDraftOutputPolicyRevision
	}
	return FormCommitOutputPolicyRevision
}
