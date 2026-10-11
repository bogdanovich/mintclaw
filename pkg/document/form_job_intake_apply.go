package document

import (
	"context"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

// Proposals are private interpreter output. Values are derived from exact
// source literals, never from replacement text supplied by an interpreter.
type FormIntakeProposal struct {
	FieldID            string              `json:"field_id"`
	Literals           []FormIntakeLiteral `json:"-"`
	SupersedesEventID  string              `json:"supersedes_event_id,omitempty"`
	ReplaceExisting    bool                `json:"replace_existing,omitempty"`
	CorrectionEvidence []FormIntakeLiteral `json:"-"`
}

type FormIntakeApplyRequest struct {
	Reference        string
	PassID           string
	ExpectedRevision int64
	Owner            FormJobOwner
	Schema           FormFieldsFacts      `json:"-"`
	Proposals        []FormIntakeProposal `json:"-"`
	NonData          []FormIntakeLiteral  `json:"-"`
}

// FormIntakeAccountedLiteral is an ephemeral private view for the next pass.
// A FieldID identifies already accepted evidence, not a new assignment.
type FormIntakeAccountedLiteral struct {
	Literal FormIntakeLiteral `json:"-"`
	FieldID string            `json:"field_id,omitempty"`
}

type FormIntakeApplyResult struct {
	Job              FormJobRecord             `json:"job"`
	Progress         FormIntakeProgress        `json:"intake"`
	AppliedRevision  int64                     `json:"applied_revision"`
	AcceptedFieldIDs []string                  `json:"accepted_field_ids,omitempty"`
	AcknowledgedIDs  []string                  `json:"acknowledged_field_ids,omitempty"`
	Findings         []FormFieldMappingBlocker `json:"findings,omitempty"`
	// Consumed is this original pass's outcome; Job/Progress are current on replay.
	Consumed bool `json:"consumed"`
	Reused   bool `json:"reused"`
}

type formIntakeStoredOutcome struct {
	Digest          string                    `json:"digest"`
	AppliedRevision int64                     `json:"applied_revision"`
	AcceptedIDs     []string                  `json:"accepted_ids,omitempty"`
	AcknowledgedIDs []string                  `json:"acknowledged_ids,omitempty"`
	Findings        []FormFieldMappingBlocker `json:"findings,omitempty"`
	Consumed        bool                      `json:"consumed"`
}

// ApplyFormIntakePass publishes accepted ledger events, raw-input accounting
// and exact replay outcome together in the existing atomic snapshot write.
func (store *FormJobStore) ApplyFormIntakePass(
	ctx context.Context, request FormIntakeApplyRequest,
) (FormIntakeApplyResult, error) {
	jobID, inputID, err := parseFormIntakeReceipt(request.Reference)
	if err != nil || request.ExpectedRevision <= 0 || !validFormFieldsFacts(request.Schema) ||
		!validFormIntakeJobID(request.PassID) || !strings.HasPrefix(request.PassID, "form_pass_") {
		return FormIntakeApplyResult{}, ErrFormJobAnswerConflict
	}
	digest, err := store.formIntakeProposalDigest(request)
	if err != nil {
		return FormIntakeApplyResult{}, err
	}
	var result FormIntakeApplyResult
	err = store.update(ctx, func(snapshot *formJobStoreDocument, now time.Time) (bool, error) {
		record, key, authErr := store.authorizedRecord(snapshot, jobID, request.Owner)
		if authErr != nil {
			return false, authErr
		}
		defer clear(key)
		if matchErr := formJobMatchesSchema(record.Public, request.Schema); matchErr != nil {
			return false, matchErr
		}
		source, sourceErr := readFormIntakeSource(record, key)
		if sourceErr != nil {
			return false, sourceErr
		}
		index := formIntakeInputIndex(source.Intake, inputID)
		if index < 0 {
			return false, ErrFormJobNotFound
		}
		input := &source.Intake.Inputs[index]
		passIndex := slices.IndexFunc(input.Reservations, func(pass formIntakeStoredPass) bool {
			return pass.ID == request.PassID
		})
		if passIndex < 0 || input.Reservations[passIndex].Revision != request.ExpectedRevision {
			return false, ErrFormJobAnswerConflict
		}
		pass := &input.Reservations[passIndex]
		if pass.Outcome != nil {
			if !constantTimeStringEqual(pass.Outcome.Digest, digest) {
				return false, ErrFormJobAnswerConflict
			}
			result = formIntakeApplyResult(record.Public, source.Intake, pass.Outcome, true)
			return false, nil
		}
		if source.Intake.State != FormIntakeActive || input.State != formIntakeInputCommitted ||
			!formIntakeMutable(record.Public) || record.Public.Revision != request.ExpectedRevision ||
			len(record.PendingEvents) != 0 {
			return false, ErrFormJobConflict
		}
		if stageErr := requireNoStagedFormIntake(record, key); stageErr != nil {
			return false, stageErr
		}
		selection, selectErr := selectFormIntakeProposals(record, key, request, *input, pass.CandidateIDs)
		if selectErr != nil {
			return false, selectErr
		}
		if len(record.Events)+len(selection.Payloads) > store.maxEventsPerJob ||
			len(selection.Accounted) > MaxFormIntakeAccountedSpans {
			return false, ErrFormJobCapacityExceeded
		}
		if record.Public.Revision > math.MaxInt64-int64(len(selection.Payloads))-1 {
			return false, ErrFormJobConflict
		}
		id := "form_batch_" + keyedDigest(store.profileKey, "intake_apply", pass.ID)[:32]
		if appendErr := store.appendCompositeToRecord(
			&record, key, selection.Payloads, id, digest, request.ExpectedRevision, now,
		); appendErr != nil {
			return false, appendErr
		}
		// Accounting-only writes also detach Public before invalidation: pruning
		// unrelated jobs must not publish a prefix after a sealing/write failure.
		record.Public = cloneFormJobRecord(record.Public)
		record.Public.Revision++
		invalidateFormIntakeReview(&record.Public)
		input.OriginalBytes, input.Accounted = len(input.Text), selection.Accounted
		consumed := input.Intent != interactions.ProtectedAnswerValue ||
			formIntakeTextAccounted(input.Text, input.Accounted)
		if consumed {
			input.State, input.Text = formIntakeInputConsumed, ""
		}
		pass.Outcome = &formIntakeStoredOutcome{
			Digest: digest, AppliedRevision: record.Public.Revision, AcceptedIDs: selection.AcceptedIDs,
			AcknowledgedIDs: selection.AcknowledgedIDs, Findings: selection.Findings, Consumed: consumed,
		}
		if saveErr := store.writeFormIntakeSource(&record, key, source, now); saveErr != nil {
			return false, saveErr
		}
		snapshot.Records[record.Public.JobID] = record
		result = formIntakeApplyResult(record.Public, source.Intake, pass.Outcome, false)
		return true, nil
	})
	if err != nil {
		return FormIntakeApplyResult{}, err
	}
	return result, nil
}

// Public literal fields deliberately have json:"-". Use an explicit private
// digest representation so changed text/occurrence cannot replay as identical.
func (store *FormJobStore) formIntakeProposalDigest(request FormIntakeApplyRequest) (string, error) {
	type literal struct {
		Text       string
		Occurrence int
	}
	type proposal struct {
		FieldID           string
		Literals          []literal
		SupersedesEventID string
		ReplaceExisting   bool
		Correction        []literal
	}
	count, bytes, valid := 0, 0, true
	convert := func(refs []FormIntakeLiteral) []literal {
		result := make([]literal, len(refs))
		for i, ref := range refs {
			valid = valid && utf8.ValidString(ref.Text) && ref.Text != "" && strings.TrimSpace(ref.Text) == ref.Text &&
				len(ref.Text) <= MaxFormIntakeInputBytes && ref.Occurrence > 0 &&
				ref.Occurrence <= MaxFormIntakeAccountedSpans
			count++
			bytes += len(ref.Text)
			result[i] = literal(ref)
		}
		return result
	}
	if len(request.Proposals) > MaxFormIntakeCandidates || len(request.NonData) > MaxFormIntakeAccountedSpans {
		return "", ErrFormJobCapacityExceeded
	}
	proposals := make([]proposal, len(request.Proposals))
	for i, item := range request.Proposals {
		if !opaqueFieldID.MatchString(item.FieldID) || len(item.Literals) == 0 ||
			len(item.Literals) > maxFormJobChoices ||
			len(item.CorrectionEvidence) > MaxFormIntakeCandidates ||
			len(item.SupersedesEventID) > maxFormJobEventIDLength ||
			(item.SupersedesEventID != "" && !safeFormJobCodePattern.MatchString(item.SupersedesEventID)) {
			return "", ErrFormJobAnswerConflict
		}
		proposals[i] = proposal{
			item.FieldID, convert(item.Literals), item.SupersedesEventID,
			item.ReplaceExisting, convert(item.CorrectionEvidence),
		}
	}
	nonData := convert(request.NonData)
	if !valid {
		return "", ErrFormJobAnswerConflict
	}
	if count > MaxFormIntakeAccountedSpans || bytes > MaxFormIntakeInputBytes {
		return "", ErrFormJobCapacityExceeded
	}
	return formJobJSONKeyedDigest(store.profileKey, struct {
		Reference string
		PassID    string
		Revision  int64
		Schema    FormFieldsFacts
		Proposals []proposal
		NonData   []literal
	}{request.Reference, request.PassID, request.ExpectedRevision, request.Schema, proposals, nonData})
}

func formIntakeApplyResult(
	record FormJobRecord, mode *formIntakeStoredMode, outcome *formIntakeStoredOutcome, reused bool,
) FormIntakeApplyResult {
	return FormIntakeApplyResult{
		Job: cloneFormJobRecord(
			record,
		),
		Progress:         formIntakeProgress(record, mode),
		AppliedRevision:  outcome.AppliedRevision,
		AcceptedFieldIDs: slices.Clone(outcome.AcceptedIDs),
		AcknowledgedIDs:  slices.Clone(outcome.AcknowledgedIDs),
		Findings:         slices.Clone(outcome.Findings),
		Consumed:         outcome.Consumed,
		Reused:           reused,
	}
}
