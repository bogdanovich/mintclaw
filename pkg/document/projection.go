package document

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/media"
)

// AttachmentProjection is the bounded, path-free description of one exact
// authority-bound media object. SourceSHA256 is retained for provenance but
// callers should omit it from the initial model-facing attachment summary.
type AttachmentProjection struct {
	Ref          string `json:"ref"`
	ContentType  string `json:"content_type"`
	Size         int64  `json:"size"`
	Filename     string `json:"filename"`
	SourceSHA256 string `json:"source_sha256"`
}

// ProjectMedia classifies one exact owned media object without exposing its
// backing path or sending document bytes to a provider. It uses the same PDF
// signature admitted by acquisition; filenames and channel MIME metadata are
// presentation hints only.
func ProjectMedia(
	resolver OwnedMediaResolver,
	ref string,
	owner media.MediaOwner,
	maxBytes int64,
) (AttachmentProjection, *Failure) {
	if resolver == nil || !validOwnedMediaInput(ref, owner) {
		return AttachmentProjection{}, projectionFailure(
			FailureSourceUnauthorized,
			"document source is unavailable for this authority",
		)
	}
	source, err := resolver.OpenOwned(strings.TrimSpace(ref), owner)
	if err != nil || source == nil || source.File == nil {
		return AttachmentProjection{}, projectionFailure(
			FailureSourceUnauthorized,
			"document source is unavailable for this authority",
		)
	}
	defer func() { _ = source.Close() }()

	if maxBytes <= 0 || maxBytes > DefaultMaxInputBytes {
		maxBytes = DefaultMaxInputBytes
	}
	if source.Identity.Size <= 0 || source.Identity.Size > maxBytes {
		return AttachmentProjection{}, projectionFailure(
			FailureLimitExceeded,
			"document exceeds the input byte limit",
		)
	}
	header := make([]byte, pdfHeaderBytes)
	count, readErr := io.ReadFull(source.File, header)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return AttachmentProjection{}, projectionFailure(
			FailureInternal,
			"document attachment could not be classified",
		)
	}
	if !bytes.Contains(header[:count], []byte("%PDF-")) {
		return AttachmentProjection{}, projectionFailure(
			FailureUnsupportedType,
			"attachment bytes are not a PDF document",
		)
	}
	return AttachmentProjection{
		Ref:          strings.TrimSpace(ref),
		ContentType:  "application/pdf",
		Size:         source.Identity.Size,
		Filename:     safeMediaFilename(source.Meta.Filename),
		SourceSHA256: source.Identity.SHA256,
	}, nil
}

func projectionFailure(code FailureCode, message string) *Failure {
	return &Failure{Code: code, Message: message}
}
