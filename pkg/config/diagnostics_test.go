package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateConfigPatchJSONUsesCurrentExplicitChannelType(t *testing.T) {
	current := DefaultConfig()
	current.Channels["alerts"] = &Channel{Type: ChannelTelegram}

	err := ValidateConfigPatchJSON(
		[]byte(`{"channel_list":{"alerts":{"settings":{"unknown_setting":true}}}}`),
		current,
	)
	require.ErrorContains(t, err, "channel_list.alerts.settings.unknown_setting")
}
