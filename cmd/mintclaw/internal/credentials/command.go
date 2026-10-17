package credentials

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal"
	"github.com/bogdanovich/mintclaw/internal/credentials/approval"
	"github.com/bogdanovich/mintclaw/internal/credentials/vault"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/pid"
)

var errSetupUnavailable = errors.New("private credential setup unavailable")

type commandDeps struct {
	loadConfig   func() (*config.Config, error)
	readGateway  func() *pid.PidFileData
	openTerminal func() (io.WriteCloser, error)
	readToken    func() ([]byte, error)
}

func NewCredentialsCommand() *cobra.Command {
	return newCredentialsCommand(commandDeps{
		loadConfig:   internal.LoadConfig,
		readGateway:  func() *pid.PidFileData { return pid.ReadPidFileWithCheck(internal.GetMintClawHome()) },
		openTerminal: openPrivateTerminal,
		readToken:    readPrivateToken,
	})
}

func newCredentialsCommand(deps commandDeps) *cobra.Command {
	command := &cobra.Command{Use: "credentials", Short: "Set up private account access", Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{
		Use: "init", Short: "Prepare the private approval state directory", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := deps.loadConfig()
			if err != nil || cfg == nil || !cfg.Credentials.Enabled || cfg.ValidateCredentials() != nil {
				return errSetupUnavailable
			}
			if os.MkdirAll(cfg.Credentials.StateDirectory, 0o700) != nil {
				return errSetupUnavailable
			}
			controller, _, err := approval.New(approval.Config{
				OwnerID: cfg.Credentials.OwnerID, Origin: cfg.Credentials.Origin,
				StateDirectory: cfg.Credentials.StateDirectory, Accounts: cfg.Credentials.Accounts,
				PagePath: "/credentials/", TrustLoopbackTLSProxy: cfg.Credentials.TrustLoopbackTLSProxy,
			})
			if err != nil || controller.Close() != nil {
				return errSetupUnavailable
			}
			_, err = fmt.Fprintln(
				cmd.OutOrStdout(),
				"Approval state prepared. Restart the gateway, then run credentials enroll.",
			)
			return err
		},
	})
	command.AddCommand(&cobra.Command{
		Use: "token", Short: "Enter your 1Password service token privately", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := deps.loadConfig()
			if err != nil || cfg == nil {
				return errSetupUnavailable
			}
			if !cfg.Credentials.Enabled {
				return errors.New(
					"credential access is disabled in the selected profile; finish configuration and run credentials init first",
				)
			}
			if cfg.ValidateCredentials() != nil || deps.readToken == nil {
				return errSetupUnavailable
			}
			token, err := deps.readToken()
			defer clear(token)
			if err != nil || vault.StoreServiceAccountToken(cfg.Credentials.StateDirectory, token) != nil {
				return errSetupUnavailable
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Private vault token stored. Restart the gateway to use it.")
			return err
		},
	})
	command.AddCommand(&cobra.Command{
		Use: "enroll", Short: "Show an expiring phone setup link in your private terminal", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			terminal, err := deps.openTerminal()
			if err != nil {
				return errSetupUnavailable
			}
			defer func() { _ = terminal.Close() }()
			cfg, err := deps.loadConfig()
			if err != nil || cfg == nil || !cfg.Credentials.Enabled || cfg.ValidateCredentials() != nil {
				return errSetupUnavailable
			}
			invite, err := requestEnrollment(cmd.Context(), cfg, deps.readGateway())
			if err != nil {
				return errSetupUnavailable
			}
			defer clear(invite)
			if _, err = io.WriteString(
				terminal,
				"Open this private setup link on your Android phone within five minutes.\nKeep it out of agent chat and logs.\n",
			); err != nil {
				return errSetupUnavailable
			}
			if count, writeErr := terminal.Write(invite); writeErr != nil || count != len(invite) {
				return errSetupUnavailable
			}
			if _, err = io.WriteString(terminal, "\n"); err != nil {
				return errSetupUnavailable
			}
			return nil
		},
	})
	return command
}

func requestEnrollment(ctx context.Context, cfg *config.Config, data *pid.PidFileData) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil || cfg == nil || data == nil || data.Token == "" ||
		data.Port < 1 || data.Port > 65535 {
		return nil, errSetupUnavailable
	}
	ip := net.ParseIP(data.Host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errSetupUnavailable
	}
	endpoint := url.URL{
		Scheme: "http", Host: net.JoinHostPort(data.Host, strconv.Itoa(data.Port)),
		Path: "/credentials/operator/enrollment",
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader("{}"))
	if err != nil {
		return nil, errSetupUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+data.Token)
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errSetupUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, errSetupUnavailable
	}
	invite, err := io.ReadAll(io.LimitReader(response.Body, 1025))
	if err != nil || len(invite) > 1024 {
		clear(invite)
		return nil, errSetupUnavailable
	}
	prefix := cfg.Credentials.Origin + "/credentials/#enroll="
	value := string(invite)
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		clear(invite)
		return nil, errSetupUnavailable
	}
	for _, character := range value[len(prefix):] {
		if (character < 'a' || character > 'f') && (character < '0' || character > '9') {
			clear(invite)
			return nil, errSetupUnavailable
		}
	}
	return invite, nil
}
