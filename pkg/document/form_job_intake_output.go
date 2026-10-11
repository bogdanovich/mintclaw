package document

import (
	"slices"
	"time"
)

// Output instructions are private exact operator evidence, not public tool args
// or finish/approval authority. Empty/nil retains legacy complete semantics.
type FormIntakeOutputMode string

const (
	FormIntakeOutputComplete FormIntakeOutputMode = "complete"
	FormIntakeOutputDraft    FormIntakeOutputMode = "editable_draft"
)

type FormIntakeOutputInstruction struct {
	Mode    FormIntakeOutputMode `json:"-"`
	Literal FormIntakeLiteral    `json:"-"`
}

func (mode FormIntakeOutputMode) valid() bool {
	return mode == FormIntakeOutputComplete || mode == FormIntakeOutputDraft
}

// This executes inside ApplyFormIntakePass's authorized atomic update, before
// publishing ledger events/accounting. Scope never fabricates value events.
func applyFormIntakeOutputScope(
	record *formJobStoredRecord, instruction *FormIntakeOutputInstruction, selection formIntakeSelection, now time.Time,
) error {
	scope := record.Public.OutputScope
	deferred := slices.Clone(record.Public.DeferredFieldIDs)
	if instruction != nil {
		if !instruction.Mode.valid() {
			return ErrFormJobAnswerConflict
		}
		if instruction.Mode == FormIntakeOutputComplete {
			scope, deferred = "", nil
		} else {
			scope = FormOutputEditableDraft
		}
	}
	if len(selection.DeferredIDs) != 0 && scope != FormOutputEditableDraft {
		return ErrFormJobAnswerConflict
	}
	for _, id := range selection.DeferredIDs {
		if !slices.Contains(deferred, id) {
			deferred = append(deferred, id)
		}
	}
	deferred = slices.DeleteFunc(deferred, func(id string) bool { return slices.Contains(selection.IncludedIDs, id) })
	slices.Sort(deferred)
	desired := FormJobRecord{OutputMode: record.Public.OutputMode, OutputScope: scope, DeferredFieldIDs: deferred}
	if err := validateFormOutputScope(desired); err != nil {
		return err
	}
	if scope == record.Public.OutputScope && slices.Equal(deferred, record.Public.DeferredFieldIDs) {
		return nil
	}
	// The caller has already fenced pending value envelopes, staged claims,
	// source/schema/owner mismatches, capacity and the optional scope revision.
	replaceFormJobOutputScope(record, scope, deferred, now)
	return nil
}
