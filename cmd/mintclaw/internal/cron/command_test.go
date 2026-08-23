package cron

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCronCommand(t *testing.T) {
	cmd := NewCronCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "Manage scheduled tasks", cmd.Short)

	assert.Len(t, cmd.Aliases, 1)
	assert.True(t, cmd.HasAlias("c"))

	assert.False(t, cmd.HasFlags())

	assert.Nil(t, cmd.Run)
	assert.NotNil(t, cmd.RunE)

	assert.NotNil(t, cmd.PersistentPreRunE)
	assert.Nil(t, cmd.PersistentPreRun)
	assert.Nil(t, cmd.PersistentPostRun)

	assert.True(t, cmd.HasSubCommands())

	allowedCommands := []string{
		"list",
		"add",
		"remove",
		"enable",
		"disable",
	}

	subcommands := cmd.Commands()
	assert.Len(t, subcommands, len(allowedCommands))

	for _, subcmd := range subcommands {
		found := slices.Contains(allowedCommands, subcmd.Name())
		assert.True(t, found, "unexpected subcommand %q", subcmd.Name())

		assert.Len(t, subcmd.Aliases, 0)
		assert.False(t, subcmd.Hidden)

		assert.False(t, subcmd.HasSubCommands())

		assert.Nil(t, subcmd.Run)
		assert.NotNil(t, subcmd.RunE)

		assert.Nil(t, subcmd.PersistentPreRun)
		assert.Nil(t, subcmd.PersistentPostRun)
	}
}

func TestCronSubcommandsRejectUnsupportedAndMalformedStores(t *testing.T) {
	stores := map[string]string{
		"v1":           `{"version":1,"jobs":[]}`,
		"malformed_v2": `{"version":2,"jobs":null}`,
	}

	for storeName, contents := range stores {
		t.Run(storeName, func(t *testing.T) {
			storePath := filepath.Join(t.TempDir(), "jobs.json")
			require.NoError(t, os.WriteFile(storePath, []byte(contents), 0o600))
			path := func() string { return storePath }

			commands := map[string]struct {
				command *cobra.Command
				args    []string
			}{
				"list": {command: newListCommand(path)},
				"add": {
					command: newAddCommand(path),
					args:    []string{"--name", "job", "--message", "run", "--every", "60"},
				},
				"remove":  {command: newRemoveCommand(path), args: []string{"job-1"}},
				"enable":  {command: newEnableCommand(path), args: []string{"job-1"}},
				"disable": {command: newDisableCommand(path), args: []string{"job-1"}},
			}

			for commandName, test := range commands {
				t.Run(commandName, func(t *testing.T) {
					test.command.SilenceUsage = true
					test.command.SilenceErrors = true
					test.command.SetArgs(test.args)
					err := test.command.Execute()
					require.ErrorContains(t, err, "load cron store")
				})
			}
		})
	}
}
