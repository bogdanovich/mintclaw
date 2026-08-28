package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestNewMintClawCommand(t *testing.T) {
	cmd := NewMintClawCommand()

	require.NotNil(t, cmd)

	short := fmt.Sprintf("%s MintClaw — personal AI assistant", internal.Logo)
	longHas := strings.Contains(cmd.Long, config.FormatVersion())

	assert.Equal(t, "mintclaw", cmd.Use)
	assert.Equal(t, short, cmd.Short)
	assert.True(t, longHas)

	assert.True(t, cmd.HasSubCommands())
	assert.True(t, cmd.HasAvailableSubCommands())

	assert.True(t, cmd.PersistentFlags().Lookup("no-color") != nil)

	assert.Nil(t, cmd.Run)
	assert.Nil(t, cmd.RunE)

	assert.NotNil(t, cmd.PersistentPreRun)
	assert.Nil(t, cmd.PersistentPostRun)

	allowedCommands := []string{
		"agent",
		"auth",
		"code",
		"config",
		"cron",
		"doctor",
		"gateway",
		"mcp",
		"migrate",
		"model",
		"nodes",
		"onboard",
		"resume",
		"skills",
		"status",
		"threads",
		"update",
		"version",
	}

	subcommands := cmd.Commands()
	assert.Len(t, subcommands, len(allowedCommands))

	for _, subcmd := range subcommands {
		found := slices.Contains(allowedCommands, subcmd.Name())
		assert.True(t, found, "unexpected subcommand %q", subcmd.Name())

		assert.False(t, subcmd.Hidden)
	}
}

func TestMachineJSONRequested(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "doctor json", args: []string{"doctor", "--json"}, want: true},
		{name: "global flag first", args: []string{"--no-color", "doctor", "--json=true"}, want: true},
		{name: "json numeric true", args: []string{"doctor", "--json=1"}, want: true},
		{name: "json short true", args: []string{"threads", "delete", "id", "--json=t"}, want: true},
		{name: "json uppercase true", args: []string{"threads", "delete", "id", "--json=TRUE"}, want: true},
		{name: "human doctor", args: []string{"doctor"}, want: false},
		{name: "nodes json", args: []string{"nodes", "list", "--json"}, want: true},
		{name: "live agent json", args: []string{"agent", "live", "--json"}, want: true},
		{name: "coding json", args: []string{"code", "fix it", "--json"}, want: true},
		{name: "resume json", args: []string{"resume", "--json"}, want: true},
		{name: "threads json", args: []string{"threads", "delete", "id", "--json"}, want: true},
		{name: "other json command", args: []string{"status", "--json"}, want: false},
		{name: "explicit false", args: []string{"doctor", "--json=false"}, want: false},
		{name: "invalid bool", args: []string{"threads", "delete", "id", "--json=yes"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, machineJSONRequested(tt.args))
		})
	}
}

func TestCodingFrontendRequested(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "code", args: []string{"code"}, want: true},
		{name: "resume", args: []string{"--no-color", "resume", "thread-id"}, want: true},
		{name: "other", args: []string{"status"}, want: false},
		{name: "prompt word", args: []string{"agent", "-m", "resume"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, codingFrontendRequested(test.args))
		})
	}
}
