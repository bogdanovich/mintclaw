// MintClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/agent"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/auth"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/cliui"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/coding"
	configcmd "github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/config"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/cron"
	doctorcmd "github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/doctor"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/gateway"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/mcp"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/migrate"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/model"
	nodescmd "github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/nodes"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/onboard"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/skills"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/status"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal/version"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/updater"
)

var rootNoColor bool

// initTermuxSSL detects Termux environment and sets SSL_CERT_FILE if not already set.
// This fixes X509 certificate errors when running MintClaw inside Termux or termux-chroot.
func initTermuxSSL() {
	// Only applicable on Linux/Android
	if runtime.GOOS != "linux" && runtime.GOOS != "android" {
		return
	}

	// Skip if already set
	if os.Getenv("SSL_CERT_FILE") != "" {
		return
	}

	// Check for Termux prefix in PATH or HOME
	home := os.Getenv("HOME")
	path := os.Getenv("PATH")

	isTermux := strings.Contains(home, "com.termux") ||
		strings.Contains(path, "com.termux") ||
		strings.Contains(home, "/data/data/com.termux")

	if !isTermux {
		return
	}

	// Check common CA bundle locations in Termux
	caPaths := []string{
		"$PREFIX/etc/tls/cert.pem",
		os.Getenv("PREFIX") + "/etc/tls/cert.pem",
		"/data/data/com.termux/files/usr/etc/tls/cert.pem",
		"/usr/etc/tls/cert.pem",
	}

	for _, caPath := range caPaths {
		expanded := os.ExpandEnv(caPath)
		if _, err := os.Stat(expanded); err == nil {
			_ = os.Setenv("SSL_CERT_FILE", expanded)
			return
		}
	}
}

// machineJSONRequested reports whether argv selects machine-readable output.
// Console logging must be suppressed so stdout remains valid JSON.
func machineJSONRequested(args []string) bool {
	hasJSONCommand := false
	hasJSON := false
	for _, arg := range args {
		switch arg {
		case "doctor", "nodes", "agent", "code", "resume", "threads":
			hasJSONCommand = true
		case "--json":
			hasJSON = true
		default:
			if value, ok := strings.CutPrefix(arg, "--json="); ok {
				parsed, err := strconv.ParseBool(value)
				hasJSON = hasJSON || err == nil && parsed
			}
		}
	}
	return hasJSONCommand && hasJSON
}

// codingFrontendRequested suppresses process-level output which would corrupt
// the future alternate-screen frontend and today's stable plain renderer.
func codingFrontendRequested(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg == "code" || arg == "resume"
	}
	return false
}

func NewMintClawCommand() *cobra.Command {
	short := "MintClaw — personal AI assistant"
	long := fmt.Sprintf(`MintClaw is a lightweight personal AI assistant.

Version: %s`, config.FormatVersion())

	cmd := &cobra.Command{
		Use:   "mintclaw",
		Short: short,
		Long:  long,
		Example: `mintclaw version
mintclaw onboard
mintclaw --no-color status`,
		SilenceErrors: true,
		// Avoid dumping usage for runtime errors. Flag and usage errors receive a
		// compact command hint from cliui instead.
		SilenceUsage: true,
	}

	cmd.PersistentFlags().BoolVar(&rootNoColor, "no-color", false,
		"Disable colors")

	cmd.SetHelpFunc(func(c *cobra.Command, _ []string) {
		fmt.Fprint(c.OutOrStdout(), cliui.RenderCommandHelp(c))
	})

	cmd.AddCommand(
		configcmd.NewConfigCommand(),
		coding.NewCodeCommand(),
		coding.NewResumeCommand(),
		coding.NewThreadsCommand(),
		onboard.NewOnboardCommand(),
		agent.NewAgentCommand(),
		auth.NewAuthCommand(),
		gateway.NewGatewayCommand(),
		status.NewStatusCommand(),
		doctorcmd.NewDoctorCommand(),
		cron.NewCronCommand(),
		mcp.NewMCPCommand(),
		migrate.NewMigrateCommand(),
		skills.NewSkillsCommand(),
		model.NewModelCommand(),
		nodescmd.NewNodesCommand(),
		updater.NewUpdateCommand("mintclaw"),
		version.NewVersionCommand(),
	)

	return cmd
}

func main() {
	// Initialize Termux SSL certificate detection before anything else
	initTermuxSSL()

	machineJSON := machineJSONRequested(os.Args[1:])
	quietStartup := machineJSON || codingFrontendRequested(os.Args[1:])
	if quietStartup {
		logger.DisableConsole()
	}

	tzEnv := os.Getenv("TZ")
	if tzEnv != "" {
		loc, err := time.LoadLocation(tzEnv)
		if err == nil {
			time.Local = loc //nolint:gosmopolitan // We intentionally set local timezone from TZ env
		}
	}

	cmd := NewMintClawCommand()
	last, err := cmd.ExecuteC()
	if err != nil {
		var doctorExit *doctorcmd.ExitError
		if errors.As(err, &doctorExit) {
			os.Exit(doctorExit.Code)
		}
		fmt.Fprint(os.Stderr, cliui.FormatCLIError(err.Error(), last))
		os.Exit(1)
	}
}
