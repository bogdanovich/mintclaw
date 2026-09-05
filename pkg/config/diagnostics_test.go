package config

import (
	"fmt"
	"strings"
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

func TestValidateConfigPatchJSONConsumesLegacyModelConnectMode(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		encoded string
	}{
		{name: "empty", encoded: `""`},
		{name: "grpc", encoded: `"grpc"`},
		{name: "null", encoded: `null`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			raw := fmt.Sprintf(`{"model_list":[{"connect_mode":%s}]}`, test.encoded)
			require.NoError(t, ValidateConfigPatchJSON([]byte(raw), DefaultConfig()))
		})
	}

	err := ValidateConfigPatchJSON(
		[]byte(`{"model_list":[{"connect_mode":"stdio"}]}`),
		DefaultConfig(),
	)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "model_list[0].connect_mode"))
	require.True(t, strings.Contains(err.Error(), "no longer supported"))
}
