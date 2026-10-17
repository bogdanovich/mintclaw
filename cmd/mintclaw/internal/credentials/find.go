package credentials

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/internal/credentials/vault"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func findPrivateLogins(ctx context.Context, directory, vaultID string,
	query vault.DiscoveryQuery,
) (result []vault.Candidate, resultErr error) {
	err := vault.UseServiceAccountToken(directory, func(token []byte) error {
		var err error
		result, err = vault.FindLogins(ctx, token, vaultID, query)
		return err
	})
	if err != nil {
		return nil, errSetupUnavailable
	}
	return result, nil
}

func newFindCommand(deps commandDeps) *cobra.Command {
	var website, name, bind string
	command := &cobra.Command{
		Use: "find", Short: "Find Login items by website or name in your dedicated vault", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var terminal io.WriteCloser
			if bind != "" {
				var err error
				terminal, err = deps.openTerminal()
				if err != nil {
					return errSetupUnavailable
				}
				defer func() { _ = terminal.Close() }()
			}
			cfg, err := deps.loadConfig()
			if err != nil || cfg == nil || !cfg.Credentials.Enabled || cfg.ValidateCredentials() != nil ||
				deps.findLogins == nil || (website == "" && strings.TrimSpace(name) == "") {
				return errSetupUnavailable
			}
			index := -1
			if bind != "" {
				if cfg.Credentials.BrowserLoginEnabled || deps.updateConfig == nil || deps.readGateway == nil ||
					deps.readGateway() != nil {
					return errSetupUnavailable
				}
				for i, account := range cfg.Credentials.Accounts {
					if account.Alias == bind {
						index = i
					}
				}
				if index < 0 {
					return errSetupUnavailable
				}
			}
			vaultID := cfg.Credentials.Accounts[0].VaultID
			candidates, err := deps.findLogins(cmd.Context(), cfg.Credentials.StateDirectory, vaultID,
				vault.DiscoveryQuery{Website: website, Name: name})
			if err != nil || len(candidates) > 20 {
				return errSetupUnavailable
			}
			if bind == "" {
				if candidates == nil {
					candidates = []vault.Candidate{}
				}
				if json.NewEncoder(cmd.OutOrStdout()).Encode(candidates) != nil {
					return errSetupUnavailable
				}
				return nil
			}
			for i, candidate := range candidates {
				encoded, encodeErr := json.Marshal(candidate)
				if encodeErr != nil || candidate.VaultID != vaultID {
					return errSetupUnavailable
				}
				if _, err = fmt.Fprintf(terminal, "%d. %s\n", i+1, encoded); err != nil {
					return errSetupUnavailable
				}
			}
			if len(candidates) == 0 {
				_, err = fmt.Fprintln(terminal, "No matching Login items.")
				if err != nil {
					return errSetupUnavailable
				}
				return nil
			}
			reader, ok := terminal.(io.Reader)
			if !ok {
				return errSetupUnavailable
			}
			if _, err = fmt.Fprintf(terminal,
				"Choose an item number for %q, origins %q (empty cancels). Principals stay unchanged: ",
				bind, cfg.Credentials.Accounts[index].Origins,
			); err != nil {
				return errSetupUnavailable
			}
			line, err := bufio.NewReader(io.LimitReader(reader, 16)).ReadString('\n')
			if err != nil || strings.TrimSpace(line) == "" {
				return errSetupUnavailable
			}
			selection, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil || selection < 1 || selection > len(candidates) {
				return errSetupUnavailable
			}
			selected := candidates[selection-1]
			err = deps.updateConfig(func(current *config.Config) error {
				if current == nil || !reflect.DeepEqual(current.Credentials, cfg.Credentials) {
					return errSetupUnavailable
				}
				current.Credentials.Accounts[index].ItemID = selected.ItemID
				if current.ValidateCredentials() != nil {
					return errSetupUnavailable
				}
				return nil
			})
			if err != nil {
				return errSetupUnavailable
			}
			_, err = fmt.Fprintln(terminal, "Reference saved. Restart the gateway; browser login remains disabled.")
			if err != nil {
				return errSetupUnavailable
			}
			return nil
		},
	}
	command.Flags().StringVar(&website, "website", "", "Website URL or domain to match")
	command.Flags().StringVar(&name, "name", "", "Case-insensitive item name substring")
	command.Flags().StringVar(&bind, "bind", "", "Choose and save an item for an already configured alias")
	return command
}
