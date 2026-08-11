package internal

import (
	"os"
	"path/filepath"

	"github.com/bogdanovich/mintclaw/pkg"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

const Logo = pkg.Logo

// GetMintClawHome returns the mintclaw home directory.
// Priority: $MINTCLAW_HOME > ~/.mintclaw
func GetMintClawHome() string {
	return config.GetHome()
}

func GetConfigPath() string {
	if configPath := os.Getenv(config.EnvConfig); configPath != "" {
		return configPath
	}
	return filepath.Join(GetMintClawHome(), "config.json")
}

func LoadConfig() (*config.Config, error) {
	cfg, err := config.LoadConfig(GetConfigPath())
	if err != nil {
		return nil, err
	}
	logger.SetLevelFromString(cfg.Gateway.LogLevel)
	return cfg, nil
}
