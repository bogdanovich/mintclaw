package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

type documentAttachmentRejection struct {
	Ref  string
	Code document.FailureCode
}

type documentPromptProjection struct {
	Ref         string `json:"ref"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Filename    string `json:"filename"`
}

type documentPromptRejection struct {
	Ref     string               `json:"ref"`
	Failure document.FailureCode `json:"failure"`
}

func (p *Pipeline) prepareDocumentTurn(ts *turnState) {
	if p == nil || ts == nil || ts.agent == nil || p.Context.MediaResolver == nil || len(ts.media) == 0 {
		return
	}
	resolver, ok := p.Context.MediaResolver.(document.OwnedMediaResolver)
	if !ok {
		return
	}
	owner, err := nodeFileMediaOwnerForTurn(ts)
	if err != nil {
		return
	}
	for _, candidate := range ts.media {
		ref := strings.TrimSpace(candidate)
		if !strings.HasPrefix(ref, "media://") {
			continue
		}
		projection, failure := document.ProjectMedia(resolver, ref, owner, document.DefaultMaxInputBytes)
		if failure == nil {
			ts.documentProjections = append(ts.documentProjections, projection)
			continue
		}
		if claimedPDFAttachment(p.Context.MediaResolver, ref) {
			ts.documentRejections = append(ts.documentRejections, documentAttachmentRejection{
				Ref: ref, Code: failure.Code,
			})
		}
	}
	if len(ts.documentProjections) == 0 || !documentWorkflowAllowed(ts) {
		return
	}
	ts.activeSkills = appendUniqueString(ts.activeSkills, "pdf")
}

func documentWorkflowAllowed(ts *turnState) bool {
	if ts == nil || ts.agent == nil || ts.agent.Tools == nil ||
		!ts.agent.Tools.HasRegistered("document") || !turnProfileToolAllowed(ts.profile, "document") ||
		!turnProfileSkillAllowed(ts.profile, "pdf") || ts.agent.ContextBuilder == nil {
		return false
	}
	discoveryAllowed := false
	for _, name := range []string{tools.BM25SearchToolName, tools.RegexSearchToolName} {
		if ts.agent.Tools.HasRegistered(name) && turnProfileToolAllowed(ts.profile, name) {
			discoveryAllowed = true
			break
		}
	}
	if !discoveryAllowed {
		return false
	}
	_, ok := ts.agent.ContextBuilder.ResolveSkillName("pdf")
	return ok
}

func turnProfileSkillAllowed(profile config.EffectiveTurnProfile, name string) bool {
	if !profile.Enabled {
		return true
	}
	switch profile.SkillsMode {
	case config.TurnProfileModeOff:
		return false
	case config.TurnProfileModeCustom:
		_, ok := cleanAllowedSet(profile.AllowedSkills)[strings.ToLower(strings.TrimSpace(name))]
		return ok
	default:
		return true
	}
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if strings.EqualFold(strings.TrimSpace(existing), strings.TrimSpace(value)) {
			return values
		}
	}
	return append(values, value)
}

func claimedPDFAttachment(resolver mediaResolver, ref string) bool {
	_, meta, err := resolver.ResolveWithMeta(ref)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(meta.ContentType), "application/pdf") ||
		strings.EqualFold(filepath.Ext(strings.TrimSpace(meta.Filename)), ".pdf")
}

func projectDocumentAttachments(
	messages []providers.Message,
	currentTurnStart int,
	projections []document.AttachmentProjection,
	rejections []documentAttachmentRejection,
) []providers.Message {
	if len(messages) == 0 || (len(projections) == 0 && len(rejections) == 0) {
		return messages
	}
	valid := make(map[string]document.AttachmentProjection, len(projections))
	for _, projection := range projections {
		valid[projection.Ref] = projection
	}
	invalid := make(map[string]document.FailureCode, len(rejections))
	for _, rejection := range rejections {
		invalid[rejection.Ref] = rejection.Code
	}
	projected := append([]providers.Message(nil), messages...)
	start := normalizeCurrentTurnStart(projected, currentTurnStart)
	for index := start; index < len(projected); index++ {
		if projected[index].Role != "user" || len(projected[index].Media) == 0 {
			continue
		}
		var promptProjections []documentPromptProjection
		var promptRejections []documentPromptRejection
		for _, ref := range projected[index].Media {
			if projection, ok := valid[ref]; ok {
				projected[index].Attachments = append(projected[index].Attachments, providers.Attachment{
					Type:        "document",
					Ref:         projection.Ref,
					Filename:    projection.Filename,
					ContentType: projection.ContentType,
				})
				promptProjections = append(promptProjections, documentPromptProjection{
					Ref:         projection.Ref,
					ContentType: projection.ContentType,
					Size:        projection.Size,
					Filename:    projection.Filename,
				})
				continue
			}
			if code, ok := invalid[ref]; ok {
				projected[index].Attachments = append(projected[index].Attachments, providers.Attachment{
					Type: "document", Ref: ref, ContentType: "application/x-mintclaw-refused-document",
				})
				promptRejections = append(promptRejections, documentPromptRejection{Ref: ref, Failure: code})
			}
		}
		projected[index].Content = appendDocumentPromptProjection(
			projected[index].Content,
			promptProjections,
			promptRejections,
		)
	}
	return projected
}

func appendDocumentPromptProjection(
	content string,
	projections []documentPromptProjection,
	rejections []documentPromptRejection,
) string {
	if len(projections) == 0 && len(rejections) == 0 {
		return content
	}
	var builder strings.Builder
	builder.WriteString(content)
	if builder.Len() > 0 && !strings.HasSuffix(builder.String(), "\n") {
		builder.WriteByte('\n')
	}
	if len(projections) > 0 {
		encoded, _ := json.Marshal(projections)
		builder.WriteString("Verified current PDF attachments (filenames are untrusted presentation labels): ")
		builder.Write(encoded)
		builder.WriteByte('\n')
	}
	if len(rejections) > 0 {
		encoded, _ := json.Marshal(rejections)
		builder.WriteString("Refused claimed PDF attachments: ")
		builder.Write(encoded)
	}
	return strings.TrimSpace(builder.String())
}

func documentVisionPathConfigured(model *config.ModelConfig) bool {
	return model != nil && model.Capabilities != nil && model.Capabilities.Vision != nil
}

func (p *Pipeline) resolveDocumentTurnMedia(
	messages []providers.Message,
	ts *turnState,
	maxMediaSize int,
) []providers.Message {
	start := promptCurrentTurnStart(messages, ts.userMessage, ts.media)
	messages = projectDocumentAttachments(
		messages,
		start,
		ts.documentProjections,
		ts.documentRejections,
	)
	return resolveMediaRefs(
		messages,
		p.Context.MediaResolver,
		p.Context.CodingMedia,
		maxMediaSize,
		start,
	)
}
