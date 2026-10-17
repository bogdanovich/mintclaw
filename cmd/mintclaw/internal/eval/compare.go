package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	manualeval "github.com/bogdanovich/mintclaw/internal/eval"
)

func newCompareCommand() *cobra.Command {
	var outputJSON bool
	command := &cobra.Command{
		Use:   "compare BASELINE_REPORT CURRENT_REPORT",
		Short: "Compare complete private eval reports without target or model work",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := manualeval.Compare(args[0], args[1])
			if err != nil && !errors.Is(err, manualeval.ErrNotComparable) {
				return err
			}
			if outputJSON {
				if writeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); writeErr != nil {
					return writeErr
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Run verdicts: baseline=%s current=%s\n",
					result.BaselineVerdict, result.CurrentVerdict)
				fmt.Fprintln(cmd.OutOrStdout(), "Case\tBaseline pass/trials\tCurrent pass/trials\tStatus")
				for _, item := range result.Cases {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%d/%d\t%d/%d\t%s\n", item.CaseID,
						item.Baseline.Coverage[manualeval.Pass], item.Baseline.Trials,
						item.Current.Coverage[manualeval.Pass], item.Current.Trials, item.Status)
					fmt.Fprintf(cmd.OutOrStdout(), "  baseline: %s; current: %s\n",
						comparisonCoverage(item.Baseline), comparisonCoverage(item.Current))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Compatible cases: %t; environment changes: %s\n",
					result.CompatibleCases, strings.Join(result.EnvironmentChanges, ", "))
				fmt.Fprintf(
					cmd.OutOrStdout(),
					"Unknown conditions: %s\n",
					strings.Join(result.EnvironmentUnknowns, ", "),
				)
				fmt.Fprintln(cmd.OutOrStdout(), "Recorded runtime/roles only; instructions and skills unrecorded. "+
					"Causal quality attribution is not established.")
			}
			return err
		},
	}
	command.Flags().BoolVar(&outputJSON, "json", false, "Emit versioned counts and compatibility metadata")
	return command
}

func comparisonCoverage(counts manualeval.ComparisonCounts) string {
	return fmt.Sprintf("fail=%d error=%d inconclusive=%d skipped=%d",
		counts.Coverage[manualeval.Fail], counts.Coverage[manualeval.Error],
		counts.Coverage[manualeval.Inconclusive], counts.Coverage[manualeval.Skipped])
}
