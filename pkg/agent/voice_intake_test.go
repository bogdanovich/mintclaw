package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/audio/asr"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
)

type intakeTranscriber struct {
	result *asr.TranscriptionResponse
	err    error
}

type partialIntakeTranscriber struct{ calls int }

func (s *partialIntakeTranscriber) Name() string { return "partial-intake-test" }
func (s *partialIntakeTranscriber) Transcribe(context.Context, string) (*asr.TranscriptionResponse, error) {
	s.calls++
	if s.calls == 1 {
		return &asr.TranscriptionResponse{Text: "first clip"}, nil
	}
	return nil, errors.New("second clip failed")
}

func TestVoiceIntakeRejectsPartialTranscription(t *testing.T) {
	al, cleanup := newInboundDispatchTestLoop(t)
	defer cleanup()
	msg := voiceIntakeMessage(t, al)
	msg.Content = "[voice]\n[voice]"
	msg.Media = append(msg.Media, msg.Media[0])
	al.SetTranscriber(&partialIntakeTranscriber{})
	_, err := al.buildInboundMessageTurn(t.Context(), msg)
	if err == nil {
		t.Fatal("partially transcribed request reached dispatch")
	}
	if strings.Contains(err.Error(), "second clip") {
		t.Fatal("provider error exposed")
	}
}

func TestVoiceIntakeSystemAndScheduledFailure(t *testing.T) {
	for _, mode := range []string{"system", "scheduled"} {
		t.Run(mode, func(t *testing.T) {
			al, cleanup := newInboundDispatchTestLoop(t)
			defer cleanup()
			msg := voiceIntakeMessage(t, al)
			al.SetTranscriber(intakeTranscriber{err: errors.New("provider failure")})
			var err error
			if mode == "system" {
				msg.Context.Channel = "system"
				_, err = al.buildInboundMessageTurn(t.Context(), msg)
			} else {
				_, _, err = al.processScheduledMessage(t.Context(), msg)
			}
			if !errors.Is(err, errVoiceTranscriptionFailed) {
				t.Fatalf("unexpected preparation error: %v", err)
			}
			if formatUserFacingAgentError(err) != voiceTranscriptionFailureNotice {
				t.Fatal("unsafe error format")
			}
		})
	}
}

func TestVoiceIntakeNativeGemini(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "gemini-3.5-flash-lite:generateContent") {
			t.Errorf("unexpected transcription route: %s", r.URL.Path)
		}
		var request struct {
			Contents []struct {
				Parts []struct {
					InlineData *struct {
						MIMEType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"contents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		found := false
		for _, content := range request.Contents {
			for _, part := range content.Parts {
				if part.InlineData != nil {
					data, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
					found = err == nil && string(data) == "synthetic audio" && part.InlineData.MIMEType == "audio/ogg"
				}
			}
		}
		if !found {
			t.Error("native transcription did not carry audio bytes")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(
			[]byte(
				`{"candidates":[{"content":{"role":"model","parts":[{"text":"synthetic transcript"}]},"finishReason":"STOP"}]}`,
			),
		)
	}))
	defer server.Close()
	al, cleanup := newInboundDispatchTestLoop(t)
	defer cleanup()
	msg := voiceIntakeMessage(t, al)
	transcriber := asr.DetectTranscriber(&config.Config{
		Voice: config.VoiceConfig{ModelName: "voice-test"},
		ModelList: []*config.ModelConfig{{
			ModelName: "voice-test", Provider: "gemini", Model: "gemini-3.5-flash-lite",
			APIBase: server.URL, APIKeys: config.SimpleSecureStrings("test-key"), Enabled: true,
		}},
	})
	if transcriber == nil {
		t.Fatal("configured Gemini ASR was not recognized")
	}
	al.SetTranscriber(transcriber)
	turn, err := al.buildInboundMessageTurn(t.Context(), msg)
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Cleanup()
	if !strings.Contains(turn.Message.Content, "[voice: synthetic transcript]") {
		t.Fatalf("transcript missing before model dispatch: %q", turn.Message.Content)
	}
}

func (s intakeTranscriber) Name() string { return "intake-test" }
func (s intakeTranscriber) Transcribe(context.Context, string) (*asr.TranscriptionResponse, error) {
	return s.result, s.err
}

func voiceIntakeMessage(t *testing.T, al *AgentLoop) bus.InboundMessage {
	t.Helper()
	store := media.NewFileMediaStore()
	path := filepath.Join(t.TempDir(), "voice.ogg")
	if err := os.WriteFile(path, []byte("synthetic audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{
		Filename: "voice.ogg", ContentType: "audio/ogg", CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "voice-test")
	if err != nil {
		t.Fatal(err)
	}
	al.SetMediaStore(store)
	msg := finalResponseAdmissionInboundMessage("voice-spool")
	msg.Content, msg.Media = "[voice]", []string{ref}
	return msg
}

func TestVoiceIntakeRejectsIncompleteTranscription(t *testing.T) {
	for _, tc := range []struct {
		name        string
		transcriber intakeTranscriber
	}{
		{"provider error", intakeTranscriber{err: errors.New("401 private provider details")}},
		{"nil result", intakeTranscriber{}},
		{"empty result", intakeTranscriber{result: &asr.TranscriptionResponse{Text: "  "}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			al, cleanup := newInboundDispatchTestLoop(t)
			defer cleanup()
			al.SetTranscriber(tc.transcriber)
			msg := voiceIntakeMessage(t, al)
			_, err := al.buildInboundMessageTurn(t.Context(), msg)
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("failed transcription must stop intake with a safe error, got %v", err)
			}
		})
	}
}

func TestVoiceIntakeSuccessAndDisabledASR(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		al, cleanup := newInboundDispatchTestLoop(t)
		msg := voiceIntakeMessage(t, al)
		if enabled {
			al.SetTranscriber(intakeTranscriber{result: &asr.TranscriptionResponse{Text: "test voice"}})
		}
		turn, err := al.buildInboundMessageTurn(t.Context(), msg)
		if err != nil {
			t.Fatal(err)
		}
		if enabled && !strings.Contains(turn.Message.Content, "[voice: test voice]") {
			t.Fatalf("missing native transcript: %q", turn.Message.Content)
		}
		turn.Cleanup()
		cleanup()
	}
}

func TestVoiceIntakeFailureAcknowledgesOnlyAfterNoticeAdmission(t *testing.T) {
	for _, mode := range []string{"normal", "busy", "deferred"} {
		for _, rejectNotice := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/accepted", true: "/rejected"}[rejectNotice], func(t *testing.T) {
				provider := &countingAdmissionProvider{}
				al, _, cleanup := newTurnCoordTestLoop(t, provider)
				defer cleanup()
				tracking := &finalResponseAdmissionTestBus{MessageBus: al.bus.(*bus.MessageBus)}
				if rejectNotice {
					tracking.publishErr = errors.New("notice unavailable")
				}
				setTestMessageBus(al, tracking)
				msg := voiceIntakeMessage(t, al)
				al.SetTranscriber(intakeTranscriber{err: errors.New("401 secret")})
				coordinator, target, claim := prepareFinalResponseAdmissionTurn(t, al, msg, "")
				if mode != "normal" {
					defer claim.releaseIfOwned()
				}
				switch mode {
				case "normal":
					coordinator.runWorker(t.Context(), msg, target, claim)
				case "busy":
					coordinator.handleBusySession(t.Context(), msg, target)
				case "deferred":
					coordinator.deferInteractionInbound(t.Context(), msg, target)
				}
				acked, released, _ := tracking.ownership()
				if rejectNotice {
					if len(acked) != 0 || len(released) != 1 {
						t.Fatalf("ownership: %v %v", acked, released)
					}
				} else {
					if len(acked) != 1 || len(released) != 0 {
						t.Fatalf("ownership: %v %v", acked, released)
					}
					select {
					case notice := <-tracking.OutboundChan():
						if !strings.Contains(notice.Content, "transcribe") ||
							strings.Contains(notice.Content, "secret") {
							t.Fatalf("unsafe or missing notice: %q", notice.Content)
						}
					default:
						t.Fatal("missing failure notice")
					}
				}
				if provider.calls != 0 || al.pendingSteeringCountForScope(target.runtimeSessionScope()) != 0 {
					t.Fatal("failed voice reached model or steering queue")
				}
			})
		}
	}
}
