package skills

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	runtimeskills "github.com/bogdanovich/mintclaw/pkg/skills"
)

type ExitError struct {
	Code int
}

func (err *ExitError) Error() string {
	return fmt.Sprintf("skills doctor found issues (exit code %d)", err.Code)
}

func newDoctorCommand(loaderFn runtimeLoader) *cobra.Command {
	var runtimeName string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check skill compatibility without changing the system",
		Long: `Check skill metadata and runtime dependencies without installing packages,
starting MCP servers, or changing tool policy.

Exit codes:
  0: catalog inspected with no malformed skills or missing dependencies
  1: command or configuration error
  2: catalog inspection failure, malformed skill, or missing dependency`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			runtimeProduct, err := parseSkillRuntime(runtimeName)
			if err != nil {
				return err
			}
			loader, err := loaderFn(cmd.Context(), runtimeProduct)
			if err != nil {
				return err
			}
			report := loader.Compatibility(runtimeProduct)
			if err = renderSkillsDoctor(cmd.OutOrStdout(), report, jsonOutput); err != nil {
				return err
			}
			if skillsDoctorExitCode(report) != 0 {
				return &ExitError{Code: 2}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(
		&runtimeName,
		"runtime",
		string(runtimeskills.SkillRuntimeGateway),
		"Runtime product: gateway or coding",
	)
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit stable JSON report")
	return cmd
}

func parseSkillRuntime(value string) (runtimeskills.SkillRuntime, error) {
	switch runtimeProduct := runtimeskills.SkillRuntime(strings.ToLower(strings.TrimSpace(value))); runtimeProduct {
	case runtimeskills.SkillRuntimeGateway, runtimeskills.SkillRuntimeCoding:
		return runtimeProduct, nil
	default:
		return "", fmt.Errorf(
			"runtime must be %q or %q",
			runtimeskills.SkillRuntimeGateway,
			runtimeskills.SkillRuntimeCoding,
		)
	}
}

func renderSkillsDoctor(w io.Writer, report runtimeskills.SkillCompatibilityReport, jsonOutput bool) error {
	if jsonOutput {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	}
	fmt.Fprintf(w, "Skill compatibility for %s runtime:\n", report.Runtime)
	for _, skill := range report.Skills {
		fmt.Fprintf(w, "  %-28s %-12s %s\n", skill.Name, skill.Scope, skill.Status)
		for _, check := range skill.Checks {
			fmt.Fprintf(w, "    - %s %s: %s\n", check.Kind, check.Name, check.State)
		}
	}
	if len(report.Diagnostics) > 0 {
		fmt.Fprintln(w, "Catalog diagnostics:")
		for _, diagnostic := range report.Diagnostics {
			fmt.Fprintf(w, "  - %s", diagnostic.Kind)
			if diagnostic.Scope != "" {
				fmt.Fprintf(w, " [%s]", diagnostic.Scope)
			}
			if diagnostic.Path != "" {
				fmt.Fprintf(w, " %s", diagnostic.Path)
			}
			fmt.Fprintf(w, ": %s\n", diagnostic.Message)
		}
	}
	return nil
}

func skillsDoctorExitCode(report runtimeskills.SkillCompatibilityReport) int {
	for _, diagnostic := range report.Diagnostics {
		switch diagnostic.Kind {
		case runtimeskills.CatalogDiagnosticRootUnreadable,
			runtimeskills.CatalogDiagnosticPathEscape,
			runtimeskills.CatalogDiagnosticMetadataUnreadable:
			return 2
		default:
		}
	}
	for _, skill := range report.Skills {
		switch skill.Status {
		case runtimeskills.SkillCompatibilityMalformed, runtimeskills.SkillCompatibilityMissingDependency:
			return 2
		default:
		}
	}
	return 0
}
