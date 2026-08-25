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
