package agent

import (
	"context"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func TestNativeFinalRetentionRequiresConfiguredOwnerAndDurableSource(t *testing.T) {
	for _, tc := range []struct {
		name, channel, channelType string
		native, ordinary           bool
		want                       string
	}{
		{"telegram_alias", "telegram_personal", config.ChannelTelegram, true, true, "retired"},
		{"eval_alias_named_telegram", "telegram", config.ChannelMintClaw, true, true, "protected_class"},
		{"native_eval", "mintclaw", config.ChannelMintClaw, true, true, "protected_class"},
		{"unknown_channel", "unconfigured", "", true, true, "protected_class"},
		{"unspooled", "telegram", config.ChannelTelegram, false, true, "protected_class"},
		{"generic_publication", "telegram", config.ChannelTelegram, true, false, "protected_class"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentLoopTestFixture(t, &simpleMockProvider{response: "unused"}, func(cfg *config.Config) {
				cfg.Channels = make(config.ChannelsConfig)
				if tc.channelType != "" {
					cfg.Channels[tc.channel] = &config.Channel{Type: tc.channelType, Enabled: true}
				}
			})
			root := t.TempDir()
			installTestOutboundCoordinator(t, f.Loop, root)
			msg := bus.NormalizeInboundMessage(bus.InboundMessage{
				Context: bus.InboundContext{
					Channel:  tc.channel,
					ChatID:   "native-chat",
					ChatType: "direct",
					SenderID: "native-owner",
				},
				Content:    "input",
				SessionKey: "native-retention-session",
			})
			if tc.native {
				spool, err := bus.NewInboundSpool(bus.InboundSpoolPath(root))
				if err != nil {
					t.Fatal(err)
				}
				f.Bus.SetInboundSpool(spool)
				msg, err = spool.Prepare(t.Context(), msg)
				if err != nil {
					t.Fatal(err)
				}
			}
			source := msg.SpoolID
			if source == "" {
				source = "programmatic-without-native-proof"
			}
			ctx := withOutboundTransaction(t.Context(), source)
			var admission finalResponseAdmission
			if tc.ordinary {
				admission = f.Loop.runTurnAndDrainSteering(
					ctx,
					msg,
					func() (string, error) { return "ordinary final reply", nil },
					&continuationTarget{
						Workspace: f.Agent.Workspace, AgentID: f.Agent.ID, SessionKey: msg.SessionKey,
						Channel: msg.Context.Channel, ChatID: msg.Context.ChatID,
					},
				)
			} else {
				publication, err := f.Loop.publishTransactionMessageReceiptAtBoundary(
					ctx,
					f.Agent.Workspace,
					bus.OutboundMessage{
						Channel:    msg.Context.Channel,
						ChatID:     msg.Context.ChatID,
						SessionKey: msg.SessionKey,
						Content:    "generic final-shaped text",
						Metadata:   bus.OutboundMetadata{MessageKind: bus.OutboundMessageKindFinalReply},
					},
					nil,
				)
				if err != nil {
					t.Fatal(err)
				}
				admission = finalResponseAdmission{status: finalResponseAdmissionAccepted, publication: publication}
			}
			if !admission.permitsInboundAck() {
				t.Fatalf("final admission=%#v", admission)
			}
			var outbound bus.OutboundMessage
			select {
			case outbound = <-f.Bus.OutboundChan():
			default:
				t.Fatal("missing outbound")
			}
			c := f.Loop.outboundCoordinator()
			if err := c.BeginAttempt(outbound.DeliveryID); err != nil {
				t.Fatal(err)
			}
			if err := c.MarkDelivered(outbound.DeliveryID, outbox.Outcome{}); err != nil {
				t.Fatal(err)
			}
			if err := f.Loop.settleInboundAdmission(ctx, msg, admission); err != nil {
				t.Fatal(err)
			}
			if err := f.Loop.closeOutboundOutbox(); err != nil {
				t.Fatal(err)
			}
			report, err := outbox.RetirePayloadsOffline(t.Context(), root, outbox.RetirementPolicy{
				DeliveryIDs: []string{outbound.DeliveryID}, MinAge: time.Nanosecond, Apply: true,
			})
			if err != nil || len(report.Items) != 1 || report.Items[0].Outcome != tc.want {
				t.Fatalf("owner retirement=%#v,%v", report, err)
			}
		})
	}
}

type retentionReleaseTrackingBus struct {
	*bus.MessageBus
	releases int
}

func (b *retentionReleaseTrackingBus) ReleaseInbound(ctx context.Context, msg bus.InboundMessage, cause error) error {
	b.releases++
	return b.MessageBus.ReleaseInbound(ctx, msg, cause)
}

func TestNativeFinalSealFailureNeverReleasesAcknowledgedSource(t *testing.T) {
	f := newAgentLoopTestFixture(t, &simpleMockProvider{response: "unused"}, func(cfg *config.Config) {
		cfg.Channels = config.ChannelsConfig{"telegram": &config.Channel{Type: config.ChannelTelegram, Enabled: true}}
	})
	root := t.TempDir()
	installTestOutboundCoordinator(t, f.Loop, root)
	spool, err := bus.NewInboundSpool(bus.InboundSpoolPath(root))
	if err != nil {
		t.Fatal(err)
	}
	f.Bus.SetInboundSpool(spool)
	tracked := &retentionReleaseTrackingBus{MessageBus: f.Bus}
	f.Loop.bus = tracked
	f.Loop.turns.inbound.bus = tracked
	msg, err := spool.Prepare(t.Context(), bus.NormalizeInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{Channel: "telegram", ChatID: "chat", ChatType: "direct", SenderID: "owner"},
		Content: "input", SessionKey: "native-seal-failure",
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := withOutboundTransaction(t.Context(), msg.SpoolID)
	admission := f.Loop.runTurnAndDrainSteering(
		ctx,
		msg,
		func() (string, error) { return "final reply", nil },
		&continuationTarget{
			Workspace:  f.Agent.Workspace,
			AgentID:    f.Agent.ID,
			SessionKey: msg.SessionKey,
			Channel:    "telegram",
			ChatID:     "chat",
		},
	)
	if !admission.permitsInboundAck() || admission.publication.deliveryID == "" {
		t.Fatalf("final=%#v", admission)
	}
	// Closing the outbox forces the subsequent seal to fail after native Ack.
	if err = f.Loop.outboundCoordinator().Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Loop.settleInboundAdmission(ctx, msg, admission); err != nil {
		t.Fatal(err)
	}
	if tracked.releases != 0 {
		t.Fatal("source released after durable acknowledgement")
	}
	pending, err := spool.Pending(t.Context(), 0)
	if err != nil || len(pending) != 0 {
		t.Fatalf("source remains replayable=%#v,%v", pending, err)
	}
}
