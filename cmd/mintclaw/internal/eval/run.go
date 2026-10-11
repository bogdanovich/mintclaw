package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	manualeval "github.com/bogdanovich/mintclaw/internal/eval"
)

func newRunCommand() *cobra.Command {
	options := corpusOptions{root: "evals", selection: manualeval.Selection{Suite: "all"}}
	var outputParent string
	python := os.Getenv("MINTCLAW_EVAL_PDF_ORACLE_PYTHON")
	suiteTimeout := time.Hour
	repeat := 1
	command := &cobra.Command{
		Use: "run", Short: "Run selected scenarios against a prepared isolated gateway", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repeat < 1 || repeat > manualeval.MaxRepeat {
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
			result, runErr := manualeval.Run(cmd.Context(), preset, selected, manualeval.RunOptions{
				Home: manualeval.RuntimeHome(), OutputParent: outputParent, SuiteTimeout: suiteTimeout,
				PDFOraclePython: python,
				Repeat:          repeat,
			})
			if result.ID == "" {
				return runErr
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
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%d/%d\t%s\t%s\n",
						trial.CaseID, trial.Repetition, result.Repeat, trial.Verdict, trial.Code)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Verdict: %s; private report: %s\n", result.Verdict, result.ReportDir)
			}
			return runErr
		},
	}
	bindCorpusFlags(command, &options)
	command.Flags().StringVar(&outputParent, "out", "", "Existing private report directory outside the workspace")
	command.Flags().DurationVar(&suiteTimeout, "suite-timeout", suiteTimeout, "Whole run limit (maximum 4h)")
	command.Flags().IntVar(&repeat, "repeat", repeat, "Independent serial trials per case (1-64; at most 256 total)")
	command.Flags().
		StringVar(&python, "pdf-oracle-python", python, "Absolute executable of the pinned independent PDF oracle")
	_ = command.MarkFlagRequired("preset")
	_ = command.MarkFlagRequired("out")
	return command
}
