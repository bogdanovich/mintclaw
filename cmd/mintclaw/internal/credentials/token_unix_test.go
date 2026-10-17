//go:build linux || darwin

package credentials

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/credentials/vault"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func tokenCommandConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	dir := t.TempDir()
	if os.Chmod(dir, 0o700) != nil {
		t.Fatal("private token directory unavailable")
	}
	cfg.Credentials = config.CredentialConfig{
		Enabled: true, OwnerID: "owner", Origin: "https://approval.example.org", StateDirectory: dir,
		Accounts: []credentialbroker.AccountRule{{
			Alias: "ebay", VaultID: "vault", ItemID: "item", Origins: []string{"https://example.com"},
			Principals: []credentialbroker.Principal{{
				OwnerID: "owner", ActorID: "actor", AgentID: "agent", HostID: "host",
			}},
		}},
	}
	return cfg
}

func TestTokenCommandStoresPrivateInputWithoutOutputAndClearsBuffers(t *testing.T) {
	for _, failInput := range []bool{false, true} {
		cfg := tokenCommandConfig(t)
		const value = "synthetic-operator-service-token-K43"
		token := []byte(value)
		command := newCredentialsCommand(commandDeps{
			loadConfig: func() (*config.Config, error) { return cfg, nil },
			readToken: func() ([]byte, error) {
				if failInput {
					return token, errors.New(value)
				}
				return token, nil
			},
		})
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs([]string{"token"})
		err := command.Execute()
		if (err != nil) != failInput || bytes.Contains(output.Bytes(), []byte(value)) ||
			!bytes.Equal(token, make([]byte, len(token))) {
			t.Fatal("private token command exposed or retained input")
		}
		if failInput {
			if !errors.Is(err, errSetupUnavailable) || err.Error() != errSetupUnavailable.Error() {
				t.Fatal("private token input failure escaped its fixed error projection")
			}
			continue
		}
		if vault.UseServiceAccountToken(cfg.Credentials.StateDirectory, func(stored []byte) error {
			if !bytes.Equal(stored, []byte(value)) {
				t.Fatal("operator token was not privately provisioned")
			}
			return nil
		}) != nil {
			t.Fatal("provisioned token was unavailable to private startup")
		}
	}
}
