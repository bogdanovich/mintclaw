package skills

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewInstallSubcommand(t *testing.T) {
	cmd := newInstallCommand(&deps{})

	require.NotNil(t, cmd)

	assert.Equal(t, "install <slug>", cmd.Use)
	assert.Equal(t, "Install a skill into an explicit ownership scope", cmd.Short)

	assert.Nil(t, cmd.Run)
	assert.NotNil(t, cmd.RunE)

	assert.True(t, cmd.HasExample())
	assert.False(t, cmd.HasSubCommands())

	assert.True(t, cmd.HasFlags())
	assert.NotNil(t, cmd.Flags().Lookup("registry"))
	assert.Equal(t, "user", cmd.Flags().Lookup("scope").DefValue)
	assert.NotNil(t, cmd.Flags().Lookup("project"))
	assert.NotNil(t, cmd.Flags().Lookup("version"))
	assert.NotNil(t, cmd.Flags().Lookup("replace"))
	assert.NotNil(t, cmd.Flags().Lookup("dry-run"))
	assert.NotNil(t, cmd.Flags().Lookup("json"))

	assert.Len(t, cmd.Aliases, 0)
}

func TestInstallCommandArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		expectError bool
	}{
		{
			name:        "no registry, one arg",
			args:        []string{"owner/repository/skills/weather"},
			expectError: false,
		},
		{
			name:        "no registry, no args",
			args:        []string{},
			expectError: true,
		},
		{
			name:        "no registry, too many args",
			args:        []string{"arg1", "arg2"},
			expectError: true,
		},
		{
			name:        "with registry, one arg",
			args:        []string{"weather-skill"},
			expectError: false,
		},
		{
			name:        "with registry, no args",
			args:        []string{},
			expectError: true,
		},
		{
			name:        "with registry, too many args",
			args:        []string{"arg1", "arg2"},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newInstallCommand(&deps{})

			err := cmd.Args(cmd, tt.args)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
