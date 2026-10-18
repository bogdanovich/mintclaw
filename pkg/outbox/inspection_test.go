package outbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

func TestInspectIntentUsesBoundedNativeReadContractAndValueFreeErrors(t *testing.T) {
	const private = "PRIVATE_OUTBOX_INSPECTION_CONTROL_91af"
	intent, err := NewMessageIntent("workspace-eval", Identity{
		SourceID: "interaction-eval-prompt", Channel: "mintclaw", ChatID: "mintclaw:session-eval",
		SessionKey: "session-key-eval",
	}, bus.OutboundMessage{AgentID: "eval-agent", Content: private}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	intent.Status = StatusDelivered
	intent.PlatformMessageIDs = []string{"wire-message-id"}
	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	got, err := InspectIntent(data, intent.ID)
	if err != nil || got.ID != intent.ID || got.Status != StatusDelivered ||
		len(got.PlatformMessageIDs) != 1 || got.Message.Content != private {
		t.Fatal("bounded native intent observation not preserved", err)
	}
	got.PlatformMessageIDs[0] = "changed"
	again, err := InspectIntent(data, intent.ID)
	if err != nil || again.PlatformMessageIDs[0] != "wire-message-id" {
		t.Fatal("separate native observations alias each other")
	}
	for _, malformed := range [][]byte{
		nil, []byte("private-malformed-" + private), append(bytes.Clone(data), []byte(" trailing")...),
		bytes.Repeat([]byte("x"), MaxInspectionBytes+1),
	} {
		if _, err = InspectIntent(malformed, intent.ID); !errors.Is(err, ErrInvalidInspection) ||
			strings.Contains(err.Error(), private) {
			t.Fatal("invalid/private outbox diagnostic admitted or exposed")
		}
	}
	for _, id := range []string{"../escape", "out_" + strings.Repeat("0", 32)} {
		if _, err = InspectIntent(data, id); !errors.Is(err, ErrInvalidInspection) {
			t.Fatal("filename/identity mismatch admitted")
		}
	}
	for _, mutate := range []func(*Intent){
		func(record *Intent) { record.Version = retentionRecordVersion + 1 },
		func(record *Intent) { record.Status = "invented" },
		func(record *Intent) { record.OwnerWorkspace = "" },
		func(record *Intent) { record.Identity.ChatID = "another-chat" },
		func(record *Intent) { record.Message = nil },
	} {
		candidate := intent
		mutate(&candidate)
		encoded, _ := json.Marshal(candidate)
		if _, err = InspectIntent(encoded, intent.ID); !errors.Is(err, ErrInvalidInspection) {
			t.Fatal("native intent invariant not checked")
		}
	}
}
