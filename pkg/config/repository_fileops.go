package config

import (
	"os"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

var (
	configWriteFileAtomic = fileutil.WriteFileAtomic
	configSyncDirectory   = fileutil.SyncDirectory
	configRemoveFile      = os.Remove
)

func writeConfigDocuments(path string, cfg *Config) error {
	_, err := NewRepository(path).Save(cfg)
	return err
}
