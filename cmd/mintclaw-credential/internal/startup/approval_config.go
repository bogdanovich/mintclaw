package startup

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

const maxApprovalConfigBytes = 65536

// approvalConfig is immutable Root configuration. It contains no token source
// or provider activation switch. Serving this configuration cannot initialize
// the SDK, retrieve credentials or expose a model intent listener.
type approvalConfig struct {
	Version         int                            `json:"version"`
	ServiceUID      uint32                         `json:"service_uid"`
	ServiceGID      uint32                         `json:"service_gid"`
	OwnerID         string                         `json:"owner_id"`
	Origin          string                         `json:"origin"`
	ListenAddress   string                         `json:"listen_address"`
	StateDirectory  string                         `json:"state_directory"`
	CertificateFile string                         `json:"certificate_file"`
	PrivateKeyFile  string                         `json:"private_key_file"`
	Accounts        []credentialbroker.AccountRule `json:"accounts"`
}

func decodeApprovalConfig(data []byte) (approvalConfig, error) {
	var config approvalConfig
	if len(data) == 0 || len(data) > maxApprovalConfigBytes {
		return config, errUnavailable
	}
	// Check exact key spelling and duplicates throughout the tree before the
	// typed decoder, which otherwise accepts case aliases and duplicate keys.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if strictConfigValue(decoder, 0) != nil {
		return config, errUnavailable
	}
	if _, err := decoder.Token(); err != io.EOF {
		return config, errUnavailable
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || config.valid() != nil {
		return approvalConfig{}, errUnavailable
	}
	return config, nil
}

func strictConfigValue(decoder *json.Decoder, depth int) error {
	if depth > 16 {
		return errUnavailable
	}
	token, err := decoder.Token()
	if err != nil {
		return errUnavailable
	}
	delimiter, nested := token.(json.Delim)
	if !nested {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return errUnavailable
	}
	seen := make(map[string]struct{})
	count := 0
	for decoder.More() {
		count++
		if count > 1024 {
			return errUnavailable
		}
		if delimiter == '{' {
			key, keyErr := decoder.Token()
			name, text := key.(string)
			if keyErr != nil || !text || !approvalConfigKey(name) {
				return errUnavailable
			}
			if _, duplicate := seen[name]; duplicate {
				return errUnavailable
			}
			seen[name] = struct{}{}
		}
		if strictConfigValue(decoder, depth+1) != nil {
			return errUnavailable
		}
	}
	closing, err := decoder.Token()
	if err != nil || (delimiter == '{' && closing != json.Delim('}')) ||
		(delimiter == '[' && closing != json.Delim(']')) {
		return errUnavailable
	}
	return nil
}

func approvalConfigKey(name string) bool {
	switch name {
	case "version", "service_uid", "service_gid", "owner_id", "origin", "listen_address",
		"state_directory", "certificate_file", "private_key_file", "accounts", "alias", "vault_id", "item_id",
		"origins", "principals", "actor_id", "agent_id", "host_id", "login", "navigation_url", "entry_origin",
		"form_id", "username_id", "password_id", "username_type", "submit_url", "success_url", "success_marker_id",
		"handoff_url", "handoff_marker_id":
		return true
	}
	return false
}

func (config approvalConfig) valid() error {
	canonical, err := browserpolicy.NormalizeHTTPOrigin(config.Origin)
	parsed, parseErr := url.Parse(config.Origin)
	address, addressErr := netip.ParseAddrPort(config.ListenAddress)
	if config.Version != 1 || config.ServiceUID == 0 || config.ServiceGID == 0 || !intent.ValidAlias(config.OwnerID) ||
		err != nil || parseErr != nil || canonical != config.Origin || parsed.Scheme != "https" ||
		net.ParseIP(parsed.Hostname()) != nil || !strings.Contains(parsed.Hostname(), ".") ||
		addressErr != nil || address.Addr().Zone() != "" || address.Port() < 1024 ||
		address.String() != config.ListenAddress || len(config.Accounts) == 0 {
		return errUnavailable
	}
	for _, path := range []string{config.StateDirectory, config.CertificateFile, config.PrivateKeyFile} {
		if !validDirectoryPath(path) {
			return errUnavailable
		}
	}
	if config.CertificateFile == config.PrivateKeyFile || config.CertificateFile == config.StateDirectory ||
		config.PrivateKeyFile == config.StateDirectory {
		return errUnavailable
	}
	if _, err = credentialbroker.NewPolicy(config.Accounts); err != nil {
		return errUnavailable
	}
	for _, account := range config.Accounts {
		for _, principal := range account.Principals {
			if principal.OwnerID != config.OwnerID {
				return errUnavailable
			}
		}
	}
	return nil
}
