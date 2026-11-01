package eval

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	manualeval "github.com/bogdanovich/mintclaw/internal/eval"
)

type corpusOptions struct {
	root      string
	selection manualeval.Selection
	preset    string
	json      bool
}

type catalogItem struct {
	ID            string   `json:"id"`
	Suite         string   `json:"suite"`
	Tags          []string `json:"tags"`
	Description   string   `json:"description"`
	CaseSHA256    string   `json:"case_sha256"`
	Status        string   `json:"status"`
	Default       bool     `json:"default"`
	PendingReason string   `json:"pending_reason,omitempty"`
}

type catalogResult struct {
	Version    int           `json:"version"`
	Outcome    string        `json:"outcome"`
	Cases      []catalogItem `json:"cases"`
	PresetID   string        `json:"preset_id,omitempty"`
	RuntimeRun bool          `json:"runtime_run"`
}

func NewEvalCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "eval", Short: "Run manual, evidence-backed agent evaluations",
		Args: cobra.NoArgs,
	}
	for _, name := range []string{"list", "validate"} {
		command.AddCommand(newCorpusCommand(name))
	}
	command.AddCommand(newRunCommand())
	command.AddCommand(newPrepareCommand())
	command.AddCommand(newGradeCommand())
	command.AddCommand(newCompareCommand())
	return command
}

func newCorpusCommand(name string) *cobra.Command {
	options := corpusOptions{root: "evals", selection: manualeval.Selection{Suite: "all"}}
	short := "List selected scenarios without contacting a model or gateway"
	if name == "validate" {
		short = "Validate selected scenarios, fixtures, and optional preset offline"
	}
	command := &cobra.Command{
		Use: name, Short: short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			corpus, err := manualeval.LoadCorpus(options.root)
			if err != nil {
				return err
			}
			selected, err := manualeval.Select(corpus, options.selection)
			if err != nil {
				return err
			}
			result := catalogResult{
				Version: manualeval.Version, Outcome: "valid", Cases: make([]catalogItem, 0, len(selected)),
			}
			if options.preset != "" {
				preset, presetErr := manualeval.LoadPreset(options.preset)
				if presetErr != nil {
					return presetErr
				}
				result.PresetID = preset.ID
			}
			for _, item := range selected {
				policy := item.Availability()
				result.Cases = append(result.Cases, catalogItem{
					ID: item.Case.ID, Suite: item.Case.Suite, Tags: item.Case.Tags,
					Description: item.Case.Description, CaseSHA256: item.Digest,
					Status: policy.Status, Default: policy.Default, PendingReason: policy.Reason,
				})
			}
			if options.json {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			for _, item := range result.Cases {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\tdefault=%t\t%s\t%s\n",
					item.ID, item.Suite, item.Status, item.Default, item.Description, item.PendingReason)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d scenarios validated offline; no runtime run.\n", len(result.Cases))
			return nil
		},
	}
	bindCorpusFlags(command, &options)
	return command
}

func bindCorpusFlags(command *cobra.Command, options *corpusOptions) {
	command.Flags().StringVar(&options.root, "root", options.root, "Corpus directory containing cases/ and fixtures/")
	command.Flags().StringVar(&options.selection.Suite, "suite", options.selection.Suite,
		"Select a suite (run without filters uses catalog defaults; list/validate/grade default to all)")
	command.Flags().StringArrayVar(&options.selection.Tags, "tag", nil, "Require a tag (repeatable; all must match)")
	command.Flags().StringArrayVar(&options.selection.IDs, "case", nil, "Select a case ID (repeatable)")
	command.Flags().
		StringVar(&options.preset, "preset", "", "Target preset manifest (offline for list/validate)")
	command.Flags().BoolVar(&options.json, "json", false, "Emit versioned JSON")
}
