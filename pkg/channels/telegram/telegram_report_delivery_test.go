package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

// Exercise the real HTTP serializer and channel adapter, not only a retained
// task report or a message-bus delivery notification. A blocked workflow must
// retain its explanation after the first paragraph on every presentation path.
func TestBlockedReportPreservesEveryParagraphAtTelegramTransport(t *testing.T) {
	paragraphs := []string{
		"Проверены источник и назначение. Найдены три фотографии, но перенос не выполнен.",
		"- transfer-artifact://photo_1\n- transfer-artifact://photo_2\n- transfer-artifact://photo_3",
		"Сессия закрыта. Изменений в объявлении не было.",
		"Проверка результата в новой сессии не выполнялась, потому что сохранения не было.",
		"Первый блокер: получение оригинала отклонено. Нужна проверка доступной операции, а не повтор публикации.",
		"END-OF-REPORT",
	}
	content := strings.Join(paragraphs, "\n\n")
	for _, test := range []struct {
		name     string
		rich     bool
		fallback bool
		edit     bool
		stream   bool
	}{
		{name: "legacy send"},
		{name: "rich send", rich: true},
		{name: "rich fallback", rich: true, fallback: true},
		{name: "legacy edit", edit: true},
		{name: "rich edit", rich: true, edit: true},
		{name: "rich stream final", rich: true, stream: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			type request struct {
				method  string
				payload map[string]json.RawMessage
			}
			requests := make(chan request, 8)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("decode Telegram request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests <- request{method: r.URL.Path, payload: payload}
				w.Header().Set("Content-Type", "application/json")
				if test.fallback && strings.HasSuffix(r.URL.Path, "/sendRichMessage") {
					_, _ = w.Write(
						[]byte(
							`{"ok":false,"error_code":400,"description":"Bad Request: rich message is not supported"}`,
						),
					)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok": true, "result": map[string]any{"message_id": 71, "text": content},
				})
			}))
			defer server.Close()
			bot, err := telego.NewBot(testToken,
				telego.WithAPIServer(server.URL), telego.WithHTTPClient(server.Client()), telego.WithDiscardLogger(),
			)
			require.NoError(t, err)
			channel := newTestChannel(t, &stubCaller{})
			channel.bot = bot
			channel.tgCfg.RichMessages.Enabled = test.rich
			channel.tgCfg.Streaming.Enabled = test.stream
			switch {
			case test.stream:
				streamer, beginErr := channel.BeginStream(context.Background(), "12345")
				require.NoError(t, beginErr)
				require.NoError(t, streamer.Finalize(context.Background(), content))
			case test.edit:
				require.NoError(t, channel.EditMessage(context.Background(), "12345", "71", content))
			default:
				result := channel.DeliverText(context.Background(), []bus.OutboundMessage{{
					ChatID: "12345", Content: content,
					Metadata: bus.OutboundMetadata{OutboundKind: bus.OutboundKindFinal},
				}})
				require.NoError(t, result.Err)
				require.True(t, result.Delivered())
				require.Equal(t, []string{"71"}, result.MessageIDs)
			}
			count := 1
			if test.fallback {
				count++
			}
			for range count {
				select {
				case request := <-requests:
					var text string
					if raw := request.payload["rich_message"]; raw != nil {
						var rich telego.InputRichMessage
						require.NoError(t, json.Unmarshal(raw, &rich))
						text = rich.Markdown
						require.Equal(t, content, text, request.method)
					} else {
						require.NoError(t, json.Unmarshal(request.payload["text"], &text))
					}
					for _, paragraph := range paragraphs {
						for _, line := range strings.Split(paragraph, "\n") {
							require.Contains(t, text, strings.TrimPrefix(line, "- "), request.method)
						}
					}
				default:
					t.Fatal("missing Telegram transport request")
				}
			}
			require.Empty(t, requests, "unexpected duplicate Telegram delivery")
		})
	}
}
