// MintClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/utils"
)

type audioTranscriptionStatus struct {
	audioRefs int
	completed int
}

type inboundMediaResolution struct {
	ref      string
	path     string
	isAudio  bool
	resolved bool
}

func (status audioTranscriptionStatus) complete() bool {
	return status.audioRefs > 0 && status.completed == status.audioRefs
}

func (al *AgentLoop) transcribeAudioInMessage(ctx context.Context, msg bus.InboundMessage) (bus.InboundMessage, bool) {
	msg, status := al.transcribeAudioInMessageWithStatus(ctx, msg)
	return msg, status.audioRefs > 0
}

func (al *AgentLoop) transcribeAudioInMessageWithStatus(
	ctx context.Context,
	msg bus.InboundMessage,
) (bus.InboundMessage, audioTranscriptionStatus) {
	status := audioTranscriptionStatus{}
	if al.transcriber == nil || al.mediaStore == nil || len(msg.Media) == 0 {
		return msg, status
	}
	originalContent := msg.Content

	// Resolve every media ref before assigning audio slots. Audio annotations
	// preserve the expected order even when a media ref can no longer resolve.
	mediaResolutions := make([]inboundMediaResolution, 0, len(msg.Media))
	knownAudioRemaining := 0
	for _, ref := range msg.Media {
		path, meta, err := al.mediaStore.ResolveWithMeta(ref)
		if err != nil {
			logger.WarnCF("voice", "Failed to resolve media ref", map[string]any{"ref": ref, "error": err})
			mediaResolutions = append(mediaResolutions, inboundMediaResolution{ref: ref})
			continue
		}
		resolution := inboundMediaResolution{
			ref:      ref,
			path:     path,
			isAudio:  utils.IsAudioFile(meta.Filename, meta.ContentType),
			resolved: true,
		}
		mediaResolutions = append(mediaResolutions, resolution)
		if !resolution.isAudio {
			continue
		}
		knownAudioRemaining++
	}

	expectedAudioSlots := max(
		len(audioAnnotationRe.FindAllString(msg.Content, -1)),
		len(audioAnnotationRe.FindAllString(msg.Context.Interaction.Response, -1)),
		len(audioAnnotationRe.FindAllString(msg.Context.Interaction.ResponseCandidate, -1)),
	)
	transcriptions := make([]string, 0, max(expectedAudioSlots, knownAudioRemaining))
	for _, resolution := range mediaResolutions {
		if !resolution.resolved {
			if len(transcriptions)+knownAudioRemaining < expectedAudioSlots {
				status.audioRefs++
				transcriptions = append(transcriptions, "")
			}
			continue
		}
		if !resolution.isAudio {
			continue
		}
		knownAudioRemaining--
		status.audioRefs++
		result, err := al.transcriber.Transcribe(ctx, resolution.path)
		if err != nil || result == nil || strings.TrimSpace(result.Text) == "" {
			logger.WarnCF(
				"voice",
				"Transcription failed",
				map[string]any{"ref": resolution.ref, "error": err},
			)
			transcriptions = append(transcriptions, "")
			continue
		}
		status.completed++
		transcriptions = append(transcriptions, result.Text)
	}
	for len(transcriptions) < expectedAudioSlots {
		status.audioRefs++
		transcriptions = append(transcriptions, "")
	}

	if len(transcriptions) == 0 {
		return msg, status
	}

	al.sendTranscriptionFeedback(
		ctx,
		msg.Context.Channel,
		msg.Context.ChatID,
		msg.Context.MessageID,
		transcriptions,
	)

	msg.Content = replaceAudioAnnotations(msg.Content, transcriptions, true)
	msg.Context.Interaction.Response = replaceProjectedAudioAnnotations(
		originalContent,
		msg.Context.Interaction.Response,
		transcriptions,
	)
	msg.Context.Interaction.ResponseCandidate = replaceProjectedAudioAnnotations(
		originalContent,
		msg.Context.Interaction.ResponseCandidate,
		transcriptions,
	)
	return msg, status
}

func replaceAudioAnnotations(content string, transcriptions []string, appendRemaining bool) string {
	idx := 0
	content = audioAnnotationRe.ReplaceAllStringFunc(content, func(match string) string {
		if idx >= len(transcriptions) {
			return match
		}
		text := transcriptions[idx]
		idx++
		if text == "" {
			return match
		}
		return "[voice: " + text + "]"
	})

	if appendRemaining {
		for ; idx < len(transcriptions); idx++ {
			if transcriptions[idx] != "" {
				content += "\n[voice: " + transcriptions[idx] + "]"
			}
		}
	}
	return content
}

func replaceProjectedAudioAnnotations(
	originalContent string,
	projected string,
	transcriptions []string,
) string {
	if projected == "" || !audioAnnotationRe.MatchString(projected) || len(transcriptions) == 0 {
		return projected
	}
	offset := 0
	if start := strings.LastIndex(originalContent, projected); start > 0 {
		offset = len(audioAnnotationRe.FindAllString(originalContent[:start], -1))
	}
	if offset >= len(transcriptions) {
		return projected
	}
	return replaceAudioAnnotations(projected, transcriptions[offset:], false)
}

func (al *AgentLoop) sendTranscriptionFeedback(
	ctx context.Context,
	channel, chatID, messageID string,
	validTexts []string,
) {
	if !al.cfg.Voice.EchoTranscription {
		return
	}
	if al.channelManager == nil {
		return
	}

	var nonEmpty []string
	for _, t := range validTexts {
		if t != "" {
			nonEmpty = append(nonEmpty, t)
		}
	}

	var feedbackMsg string
	if len(nonEmpty) > 0 {
		feedbackMsg = "Transcript: " + strings.Join(nonEmpty, "\n")
	} else {
		feedbackMsg = "No voice detected in the audio"
	}

	err := al.channelManager.SendMessage(ctx, bus.OutboundMessage{
		Context:          bus.NewOutboundContext(channel, chatID, messageID),
		Content:          feedbackMsg,
		ReplyToMessageID: messageID,
	})
	if err != nil {
		logger.WarnCF("voice", "Failed to send transcription feedback", map[string]any{"error": err.Error()})
	}
}
