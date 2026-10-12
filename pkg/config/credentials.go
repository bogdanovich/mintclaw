package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

// CredentialConfig contains operator-owned references and approval settings,
// never a vault token, username, password, OTP, or session cookie.
type CredentialConfig struct {
	Enabled               bool                           `json:"enabled,omitempty"`
	OwnerID               string                         `json:"owner_id,omitempty"`
	Origin                string                         `json:"origin,omitempty"`
	StateDirectory        string                         `json:"state_directory,omitempty"`
	TrustLoopbackTLSProxy bool                           `json:"trust_loopback_tls_proxy,omitempty"`
	Accounts              []credentialbroker.AccountRule `json:"accounts,omitempty"`
}

func (cfg *Config) ValidateCredentials() error {
	if cfg == nil || !cfg.Credentials.Enabled {
		return nil
	}
	c := cfg.Credentials
	if c.OwnerID == "" || c.Origin == "" || c.StateDirectory == "" || !filepath.IsAbs(c.StateDirectory) ||
		filepath.Clean(c.StateDirectory) != c.StateDirectory || len(c.Accounts) == 0 {
		return errors.New("credential approval configuration is invalid")
	}
	state, err := resolveCredentialPath(c.StateDirectory)
	if err != nil {
		return errors.New("credential state directory is invalid")
	}
	workspaces := []string{cfg.WorkspacePath()}
	for _, agent := range cfg.Agents.List {
		if agent.Workspace != "" {
			workspaces = append(workspaces, agent.Workspace)
		}
	}
	for _, workspace := range workspaces {
		absolute, err := resolveCredentialPath(fileutil.ExpandHome(workspace))
		if err != nil {
			return errors.New("credential state directory is invalid")
		}
		relative, err := filepath.Rel(absolute, state)
		if err != nil || relative == "." ||
			(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return errors.New("credential state must be outside agent workspaces")
		}
	}
	if _, err := credentialbroker.NewPolicy(c.Accounts); err != nil {
		return errors.New("credential account policy is invalid")
	}
	vault := c.Accounts[0].VaultID
	for _, account := range c.Accounts {
		if account.VaultID != vault {
			return errors.New("credential accounts must use one dedicated vault")
		}
		for _, principal := range account.Principals {
			if principal.OwnerID != c.OwnerID {
				return errors.New("credential account owner is invalid")
			}
		}
	}
	return nil
}

// Resolve existing ancestors before comparing containment. Missing tail
// components are retained so private setup can validate a directory before
// creating it. Existing broken links or other resolution errors deny setup.
// The directory tree is operator-controlled under the trusted-host model.
func resolveCredentialPath(path string) (string, error) {
	current, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var missing []string
	for {
		resolved, resolveErr := filepath.EvalSymlinks(current)
		if resolveErr == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return resolved, nil
		}
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return "", resolveErr
		}
		if _, statErr := os.Lstat(current); !errors.Is(statErr, os.ErrNotExist) {
			return "", resolveErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", resolveErr
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}
