package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type presentationFeedbackChannel struct {
	fakeMediaChannel
	feedbackMu sync.Mutex
	active     map[string]bool
	created    int
}

func (ch *presentationFeedbackChannel) DeliverText(
	ctx context.Context, pending []bus.OutboundMessage,
) channels.DeliveryResult[bus.OutboundMessage] {
	ch.fakeMediaChannel.DeliverText(ctx, pending)
	ch.feedbackMu.Lock()
	defer ch.feedbackMu.Unlock()
	ids := make([]string, 0, len(pending))
	for _, msg := range pending {
		ch.created++
		id := fmt.Sprintf("platform-%d", ch.created)
		ids = append(ids, id)
		if msg.Metadata.IsToolFeedback() {
			if ch.active == nil {
				ch.active = make(map[string]bool)
			}
			ch.active[id] = true
		}
	}
	return channels.SuccessfulDelivery[bus.OutboundMessage](ids)
}

func (*presentationFeedbackChannel) EditMessage(context.Context, string, string, string) error {
	return nil
}

func (ch *presentationFeedbackChannel) DeleteMessage(_ context.Context, _, id string) error {
	ch.feedbackMu.Lock()
	defer ch.feedbackMu.Unlock()
	delete(ch.active, id)
	return nil
}

func TestObjectivePresentationPreservesFactsAndSpecificBlocker(t *testing.T) {
	const blocker = "Craigslist requires sign-in; nothing was changed."
	const answer = "Facebook has one active listing: Yakima, $85, ID 42, https://example.com/42. " + blocker
	outcome := &taskresult.Outcome{
		Status: taskresult.OutcomePartial, Explanation: blocker,
		CompletedItems: []taskresult.Item{{
			Kind: taskresult.ObjectiveKindResult,
			Output: &taskresult.ObjectiveOutput{Kind: "records", Records: []map[string]string{{
				"site": "Facebook", "title": "Yakima", "price": "$85", "status": "active",
				"id": "42", "url": "https://example.com/42",
			}}},
		}},
	}
	if got := objectivePresentation(answer, outcome); got != answer {
		t.Fatalf("complete natural answer rejected: %q", got)
	}
	for name, incomplete := range map[string]string{
		"missing title":   strings.ReplaceAll(answer, "Yakima", "a kayak carrier"),
		"missing price":   strings.ReplaceAll(answer, "$85", "unknown price"),
		"missing link":    strings.ReplaceAll(answer, "https://example.com/42", "the listing"),
		"missing blocker": strings.TrimSuffix(answer, blocker),
		"altered price":   strings.ReplaceAll(answer, "$85", "$850"),
		"decimal price":   strings.ReplaceAll(answer, "$85", "$85.50"),
		"altered ID":      strings.ReplaceAll(answer, "42", "142"),
		"altered link":    strings.ReplaceAll(answer, "https://example.com/42", "https://example.com/420"),
		"link path":       strings.ReplaceAll(answer, "https://example.com/42", "https://example.com/42/other"),
		"link query":      strings.ReplaceAll(answer, "https://example.com/42", "https://example.com/42?other=true"),
		"link fragment":   strings.ReplaceAll(answer, "https://example.com/42", "https://example.com/42#other"),
		"JSON fragment":   answer + ` Supporting data: {"id":42}`,
		"envelope":        answer + objectiveOutcomeStart,
	} {
		t.Run(name, func(t *testing.T) {
			if got := objectivePresentation(incomplete, outcome); got != "" {
				t.Fatalf("incomplete or internal answer admitted: %q", got)
			}
		})
	}
}

func TestPresentationURLBoundaries(t *testing.T) {
	const link = "https://example.com/"
	for _, text := range []string{link, "See " + link + ".", "[Example](" + link + ")", "<" + link + ">"} {
		if !containsPresentationFact(text, link) {
			t.Fatalf("retained URL rejected: %q", text)
		}
	}
	for _, suffix := range []string{"other", "?other=true", "#other", ".other", ";other", "/other"} {
		if containsPresentationFact(link+suffix, link) {
			t.Fatalf("different URL admitted: %q", link+suffix)
		}
	}
}

func TestObjectivePresentationUsesExistingProducerAnswerAfterValidation(t *testing.T) {
	const facts = "Verified item 42 at https://example.com/42."
	const blocker = "The second account requires sign-in."
	for _, status := range []taskresult.OutcomeStatus{
		taskresult.OutcomeSucceeded, taskresult.OutcomePartial, taskresult.OutcomeBlocked,
	} {
		t.Run(string(status), func(t *testing.T) {
			checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{
				{Item: "first internal checklist instruction", Kind: "result"},
				{Item: "second internal checklist instruction", Kind: "result"},
			})
			report := reportedObjectiveOutcome{Status: string(status), Explanation: blocker}
			switch status {
			case taskresult.OutcomeSucceeded:
				report.CompletedItems = []reportedObjectiveItem{
					{ObjectiveID: "objective_1", Output: &taskresult.ObjectiveOutput{Kind: "text", Text: facts}},
					{
						ObjectiveID: "objective_2",
						Output:      &taskresult.ObjectiveOutput{Kind: "text", Text: "Second account is ready."},
					},
				}
				report.Result = facts + " Second account is ready."
			case taskresult.OutcomePartial:
				report.CompletedItems = []reportedObjectiveItem{{
					ObjectiveID: "objective_1", Output: &taskresult.ObjectiveOutput{Kind: "text", Text: facts},
				}}
				report.MissingItems = []string{"objective_2"}
				report.Result = facts + " " + blocker
			case taskresult.OutcomeBlocked:
				report.MissingItems = []string{"objective_1", "objective_2"}
				report.Result = blocker
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			clean, outcome := extractObjectiveOutcome(objectiveOutcomeStart+string(encoded)+objectiveOutcomeEnd,
				nil, true, checklist)
			got := objectiveOutcomeUserContent(clean, outcome)
			if outcome.Status != status || outcome.UserSummary != report.Result || got != report.Result {
				t.Fatalf("presentation changed verified outcome: %q, %#v", got, outcome)
			}
			if strings.Contains(got, "checklist instruction") || strings.Contains(got, "Completed:") {
				t.Fatalf("internal contract leaked: %q", got)
			}
		})
	}
}

func TestObjectivePresentationCannotHideRuntimeDowngrade(t *testing.T) {
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{{
		Item: "publish listing", Kind: taskresult.ObjectiveKindExternalAction,
	}})
	reported := reportedObjectiveOutcome{
		Status: "succeeded", Result: "Published successfully.",
		CompletedItems: []reportedObjectiveItem{{ObjectiveID: "objective_1", ReceiptIDs: []string{"invented"}}},
	}
	outcome := validateObjectiveOutcome(reported, nil, nil, checklist)
	got := objectiveOutcomeUserContent(reported.Result, outcome)
	if outcome.Status != taskresult.OutcomeBlocked || outcome.UserSummary != "" ||
		strings.Contains(got, reported.Result) || !strings.Contains(got, "missing verified runtime receipt") {
		t.Fatalf("unverified success escaped presentation: %q, %#v", got, outcome)
	}
}

func TestMixedSucceededOutcomeNeverRecyclesRejectedProducerAnswer(t *testing.T) {
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{
		{Item: "publish using internal workflow", Kind: taskresult.ObjectiveKindExternalAction},
		{Item: "report verified price", Kind: taskresult.ObjectiveKindResult},
	})
	audits := []toolshared.WriteAuditEntry{{
		Kind: "external_action", Tool: "browser_act", Success: true,
		Summary:  "browser external action completed",
		Metadata: map[string]string{"invocation_id": "verified-action", "effect": "external_commit"},
	}}
	for _, rejected := range []string{`Published. Supporting data: {"price":"$850"}`, "Published at $850.", "Published."} {
		t.Run(rejected, func(t *testing.T) {
			report := reportedObjectiveOutcome{
				Status: "succeeded", Result: rejected,
				CompletedItems: []reportedObjectiveItem{
					{ObjectiveID: "objective_1", ReceiptIDs: []string{"verified-action"}},
					{
						ObjectiveID: "objective_2",
						Output: &taskresult.ObjectiveOutput{
							Kind:    "records",
							Records: []map[string]string{{"price": "$85"}},
						},
					},
				},
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			clean, outcome := extractObjectiveOutcome(objectiveOutcomeStart+string(encoded)+objectiveOutcomeEnd,
				audits, true, checklist)
			const expected = "browser external action completed\n\n- price: $85"
			if outcome.Status != taskresult.OutcomeSucceeded || outcome.UserSummary != "" || clean != expected ||
				len(outcome.CompletedItems) != 2 || len(outcome.CompletedItems[0].Receipts) != 1 ||
				outcome.CompletedItems[0].Receipts[0].ID != "verified-action" {
				t.Fatalf("mixed fallback reused rejected prose or lost evidence: %q, %#v", clean, outcome)
			}
			// Canonical reload/finalization must not treat retained rejected prose
			// as admitted merely because the task itself succeeded.
			outcome.UserSummary = rejected
			if got := objectiveOutcomeUserContent(rejected, outcome); got != expected {
				t.Fatalf("rejected answer escaped final projection: %q", got)
			}
		})
	}
}

func TestObjectivePresentationFallbackRetainsOutputsWithoutInstructionHeadings(t *testing.T) {
	const blocker = "The remaining account requires sign-in."
	outcome := &taskresult.Outcome{
		Status: taskresult.OutcomePartial, Explanation: blocker,
		UserSummary: "I found it. " + blocker, // Omits all requested facts.
		CompletedItems: []taskresult.Item{
			{
				Item: "Publish and then verify the item using these instructions", Kind: "external_action",
				Receipts: []taskresult.Receipt{{Summary: "The item was published and verified."}},
			},
			{
				Item: "Return the full original API envelope", Kind: "result",
				Output: &taskresult.ObjectiveOutput{Kind: "text", Text: `{"id":42,"url":"https://example.com/42"}`},
			},
			{
				Item: "Return proof with all requested links", Kind: "result",
				Output: &taskresult.ObjectiveOutput{Kind: "artifact", ArtifactRefs: []string{"media://proof"}},
			},
		},
		MissingItems: []string{"Inspect the second account using the internal checklist"},
	}
	got := objectiveOutcomeUserContent("Unsupported success.", outcome)
	for _, required := range []string{"The item was published and verified.", "id: 42", "https://example.com/42", "media://proof", blocker} {
		if !strings.Contains(got, required) {
			t.Fatalf("fallback lost %q: %q", required, got)
		}
	}
	for _, forbidden := range []string{"instructions", "API envelope", "internal checklist", "Unsupported success", `{"id":42}`} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("fallback exposed %q: %q", forbidden, got)
		}
	}
}

func TestObjectivePresentationCannotMaskValidationFailureWithProducerBlocker(t *testing.T) {
	const blocker = "The second account requires sign-in."
	checklist := normalizeObjectiveChecklist([]toolshared.ObjectiveSpec{
		{Item: "Publish using the complete internal workflow", Kind: taskresult.ObjectiveKindExternalAction},
		{Item: "Inspect the second account using the internal checklist", Kind: taskresult.ObjectiveKindResult},
	})
	report := reportedObjectiveOutcome{
		Status: "partial", Result: "Published. " + blocker, Explanation: blocker,
		CompletedItems: []reportedObjectiveItem{{ObjectiveID: "objective_1", ReceiptIDs: []string{"invented"}}},
		MissingItems:   []string{"objective_2"},
	}
	outcome := validateObjectiveOutcome(report, nil, nil, checklist)
	got := objectiveOutcomeUserContent(report.Result, outcome)
	if outcome.Status != taskresult.OutcomeBlocked || outcome.UserSummary != "" ||
		!strings.Contains(got, blocker) || !strings.Contains(got, "missing verified runtime receipt") ||
		strings.Contains(got, "Published.") || strings.Contains(got, "internal workflow") ||
		strings.Contains(got, "internal checklist") {
		t.Fatalf("producer blocker hid runtime verification failure: %q, %#v", got, outcome)
	}
}

func TestStructuredObjectiveTextFallbackPreservesNestedValues(t *testing.T) {
	output := &taskresult.ObjectiveOutput{
		Kind: "text",
		Text: `{"items":[{"id":9007199254740993,"url":"https://example.com/42"}],"error":null,"empty":[],"ready":true}`,
	}
	got := renderObjectiveOutput(output)
	for _, required := range []string{"items[0].id: 9007199254740993", "items[0].url: https://example.com/42", "error: null", "empty: (empty)", "ready: true"} {
		if !strings.Contains(got, required) {
			t.Fatalf("structured fallback lost %q: %q", required, got)
		}
	}
	for _, empty := range []string{`{}`, `[]`} {
		if got := renderObjectiveOutput(&taskresult.ObjectiveOutput{Kind: "text", Text: empty}); got != "- (empty)" {
			t.Fatalf("empty container disappeared: %q", got)
		}
	}
}

func TestObjectivePresentationKeepsDeclaredExactJSONAuthoritative(t *testing.T) {
	for _, exact := range []string{`{"ok":true}`, `[1,2]`, `"done"`, `true`, `42`, `null`} {
		outcome := &taskresult.Outcome{
			Status: taskresult.OutcomeSucceeded, UserSummary: "An ordinary answer must not replace exact output.",
			CompletedItems: []taskresult.Item{{
				Kind: "result", ExactJSON: true,
				Output: &taskresult.ObjectiveOutput{Kind: "text", Text: exact},
			}},
		}
		if got := objectiveOutcomeUserContent("wrong", outcome); got != exact {
			t.Fatalf("exact output changed: %q, want %q", got, exact)
		}
	}
}
