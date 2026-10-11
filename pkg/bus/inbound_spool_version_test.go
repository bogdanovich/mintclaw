package bus

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestInboundSpoolRefusesUnsupportedVersionsWithoutMutation(t *testing.T) {
	for _, version := range []int{-1, 0, inboundDocumentSpoolVersion + 1, 999} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			spool, err := NewInboundSpool(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			const sentinel = "MINTCLAW_PRIVATE_UNSUPPORTED_SPOOL_46b7"
			record := spooledInboundRecord{
				Version: version, ID: "unsupported",
				Message: InboundMessage{Content: sentinel},
			}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			path := spool.processingPath(record.ID)
			if err = os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if found, readErr := spool.readRecord(path); !errors.Is(readErr, errInboundSpoolVersionUnsupported) ||
				found.Message.Content != "" {
				t.Fatal("unsupported format exposed a replay record")
			}
			if pending, pendingErr := spool.Pending(t.Context(), 0); len(pending) != 0 ||
				!errors.Is(pendingErr, errInboundSpoolVersionUnsupported) ||
				strings.Contains(pendingErr.Error(), sentinel) {
				t.Fatal("unsupported format was replayed or echoed")
			}
			for _, operation := range []func() error{
				func() error { return spool.PersistContext(InboundMessage{SpoolID: record.ID}) },
				func() error { return spool.Release(record.ID, errors.New("retry")) },
				func() error { return spool.Fail(record.ID, errors.New("failed")) },
				func() error { return spool.writeRecord(path, record) },
			} {
				if operationErr := operation(); !errors.Is(operationErr, errInboundSpoolVersionUnsupported) {
					t.Fatal("unsupported record was rewritten")
				}
				unchanged, readErr := os.ReadFile(path)
				if readErr != nil || string(unchanged) != string(data) {
					t.Fatal("unsupported record was mutated or removed")
				}
			}
		})
	}
}

func TestInboundSpoolCurrentVersionStillReplaysAfterRestart(t *testing.T) {
	dir := t.TempDir()
	spool, err := NewInboundSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := spool.Prepare(t.Context(), InboundMessage{
		Context: InboundContext{Channel: "telegram", ChatID: "chat", SenderID: "sender"}, Content: "original",
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewInboundSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := restarted.Pending(t.Context(), 0)
	if err != nil || len(pending) != 1 || pending[0].SpoolID != prepared.SpoolID ||
		pending[0].Content != prepared.Content {
		t.Fatal("current version lost replay identity or content")
	}
	if err = restarted.Ack(prepared.SpoolID); err != nil {
		t.Fatal(err)
	}
	pending, err = restarted.Pending(t.Context(), 0)
	if err != nil || len(pending) != 0 {
		t.Fatal("current version could not be acknowledged")
	}
}
