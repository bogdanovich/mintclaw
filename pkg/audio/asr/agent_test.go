package asr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v3/pkg/media/oggwriter"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

type fakeTranscriber struct {
	text     string
	err      error
	lastPath string
}

type blockingTranscriber struct {
	started chan struct{}
	release chan struct{}
}

type contextAwareBlockingTranscriber struct {
	started  chan struct{}
	release  chan struct{}
	canceled chan struct{}
}

func (transcriber *contextAwareBlockingTranscriber) Name() string { return "context-aware-blocking" }

func (transcriber *contextAwareBlockingTranscriber) Transcribe(
	ctx context.Context,
	_ string,
) (*TranscriptionResponse, error) {
	close(transcriber.started)
	select {
	case <-transcriber.release:
		return &TranscriptionResponse{Text: "pending speech"}, nil
	case <-ctx.Done():
		close(transcriber.canceled)
		return nil, ctx.Err()
	}
}

func (transcriber *blockingTranscriber) Name() string { return "blocking" }

func (transcriber *blockingTranscriber) Transcribe(
	context.Context,
	string,
) (*TranscriptionResponse, error) {
	close(transcriber.started)
	<-transcriber.release
	return &TranscriptionResponse{Text: "done"}, nil
}

func (f *fakeTranscriber) Name() string { return "fake" }

func (f *fakeTranscriber) Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error) {
	f.lastPath = audioFilePath
	if f.err != nil {
		return nil, f.err
	}
	return &TranscriptionResponse{Text: f.text}, nil
}

func waitForFileRemoval(t *testing.T, path string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected file to be removed: %s", path)
	}
}

func TestAgentStartWithDoneWaitsForShutdown(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	defer mb.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := NewAgent(mb, &fakeTranscriber{}).StartWithDone(ctx)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("voice agent did not finish after cancellation")
	}
}

func TestAgentStartWithDoneDrainsInFlightUtterance(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	defer mb.Close()
	transcriber := &blockingTranscriber{started: make(chan struct{}), release: make(chan struct{})}
	agent := NewAgent(mb, transcriber)
	ctx, cancel := context.WithCancel(context.Background())
	done := agent.StartWithDone(ctx)

	agent.handleChunk(bus.AudioChunk{
		SessionID: "session", SpeakerID: "speaker", ChatID: "chat", Channel: "discord",
		SampleRate: 48000, Channels: 2, Format: "opus", Data: []byte{0xF8, 0xFF, 0xFE},
	})
	agent.mu.Lock()
	for _, accumulator := range agent.sessions {
		accumulator.mu.Lock()
		accumulator.lastAudioAt = time.Now().Add(-2 * time.Second)
		accumulator.mu.Unlock()
	}
	agent.mu.Unlock()
	agent.checkSilence(ctx)
	<-transcriber.started

	cancel()
	select {
	case <-done:
		t.Fatal("voice agent stopped before its in-flight utterance drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(transcriber.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("voice agent did not stop after its in-flight utterance drained")
	}
}

func TestAgentStartWithDoneFinalizesPendingSpeechWithoutCancelingDrain(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	defer mb.Close()
	transcriber := &contextAwareBlockingTranscriber{
		started: make(chan struct{}), release: make(chan struct{}), canceled: make(chan struct{}),
	}
	agent := NewAgent(mb, transcriber)
	ctx, cancel := context.WithCancel(context.Background())
	done := agent.StartWithDone(ctx)
	agent.handleChunk(bus.AudioChunk{
		SessionID: "session", SpeakerID: "speaker", ChatID: "chat", Channel: "discord",
		SampleRate: 48000, Channels: 2, Format: "opus", Data: []byte{0xF8, 0xFF, 0xFE},
	})

	cancel()
	select {
	case <-transcriber.started:
	case <-time.After(time.Second):
		t.Fatal("pending recording was not finalized for transcription")
	}
	select {
	case <-transcriber.canceled:
		t.Fatal("intake cancellation propagated into the drain context")
	default:
	}
	close(transcriber.release)
	select {
	case msg := <-mb.InboundChan():
		if !strings.Contains(msg.Content, "pending speech") {
			t.Fatalf("unexpected drained voice message: %q", msg.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("drained pending speech was not published")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("voice agent did not finish after pending speech drained")
	}
}

func TestAgentHandleChunkCreatesSession(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	defer mb.Close()

	agent := NewAgent(mb, &fakeTranscriber{})

	chunk := bus.AudioChunk{
		SessionID:  "sess",
		SpeakerID:  "speaker",
		ChatID:     "chat",
		Channel:    "discord",
		Sequence:   1,
		Timestamp:  1,
		SampleRate: 48000,
		Channels:   2,
		Format:     "opus",
		Data:       []byte{0xF8, 0xFF, 0xFE},
	}

	agent.handleChunk(chunk)

	key := "sess_speaker"
	agent.mu.Lock()
	acc, ok := agent.sessions[key]
	agent.mu.Unlock()
	if !ok {
		t.Fatal("expected session to be created")
	}

	_ = acc.Close()
	_ = os.Remove(acc.file)
}

func TestAgentHandleChunkIgnoresUnsupportedFormat(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	defer mb.Close()

	agent := NewAgent(mb, &fakeTranscriber{})

	chunk := bus.AudioChunk{Format: "pcm"}
	agent.handleChunk(chunk)

	agent.mu.Lock()
	count := len(agent.sessions)
	agent.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected no sessions, got %d", count)
	}
}

func TestAgentProcessUtteranceLeaveCommand(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	defer mb.Close()

	tr := &fakeTranscriber{text: "please leave the voice channel now"}
	agent := NewAgent(mb, tr)

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "voice.ogg")
	if err := os.WriteFile(filePath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	acc := &speechAccumulator{
		file:      filePath,
		chatID:    "chat",
		speakerID: "speaker",
		sessionID: "sess",
		channel:   "discord",
	}

	agent.processUtterance(context.Background(), acc)

	select {
	case ctrl := <-mb.VoiceControlsChan():
		if ctrl.Action != "leave" || ctrl.Type != "command" || ctrl.SessionID != "sess" {
			t.Fatalf("unexpected voice control: %#v", ctrl)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected voice control publish")
	}

	select {
	case out := <-mb.OutboundChan():
		if !strings.Contains(out.Content, "Leaving the voice channel") {
			t.Fatalf("unexpected outbound content: %q", out.Content)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected outbound publish")
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("expected temp file to be removed")
	}
}

func TestAgentCheckSilencePublishesInboundAndCleansUp(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	defer mb.Close()

	tr := &fakeTranscriber{text: "hello there"}
	agent := NewAgent(mb, tr)

	filePath := filepath.Join(t.TempDir(), "voice.ogg")
	writer, err := oggwriter.New(filePath, 48000, 2)
	if err != nil {
		t.Fatalf("create ogg writer: %v", err)
	}

	acc := &speechAccumulator{
		writer:      writer,
		file:        filePath,
		lastAudioAt: time.Now().Add(-2 * time.Second),
		chatID:      "chat",
		speakerID:   "speaker",
		sessionID:   "sess",
		channel:     "slack",
	}

	agent.mu.Lock()
	agent.sessions["sess_speaker"] = acc
	agent.mu.Unlock()

	agent.checkSilence(context.Background())

	select {
	case msg := <-mb.InboundChan():
		if msg.Context.Channel != "slack" {
			t.Fatalf("unexpected inbound channel: %q", msg.Context.Channel)
		}
		if !strings.Contains(msg.Content, "hello there") {
			t.Fatalf("unexpected inbound content: %q", msg.Content)
		}
		if msg.Context.Raw["is_voice"] != "true" {
			t.Fatalf("expected is_voice metadata, got %#v", msg.Context.Raw)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected inbound publish")
	}

	waitForFileRemoval(t, filePath, 500*time.Millisecond)
}
