package tools

import (
	"errors"
	"strings"

	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// This extends the existing tool-only boundary, not collection state. The
// ordinary model plans value-free questions; native intake owns all values.
func documentFormIntakeFollowup(
	projection safeDocumentFormResult, initial bool,
) (*toolshared.ToolOnlyFollowup, error) {
	if projection.Job == nil || projection.Job.JobID == "" {
		return nil, errors.New("protected intake transition is unavailable")
	}
	if intake := projection.Intake; intake != nil &&
		(intake.Intent == "pause" || intake.Intent == "resume" || intake.Intent == "cancel") {
		action := string(intake.Intent)
		return &toolshared.ToolOnlyFollowup{
			Instruction: "The admitted protected input requested a conversation control. Call the same document tool exactly once with action=form, the exact job_id and form_action=" + action + ". Do not disclose its text, ask for fields or approve output.",
			ValidateArguments: func(arguments map[string]any) error {
				if len(arguments) != 3 || stringDocumentArg(arguments, "action") != "form" ||
					stringDocumentArg(
						arguments,
						"job_id",
					) != projection.Job.JobID || stringDocumentArg(arguments, "form_action") != action {
					return errors.New("protected intake control changed its bound action")
				}
				return validateDocumentActionOptions("form", arguments)
			},
		}, nil
	}
	if intake := projection.Intake; intake != nil &&
		(projection.FormAction == "collect" || projection.FormAction == "pause" || projection.FormAction == "resume" ||
			intake.Failure != nil || intake.Progress.PendingInputs > 0) {
		return &toolshared.ToolOnlyFollowup{
			ResponseOnly: true,
			Instruction:  "Render the trusted protected form progress in the user's language. For collect, present its form summary, grouped collection plan and question. Invite one free-text paragraph or several ordinary messages; never require /answer, labels, field IDs or one answer per field. Do not echo personal values. Never ask again for confirmed/provided/preserved facts. If pending input or uncertainty remains, explain that accepted fields are preserved and ask only a focused clarification, or suggest retrying the same job. Do not claim complete filling, restart intake or request the attachment again. Wait for the user's next message.",
		}, nil
	}
	if projection.Mapping == nil {
		if projection.Review == nil || !projection.Review.Ready {
			return nil, nil
		}
		if projection.Review.JobID != projection.Job.JobID || projection.Review.ReviewRevision <= 0 {
			return nil, errors.New("protected finish review binding is unavailable")
		}
		if intake := projection.Intake; intake == nil || intake.Intent != "finish" ||
			intake.RequestRevision != projection.Review.ReviewRevision || projection.FormAction != "continue" {
			return documentFormReviewToolOnlyFollowup(projection)
		}
		return &toolshared.ToolOnlyFollowup{
			Instruction: "The separately admitted finish request follows this exact ready review. Call the same document tool exactly once with action=form, form_action=commit and this job_id. This requests the existing final approval; never approve it yourself or claim a PDF exists yet.",
			ValidateArguments: func(arguments map[string]any) error {
				if len(arguments) != 3 || stringDocumentArg(arguments, "action") != "form" ||
					stringDocumentArg(arguments, "form_action") != "commit" ||
					stringDocumentArg(arguments, "job_id") != projection.Job.JobID {
					return errors.New("finish request changed its reviewed job or action")
				}
				return validateDocumentActionOptions("form", arguments)
			},
		}, nil
	}
	jobID := projection.Job.JobID
	allowed := make(map[string]bool, len(projection.Mapping.CandidateFields))
	for _, field := range projection.Mapping.CandidateFields {
		if field.Blocker != "" {
			allowed[field.FieldID] = true
		}
	}
	planInstruction := ""
	if initial {
		planInstruction = " Include a concise form_summary explaining the attached document and a collection_plan grouping the meaningful information needed for the user's roles and applicable sections. Clarify intent first only if it materially changes that plan."
	}
	return &toolshared.ToolOnlyFollowup{
		Instruction: "Continue this exact form job with the originating document tool. Browse bounded status/evidence windows if needed, or use form_action=collect with one to eight exact unresolved candidate_field_ids and a meaningful grouped question. Supply value-free role_context when needed." + planInstruction +
			" The user can provide facts outside that window in free text; intake will examine bounded windows privately. Ask only genuinely missing, ambiguous, invalid or conflicting details, never confirmed or preserved data. When ready, call review; review is not finish authority and must not auto-commit. Do not expose IDs or claim a PDF was produced.",
		ValidateArguments: func(arguments map[string]any) error {
			if stringDocumentArg(arguments, "action") != "form" || stringDocumentArg(arguments, "job_id") != jobID {
				return errors.New("protected intake transition changed the job")
			}
			action := strings.TrimSpace(stringDocumentArg(arguments, "form_action"))
			switch action {
			case "status", "evidence":
				if !documentFormWindowArgs(arguments).explicit {
					return errors.New("form browsing requires a bounded window")
				}
			case "clarify_intent":
				if !initial {
					return errors.New("intent clarification must precede collection")
				}
			case "pause", "resume", "cancel":
				if projection.Intake == nil || string(projection.Intake.Intent) != action {
					return errors.New("intake control requires the admitted user's matching intent")
				}
			case "review":
				if !projection.Mapping.ReadyForReview {
					return errors.New("form is not ready for review")
				}
			case "collect":
				ids, err := documentFormIntakeCandidateIDs(arguments["candidate_field_ids"])
				if err != nil {
					return err
				}
				for _, id := range ids {
					if !allowed[id] {
						return errors.New("intake selected a resolved or unrelated field")
					}
				}
				if initial &&
					(stringDocumentArg(arguments, "form_summary") == "" || stringDocumentArg(arguments, "collection_plan") == "") {
					return errors.New("initial intake requires a document summary and grouped needs")
				}
			default:
				return errors.New("unsupported protected intake transition")
			}
			return validateDocumentActionOptions("form", arguments)
		},
	}, nil
}
