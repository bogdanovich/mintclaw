package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/internal/credentials/vault"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/pid"
)

type discoveryTerminal struct {
	input  *strings.Reader
	output bytes.Buffer
}

func (terminal *discoveryTerminal) Read(p []byte) (int, error)  { return terminal.input.Read(p) }
func (terminal *discoveryTerminal) Write(p []byte) (int, error) { return terminal.output.Write(p) }
func (*discoveryTerminal) Close() error                         { return nil }

func discoveryConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Credentials = config.CredentialConfig{
		Enabled: true, OwnerID: "owner", Origin: "https://approval.example.org", StateDirectory: t.TempDir(),
		Accounts: []credentialbroker.AccountRule{{
			Alias: "example", VaultID: "vault", ItemID: "old",
			Origins: []string{"https://accounts.example.com"}, Principals: []credentialbroker.Principal{
				{OwnerID: "owner", ActorID: "actor", AgentID: "agent", HostID: "host"},
			},
		}},
	}
	return cfg
}

func TestFindBindingNeedsPrivateChoiceAndPreservesAuthority(t *testing.T) {
	cfg := discoveryConfig(t)
	oldPolicy, err := credentialbroker.NewPolicy(cfg.Credentials.Accounts)
	if err != nil {
		t.Fatal("fixture policy unavailable")
	}
	terminal := &discoveryTerminal{input: strings.NewReader("2\n")}
	saved := false
	command := newCredentialsCommand(commandDeps{
		loadConfig:   func() (*config.Config, error) { return cfg, nil },
		openTerminal: func() (io.WriteCloser, error) { return terminal, nil },
		readGateway:  func() *pid.PidFileData { return nil },
		findLogins: func(_ context.Context, _, vaultID string, query vault.DiscoveryQuery) ([]vault.Candidate, error) {
			if vaultID != "vault" || query.Website != "example.com" {
				t.Fatal("discovery escaped dedicated vault or changed query")
			}
			return []vault.Candidate{
				{Binding: vault.Binding{VaultID: "vault", ItemID: "first"}, Name: "First\x1b[31m"},
				{Binding: vault.Binding{VaultID: "vault", ItemID: "chosen"}, Name: "Second"},
			}, nil
		},
		updateConfig: func(mutate func(*config.Config) error) error { saved = true; return mutate(cfg) },
	})
	var standard bytes.Buffer
	command.SetOut(&standard)
	command.SetErr(&standard)
	command.SetArgs([]string{"find", "--website", "example.com", "--bind", "example"})
	if command.Execute() != nil || !saved || standard.Len() != 0 ||
		cfg.Credentials.Accounts[0].ItemID != "chosen" || cfg.Credentials.BrowserLoginEnabled ||
		cfg.Credentials.Accounts[0].Origins[0] != "https://accounts.example.com" ||
		cfg.Credentials.Accounts[0].Principals[0].ActorID != "actor" ||
		strings.Contains(terminal.output.String(), "\x1b") {
		t.Fatal("private selection lost reference-only save, scope, or output boundaries")
	}
	newPolicy, err := credentialbroker.NewPolicy(cfg.Credentials.Accounts)
	if err != nil || newPolicy.Revision() == oldPolicy.Revision() {
		t.Fatal("binding change did not change approval revision")
	}
}

func TestFindBindingRejectsMissingChoiceAndConcurrentPolicyChange(t *testing.T) {
	for _, input := range []string{"\n", "0\n", "2\n", "1\n"} {
		t.Run(strings.ReplaceAll(input, "\n", "enter"), func(t *testing.T) {
			cfg := discoveryConfig(t)
			terminal := &discoveryTerminal{input: strings.NewReader(input)}
			command := newCredentialsCommand(commandDeps{
				loadConfig:   func() (*config.Config, error) { return cfg, nil },
				openTerminal: func() (io.WriteCloser, error) { return terminal, nil },
				readGateway:  func() *pid.PidFileData { return nil },
				findLogins: func(context.Context, string, string, vault.DiscoveryQuery) ([]vault.Candidate, error) {
					return []vault.Candidate{{Binding: vault.Binding{VaultID: "vault", ItemID: "chosen"}}}, nil
				},
				updateConfig: func(mutate func(*config.Config) error) error {
					current := *cfg
					current.Credentials.OwnerID = "changed"
					return mutate(&current)
				},
			})
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			command.SetArgs([]string{"find", "--name", "example", "--bind", "example"})
			if command.Execute() == nil || cfg.Credentials.Accounts[0].ItemID != "old" {
				t.Fatal("invalid or stale selection saved a binding")
			}
		})
	}
}

func TestFindBindingDeniesLiveGatewayBeforeReadingVault(t *testing.T) {
	cfg := discoveryConfig(t)
	listed := false
	command := newCredentialsCommand(commandDeps{
		loadConfig: func() (*config.Config, error) { return cfg, nil },
		openTerminal: func() (io.WriteCloser, error) {
			return &discoveryTerminal{input: strings.NewReader("1\n")}, nil
		},
		readGateway: func() *pid.PidFileData { return &pid.PidFileData{} },
		findLogins: func(context.Context, string, string, vault.DiscoveryQuery) ([]vault.Candidate, error) {
			listed = true
			return nil, nil
		},
		updateConfig: func(func(*config.Config) error) error { return nil },
	})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"find", "--name", "example", "--bind", "example"})
	if command.Execute() == nil || listed {
		t.Fatal("live runtime binding update reached the vault")
	}
}

func TestFindMetadataWorksWithoutPrivateTerminal(t *testing.T) {
	cfg := discoveryConfig(t)
	command := newCredentialsCommand(commandDeps{
		loadConfig: func() (*config.Config, error) { return cfg, nil },
		openTerminal: func() (io.WriteCloser, error) {
			t.Fatal("metadata-only lookup requested a private terminal")
			return nil, errSetupUnavailable
		},
		findLogins: func(context.Context, string, string, vault.DiscoveryQuery) ([]vault.Candidate, error) {
			return []vault.Candidate{{
				Binding: vault.Binding{VaultID: "vault", ItemID: "candidate"},
				Name:    "Example", Domains: []string{"example.com"},
			}}, nil
		},
	})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"find", "--name", "example"})
	var matches []vault.Candidate
	if command.Execute() != nil || json.Unmarshal(output.Bytes(), &matches) != nil || len(matches) != 1 ||
		matches[0].ItemID != "candidate" || matches[0].Name != "Example" ||
		cfg.Credentials.Accounts[0].ItemID != "old" {
		t.Fatal("headless lookup lost metadata projection or modified configuration")
	}
}
