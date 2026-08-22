package asr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3/pkg/media/oggwriter"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

type speechAccumulator struct {
	writer      *oggwriter.OggWriter
	file        string
	lastAudioAt time.Time
	mu          sync.Mutex
	closed      bool
	chatID      string
	speakerID   string
	sessionID   string
	channel     string
}

func (a *speechAccumulator) Push(chunk bus.AudioChunk) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return
	}

	a.lastAudioAt = time.Now()

	pkt := &rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: uint16(chunk.Sequence),
			Timestamp:      chunk.Timestamp,
			SSRC:           1, // Stable arbitrary dummy
		},
		Payload: chunk.Data,
	}

	if err := a.writer.WriteRTP(pkt); err != nil {
		logger.ErrorCF("voice-agent", "Failed to write RTP", map[string]any{"error": err})
	}
}

// Close finalizes the Ogg stream. The writer seeks back and writes the final
// Ogg page and CRC, so a non-nil error means the recording is malformed and
// must not be transcribed.
func (a *speechAccumulator) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.closed = true
		return a.writer.Close()
	}
	return nil
}

type Agent struct {
	bus         *bus.MessageBus
	transcriber Transcriber

	mu         sync.Mutex
	sessions   map[string]*speechAccumulator // keyed by sessionID_speakerID
	utterances sync.WaitGroup
}

func NewAgent(mb *bus.MessageBus, t Transcriber) *Agent {
	return &Agent{
		bus:         mb,
		transcriber: t,
		sessions:    make(map[string]*speechAccumulator),
	}
}

func (a *Agent) Start(ctx context.Context) {
	a.StartWithDone(ctx)
}

// StartWithDone starts the voice consumer and returns a channel closed after
// its readers, cleanup, and in-flight utterance workers have exited.
func (a *Agent) StartWithDone(ctx context.Context) <-chan struct{} {
	logger.InfoCF("voice-agent", "Started Voice Agent orchestrator", nil)
	done := make(chan struct{})
	drainCtx := context.WithoutCancel(ctx)
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		a.listenChunks(ctx)
	}()
	go func() {
		defer readers.Done()
		a.vadTick(ctx, drainCtx)
	}()
	go func() {
		readers.Wait()
		a.finishSessions(drainCtx, true)
		a.utterances.Wait()
		logger.InfoCF("voice-agent", "Cleaned up voice sessions on shutdown", nil)
		close(done)
	}()
	return done
}

func (a *Agent) listenChunks(ctx context.Context) {
	chunks := a.bus.AudioChunksChan()
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-chunks:
			if !ok {
				return
			}
			a.handleChunk(chunk)
		}
	}
}

func (a *Agent) handleChunk(chunk bus.AudioChunk) {
	// Only accept Opus-encoded audio
	if chunk.Format != "opus" {
		logger.DebugCF("voice-agent", "Ignoring unsupported audio format", map[string]any{"format": chunk.Format})
		return
	}

	key := fmt.Sprintf("%s_%s", chunk.SessionID, chunk.SpeakerID)

	a.mu.Lock()
	acc, exists := a.sessions[key]
	if !exists {
		filename := filepath.Join(os.TempDir(), fmt.Sprintf("voice_%s_%d.ogg", key, time.Now().UnixNano()))
		writer, err := oggwriter.New(filename, uint32(chunk.SampleRate), uint16(chunk.Channels))
		if err != nil {
			a.mu.Unlock()
			logger.ErrorCF("voice-agent", "Failed to create OggWriter", map[string]any{"error": err})
			return
		}

		acc = &speechAccumulator{
			writer:      writer,
			file:        filename,
			lastAudioAt: time.Now(),
			chatID:      chunk.ChatID,
			speakerID:   chunk.SpeakerID,
			sessionID:   chunk.SessionID,
			channel:     chunk.Channel,
		}
		a.sessions[key] = acc
		logger.DebugCF("voice-agent", "Started accumulating voice", map[string]any{"key": key, "file": filename})
	}
	a.mu.Unlock()

	acc.Push(chunk)
}

func (a *Agent) vadTick(ctx context.Context, drainCtx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.checkSilence(drainCtx)
		}
	}
}

func (a *Agent) checkSilence(ctx context.Context) {
	a.finishSessions(ctx, false)
}

func (a *Agent) finishSessions(ctx context.Context, force bool) {
	a.mu.Lock()
	now := time.Now()
	var finished []*speechAccumulator

	for key, acc := range a.sessions {
		acc.mu.Lock()
		last := acc.lastAudioAt
		acc.mu.Unlock()

		if force || now.Sub(last) > 1500*time.Millisecond {
			if err := acc.Close(); err != nil {
				logger.ErrorCF("voice-agent", "Failed to finalize Ogg recording; skipping transcription",
					map[string]any{"file": acc.file, "error": err})
				os.Remove(acc.file)
				delete(a.sessions, key)
				continue
			}
			delete(a.sessions, key)
			finished = append(finished, acc)
		}
	}
	a.mu.Unlock()

	for _, acc := range finished {
		a.utterances.Add(1)
		go func() {
			defer a.utterances.Done()
			a.processUtterance(ctx, acc)
		}()
	}
}

func (a *Agent) processUtterance(ctx context.Context, acc *speechAccumulator) {
	defer os.Remove(acc.file)

	logger.InfoCF("voice-agent", "User finished speaking, transcribing...", map[string]any{"file": acc.file})

	if a.transcriber == nil {
		logger.ErrorCF("voice-agent", "No STT configured!", nil)
		return
	}

	res, err := a.transcriber.Transcribe(ctx, acc.file)
	if err != nil {
		logger.ErrorCF("voice-agent", "Transcription failed", map[string]any{"error": err})
		return
	}

	if res.Text == "" {
		logger.DebugCF("voice-agent", "Ignored empty transcription", map[string]any{"file": acc.file})
		return
	}

	logger.InfoCF("voice-agent", "Transcription result", map[string]any{"text": res.Text, "duration": res.Duration})

	text := strings.ToLower(strings.TrimSpace(res.Text))
	if strings.Contains(text, "leave the voice channel") || strings.Contains(text, "leave voice") ||
		strings.Contains(text, "disconnect voice") || strings.Contains(text, "leave the channel") ||
		strings.Contains(text, "leave channel") {
		logger.InfoCF("voice-agent", "Voice command triggered: leave", nil)
		if err := a.bus.PublishVoiceControl(ctx, bus.VoiceControl{
			SessionID: acc.sessionID,
			Type:      "command",
			Action:    "leave",
		}); err != nil {
			logger.ErrorCF("voice-agent", "Failed to publish leave control", map[string]any{"error": err})
		}
		if err := a.bus.PublishOutbound(ctx, bus.OutboundMessage{
			Context: bus.NewOutboundContext(acc.channel, acc.chatID, ""),
			Content: "Goodbye! Leaving the voice channel.",
		}); err != nil {
			logger.ErrorCF("voice-agent", "Failed to publish goodbye message", map[string]any{"error": err})
		}
		return
	}

	oralPrompt := "\n\n[SYSTEM]: The user just spoke this to you over voice chat. Please reply in a highly concise, conversational, oral style suitable for text-to-speech. Do not use markdown, emojis, asterisks, or code blocks. Speak naturally."

	if err := a.bus.PublishInbound(ctx, bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  acc.channel,
			ChatID:   acc.chatID,
			ChatType: "channel",
			SenderID: acc.speakerID,
			Raw: map[string]string{
				"is_voice": "true",
			},
		},
		Content: res.Text + oralPrompt,
	}); err != nil {
		logger.ErrorCF("voice-agent", "Failed to publish inbound message", map[string]any{"error": err})
	}
}
