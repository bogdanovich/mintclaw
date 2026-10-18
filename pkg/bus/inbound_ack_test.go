package bus

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

func TestInboundAckReceiptBindsValidatedSourceAndInstance(t *testing.T) {
	root := t.TempDir()
	spool, err := NewInboundSpool(InboundSpoolPath(root))
	if err != nil {
		t.Fatal(err)
	}
	msg := prepareDurabilityMessage(t, spool)
	messageBus := NewMessageBus()
	t.Cleanup(messageBus.Close)
	messageBus.SetInboundSpool(spool)
	receipt, err := messageBus.AckInbound(t.Context(), msg)
	if err != nil || !receipt.Acknowledges(root, msg.SpoolID) {
		t.Fatalf("native acknowledgement = %#v, %v", receipt, err)
	}
	otherRoot := t.TempDir()
	if _, err = NewInboundSpool(InboundSpoolPath(otherRoot)); err != nil {
		t.Fatal(err)
	}
	if receipt.Acknowledges(root, "other-source") || receipt.Acknowledges(otherRoot, msg.SpoolID) ||
		receipt.Acknowledges("", msg.SpoolID) || (InboundAckReceipt{}).Acknowledges(root, msg.SpoolID) {
		t.Fatal("receipt accepted a different source, namespace, or absent proof")
	}
	// Generic event encoding must not expose the receipt's native identities.
	encoded, err := json.Marshal(map[string]any{"ack": receipt})
	if err != nil || string(encoded) != `{"ack":{}}` {
		t.Fatalf("receipt exposed native identities as JSON: %s, %v", encoded, err)
	}
	pending, err := spool.Pending(t.Context(), 0)
	if err != nil || len(pending) != 0 {
		t.Fatalf("acknowledged source remained replayable: %#v, %v", pending, err)
	}
	// An absent-name retry is successful, but cannot mint a second proof.
	retry, err := messageBus.AckInbound(t.Context(), msg)
	if err != nil || retry != (InboundAckReceipt{}) {
		t.Fatalf("absent retry minted proof: %#v, %v", retry, err)
	}
}

func TestInboundAckReceiptUsesActualNamespaceThroughRootAlias(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "instance-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	spool, err := NewInboundSpool(InboundSpoolPath(alias))
	if err != nil {
		t.Fatal(err)
	}
	msg := prepareDurabilityMessage(t, spool)
	receipt, err := spool.acknowledge(msg.SpoolID)
	if err != nil || !receipt.Acknowledges(root, msg.SpoolID) || !receipt.Acknowledges(alias, msg.SpoolID) {
		t.Fatalf("actual namespace alias rejected: %#v, %v", receipt, err)
	}
}

func TestInboundAckWithoutNativeSourceReturnsNoReceipt(t *testing.T) {
	messageBus := NewMessageBus()
	t.Cleanup(messageBus.Close)
	msg := InboundMessage{SpoolID: "unproven-source"}
	receipt, err := messageBus.AckInbound(t.Context(), msg)
	if err != nil || receipt != (InboundAckReceipt{}) {
		t.Fatalf("in-memory acknowledgement minted proof: %#v, %v", receipt, err)
	}
	spool, err := NewInboundSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	messageBus.SetInboundSpool(spool)
	for _, id := range []string{"", "missing-source"} {
		receipt, err = messageBus.AckInbound(t.Context(), InboundMessage{SpoolID: id})
		if err != nil || receipt != (InboundAckReceipt{}) {
			t.Fatalf("absent source %q minted proof: %#v, %v", id, receipt, err)
		}
	}
}

func TestInboundAckReceiptAcceptsRecoveredPendingSources(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		t.Run(map[bool]string{false: "version1", true: "document_version2"}[guarded], func(t *testing.T) {
			root := t.TempDir()
			spool, err := NewInboundSpool(InboundSpoolPath(root))
			if err != nil {
				t.Fatal(err)
			}
			msg := prepareDurabilityMessage(t, spool)
			if guarded {
				msg.Context.DocumentIntake = documentIntakeTestGuard()
				if err = spool.PersistContext(msg); err != nil {
					t.Fatal(err)
				}
			}
			if err = spool.Release(msg.SpoolID, nil); err != nil {
				t.Fatal(err)
			}
			reopened, err := NewInboundSpool(InboundSpoolPath(root))
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := reopened.acknowledge(msg.SpoolID)
			if err != nil || !receipt.Acknowledges(root, msg.SpoolID) {
				t.Fatalf("recovered source acknowledgement = %#v, %v", receipt, err)
			}
		})
	}
}

func TestInboundAckDoesNotMintReceiptAfterUnconfirmedRemoval(t *testing.T) {
	spool, err := NewInboundSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msg := prepareDurabilityMessage(t, spool)
	injected := errors.New("directory durability unconfirmed")
	spool.syncDirectory = func(string) error { return injected }
	receipt, err := spool.acknowledge(msg.SpoolID)
	if !errors.Is(err, injected) || receipt != (InboundAckReceipt{}) {
		t.Fatalf("unconfirmed removal minted proof: %#v, %v", receipt, err)
	}
	spool.syncDirectory = fileutil.SyncDirectory
	receipt, err = spool.acknowledge(msg.SpoolID)
	if err != nil || receipt != (InboundAckReceipt{}) {
		t.Fatalf("absent source retry reconstructed proof: %#v, %v", receipt, err)
	}
}

func TestInboundAckPreservesInvalidOrAmbiguousSource(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*spooledInboundRecord)
		raw    string
		copy   bool
	}{
		{name: "invalid JSON", raw: "private-invalid-record"},
		{name: "unsupported schema", mutate: func(r *spooledInboundRecord) { r.Version = inboundDocumentSpoolVersion + 1 }},
		{name: "wrong record identity", mutate: func(r *spooledInboundRecord) { r.ID = "other-source" }},
		{name: "missing record identity", mutate: func(r *spooledInboundRecord) { r.ID = "" }},
		{name: "wrong message identity", mutate: func(r *spooledInboundRecord) { r.Message.SpoolID = "other-source" }},
		{name: "retired context", mutate: func(r *spooledInboundRecord) {
			r.Message.Context.Raw = map[string]string{"interaction_response": "retired"}
		}},
		{name: "ambiguous duplicate", copy: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			spool, err := NewInboundSpool(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			msg := prepareDurabilityMessage(t, spool)
			path := spool.processingPath(msg.SpoolID)
			rec, err := spool.readRecord(path)
			if err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(&rec)
			}
			data, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			if test.raw != "" {
				data = []byte(test.raw)
			}
			if err = os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if test.copy {
				if err = os.WriteFile(spool.pendingPath(msg.SpoolID), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := spool.acknowledge(msg.SpoolID)
			if err == nil || receipt != (InboundAckReceipt{}) {
				t.Fatalf("invalid source acknowledged: %#v, %v", receipt, err)
			}
			preserved, err := os.ReadFile(path)
			if err != nil || string(preserved) != string(data) {
				t.Fatalf("source evidence changed: %q, %v", preserved, err)
			}
			if test.copy {
				if _, err = os.Stat(spool.pendingPath(msg.SpoolID)); err != nil {
					t.Fatal("ambiguous duplicate was removed")
				}
			}
		})
	}
}

func TestInboundAckRejectsTraversalAndLinkedRecords(t *testing.T) {
	spool, err := NewInboundSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{".", "..", "../outside", `..\outside`, "/absolute"} {
		if receipt, err := spool.acknowledge(id); err == nil || receipt != (InboundAckReceipt{}) {
			t.Fatalf("invalid identity %q acknowledged: %#v, %v", id, receipt, err)
		}
	}
	outside := filepath.Join(t.TempDir(), "source-evidence")
	if err = os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, spool.processingPath("linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if receipt, err := spool.acknowledge("linked"); err == nil || receipt != (InboundAckReceipt{}) {
		t.Fatalf("linked record acknowledged: %#v, %v", receipt, err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
		t.Fatalf("external evidence changed: %q, %v", data, err)
	}
}

func TestInboundAckCannotBeResurrectedByConcurrentContextWrite(t *testing.T) {
	spool, err := NewInboundSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msg := prepareDurabilityMessage(t, spool)
	write := spool.writeAtomic
	entered := make(chan struct{})
	release := make(chan struct{})
	spool.writeAtomic = func(path string, data []byte, mode os.FileMode) error {
		close(entered)
		<-release
		return write(path, data, mode)
	}
	persisted := make(chan error, 1)
	go func() { persisted <- spool.PersistContext(msg) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("context write did not start")
	}
	type outcome struct {
		receipt InboundAckReceipt
		err     error
	}
	acked := make(chan outcome, 1)
	go func() {
		receipt, err := spool.acknowledge(msg.SpoolID)
		acked <- outcome{receipt, err}
	}()
	select {
	case result := <-acked:
		close(release)
		<-persisted
		t.Fatalf("ack raced an unfinished context write: %#v", result)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err = <-persisted; err != nil {
		t.Fatal(err)
	}
	result := <-acked
	if result.err != nil || result.receipt == (InboundAckReceipt{}) {
		t.Fatalf("serialized acknowledgement = %#v", result)
	}
	if err = spool.PersistContext(msg); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("late context write recreated source: %v", err)
	}
	for _, path := range []string{spool.processingPath(msg.SpoolID), spool.pendingPath(msg.SpoolID)} {
		if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("acknowledged source resurrected: %s, %v", path, err)
		}
	}
}
