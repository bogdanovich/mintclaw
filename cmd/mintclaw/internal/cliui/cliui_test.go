package cliui

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRenderCommandHelpIsPlain(t *testing.T) {
	cmd := &cobra.Command{
		Use:   "mintclaw test",
		Short: "Test a command",
		Run:   func(*cobra.Command, []string) {},
	}

	got := RenderCommandHelp(cmd)
	for _, want := range []string{"Test a command", "Usage:", "mintclaw test"} {
		if !strings.Contains(got, want) {
			t.Errorf("help does not contain %q: %q", want, got)
		}
	}
	assertNoDecoration(t, got)
}

func TestFormatCLIErrorIsPlain(t *testing.T) {
	got := FormatCLIError("coding runtime: model is required", nil)
	if got != "Error: coding runtime: model is required\n" {
		t.Fatalf("unexpected error output: %q", got)
	}
	assertNoDecoration(t, got)
}

func TestShowErrHint(t *testing.T) {
	cases := map[string]bool{
		"unknown flag: --foo":                true,
		"unknown shorthand flag: 'f' in -f":  true,
		"flag needs an argument: --output":   true,
		"required flag(s) \"model\" not set": true,
		"invalid argument \"abc\"":           true,
		"bad input\nusage: mintclaw":         true,
		"feature flag not set":               false,
		"invalid API key provided":           false,
		"network timeout":                    false,
	}
	for msg, want := range cases {
		if got := showErrHint(msg); got != want {
			t.Errorf("showErrHint(%q) = %v, want %v", msg, got, want)
		}
	}
}

func assertNoDecoration(t *testing.T, output string) {
	t.Helper()
	for _, unwanted := range []string{"🦞", "╭", "╰", "│", "███", "\x1b["} {
		if strings.Contains(output, unwanted) {
			t.Errorf("output contains decorative token %q: %q", unwanted, output)
		}
	}
}
