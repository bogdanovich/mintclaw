package outbox

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

func retentionNativeSource(t *testing.T, root string) (*bus.MessageBus, bus.InboundMessage) {
	t.Helper()
	spool, err := bus.NewInboundSpool(bus.InboundSpoolPath(root))
	if err != nil {
		t.Fatal(err)
	}
	msg, err := spool.Prepare(t.Context(), bus.InboundMessage{
		Context: bus.InboundContext{Channel: "telegram", ChatID: "chat", SenderID: "owner"}, Content: "native input",
	})
	if err != nil {
		t.Fatal(err)
	}
	messageBus := bus.NewMessageBus()
	messageBus.SetInboundSpool(spool)
	t.Cleanup(messageBus.Close)
	return messageBus, msg
}

func retentionNativeAdmission(t *testing.T, root string, msg bus.InboundMessage) (*Coordinator, Admission) {
	t.Helper()
	c, err := OpenCoordinator(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	a, err := c.AdmitNativeFinalMessage("/agents/main", Identity{
		SourceID: msg.SpoolID, Kind: KindMessage, Channel: "telegram", ChatID: "chat",
	}, bus.OutboundMessage{Content: "protected until exact source completion"})
	if err != nil {
		t.Fatal(err)
	}
	commitTestAdmission(t, c, a.Lease)
	return c, a
}

func TestNativeSourceSealRequiresExactSourceInstanceAndPendingOwner(t *testing.T) {
	root := t.TempDir()
	native, source := retentionNativeSource(t, root)
	c, a := retentionNativeAdmission(t, root, source)
	_, wrongSource := retentionNativeSource(t, root)
	wrongSourceProof, err := native.AckInbound(t.Context(), wrongSource)
	if err != nil {
		t.Fatal(err)
	}
	otherNative, otherSource := retentionNativeSource(t, t.TempDir())
	otherProof, err := otherNative.AckInbound(t.Context(), otherSource)
	if err != nil {
		t.Fatal(err)
	}
	for _, proof := range []bus.InboundAckReceipt{{}, wrongSourceProof, otherProof} {
		sealed, err := c.SealNativeFinalMessage(a.Intent.ID, proof)
		if err != nil || sealed {
			t.Fatalf("unqualified seal=%v,%v", sealed, err)
		}
	}
	current, err := c.Get(a.Intent.ID)
	if err != nil || current.RetentionClass != retentionSourcePending || current.Message == nil {
		t.Fatalf("pending=%#v,%v", current, err)
	}
	proof, err := native.AckInbound(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := c.SealNativeFinalMessage(a.Intent.ID, proof)
	if err != nil || !sealed {
		t.Fatalf("native seal=%v,%v", sealed, err)
	}
	current, err = c.Get(a.Intent.ID)
	if err != nil || current.RetentionClass != retentionPlainMessage || current.Status != StatusPending {
		t.Fatalf("seal changed transport=%#v,%v", current, err)
	}
	if reason := payloadRetirementBlocker(current, time.Now(), time.Now()); reason != "protected_outcome" {
		t.Fatalf("pending transport eligible: %s", reason)
	}
	if err = c.ProtectPayload(a.Intent.ID); err != nil {
		t.Fatal(err)
	}
	sealed, err = c.SealNativeFinalMessage(a.Intent.ID, proof)
	if err != nil || sealed {
		t.Fatalf("stale proof promoted revoked payload=%v,%v", sealed, err)
	}
	current, err = c.Get(a.Intent.ID)
	if err != nil || current.RetentionClass != "" || current.Message == nil {
		t.Fatalf("revoked=%#v,%v", current, err)
	}
	identity := a.Intent.Identity
	identity.Ordinal++
	legacy, err := c.AdmitMessage("/agents/main", identity, bus.OutboundMessage{Content: "legacy remains protected"})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err = c.SealNativeFinalMessage(legacy.Intent.ID, proof)
	if err != nil || sealed {
		t.Fatalf("proof promoted legacy=%v,%v", sealed, err)
	}
}

func TestNativeSourceSealInterruptedWritePreservesNoReplayAndRevocation(t *testing.T) {
	for _, afterWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_write", true: "after_write"}[afterWrite], func(t *testing.T) {
			root := t.TempDir()
			native, source := retentionNativeSource(t, root)
			c, a := retentionNativeAdmission(t, root, source)
			proof, err := native.AckInbound(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			write := c.store.writeAtomic
			injected := errors.New("source seal write unconfirmed")
			c.store.writeAtomic = func(path string, data []byte, mode os.FileMode) error {
				if afterWrite {
					if err := write(path, data, mode); err != nil {
						return err
					}
					return &fileutil.CommittedWriteError{Err: injected}
				}
				return injected
			}
			sealed, err := c.SealNativeFinalMessage(a.Intent.ID, proof)
			if !errors.Is(err, injected) || sealed {
				t.Fatalf("unconfirmed seal=%v,%v", sealed, err)
			}
			c.store.writeAtomic = write
			current, err := c.Get(a.Intent.ID)
			if err != nil || current.Message == nil || current.PayloadRetired {
				t.Fatalf("lost payload=%#v,%v", current, err)
			}
			expected := retentionSourcePending
			if afterWrite {
				expected = retentionPlainMessage
			}
			if current.RetentionClass != expected {
				t.Fatalf("class=%q want %q", current.RetentionClass, expected)
			}
			retry, err := native.AckInbound(t.Context(), source)
			if err != nil || retry != (bus.InboundAckReceipt{}) {
				t.Fatal("absent retry invented source proof")
			}
			if err = c.ProtectPayload(a.Intent.ID); err != nil {
				t.Fatal(err)
			}
			sealed, err = c.SealNativeFinalMessage(a.Intent.ID, proof)
			if err != nil || sealed {
				t.Fatalf("old proof reversed protection=%v,%v", sealed, err)
			}
		})
	}
}
