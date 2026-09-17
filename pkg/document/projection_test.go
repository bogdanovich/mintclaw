package document

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/media"
)

func TestProjectMediaUsesExactOwnedBytesInsteadOfFilenameOrMetadata(t *testing.T) {
	owner := testMediaOwner(t)
	tests := []struct {
		name        string
		data        []byte
		contentType string
		filename    string
		wantCode    FailureCode
	}{
		{
			name: "pdf bytes override generic metadata", data: []byte("%PDF-1.7\nowned\n%%EOF\n"),
			contentType: "application/octet-stream", filename: "upload.bin",
		},
		{
			name: "fake extension refused", data: []byte("not a pdf"),
			contentType: "application/pdf", filename: "claim.pdf", wantCode: FailureUnsupportedType,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := media.NewFileMediaStore()
			path := filepath.Join(t.TempDir(), test.filename)
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			ref, err := store.Store(path, media.MediaMeta{
				Filename: test.filename, ContentType: test.contentType, CleanupPolicy: media.CleanupPolicyForgetOnly,
			}, "projection")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.BindOwner(ref, owner); err != nil {
				t.Fatal(err)
			}
			projection, failure := ProjectMedia(store, ref, owner, DefaultMaxInputBytes)
			if test.wantCode != "" {
				if failure == nil || failure.Code != test.wantCode {
					t.Fatalf("failure = %#v, want %q", failure, test.wantCode)
				}
				return
			}
			if failure != nil || projection.ContentType != "application/pdf" || projection.Ref != ref ||
				projection.Size != int64(len(test.data)) || projection.SourceSHA256 == "" {
				t.Fatalf("projection = %#v, failure = %#v", projection, failure)
			}
		})
	}
}

func TestProjectMediaRequiresExactOwnerAndKeepsSameNameBytesDistinct(t *testing.T) {
	store := media.NewFileMediaStore()
	owner := testMediaOwner(t)
	var projections []AttachmentProjection
	for index, data := range [][]byte{
		[]byte("%PDF-1.7\nfirst\n%%EOF\n"),
		[]byte("%PDF-1.7\nsecond\n%%EOF\n"),
	} {
		path := filepath.Join(t.TempDir(), "same.pdf")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		ref, err := store.Store(path, media.MediaMeta{
			Filename: "same.pdf", ContentType: "application/pdf", CleanupPolicy: media.CleanupPolicyForgetOnly,
		}, "projection-"+string(rune('0'+index)))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.BindOwner(ref, owner); err != nil {
			t.Fatal(err)
		}
		projection, failure := ProjectMedia(store, ref, owner, DefaultMaxInputBytes)
		if failure != nil {
			t.Fatal(failure)
		}
		projections = append(projections, projection)
	}
	if projections[0].Ref == projections[1].Ref || projections[0].SourceSHA256 == projections[1].SourceSHA256 {
		t.Fatalf("same-name projections aliased: %#v", projections)
	}
	mismatch := owner
	mismatch.ActorID += "-other"
	if _, failure := ProjectMedia(store, projections[0].Ref, mismatch, DefaultMaxInputBytes); failure == nil ||
		failure.Code != FailureSourceUnauthorized {
		t.Fatalf("owner mismatch failure = %#v", failure)
	}
}
