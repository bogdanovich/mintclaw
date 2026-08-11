package matrix

import (
	"os"

	_ "modernc.org/sqlite"

	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/media"
)

func matrixMediaTempDir() (string, error) {
	mediaDir := media.TempDir()
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		return "", err
	}
	return mediaDir, nil
}

func (c *MatrixChannel) VoiceCapabilities() channels.VoiceCapabilities {
	return channels.VoiceCapabilities{ASR: true, TTS: true}
}
