package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func TestCredentialStateContainmentResolvesExistingFilesystemAliases(t *testing.T) {
	for _, kind := range []string{
		"state-parent-link", "missing-state-below-link", "workspace-link", "broken-link", "separate-state",
	} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "workspace")
			outside := filepath.Join(root, "outside")
			if os.Mkdir(workspace, 0o700) != nil || os.Mkdir(outside, 0o700) != nil {
				t.Fatal("directory containment fixture unavailable")
			}
			cfg := DefaultConfig()
			cfg.Agents.Defaults.Workspace = workspace
			cfg.Credentials = CredentialConfig{
				Enabled: true, OwnerID: "owner", Origin: "https://approval.example.org",
				StateDirectory: filepath.Join(outside, "private"),
				Accounts: []credentialbroker.AccountRule{
					{
						Alias:   "ebay",
						VaultID: "vault",
						ItemID:  "item",
						Origins: []string{"https://example.com"},
						Principals: []credentialbroker.Principal{
							{OwnerID: "owner", ActorID: "actor", AgentID: "agent", HostID: "host"},
						},
					},
				},
			}
			link := filepath.Join(outside, "workspace-link")
			if kind == "broken-link" {
				if os.Symlink(filepath.Join(root, "missing"), link) != nil {
					t.Skip("filesystem symlinks unavailable")
				}
			} else if kind != "separate-state" {
				if os.Symlink(workspace, link) != nil {
					t.Skip("filesystem symlinks unavailable")
				}
			}
			switch kind {
			case "state-parent-link":
				if os.Mkdir(filepath.Join(workspace, "private"), 0o700) != nil {
					t.Fatal("existing state fixture unavailable")
				}
				cfg.Credentials.StateDirectory = filepath.Join(link, "private")
			case "missing-state-below-link":
				cfg.Credentials.StateDirectory = filepath.Join(link, "not-created", "private")
			case "workspace-link":
				cfg.Agents.Defaults.Workspace = link
				cfg.Credentials.StateDirectory = filepath.Join(workspace, "not-created", "private")
			case "broken-link":
				cfg.Credentials.StateDirectory = filepath.Join(link, "private")
			}
			if (cfg.ValidateCredentials() == nil) != (kind == "separate-state") {
				t.Fatal("filesystem alias bypassed credential state containment")
			}
		})
	}
}

func TestCredentialStateContainmentUsesFilesystemIdentityForCaseAliases(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "Workspace")
	alias := filepath.Join(root, "WORKSPACE")
	if os.Mkdir(workspace, 0o700) != nil {
		t.Fatal("case alias fixture unavailable")
	}
	actual, err := os.Stat(workspace)
	if err != nil {
		t.Fatal("workspace identity unavailable")
	}
	other, aliasErr := os.Stat(alias)
	caseInsensitive := aliasErr == nil && os.SameFile(actual, other)
	if !caseInsensitive && os.Mkdir(alias, 0o700) != nil {
		t.Fatal("separate case-sensitive directory unavailable")
	}
	cfg := DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Credentials = CredentialConfig{
		Enabled: true, OwnerID: "owner", Origin: "https://approval.example.org",
		StateDirectory: filepath.Join(alias, "not-created", "private"),
		Accounts: []credentialbroker.AccountRule{{
			Alias: "ebay", VaultID: "vault", ItemID: "item", Origins: []string{"https://example.com"},
			Principals: []credentialbroker.Principal{{
				OwnerID: "owner", ActorID: "actor", AgentID: "agent", HostID: "host",
			}},
		}},
	}
	if (cfg.ValidateCredentials() != nil) != caseInsensitive {
		t.Fatal("case alias containment differs from actual filesystem identity")
	}
	if caseInsensitive {
		t.Log("case-insensitive-volume containment verified")
	} else {
		t.Log("distinct case-sensitive directories remain valid")
	}
}
