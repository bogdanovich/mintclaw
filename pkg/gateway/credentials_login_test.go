package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestCredentialLoginProfileAdmissionExcludesUnqualifiedPlacementsBeforePhoneRequest(t *testing.T) {
	for _, mode := range []string{"local", "managed", "mcp", "node", "cloud", "steel", "restricted", "dry-run"} {
		t.Run(mode, func(t *testing.T) {
			cfg := gatewayBrowserConfig(t.TempDir())
			target := cfg.Tools.Browser.Targets[config.BrowserDefaultTarget]
			target.Driver = config.BrowserDriverPlaywrightLibrary
			profile := target.Profiles[config.BrowserDefaultProfile]
			profile.Mode, profile.DryRun, profile.AllowApprovedActions = config.BrowserProfileEphemeral, false, true
			switch mode {
			case "managed":
				profile.Mode = config.BrowserProfileManaged
			case "mcp":
				target.Driver = config.BrowserDriverPlaywrightMCP
			case "node":
				target.Placement = config.BrowserPlacementNode
			case "cloud":
				target.Placement = config.BrowserPlacementCloud
			case "steel":
				target.Provider = config.BrowserProviderSteel
			case "restricted":
				profile.CapabilityMode = config.BrowserCapabilityRestricted
			case "dry-run":
				profile.DryRun = true
			}
			target.Profiles[config.BrowserDefaultProfile] = profile
			cfg.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
			profiles := browserCredentialProfiles(cfg.Tools.Browser)
			if (len(profiles) == 1) != (mode == "local") {
				t.Fatal("credential tool advertised an unqualified profile")
			}
		})
	}
}

func TestCredentialNotificationUsesExistingAuthenticatedRouteAndGenericContent(t *testing.T) {
	messageBus := bus.NewMessageBus()
	t.Cleanup(messageBus.Close)
	ctx := toolshared.WithToolInboundMetadata(t.Context(), bus.InboundContext{
		Channel: "telegram", ChatID: "owner-chat", TopicID: "owner-topic", Account: "primary",
		MessageID: "request-message", ActorID: "owner", SenderID: "owner",
		Raw: map[string]string{"private-input": "notification-must-not-copy-raw-H56"},
	})
	ctx = toolshared.WithToolRouteSessionKey(ctx, "owner-session")
	link := "https://approval.example.org/credentials/#request=" + strings.Repeat("a", 64)
	if credentialNotificationPublisher(messageBus)(ctx, link) != nil {
		t.Fatal("generic phone notification was not admitted to the existing outbound queue")
	}
	select {
	case message := <-messageBus.OutboundChan():
		encoded, err := json.Marshal(message)
		if err != nil || strings.Contains(string(encoded), "notification-must-not-copy-raw-H56") ||
			!strings.Contains(message.Content, link) || strings.Contains(message.Content, "facebook") ||
			message.Context.Channel != "telegram" || message.Context.ChatID != "owner-chat" ||
			message.Context.TopicID != "owner-topic" || message.Context.Account != "primary" ||
			message.Context.ReplyToMessageID != "request-message" || message.SessionKey != "owner-session" {
			t.Fatal("phone notification lost its authenticated route or copied private inbound data")
		}
	case <-time.After(time.Second):
		t.Fatal("phone notification was not queued")
	}
	if credentialNotificationPublisher(messageBus)(t.Context(), link) == nil ||
		credentialNotificationPublisher(nil)(ctx, link) == nil {
		t.Fatal("phone notification invented a route or dispatcher")
	}
}
