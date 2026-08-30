package internal

import (
	"os"
	"path/filepath"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

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
	cfg, err := LoadConfigAt(GetConfigPath())
	if err != nil {
		return nil, err
	}
	logger.SetLevelFromString(cfg.Gateway.LogLevel)
	return cfg, nil
}

func LoadConfigAt(path string) (*config.Config, error) {
	snapshot, err := config.NewRepository(path).ReadOnly()
	if err != nil {
		return nil, err
	}
	return snapshot.Config, nil
}

func UpdateConfig(mutate func(*config.Config) error) (config.Snapshot, error) {
	return UpdateConfigAt(GetConfigPath(), mutate)
}

func UpdateConfigAt(path string, mutate func(*config.Config) error) (config.Snapshot, error) {
	return config.NewRepository(path).Update(mutate)
}
