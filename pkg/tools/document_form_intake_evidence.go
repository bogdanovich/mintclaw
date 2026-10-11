package tools

import (
	"context"
	"slices"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/media"
)

type documentFormIntakeEvidenceResolver func(
	context.Context, ownedDocumentMediaStore, string, media.MediaOwner, []int,
) ([]document.FormIntakeTextEvidence, error)

func (tool *DocumentTool) privateFormIntakeEvidence(
	ctx context.Context, owner document.FormJobOwner, jobID string,
	schema document.FormFieldsFacts, ids []string,
) ([]document.FormIntakeTextEvidence, error) {
	var pages []int
	for _, field := range schema.Fields {
		if !slices.Contains(ids, field.ID) {
			continue
		}
		// The first widget locates this bounded field view. Repeated widgets
		// are verified by the existing writer, not expanded into this prompt.
		if len(field.Widgets) > 0 && !slices.Contains(pages, field.Widgets[0].Page) {
			pages = append(pages, field.Widgets[0].Page)
		}
	}
	slices.Sort(pages)
	if len(pages) == 0 {
		return nil, nil
	}
	if len(pages) > documentFormEvidencePageLimit {
		return nil, document.ErrFormJobCapacityExceeded
	}
	store, mediaOwner, err := tool.executionAuthority(ctx)
	if err != nil {
		return nil, document.ErrFormJobUnauthorized
	}
	ref, err := tool.formJobs.SourceRef(ctx, jobID, owner)
	if err != nil {
		return nil, err
	}
	if tool.formIntakeEvidence != nil {
		return tool.formIntakeEvidence(ctx, store, ref, mediaOwner, pages)
	}
	snapshot, report := document.ExtractMedia(ctx, store, ref, mediaOwner, document.ReadOptions{
		Acquire: document.AcquireOptions{ScratchRoot: tool.scratchRoot},
		Pages:   pages,
		Limits: document.ReadLimits{
			MaxPages:      documentFormEvidencePageLimit,
			MaxCharacters: documentFormEvidenceTextLimit,
		},
	})
	if snapshot != nil {
		defer func() { _ = snapshot.Close() }()
	}
	if report.State != document.StateSucceeded || report.Input == nil || report.Input.SHA256 != schema.SourceSHA256 {
		return nil, document.ErrFormAuditUnavailable
	}
	extracted, err := document.ReadExtractedPages(snapshot, report)
	if err != nil {
		return nil, document.ErrFormAuditUnavailable
	}
	evidence := make([]document.FormIntakeTextEvidence, len(extracted))
	for index, page := range extracted {
		evidence[index] = document.FormIntakeTextEvidence{Page: page.Page, Text: page.Text}
	}
	return evidence, nil
}
