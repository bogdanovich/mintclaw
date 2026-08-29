//go:build unix

package thread

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExternalAttachmentRejectsNonUTF8CanonicalPath(t *testing.T) {
	store, metadata := newLeaseTestThread(t)
	lease := acquireAttachmentTestLease(t, store, metadata.ThreadID)
	invalidDirectory := filepath.Join(t.TempDir(), string([]byte{0xff}))
	if err := os.Mkdir(invalidDirectory, 0o700); err != nil {
		t.Skipf("filesystem rejects non-UTF-8 paths: %v", err)
	}
	source := filepath.Join(invalidDirectory, "attachment.txt")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, AttachmentInput{
		Path: source, Mode: AttachmentModeExternal, At: time.Now(),
	})
	if err == nil || attachment.Ref != "" || !strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("non-UTF-8 external admission = %+v, %v", attachment, err)
	}
}
