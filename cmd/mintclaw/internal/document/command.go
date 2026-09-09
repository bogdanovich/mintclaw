package document

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	documentpkg "github.com/bogdanovich/mintclaw/pkg/document"
)

type commandDeps struct {
	capabilities func() documentpkg.CapabilityReport
	acquire      func(context.Context, string, documentpkg.AcquireOptions) (*documentpkg.Snapshot, documentpkg.Report)
	inspect      func(context.Context, string, documentpkg.AcquireOptions) (*documentpkg.Snapshot, documentpkg.Report)
	extract      func(context.Context, string, documentpkg.ReadOptions) (*documentpkg.Snapshot, documentpkg.Report)
	render       func(context.Context, string, documentpkg.ReadOptions) (*documentpkg.Snapshot, documentpkg.Report)
	scratchRoot  func() string
	serveWorker  func(io.Reader, io.Reader, io.Writer) error
	workerInput  func() (io.ReadCloser, error)
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
		inspect:      documentpkg.Inspect,
		extract:      documentpkg.Extract,
		render:       documentpkg.Render,
		scratchRoot:  scratchRoot,
		serveWorker:  documentpkg.ServeWorker,
		workerInput:  openWorkerInput,
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
	cmd.AddCommand(
		newCapabilitiesCommand(deps),
		newAcquireCommand(deps),
		newInspectCommand(deps),
		newExtractCommand(deps),
		newRenderCommand(deps),
		newWorkerCommand(deps),
	)
	return cmd
}

func newWorkerCommand(deps commandDeps) *cobra.Command {
	return &cobra.Command{
		Use:           "_worker",
		Short:         "Run the private document worker",
		Args:          cobra.NoArgs,
		Hidden:        true,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if deps.serveWorker == nil || deps.workerInput == nil {
				return fmt.Errorf("document worker dependencies are unavailable")
			}
			input, err := deps.workerInput()
			if err != nil {
				return err
			}
			defer func() { _ = input.Close() }()
			return deps.serveWorker(cmd.InOrStdin(), input, cmd.OutOrStdout())
		},
	}
}

func openWorkerInput() (io.ReadCloser, error) {
	file := os.NewFile(documentpkg.WorkerInputFileDescriptor(), "document-snapshot")
	if file == nil {
		return nil, fmt.Errorf("document worker input is unavailable")
	}
	return file, nil
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

func newInspectCommand(deps commandDeps) *cobra.Command {
	var input string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Inspect immutable PDF structure",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if deps.inspect == nil {
				return fmt.Errorf("document inspection is unavailable")
			}
			snapshot, report := deps.inspect(cmd.Context(), input, documentpkg.AcquireOptions{
				ScratchRoot: deps.scratchRoot(),
			})
			if snapshot != nil {
				defer func() { _ = snapshot.Close() }()
				if err := snapshot.Close(); err != nil {
					report.State = documentpkg.StateFailed
					report.Inspection = nil
					report.Failure = &documentpkg.Failure{
						Code: documentpkg.FailureInternal, Message: "protected scratch cleanup failed",
					}
				}
			}
			if jsonOutput {
				if err := writeJSON(cmd.OutOrStdout(), report); err != nil {
					return err
				}
			} else if err := writeInspectReport(cmd.OutOrStdout(), report); err != nil {
				return err
			}
			if code := reportExitCode(report); code != 0 {
				return &ExitError{Code: code, Message: "document inspection did not succeed"}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&input, "input", "", "Path to the local PDF")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit stable JSON output")
	_ = cmd.MarkFlagRequired("input")
	return cmd
}

func newExtractCommand(deps commandDeps) *cobra.Command {
	var input, pageSelection, output string
	var jsonOutput bool
	var maxCharacters int
	cmd := &cobra.Command{
		Use:   "extract",
		Short: "Extract bounded text from immutable PDF pages",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if deps.extract == nil {
				return fmt.Errorf("document extraction is unavailable")
			}
			pages, err := parsePageSelection(pageSelection, documentpkg.DefaultMaxExtractPages)
			if err != nil {
				return &ExitError{Code: 4, Message: err.Error()}
			}
			snapshot, report := deps.extract(cmd.Context(), input, documentpkg.ReadOptions{
				Acquire: documentpkg.AcquireOptions{ScratchRoot: deps.scratchRoot()},
				Pages:   pages,
				Limits:  documentpkg.ReadLimits{MaxCharacters: maxCharacters},
			})
			var staged *stagedArtifactOutput
			if snapshot != nil && report.State == documentpkg.StateSucceeded && len(report.Artifacts) == 1 {
				if staged, err = stageArtifactFile(snapshot, report.Artifacts[0].Ref, output); err != nil {
					failArtifactPublication(&report)
				}
			}
			var closeSnapshot func() error
			if snapshot != nil {
				closeSnapshot = snapshot.Close
			}
			finishArtifactPublication(closeSnapshot, staged, &report)
			if jsonOutput {
				err = writeJSON(cmd.OutOrStdout(), report)
			} else {
				err = writeReadReport(cmd.OutOrStdout(), report, output)
			}
			if err != nil {
				return err
			}
			if code := reportExitCode(report); code != 0 {
				return &ExitError{Code: code, Message: "document extraction did not succeed"}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&input, "input", "", "Path to the local PDF")
	cmd.Flags().
		StringVar(&pageSelection, "pages", "", "One-based pages, for example 1,3-5 (defaults to all within limit)")
	cmd.Flags().StringVar(&output, "output", "", "Destination for the UTF-8 JSON Lines artifact")
	cmd.Flags().IntVar(&maxCharacters, "max-characters", 0, "Lower the extraction character limit")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit stable JSON output")
	_ = cmd.MarkFlagRequired("input")
	_ = cmd.MarkFlagRequired("output")
	return cmd
}

func newRenderCommand(deps commandDeps) *cobra.Command {
	var input, pageSelection, outputDirectory string
	var jsonOutput bool
	var dpi, maxDimension int
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Render bounded immutable PDF pages to PNG",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if deps.render == nil {
				return fmt.Errorf("document rendering is unavailable")
			}
			pages, err := parsePageSelection(pageSelection, documentpkg.DefaultMaxRenderPages)
			if err != nil {
				return &ExitError{Code: 4, Message: err.Error()}
			}
			snapshot, report := deps.render(cmd.Context(), input, documentpkg.ReadOptions{
				Acquire: documentpkg.AcquireOptions{ScratchRoot: deps.scratchRoot()},
				Pages:   pages,
				Limits:  documentpkg.ReadLimits{DPI: dpi, MaxDimension: maxDimension},
			})
			var staged *stagedArtifactOutput
			if snapshot != nil && report.State == documentpkg.StateSucceeded && len(report.Artifacts) > 0 {
				if staged, err = stageArtifactDirectory(
					snapshot,
					report.Artifacts,
					outputDirectory,
				); err != nil {
					failArtifactPublication(&report)
				}
			}
			var closeSnapshot func() error
			if snapshot != nil {
				closeSnapshot = snapshot.Close
			}
			finishArtifactPublication(closeSnapshot, staged, &report)
			if jsonOutput {
				err = writeJSON(cmd.OutOrStdout(), report)
			} else {
				err = writeReadReport(cmd.OutOrStdout(), report, outputDirectory)
			}
			if err != nil {
				return err
			}
			if code := reportExitCode(report); code != 0 {
				return &ExitError{Code: code, Message: "document rendering did not succeed"}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&input, "input", "", "Path to the local PDF")
	cmd.Flags().
		StringVar(&pageSelection, "pages", "", "One-based pages, for example 1,3-5 (defaults to all within limit)")
	cmd.Flags().StringVar(&outputDirectory, "output-dir", "", "Destination directory for page PNG artifacts")
	cmd.Flags().IntVar(&dpi, "dpi", 0, "Lower the render DPI limit")
	cmd.Flags().IntVar(&maxDimension, "max-dimension", 0, "Lower the maximum rendered edge")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit stable JSON output")
	_ = cmd.MarkFlagRequired("input")
	_ = cmd.MarkFlagRequired("output-dir")
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
	_, err := fmt.Fprintf(
		writer,
		"Limits: input=%d bytes, pages=%d, decoded content=%d bytes, objects=%d, recursion=%d\n",
		report.Limits.MaxInputBytes,
		report.Limits.MaxPages,
		report.Limits.MaxContentBytes,
		report.Limits.MaxObjects,
		report.Limits.MaxRecursionDepth,
	)
	if err != nil {
		return err
	}
	extract := report.ReadLimits["extract"]
	render := report.ReadLimits["render"]
	_, err = fmt.Fprintf(
		writer,
		"Read limits: extract-pages=%d, characters=%d; render-pages=%d, dpi=%d, edge=%d, pixels=%d\n",
		extract.MaxPages,
		extract.MaxCharacters,
		render.MaxPages,
		render.DPI,
		render.MaxDimension,
		render.MaxTotalPixels,
	)
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

func writeInspectReport(writer io.Writer, report documentpkg.Report) error {
	if report.State != documentpkg.StateSucceeded || report.Input == nil || report.Inspection == nil {
		message := "document inspection failed"
		if report.Failure != nil {
			message = report.Failure.Message
		}
		_, err := fmt.Fprintf(writer, "Document inspection %s: %s\n", report.State, message)
		return err
	}
	facts := report.Inspection
	if _, err := fmt.Fprintf(writer, "Inspected %q\n", filepath.Base(report.Input.OriginalFilename)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "SHA-256: %s\n", report.Input.SHA256); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "PDF version: %s\n", facts.PDFVersion.Value); err != nil {
		return err
	}
	pages := 0
	if facts.PageCount.Value != nil {
		pages = *facts.PageCount.Value
	}
	_, err := fmt.Fprintf(
		writer,
		"Pages: %d; encrypted: %s; signatures: %s; AcroForm: %s; XFA: %s; text: %s\n",
		pages,
		facts.Encryption.State,
		facts.Signatures.State,
		facts.AcroForm.State,
		facts.XFA.State,
		facts.ExtractableText.State,
	)
	return err
}

func writeReadReport(writer io.Writer, report documentpkg.Report, destination string) error {
	if report.State != documentpkg.StateSucceeded || report.Input == nil || len(report.Artifacts) == 0 {
		message := "document read failed"
		if report.Failure != nil {
			message = report.Failure.Message
		}
		_, err := fmt.Fprintf(writer, "Document %s %s: %s\n", report.Operation, report.State, message)
		return err
	}
	if _, err := fmt.Fprintf(
		writer,
		"Document %s succeeded for %q\n",
		report.Operation,
		filepath.Base(report.Input.OriginalFilename),
	); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "Source SHA-256: %s\n", report.Input.SHA256); err != nil {
		return err
	}
	_, err := fmt.Fprintf(writer, "Artifacts: %d written to %s\n", len(report.Artifacts), destination)
	return err
}

func reportExitCode(report documentpkg.Report) int {
	switch report.State {
	case documentpkg.StateSucceeded:
		return 0
	case documentpkg.StateUnavailable, documentpkg.StateDenied:
		return 3
	case documentpkg.StateUnsupported:
		if report.Failure != nil &&
			(report.Failure.Code == documentpkg.FailureUnsupportedType ||
				report.Failure.Code == documentpkg.FailureMalformedPDF ||
				report.Failure.Code == documentpkg.FailureUnsupportedFeature ||
				report.Failure.Code == documentpkg.FailureTextUnavailable ||
				report.Failure.Code == documentpkg.FailureVisionUnavailable) {
			return 4
		}
		return 3
	case documentpkg.StateCanceled:
		return 6
	case documentpkg.StateUncertain:
		return 7
	case documentpkg.StateFailed:
		if report.Failure != nil &&
			(report.Failure.Code == documentpkg.FailureLimitExceeded ||
				report.Failure.Code == documentpkg.FailureInspectionLimit ||
				report.Failure.Code == documentpkg.FailureExtractionLimit ||
				report.Failure.Code == documentpkg.FailureRenderLimit) {
			return 5
		}
		if report.Failure != nil &&
			(report.Failure.Code == documentpkg.FailureInvalidInput ||
				report.Failure.Code == documentpkg.FailureSourceChanged ||
				report.Failure.Code == documentpkg.FailureMalformedPDF ||
				report.Failure.Code == documentpkg.FailureInvalidPageSelection) {
			return 4
		}
	}
	return 1
}
