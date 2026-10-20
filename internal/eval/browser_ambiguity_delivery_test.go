package eval

import (
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

func ambiguityControlWithNativeClosure(t *testing.T) *browserAmbiguityControl {
	t.Helper()
	c := completeBrowserAmbiguityControl(t)
	final := &c.trial.Events[len(c.trial.Events)-1]
	final.InteractionKind, final.Controls = "approval", bus.OutboundInteractionControlsRemove
	final.InteractionID, final.InteractionShortID = c.domain.approval.record.ID, c.domain.approval.record.ShortID
	intent, err := newBrowserFinalIntent(c.fixture.workspace, c.trial, *final)
	if err != nil {
		t.Fatal(err)
	}
	metadata := &intent.Message.Metadata
	metadata.InteractionKind, metadata.InteractionControls = final.InteractionKind, final.Controls
	metadata.InteractionID, metadata.InteractionShortID = final.InteractionID, final.InteractionShortID
	writeAdmissionJSON(t, filepath.Join(c.fixture.workspace, "state/outbox", intent.ID+".json"), intent)
	return c
}

func TestBrowserAmbiguityNativeApprovalClosureIsNotAnotherPrompt(t *testing.T) {
	c := ambiguityControlWithNativeClosure(t)
	_, trial := finalizedAmbiguityControl(t, c)
	if !validBrowserAmbiguityEvidence(trial, c.item.Case, c.fixture.workspace) {
		t.Fatal("native final control removal was mistaken for a prompt", trial.Browser.Ambiguity.Native.Code)
	}
	proof := trial.Browser.Ambiguity.Native
	if proof.PermissionPromptDeliveries != 1 || proof.ApprovalPromptDeliveries != 1 || proof.FinalDeliveries != 1 {
		t.Fatal("actual native prompt and final deliveries were not counted separately")
	}
	native, err := observeBrowserNative(t.Context(), c.fixture.workspace, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{
		"foreign-interaction", "foreign-short", "question-closure", "not-final", "prompt",
		"event-controls", "event-identity", "missing-final",
	} {
		t.Run(mode, func(t *testing.T) {
			copy := native
			copy.intents = cloneBrowserFormControlValue(t, native.intents)
			changed := cloneBrowserFormControlValue(t, trial)
			final := &changed.Events[len(changed.Events)-1]
			for index := range copy.intents {
				intent := &copy.intents[index]
				if intent.Message == nil || !intent.Message.Metadata.RemovesInteractionControls() {
					continue
				}
				metadata := &intent.Message.Metadata
				switch mode {
				case "foreign-interaction":
					metadata.InteractionID, final.InteractionID = "foreign", "foreign"
				case "foreign-short":
					metadata.InteractionShortID, final.InteractionShortID = "foreign", "foreign"
				case "question-closure":
					metadata.InteractionKind, final.InteractionKind = "question", "question"
					metadata.InteractionID = changed.Browser.Ambiguity.Clarification.InteractionID
					final.InteractionID = metadata.InteractionID
					metadata.InteractionShortID = changed.Browser.Ambiguity.Clarification.ShortID
					final.InteractionShortID = metadata.InteractionShortID
				case "not-final":
					metadata.MessageKind, metadata.OutboundKind = "", ""
				case "prompt":
					metadata.InteractionControls = bus.OutboundInteractionControlsPrompt
				case "event-controls":
					final.Controls = bus.OutboundInteractionControlsPrompt
				case "event-identity":
					final.InteractionShortID = "foreign"
				case "missing-final":
					copy.intents = append(copy.intents[:index], copy.intents[index+1:]...)
				}
				break
			}
			observed := inspectBrowserTask(changed, c.domain.target, c.domain.fixture, copy, true,
				browserTaskEffects{ambiguity: changed.Browser.Ambiguity})
			if observed.Code == "native_post_stop_observed" {
				t.Fatal("invalid native completion qualified", mode)
			}
		})
	}
}
