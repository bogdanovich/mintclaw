package document

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	documentpkg "github.com/bogdanovich/mintclaw/pkg/document"
)

type commandDeps struct {
	capabilities func() documentpkg.CapabilityReport
	acquire      func(context.Context, string, documentpkg.AcquireOptions) (*documentpkg.Snapshot, documentpkg.Report)
	scratchRoot  func() string
}

type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string {
	return e.Message
}

func NewDocumentCommand(scratchRoot func() string) *cobra.Command {
	return newDocumentCommand(commandDeps{
		capabilities: documentpkg.Capabilities,
		acquire:      documentpkg.Acquire,
		scratchRoot:  scratchRoot,
	})
}

func newDocumentCommand(deps commandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "document",
		Short:         "Inspect and process PDF documents",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.AddCommand(newCapabilitiesCommand(deps), newAcquireCommand(deps))
	return cmd
}

func newCapabilitiesCommand(deps commandDeps) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "capabilities",
		Short: "Show admitted document capabilities",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report := deps.capabilities()
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			return writeCapabilities(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit stable JSON output")
	return cmd
}

func newAcquireCommand(deps commandDeps) *cobra.Command {
	var input string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "acquire",
		Short: "Acquire an immutable PDF identity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, report := deps.acquire(cmd.Context(), input, documentpkg.AcquireOptions{
				ScratchRoot: deps.scratchRoot(),
			})
			if snapshot != nil {
				defer func() { _ = snapshot.Close() }()
				if err := snapshot.Close(); err != nil {
					report.State = documentpkg.StateFailed
					report.Input = nil
					report.Failure = &documentpkg.Failure{
						Code: documentpkg.FailureInternal, Message: "protected scratch cleanup failed",
					}
				}
			}
			if jsonOutput {
				if err := writeJSON(cmd.OutOrStdout(), report); err != nil {
					return err
				}
			} else {
				if err := writeAcquireReport(cmd.OutOrStdout(), report); err != nil {
					return err
				}
			}
			if code := reportExitCode(report); code != 0 {
				return &ExitError{Code: code, Message: "document acquisition did not succeed"}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&input, "input", "", "Path to the local PDF")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit stable JSON output")
	_ = cmd.MarkFlagRequired("input")
	return cmd
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func writeCapabilities(writer io.Writer, report documentpkg.CapabilityReport) error {
	if _, err := fmt.Fprintf(
		writer,
		"Document capabilities for %s/%s:\n",
		report.Platform,
		report.Architecture,
	); err != nil {
		return err
	}
	order := []string{"acquire", "inspect", "extract", "render", "fields", "fill", "verify", "flatten"}
	for _, name := range order {
		capability := report.Operations[name]
		if capability.Reason == "" {
			if _, err := fmt.Fprintf(writer, "  %s: %s\n", name, capability.State); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(writer, "  %s: %s — %s\n", name, capability.State, capability.Reason); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(writer, "Maximum input size: %d bytes\n", report.Limits.MaxInputBytes)
	return err
}

func writeAcquireReport(writer io.Writer, report documentpkg.Report) error {
	if report.State != documentpkg.StateSucceeded || report.Input == nil {
		message := "document acquisition failed"
		if report.Failure != nil {
			message = report.Failure.Message
		}
		_, err := fmt.Fprintf(writer, "Document acquisition %s: %s\n", report.State, message)
		return err
	}
	input := report.Input
	if _, err := fmt.Fprintf(writer, "Acquired %q\n", filepath.Base(input.OriginalFilename)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "SHA-256: %s\n", input.SHA256); err != nil {
		return err
	}
	_, err := fmt.Fprintf(writer, "Size: %d bytes\n", input.Size)
	return err
}

func reportExitCode(report documentpkg.Report) int {
	switch report.State {
	case documentpkg.StateSucceeded:
		return 0
	case documentpkg.StateUnavailable, documentpkg.StateUnsupported, documentpkg.StateDenied:
		return 3
	case documentpkg.StateCanceled:
		return 6
	case documentpkg.StateUncertain:
		return 7
	case documentpkg.StateFailed:
		if report.Failure != nil && report.Failure.Code == documentpkg.FailureLimitExceeded {
			return 5
		}
		if report.Failure != nil &&
			(report.Failure.Code == documentpkg.FailureInvalidInput ||
				report.Failure.Code == documentpkg.FailureUnsupportedType ||
				report.Failure.Code == documentpkg.FailureSourceChanged) {
			return 4
		}
	}
	return 1
}
