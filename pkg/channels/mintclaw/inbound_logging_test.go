package mintclaw

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func TestHandleMessageSendLogsMetadataNotContent(t *testing.T) {
	previousLevel := logger.GetLevel()
	logger.SetLevel(logger.DEBUG)
	t.Cleanup(func() { logger.SetLevel(previousLevel) })
	logPath := filepath.Join(t.TempDir(), "inbound.jsonl")
	if err := logger.EnableFileLogging(logPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.DisableFileLogging)

	messageBus := bus.NewMessageBus()
	channelConfig := &config.Channel{
		Type: config.ChannelMintClaw, Enabled: true, AllowFrom: []string{"mintclaw-user"},
	}
	settings := &config.MintClawSettings{}
	settings.SetToken("test-token")
	channel, err := NewMintClawChannel(channelConfig, settings, messageBus)
	if err != nil {
		t.Fatal(err)
	}
	channel.ctx = context.Background()
	content := "PDFI4_PRIVATE_INPUT_SENTINEL_" + strings.Repeat("x", 80)
	channel.handleMessageSend(&mintclawConn{id: "conn-private", sessionID: "sess-private"}, MintClawMessage{
		Type: TypeMessageSend, ID: "msg-private", SessionID: "sess-private",
		Payload: map[string]any{PayloadKeyContent: content},
	})
	select {
	case inbound := <-messageBus.InboundChan():
		if inbound.Content != content || inbound.Context.ClientSessionID != "sess-private" {
			t.Fatal("logging changed the input or its session provenance")
		}
	case <-time.After(time.Second):
		t.Fatal("inbound message was not admitted")
	}

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), content[:40]) || strings.Contains(string(logged), `"preview"`) {
		t.Fatal("pre-admission debug logging retained an input-body preview")
	}
	if !strings.Contains(string(logged), fmt.Sprintf(`"content_bytes":%d`, len(content))) ||
		!strings.Contains(string(logged), `"session_id":"sess-private"`) {
		t.Fatal("inbound diagnostic metadata was not retained")
	}
}
