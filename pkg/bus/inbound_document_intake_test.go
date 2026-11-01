package bus

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func documentIntakeTestGuard() InboundDocumentIntakeGuard {
	return InboundDocumentIntakeGuard{
		JobID: "form_job_" + strings.Repeat("a", 32), ModeID: "form_intake_" + strings.Repeat("b", 32),
	}
}

func TestInboundDocumentIntakeGuardSurvivesRestartAndCannotBeCleared(t *testing.T) {
	dir := t.TempDir()
	spool, err := NewInboundSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	const sentinel = "MINTCLAW_PRIVATE_INTAKE_REPLAY_0ce6"
	message, err := spool.Prepare(t.Context(), InboundMessage{
		Context: InboundContext{Channel: "telegram", MessageID: "message-1"}, Content: sentinel,
	})
	if err != nil {
		t.Fatal(err)
	}
	message.Context.DocumentIntake = documentIntakeTestGuard()
	if err = spool.PersistContext(message); err != nil {
		t.Fatal(err)
	}
	message.Context.DocumentIntake.Reference = "form_intake." + message.Context.DocumentIntake.JobID +
		".form_input_" + strings.Repeat("d", 32)
	if err = spool.PersistContext(message); err != nil {
		t.Fatal(err)
	}
	path := spool.processingPath(message.SpoolID)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, guard := range []InboundDocumentIntakeGuard{
		{},
		{
			JobID: message.Context.DocumentIntake.JobID, ModeID: message.Context.DocumentIntake.ModeID,
			Reference: message.Context.DocumentIntake.Reference, ConversationHistory: true,
		},
		{JobID: "incomplete"},
		documentIntakeTestGuard(),
		{
			JobID: message.Context.DocumentIntake.JobID, ModeID: message.Context.DocumentIntake.ModeID,
			Reference: "form_intake." + message.Context.DocumentIntake.JobID + ".form_input_" + strings.Repeat("e", 32),
		},
		{JobID: "form_job_" + strings.Repeat("c", 32), ModeID: message.Context.DocumentIntake.ModeID},
	} {
		changed := message
		changed.Context.DocumentIntake = guard
		if err = spool.PersistContext(changed); !errors.Is(err, errInboundDocumentIntakeGuard) ||
			strings.Contains(err.Error(), sentinel) {
			t.Fatal("durable guard was cleared, rebound, or echoed")
		}
		unchanged, readErr := os.ReadFile(path)
		if readErr != nil || string(unchanged) != string(original) {
			t.Fatal("rejected guard change mutated spool")
		}
	}
	if err = spool.Release(message.SpoolID, errors.New("protected intake retry")); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewInboundSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := reopened.Pending(t.Context(), 1)
	if err != nil || len(pending) != 1 || pending[0].Context.DocumentIntake != message.Context.DocumentIntake ||
		pending[0].Content != sentinel || pending[0].Context.MessageID != "message-1" {
		t.Fatal("protected replay lost exact binding or transport identity")
	}
	if err = reopened.Ack(message.SpoolID); err != nil {
		t.Fatal(err)
	}
}

func TestInboundDocumentIntakeGuardRefusesInconsistentFormat(t *testing.T) {
	for _, record := range []spooledInboundRecord{
		{Version: inboundSpoolVersion, Message: InboundMessage{Context: InboundContext{
			DocumentIntake: documentIntakeTestGuard(),
		}}},
		{Version: inboundDocumentSpoolVersion},
		{Version: inboundDocumentSpoolVersion, Message: InboundMessage{Context: InboundContext{
			DocumentIntake: InboundDocumentIntakeGuard{JobID: "invalid", ModeID: "invalid"},
		}}},
	} {
		spool, err := NewInboundSpool(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		path := spool.processingPath("invalid-format")
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		if read, readErr := spool.readRecord(path); !errors.Is(readErr, errInboundDocumentIntakeGuard) ||
			!read.Message.Context.DocumentIntake.IsZero() {
			t.Fatal("inconsistent guard exposed a replay record")
		}
		if err = spool.writeRecord(path, record); !errors.Is(err, errInboundDocumentIntakeGuard) {
			t.Fatal("inconsistent guard was durably written")
		}
	}
}

func TestInboundDocumentIntakeUnavailableSurvivesRestartWithoutRegainingAuthority(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprint(bound), func(t *testing.T) {
			dir := t.TempDir()
			spool, err := NewInboundSpool(dir)
			if err != nil {
				t.Fatal(err)
			}
			message, err := spool.Prepare(t.Context(), InboundMessage{
				Context: InboundContext{Channel: "telegram", MessageID: "unavailable-one"},
				Content: "Borrower Casey Mint. Also, what is seven squared?",
			})
			if err != nil {
				t.Fatal(err)
			}
			if bound {
				message.Context.DocumentIntake = documentIntakeTestGuard()
				message.Context.DocumentIntake.ConversationHistory = true
				if err = spool.PersistContext(message); err != nil {
					t.Fatal(err)
				}
			}
			message.Context.DocumentIntake.ConversationHistory = true
			message.Context.DocumentIntake.Unavailable = true
			if err = spool.PersistContext(message); err != nil {
				t.Fatal(err)
			}
			if err = spool.Release(message.SpoolID, errors.New("synthetic restart")); err != nil {
				t.Fatal(err)
			}
			spool, err = NewInboundSpool(dir)
			if err != nil {
				t.Fatal(err)
			}
			pending, err := spool.Pending(t.Context(), 1)
			if err != nil || len(pending) != 1 || pending[0].Content != message.Content ||
				pending[0].Context.DocumentIntake != message.Context.DocumentIntake {
				t.Fatal("restart lost ordinary text or the no-execution disposition")
			}
			for _, guard := range []InboundDocumentIntakeGuard{{}, documentIntakeTestGuard()} {
				changed := pending[0]
				changed.Context.DocumentIntake = guard
				if err = spool.PersistContext(changed); !errors.Is(err, errInboundDocumentIntakeGuard) {
					t.Fatal("unavailable replay became an unrestricted or newly bound operation")
				}
			}
			changed := pending[0]
			changed.Context.DocumentIntake.ConversationHistory = false
			if err = spool.PersistContext(changed); !errors.Is(err, errInboundDocumentIntakeGuard) {
				t.Fatal("replay changed prospective history policy")
			}
		})
	}
}
