package bus

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

func prepareDurabilityMessage(t *testing.T, spool *InboundSpool) InboundMessage {
	t.Helper()
	msg, err := spool.Prepare(t.Context(), InboundMessage{
		Context: InboundContext{
			Channel:  "telegram",
			ChatID:   "chat",
			SenderID: "sender",
		},
		Content: "original-private-payload",
	})
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestInboundPublishDoesNotQueueUnconfirmedRecordWrite(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_write", true: "after_commit"}[committed], func(t *testing.T) {
			spool, err := NewInboundSpool(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			write := spool.writeAtomic
			injected := errors.New("synthetic write durability failure")
			spool.writeAtomic = func(path string, data []byte, mode os.FileMode) error {
				if committed {
					if err := write(path, data, mode); err != nil {
						return err
					}
					return &fileutil.CommittedWriteError{Err: injected}
				}
				return injected
			}
			messageBus := NewMessageBus()
			t.Cleanup(messageBus.Close)
			messageBus.SetInboundSpool(spool)
			err = messageBus.PublishInbound(t.Context(), InboundMessage{
				Context: InboundContext{Channel: "telegram", ChatID: "chat"}, Content: "original-private-payload",
			})
			if !errors.Is(err, injected) {
				t.Fatalf("unconfirmed publish = %v", err)
			}
			select {
			case msg := <-messageBus.InboundChan():
				t.Fatalf("unconfirmed source reached agent: %#v", msg)
			default:
			}
			reopened, err := NewInboundSpool(spool.Dir())
			if err != nil {
				t.Fatal(err)
			}
			pending, err := reopened.Pending(t.Context(), 0)
			if err != nil {
				t.Fatal(err)
			}
			if committed {
				if len(pending) != 1 || pending[0].Content != "original-private-payload" || pending[0].SpoolID == "" {
					t.Fatalf("committed source lost: %#v", pending)
				}
			} else if len(pending) != 0 {
				t.Fatal("uncommitted write created a source")
			}
		})
	}
}

func TestInboundClaimRequiresDirectorySyncBeforePublication(t *testing.T) {
	spool, err := NewInboundSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("claim directory durability unconfirmed")
	syncs := 0
	spool.syncDirectory = func(dir string) error {
		syncs++
		if dir != spool.Dir() {
			t.Fatalf("wrong directory synced: %s", dir)
		}
		return injected
	}
	messageBus := NewMessageBus()
	t.Cleanup(messageBus.Close)
	messageBus.SetInboundSpool(spool)
	err = messageBus.PublishInbound(t.Context(), InboundMessage{
		Context: InboundContext{Channel: "telegram", ChatID: "chat"}, Content: "claimed-payload",
	})
	if !errors.Is(err, injected) || syncs != 1 {
		t.Fatalf("claim confirmation = %v, syncs %d", err, syncs)
	}
	select {
	case <-messageBus.InboundChan():
		t.Fatal("unconfirmed claim reached agent")
	default:
	}
	reopened, err := NewInboundSpool(spool.Dir())
	if err != nil {
		t.Fatal(err)
	}
	pending, err := reopened.Pending(t.Context(), 0)
	if err != nil || len(pending) != 1 || pending[0].SpoolID == "" || pending[0].Content != "claimed-payload" {
		t.Fatalf("claim recovery = %#v, %v", pending, err)
	}
}

func TestInboundReleaseAndFailurePreserveIdentityAfterUnconfirmedRename(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "failed"}[terminal], func(t *testing.T) {
			spool, err := NewInboundSpool(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			msg := prepareDurabilityMessage(t, spool)
			injected := errors.New("rename durability unconfirmed")
			spool.syncDirectory = func(string) error { return injected }
			if terminal {
				err = spool.Fail(msg.SpoolID, errors.New("failed"))
			} else {
				err = spool.Release(msg.SpoolID, errors.New("retry"))
			}
			if !errors.Is(err, injected) {
				t.Fatalf("rename confirmation = %v", err)
			}
			// An absent old name after an unconfirmed rename is not proof of
			// durability. Retry reconfirms the directory without another attempt.
			if terminal {
				err = spool.Fail(msg.SpoolID, nil)
			} else {
				err = spool.Release(msg.SpoolID, nil)
			}
			if !errors.Is(err, injected) {
				t.Fatalf("rename retry skipped confirmation: %v", err)
			}
			spool.syncDirectory = fileutil.SyncDirectory
			if terminal {
				err = spool.Fail(msg.SpoolID, nil)
			} else {
				err = spool.Release(msg.SpoolID, nil)
			}
			if err != nil {
				t.Fatalf("confirmed rename retry: %v", err)
			}
			reopened, err := NewInboundSpool(spool.Dir())
			if err != nil {
				t.Fatal(err)
			}
			path := reopened.pendingPath(msg.SpoolID)
			if terminal {
				path = reopened.failedPath(msg.SpoolID)
			}
			record, err := reopened.readRecord(path)
			if err != nil || record.ID != msg.SpoolID || record.Attempts != 1 || record.Message.Content != msg.Content {
				t.Fatalf("preserved source = %#v, %v", record, err)
			}
			pending, err := reopened.Pending(t.Context(), 0)
			if err != nil {
				t.Fatal(err)
			}
			if terminal && len(pending) != 0 || !terminal && (len(pending) != 1 || pending[0].SpoolID != msg.SpoolID) {
				t.Fatalf("replay classification = %#v", pending)
			}
		})
	}
}

func TestInboundAcknowledgementReconfirmsUnlinksOnRetry(t *testing.T) {
	spool, err := NewInboundSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msg := prepareDurabilityMessage(t, spool)
	// Ack owns both names, including a leftover duplicate from recovery.
	data, err := os.ReadFile(spool.processingPath(msg.SpoolID))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(spool.pendingPath(msg.SpoolID), data, 0o600); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("acknowledgement durability unconfirmed")
	syncs := 0
	spool.syncDirectory = func(dir string) error {
		syncs++
		for _, path := range []string{spool.processingPath(msg.SpoolID), spool.pendingPath(msg.SpoolID)} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("ack confirmed before both names were removed")
			}
		}
		if syncs == 1 {
			return injected
		}
		return fileutil.SyncDirectory(dir)
	}
	if err = spool.Ack(msg.SpoolID); !errors.Is(err, injected) {
		t.Fatalf("unconfirmed ack = %v", err)
	}
	if err = spool.Ack(msg.SpoolID); err != nil || syncs != 2 {
		t.Fatalf("ack retry = %v, syncs %d", err, syncs)
	}
}

func TestInboundAcknowledgementSyncsPartialRemovalBeforeReturningFailure(t *testing.T) {
	spool, err := NewInboundSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msg := prepareDurabilityMessage(t, spool)
	if err = os.Mkdir(spool.pendingPath(msg.SpoolID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(
		filepath.Join(spool.pendingPath(msg.SpoolID), "evidence"),
		[]byte("keep"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	syncs := 0
	spool.syncDirectory = func(dir string) error { syncs++; return fileutil.SyncDirectory(dir) }
	if err = spool.Ack(msg.SpoolID); err == nil || syncs != 1 {
		t.Fatalf("partial ack = %v, syncs %d", err, syncs)
	}
	if _, err = os.Stat(filepath.Join(spool.pendingPath(msg.SpoolID), "evidence")); err != nil {
		t.Fatal("failed acknowledgement removed unrelated evidence")
	}
}
