package channels

import (
	"context"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

type registryTestSettings struct {
	Value string `json:"value"`
}

type registryTestChannel struct {
	*BaseChannel
}

func (c *registryTestChannel) Start(context.Context) error { return nil }

func (c *registryTestChannel) Stop(context.Context) error { return nil }

func (c *registryTestChannel) DeliverText(
	context.Context,
	[]bus.OutboundMessage,
) DeliveryResult[bus.OutboundMessage] {
	return SuccessfulDelivery[bus.OutboundMessage](nil)
}

func TestRegisterTypedFactory(t *testing.T) {
	const channelType = "registry-typed-test"
	t.Cleanup(func() {
		factoriesMu.Lock()
		delete(factories, channelType)
		factoriesMu.Unlock()
	})

	RegisterTypedFactory(
		channelType,
		func(bc *config.Channel, settings *registryTestSettings, messageBus *bus.MessageBus) (*registryTestChannel, error) {
			return &registryTestChannel{BaseChannel: NewBaseChannel(bc.Name(), settings, messageBus, nil)}, nil
		},
	)
	factory, ok := getFactory(channelType)
	if !ok {
		t.Fatal("typed factory was not registered")
	}

	channelConfig := &config.Channel{
		Enabled:  true,
		Type:     channelType,
		Settings: config.RawNode(`{"value":"current"}`),
	}
	channelConfig.SetName("typed_alias")
	settings := &registryTestSettings{}
	if err := channelConfig.Decode(settings); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Channels["typed_alias"] = channelConfig

	channel, err := factory("typed_alias", cfg, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("factory() error = %v", err)
	}
	if channel.Name() != "typed_alias" || settings.Value != "current" {
		t.Fatalf("factory result name=%q settings=%+v", channel.Name(), settings)
	}

	if _, err := factory(
		"missing",
		cfg,
		bus.NewMessageBus(),
	); err == nil ||
		!strings.Contains(err.Error(), "config not found") {
		t.Fatalf("missing config error = %v", err)
	}

	wrong := &config.Channel{Enabled: true, Type: channelType, Settings: config.RawNode(`{}`)}
	wrong.SetName("wrong")
	if err := wrong.Decode(&struct{}{}); err != nil {
		t.Fatalf("wrong Decode() error = %v", err)
	}
	cfg.Channels["wrong"] = wrong
	if _, err := factory("wrong", cfg, bus.NewMessageBus()); err == nil || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("wrong settings error = %v", err)
	}
}
