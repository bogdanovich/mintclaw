package skills

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRemoveSubcommand(t *testing.T) {
	cmd := newRemoveCommand(&deps{})

	require.NotNil(t, cmd)

	assert.Equal(t, "remove <name>", cmd.Use)
	assert.Equal(t, "Remove a skill from an explicit ownership scope", cmd.Short)

	assert.Nil(t, cmd.Run)
	assert.NotNil(t, cmd.RunE)

	assert.True(t, cmd.HasExample())
	assert.False(t, cmd.HasSubCommands())

	assert.True(t, cmd.HasFlags())
	assert.Equal(t, "user", cmd.Flags().Lookup("scope").DefValue)

	assert.Len(t, cmd.Aliases, 2)
	assert.True(t, cmd.HasAlias("rm"))
	assert.True(t, cmd.HasAlias("uninstall"))
}
