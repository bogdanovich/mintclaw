package eval

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	manualeval "github.com/bogdanovich/mintclaw/internal/eval"
)

func newPrepareCommand() *cobra.Command {
	var source, parent string
	var outputJSON bool
	command := &cobra.Command{
		Use: "prepare", Short: "Prepare a fresh private profile from an admitted isolated seed", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			preset, err := manualeval.LoadPreset(source)
			if err != nil {
				return err
			}
			result, prepareErr := manualeval.PrepareProfile(cmd.Context(), preset, manualeval.RuntimeHome(), parent)
			if result.Home == "" {
				return prepareErr
			}
			if outputJSON {
				err = json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			} else {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Profile %s: %s\nPreset: %s\n",
					result.Outcome, result.Home, result.PresetPath)
			}
			if err != nil {
				return err
			}
			return prepareErr
		},
	}
	command.Flags().StringVar(&source, "preset", "", "Reviewed isolated seed preset; MINTCLAW_HOME must match")
	command.Flags().StringVar(&parent, "out", "", "Existing private directory outside the seed profile")
	command.Flags().BoolVar(&outputJSON, "json", false, "Emit versioned preparation metadata without secrets")
	_ = command.MarkFlagRequired("preset")
	_ = command.MarkFlagRequired("out")
	return command
}
