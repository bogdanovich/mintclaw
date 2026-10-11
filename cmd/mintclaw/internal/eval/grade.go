package eval

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	manualeval "github.com/bogdanovich/mintclaw/internal/eval"
)

func newGradeCommand() *cobra.Command {
	options := corpusOptions{root: "evals", selection: manualeval.Selection{Suite: "all"}}
	var outputParent string
	var calibration string
	suiteTimeout := time.Hour
	command := &cobra.Command{
		Use: "grade [private-run-directory]", Short: "Regrade eligible private evidence without gateway or tool work",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if calibration != "" {
				if len(args) != 0 || cmd.Flags().Changed("suite") || cmd.Flags().Changed("tag") ||
					cmd.Flags().Changed("case") || cmd.Flags().Changed("root") {
					return manualeval.ErrRunOptions
				}
				preset, err := manualeval.LoadPreset(options.preset)
				if err != nil {
					return err
				}
				result, gradeErr := manualeval.CalibratePreset(
					cmd.Context(),
					preset,
					calibration,
					manualeval.RunOptions{
						Home: manualeval.RuntimeHome(), OutputParent: outputParent, SuiteTimeout: suiteTimeout,
					},
				)
				if result.ID == "" {
					return gradeErr
				}
				if options.json {
					examples := make([]calibrationExampleSummary, 0, len(result.Examples))
					for _, example := range result.Examples {
						examples = append(examples, calibrationExampleSummary{
							ID:       example.ID,
							Expected: example.Expected,
							Observed: example.Observed,
							Verdict:  example.Verdict,
						})
					}
					output := struct {
						Version       int                         `json:"version"`
						ID            string                      `json:"id"`
						Verdict       manualeval.Verdict          `json:"verdict"`
						Complete      bool                        `json:"complete"`
						PromptVersion string                      `json:"prompt_version"`
						SHA256        string                      `json:"sha256"`
						Examples      []calibrationExampleSummary `json:"examples"`
						RoleUsage     []manualeval.RoleUsage      `json:"role_usage"`
						ReportDir     string                      `json:"report_dir"`
					}{
						result.Version, result.ID, result.Verdict, result.Complete, result.PromptVersion,
						result.SHA256, examples, result.RoleUsage, result.ReportDir,
					}
					if err := json.NewEncoder(cmd.OutOrStdout()).Encode(output); err != nil {
						return err
					}
				} else {
					fmt.Fprintf(
						cmd.OutOrStdout(),
						"Calibration: %s; private report: %s\n",
						result.Verdict,
						result.ReportDir,
					)
				}
				return gradeErr
			}
			if len(args) != 1 {
				return manualeval.ErrRunOptions
			}
			corpus, err := manualeval.LoadCorpus(options.root)
			if err != nil {
				return err
			}
			selected, err := manualeval.Select(corpus, options.selection)
			if err != nil {
				return err
			}
			preset, err := manualeval.LoadPreset(options.preset)
			if err != nil {
				return err
			}
			result, gradeErr := manualeval.Grade(cmd.Context(), preset, selected, args[0], manualeval.RunOptions{
				Home: manualeval.RuntimeHome(), OutputParent: outputParent, SuiteTimeout: suiteTimeout,
			})
			if result.ID == "" {
				return gradeErr
			}
			if options.json {
				output := struct {
					manualeval.RunReport
					ReportDir string `json:"report_dir"`
				}{RunReport: result, ReportDir: result.ReportDir}
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(output); err != nil {
					return err
				}
			} else {
				for _, trial := range result.Trials {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", trial.CaseID, trial.Verdict, trial.Code)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Regrade: %s; source: %s; private report: %s\n",
					result.Verdict, result.SourceRun, result.ReportDir)
			}
			return gradeErr
		},
	}
	bindCorpusFlags(command, &options)
	command.Flags().StringVar(&outputParent, "out", "", "Existing private report parent outside the workspace")
	command.Flags().DurationVar(&suiteTimeout, "suite-timeout", suiteTimeout, "Whole regrade limit (maximum 4h)")
	command.Flags().StringVar(&calibration, "calibration", "", "Judge a pinned labeled corpus instead of a target run")
	_ = command.MarkFlagRequired("preset")
	_ = command.MarkFlagRequired("out")
	return command
}

type calibrationExampleSummary struct {
	ID       string             `json:"id"`
	Expected manualeval.Verdict `json:"expected"`
	Observed manualeval.Verdict `json:"observed"`
	Verdict  manualeval.Verdict `json:"verdict"`
}
