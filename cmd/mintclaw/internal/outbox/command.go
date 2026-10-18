package outbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	outboxpkg "github.com/bogdanovich/mintclaw/pkg/outbox"
)

func NewOutboxCommand() *cobra.Command {
	var root string
	var jsonOutput bool
	var maxBytes, maxRecords int64
	var minAge time.Duration
	cmd := &cobra.Command{
		SilenceErrors: true,
		SilenceUsage:  true,
		Use:           "outbox",
		Short:         "Inspect capacity or retire eligible outbox payloads offline",
		Args:          cobra.NoArgs,
		Long:          "Inspect an existing instance outbox or retire explicitly selected eligible payloads.\nThe gateway must release its cooperating-writer lock. Delivery identities never expire.",
	}
	flags := cmd.PersistentFlags()
	flags.StringVar(&root, "instance-root", "", "Instance workspace containing state/outbox (required)")
	flags.BoolVar(&jsonOutput, "json", false, "Print the bounded machine-readable report")
	flags.DurationVar(&minAge, "min-age", 0, "Explicit minimum payload age, for example 168h (required)")
	flags.Int64Var(&maxBytes, "max-bytes", 0, "Logical-store byte budget; zero means unconfigured")
	flags.Int64Var(&maxRecords, "max-records", 0, "Retained identity count budget; zero means unconfigured")
	_ = cmd.MarkPersistentFlagRequired("instance-root")
	_ = cmd.MarkPersistentFlagRequired("min-age")
	inspect := &cobra.Command{
		Use: "inspect", Short: "Count all retained history and show up to 64 eligible delivery IDs", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			report, err := outboxpkg.InspectCapacityOffline(command.Context(), root, outboxpkg.CapacityPolicy{
				MinAge: minAge, MaxBytes: maxBytes, MaxRecords: maxRecords,
			})
			if err != nil {
				return maintenanceError(err)
			}
			if jsonOutput {
				if err = json.NewEncoder(command.OutOrStdout()).Encode(report); err != nil {
					return err
				}
			} else {
				if err = writeCapacitySummary(command.OutOrStdout(), report); err != nil {
					return err
				}
				if len(report.Candidates) > 0 {
					fmt.Fprintln(command.OutOrStdout(), "Eligible delivery IDs:")
				}
				for _, id := range report.Candidates {
					if _, err = fmt.Fprintln(command.OutOrStdout(), id); err != nil {
						return err
					}
				}
			}
			if report.OperatorActionRequired {
				return errors.New(
					"outbox capacity requires operator action; protected work and delivery identities were preserved",
				)
			}
			return nil
		},
	}
	var ids []string
	var apply bool
	retire := &cobra.Command{
		Use:   "retire",
		Short: "Dry-run one explicit bounded batch; add --apply to retire eligible payloads",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			report, err := outboxpkg.RetirePayloadsOffline(command.Context(), root, outboxpkg.RetirementPolicy{
				DeliveryIDs: ids, MinAge: minAge, Apply: apply,
				CapacityBudget: &outboxpkg.CapacityBudget{MaxBytes: maxBytes, MaxRecords: maxRecords},
			})
			if jsonOutput {
				if writeErr := json.NewEncoder(command.OutOrStdout()).Encode(report); writeErr != nil {
					return writeErr
				}
			} else {
				mode := "Dry run"
				if apply {
					mode = "Apply"
				}
				if _, writeErr := fmt.Fprintf(
					command.OutOrStdout(),
					"%s: %d observed delivery IDs\n",
					mode,
					len(report.Items),
				); writeErr != nil {
					return writeErr
				}
				if report.CapacityBefore != nil {
					fmt.Fprintln(command.OutOrStdout(), "Before batch:")
					if writeErr := writeCapacitySummary(
						command.OutOrStdout(),
						*report.CapacityBefore,
					); writeErr != nil {
						return writeErr
					}
				}
				for _, item := range report.Items {
					if _, writeErr := fmt.Fprintf(
						command.OutOrStdout(),
						"%s %s %d -> %d bytes\n",
						item.DeliveryID,
						item.Outcome,
						item.BeforeBytes,
						item.AfterBytes,
					); writeErr != nil {
						return writeErr
					}
				}
			}
			if !jsonOutput && report.CapacityAfter != nil {
				fmt.Fprintln(command.OutOrStdout(), "After batch:")
				if writeErr := writeCapacitySummary(command.OutOrStdout(), *report.CapacityAfter); writeErr != nil {
					return writeErr
				}
			}
			if err != nil {
				return maintenanceError(err)
			}
			capacity := report.CapacityAfter
			if capacity == nil {
				capacity = report.CapacityBefore
			}
			if capacity != nil && capacity.OperatorActionRequired {
				return errors.New("outbox capacity still requires operator action; no delivery identities were removed")
			}
			return nil
		},
	}
	retire.Flags().StringArrayVar(&ids, "delivery", nil, "One canonical delivery ID; repeat for up to 64 unique IDs")
	retire.Flags().
		BoolVar(&apply, "apply", false, "Retire eligible payloads after the offline preflight; default is dry-run")
	_ = retire.MarkFlagRequired("delivery")
	cmd.AddCommand(inspect, retire)
	return cmd
}

func maintenanceError(err error) error {
	if errors.Is(err, outboxpkg.ErrCoordinatorOwned) {
		return errors.New("outbox is owned by another writer; release that owner before offline maintenance")
	}
	return errors.New("outbox maintenance could not complete; records and delivery identities require inspection")
}

func writeCapacitySummary(w io.Writer, report outboxpkg.CapacityReport) error {
	_, err := fmt.Fprintf(w, "Outbox: %d identities, %d logical bytes; minimum age: %s\n"+
		"Active: %d / %d bytes; protected: %d / %d bytes; eligible: %d / %d bytes; "+
		"receipts: %d / %d bytes; unavailable: %d / %d bytes\n",
		report.TotalRecords, report.TotalBytes, report.MinimumAge,
		report.Active.Count, report.Active.Bytes, report.Protected.Count, report.Protected.Bytes,
		report.Eligible.Count, report.Eligible.Bytes, report.Receipts.Count, report.Receipts.Bytes,
		report.Unavailable.Count, report.Unavailable.Bytes)
	return err
}
