package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/pkg/skills"
)

type runtimeLoader func(context.Context, skills.SkillRuntime) (*skills.SkillsLoader, error)

func newListCommand(loaderFn runtimeLoader) *cobra.Command {
	var runtimeName string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List installed skills",
		Example: `mintclaw skills list`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			runtimeProduct, err := parseSkillRuntime(runtimeName)
			if err != nil {
				return err
			}
			loader, err := loaderFn(cmd.Context(), runtimeProduct)
			if err != nil {
				return err
			}
			return renderSkillsList(cmd.OutOrStdout(), loader.Compatibility(runtimeProduct), jsonOutput)
		},
	}
	cmd.Flags().
		StringVar(&runtimeName, "runtime", string(skills.SkillRuntimeGateway), "Runtime product: gateway or coding")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit stable JSON report")

	return cmd
}

func renderSkillsList(w io.Writer, report skills.SkillCompatibilityReport, jsonOutput bool) error {
	if jsonOutput {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	}
	if len(report.Skills) == 0 {
		_, err := fmt.Fprintln(w, "No skills installed.")
		return err
	}
	fmt.Fprintf(w, "Skills for %s runtime:\n", report.Runtime)
	for _, skill := range report.Skills {
		fmt.Fprintf(w, "  %-28s %-12s %s\n", skill.Name, skill.Scope, skill.Status)
	}
	return nil
}
