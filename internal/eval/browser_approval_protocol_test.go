package eval

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

func TestBrowserApprovalCrossesNativeChannelWithoutRelaxingTypedContent(t *testing.T) {
	c := newBrowserAmbiguityControl(t)
	c.answer(t)
	_, _, prompt := c.prepare(t)
	if err := c.domain.observeApproval(t.Context(), prompt); err != nil {
		t.Fatal(err)
	}
	request, handled, err := c.domain.request(t.Context(), Action{Kind: "allow_once"}, prompt)
	if err != nil || !handled {
		t.Fatal("native-bound approval request unavailable", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cfg := c.fixture.cfg
	settings, err := gatewayclient.ChannelSettings(cfg)
	if err != nil {
		t.Fatal(err)
	}
	messageBus := bus.NewMessageBus()
	channel, err := protocol.NewMintClawChannel(cfg.Channels.GetByType("mintclaw"), settings, messageBus)
	if err != nil || channel.Start(ctx) != nil {
		t.Fatal("native channel unavailable", err)
	}
	defer func() { _ = channel.Stop(context.Background()) }()
	server := httptest.NewServer(channel)
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Gateway.Host, cfg.Gateway.Port = endpoint.Hostname(), port
	client, err := gatewayclient.Connect(ctx, cfg, request.SessionID, gatewayclient.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	old := request
	old.ID, old.Payload = uuid.NewString(), make(map[string]any, len(request.Payload))
	for key, value := range request.Payload {
		old.Payload[key] = value
	}
	old.Payload[protocol.PayloadKeyContent] = "Allow this clarified South account removal once."
	if err = client.Send(ctx, old); err != nil {
		t.Fatal(err)
	}
	rejected, err := client.Receive(ctx)
	if err != nil || rejected.Type != protocol.TypeError ||
		payloadString(rejected.Payload, "code") != "invalid_interaction" ||
		payloadString(rejected.Payload, protocol.PayloadKeyRequestID) != old.ID {
		t.Fatal("native channel did not reject the old mismatched action", err)
	}
	select {
	case <-messageBus.InboundChan():
		t.Fatal("invalid typed action reached the inbound bus")
	default:
	}
	if err = client.Send(ctx, request); err != nil {
		t.Fatal(err)
	}
	select {
	case inbound := <-messageBus.InboundChan():
		interaction := inbound.Context.Interaction
		if inbound.Content != "Allow once" || inbound.Context.MessageID != request.ID ||
			inbound.Context.ClientSessionID != request.SessionID ||
			inbound.Context.ReplyToMessageID != prompt.MessageID ||
			interaction.Choice != bus.InboundInteractionChoiceAllowOnce || interaction.Response != "Allow once" ||
			interaction.ShortID != prompt.InteractionShortID || interaction.ResponseMessageID != prompt.MessageID {
			t.Fatal("canonical approval lost native action or prompt identity")
		}
	case <-ctx.Done():
		t.Fatal("harness approval did not reach the native channel", ctx.Err())
	}
}
