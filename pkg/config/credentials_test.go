package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func TestCredentialConfigurationRequiresDedicatedVaultAndPrivateStateLocation(t *testing.T) {
	for _, kind := range []string{
		"valid", "workspace", "agent-workspace", "different-vault", "different-owner", "missing-workspace",
		"login-valid", "login-disabled-runtime", "login-no-host", "login-invalid-host", "discovery-valid",
	} {
		t.Run(kind, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
			if os.Mkdir(cfg.WorkspacePath(), 0o700) != nil {
				t.Fatal("workspace identity fixture unavailable")
			}
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
			case "discovery-valid":
				cfg.Credentials.BrowserLoginEnabled, cfg.Credentials.HostID = true, "host"
				account := &cfg.Credentials.Accounts[0]
				account.DiscoverLogins, account.ItemID, account.Origins = true, "", nil
			case "login-valid", "login-disabled-runtime", "login-no-host", "login-invalid-host":
				cfg.Credentials.BrowserLoginEnabled = true
				cfg.Credentials.HostID = "gateway"
				if kind == "login-disabled-runtime" {
					cfg.Credentials.Enabled = false
				}
				if kind == "login-no-host" {
					cfg.Credentials.HostID = ""
				}
				if kind == "login-invalid-host" {
					cfg.Credentials.HostID = "host/name"
				}
			case "missing-workspace":
				if os.Remove(cfg.WorkspacePath()) != nil {
					t.Fatal("missing workspace fixture unavailable")
				}
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
			valid := kind == "valid" || kind == "login-valid" || kind == "discovery-valid"
			if (cfg.ValidateCredentials() == nil) != valid {
				t.Fatal("credential configuration admission differs from its scope/state contract")
			}
		})
	}
}
