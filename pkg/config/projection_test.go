package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type privateProjectionPayload struct {
	aliases []string
	secret  SecureString
}

type privateProjectionMapKey struct {
	secret SecureString
}

type privateProjectionMap map[*privateProjectionMapKey]string

func (m privateProjectionMap) MarshalJSON() ([]byte, error) {
	secrets := make([]string, 0, len(m))
	for key := range m {
		secrets = append(secrets, key.secret.String())
	}
	return json.Marshal(secrets)
}

func (p *privateProjectionPayload) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"aliases": p.aliases,
		"secret":  p.secret.String(),
	})
}

func TestProjectPublicConfigRejectsPrivateCustomMarshalerState(t *testing.T) {
	payload := &privateProjectionPayload{
		aliases: []string{"source-alias"},
		secret:  *NewSecureString("private-projection-secret"),
	}
	unsafeJSON, err := json.Marshal(payload)
	if err != nil || !strings.Contains(string(unsafeJSON), "private-projection-secret") {
		t.Fatalf("custom marshaler test setup did not expose private state: %s, %v", unsafeJSON, err)
	}
	cfg := DefaultConfig()
	cfg.ModelList[0].ExtraBody = map[string]any{"payload": payload}

	if _, err = ProjectPublicConfig(cfg); err == nil || !strings.Contains(err.Error(), "private field") {
		t.Fatalf("ProjectPublicConfig() error = %v, want private-state rejection", err)
	}
	if payload.aliases[0] != "source-alias" || payload.secret.String() != "private-projection-secret" {
		t.Fatal("failed public projection mutated private source state")
	}
}

func TestProjectPublicConfigClonesInterfaceCollections(t *testing.T) {
	sourceValues := []any{map[string]any{"value": "source"}}
	cfg := DefaultConfig()
	cfg.ModelList[0].ExtraBody = map[string]any{"values": sourceValues}

	projected, err := ProjectPublicConfig(cfg)
	if err != nil {
		t.Fatalf("ProjectPublicConfig() error = %v", err)
	}
	projectedValues := projected.ModelList[0].ExtraBody["values"].([]any)
	projectedValues[0].(map[string]any)["value"] = "changed"
	if got := sourceValues[0].(map[string]any)["value"]; got != "source" {
		t.Fatalf("source interface collection value = %v, want source", got)
	}
}

func TestProjectPublicConfigRejectsSecretBearingCustomMapKey(t *testing.T) {
	secret := "private-map-key-secret"
	payload := privateProjectionMap{
		&privateProjectionMapKey{secret: *NewSecureString(secret)}: "value",
	}
	unsafeJSON, err := json.Marshal(payload)
	if err != nil || !strings.Contains(string(unsafeJSON), secret) {
		t.Fatalf("custom map test setup did not expose private key state: %s, %v", unsafeJSON, err)
	}
	cfg := DefaultConfig()
	cfg.ModelList[0].ExtraBody = map[string]any{"payload": payload}

	if _, err = ProjectPublicConfig(cfg); err == nil || !strings.Contains(err.Error(), "private field") {
		t.Fatalf("ProjectPublicConfig() error = %v, want private-key rejection", err)
	}
	for key := range payload {
		if key.secret.String() != secret {
			t.Fatal("failed public projection mutated the source map key")
		}
	}
}

func TestProjectPublicConfigValidatesEveryChannelSchema(t *testing.T) {
	for _, test := range []struct {
		name    string
		channel *Channel
	}{
		{
			name:    "unregistered empty settings",
			channel: &Channel{Type: "unregistered-channel"},
		},
		{
			name: "registered type with mismatched decoded settings",
			channel: &Channel{
				Type:   ChannelTelegram,
				extend: &DiscordSettings{},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Channels = ChannelsConfig{"test": test.channel}
			if _, err := ProjectPublicConfig(cfg); err == nil {
				t.Fatal("ProjectPublicConfig() accepted an invalid channel schema")
			}
		})
	}
}

func TestRepositoryPublicProjectionPreservesDisabledStreamingWithTuning(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	channel := cfg.Channels.Get(ChannelMintClaw)
	channel.Settings = RawNode(
		`{"streaming":{"enabled":false,"throttle_seconds":2,"min_growth_chars":80}}`,
	)
	channel.extend = nil

	if _, err := NewRepository(configPath).Save(cfg); err != nil {
		t.Fatalf("Repository.Save() error = %v", err)
	}
	publicData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var public struct {
		Channels map[string]struct {
			Settings struct {
				Streaming StreamingConfig `json:"streaming"`
			} `json:"settings"`
		} `json:"channel_list"`
	}
	if err = json.Unmarshal(publicData, &public); err != nil {
		t.Fatal(err)
	}
	streaming := public.Channels["mintclaw"].Settings.Streaming
	if streaming.Enabled || streaming.ThrottleSeconds != 2 || streaming.MinGrowthChars != 80 {
		t.Fatalf("stored streaming config = %#v", streaming)
	}

	reloaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	decoded, err := reloaded.Channels.Get(ChannelMintClaw).GetDecoded()
	if err != nil {
		t.Fatal(err)
	}
	reloadedStreaming := decoded.(*MintClawSettings).Streaming
	if reloadedStreaming.Enabled || reloadedStreaming.ThrottleSeconds != 2 || reloadedStreaming.MinGrowthChars != 80 {
		t.Fatalf("reloaded streaming config = %#v", reloadedStreaming)
	}
}
