package config

import (
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func TestCredentialConfigurationRequiresDedicatedVaultAndPrivateStateLocation(t *testing.T) {
	for _, kind := range []string{"valid", "workspace", "agent-workspace", "different-vault", "different-owner"} {
		t.Run(kind, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
			cfg.Credentials = CredentialConfig{
				Enabled: true, OwnerID: "owner", Origin: "https://approval.example.org", StateDirectory: t.TempDir(),
				Accounts: []credentialbroker.AccountRule{
					{
						Alias:   "facebook",
						VaultID: "vault",
						ItemID:  "item",
						Origins: []string{"https://facebook.com"},
						Principals: []credentialbroker.Principal{
							{OwnerID: "owner", ActorID: "actor", AgentID: "agent", HostID: "host"},
						},
					},
				},
			}
			switch kind {
			case "workspace":
				cfg.Credentials.StateDirectory = filepath.Join(cfg.WorkspacePath(), "credential-state")
			case "agent-workspace":
				cfg.Agents.List[0].Workspace = cfg.Credentials.StateDirectory
			case "different-vault":
				other := cfg.Credentials.Accounts[0]
				other.Alias, other.ItemID, other.VaultID = "ebay", "second-item", "other-vault"
				cfg.Credentials.Accounts = append(cfg.Credentials.Accounts, other)
			case "different-owner":
				cfg.Credentials.Accounts[0].Principals[0].OwnerID = "other-owner"
			}
			if (cfg.ValidateCredentials() == nil) != (kind == "valid") {
				t.Fatal("credential configuration admission differs from its scope/state contract")
			}
		})
	}
}
