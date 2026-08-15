package nodes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/pkg/config"
	nodepkg "github.com/bogdanovich/mintclaw/pkg/nodes"
)

const enrollmentOperatorPath = "/nodes/v1/enrollment-offers"

func newEnrollCommand(loadConfig terminalConfigLoader) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Create bounded companion enrollment offers",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newAndroidEnrollmentCommand(loadConfig))
	return cmd
}

func newAndroidEnrollmentCommand(loadConfig terminalConfigLoader) *cobra.Command {
	var endpoint string
	var spkiSHA256 string
	var ttl time.Duration
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "android",
		Short: "Create a one-time Android pairing QR",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if ttl < time.Second || ttl > nodepkg.MaxEnrollmentOfferTTL || ttl%time.Second != 0 {
				return errors.New("android enrollment TTL must be whole seconds between 1s and 5m")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			response, err := requestAndroidEnrollmentOffer(cmd, cfg, nodepkg.EnrollmentOfferRequest{
				Endpoint: endpoint, SPKISHA256: spkiSHA256, TTLSeconds: int(ttl / time.Second),
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), response)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Scan this one-time QR with the MintClaw Android companion:")
			qrterminal.GenerateWithConfig(response.URI, qrterminal.Config{
				Level: qrterminal.L, Writer: cmd.OutOrStdout(), HalfBlocks: true,
			})
			fmt.Fprintf(cmd.OutOrStdout(), "\nEnrollment URI: %s\n", response.URI)
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"Expires at: %s\n",
				time.Unix(response.Offer.ExpiresAt, 0).Format(time.RFC3339),
			)
			fmt.Fprintln(cmd.OutOrStdout(), "Scanning requests pairing only; approve the pending node separately.")
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Exact public wss://.../nodes/v1/ws endpoint")
	cmd.Flags().StringVar(&spkiSHA256, "spki-sha256", "", "Optional lowercase SHA-256 SPKI pin")
	cmd.Flags().DurationVar(&ttl, "ttl", nodepkg.DefaultEnrollmentOfferTTL, "Offer lifetime (maximum 5m)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the sensitive offer as JSON instead of a QR")
	_ = cmd.MarkFlagRequired("endpoint")
	return cmd
}

func requestAndroidEnrollmentOffer(
	cmd *cobra.Command,
	cfg *config.Config,
	input nodepkg.EnrollmentOfferRequest,
) (nodepkg.EnrollmentOfferResponse, error) {
	if cfg == nil || !cfg.Nodes.Enabled {
		return nodepkg.EnrollmentOfferResponse{}, errors.New("android enrollment requires nodes.enabled")
	}
	credentials, err := mintClawOperatorCredentials(cfg)
	if err != nil {
		return nodepkg.EnrollmentOfferResponse{}, err
	}
	baseURL, err := localGatewayURL(cfg)
	if err != nil {
		return nodepkg.EnrollmentOfferResponse{}, err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nodepkg.EnrollmentOfferResponse{}, err
	}
	endpoint := *baseURL
	endpoint.Path = enrollmentOperatorPath
	request, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nodepkg.EnrollmentOfferResponse{}, err
	}
	request.Header.Set("Authorization", "Bearer "+credentials.Token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nodepkg.EnrollmentOfferResponse{}, fmt.Errorf("create Android enrollment offer: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 16*1024)).Decode(&failure)
		if failure.Error == "" {
			failure.Error = http.StatusText(response.StatusCode)
		}
		return nodepkg.EnrollmentOfferResponse{}, fmt.Errorf("create Android enrollment offer: %s", failure.Error)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16*1024))
	decoder.DisallowUnknownFields()
	var result nodepkg.EnrollmentOfferResponse
	if decodeErr := decoder.Decode(&result); decodeErr != nil {
		return nodepkg.EnrollmentOfferResponse{}, fmt.Errorf("decode Android enrollment offer: %w", decodeErr)
	}
	if trailingErr := decoder.Decode(new(any)); !errors.Is(trailingErr, io.EOF) {
		return nodepkg.EnrollmentOfferResponse{}, errors.New("gateway returned trailing Android enrollment data")
	}
	decoded, err := nodepkg.DecodeEnrollmentOfferURI(result.URI)
	if err != nil || decoded != result.Offer {
		return nodepkg.EnrollmentOfferResponse{}, errors.New("gateway returned an invalid Android enrollment offer")
	}
	return result, nil
}
