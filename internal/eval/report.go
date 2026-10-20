package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/health"
)

var ErrReport = errors.New("eval: private report unavailable")

type TrialSummary struct {
	ID         string  `json:"id"`
	CaseID     string  `json:"case_id"`
	CaseSHA256 string  `json:"case_sha256"`
	Repetition int     `json:"repetition,omitempty"`
	Verdict    Verdict `json:"verdict"`
	Code       string  `json:"code"`
	DurationMS int64   `json:"duration_ms"`
	File       string  `json:"file"`
}

type RunReport struct {
	Version         int                         `json:"version"`
	ID              string                      `json:"id"`
	PresetID        string                      `json:"preset_id"`
	Runtime         health.RuntimeProbe         `json:"runtime"`
	Verdict         Verdict                     `json:"verdict"`
	StartedAt       time.Time                   `json:"started_at"`
	DurationMS      int64                       `json:"duration_ms"`
	Trials          []TrialSummary              `json:"trials"`
	Coverage        map[Verdict]int             `json:"coverage"`
	Usage           string                      `json:"usage"`
	RoleUsage       []RoleUsage                 `json:"role_usage,omitempty"`
	TargetUsage     *TargetUsage                `json:"target_usage,omitempty"`
	NativeModels    *NativeModelObservation     `json:"native_models,omitempty"`
	Mode            string                      `json:"mode,omitempty"`
	Repeat          int                         `json:"repeat,omitempty"`
	SourceRun       string                      `json:"source_run,omitempty"`
	ReportDir       string                      `json:"-"`
	Cleanup         string                      `json:"cleanup"`
	Complete        bool                        `json:"complete"`
	TargetLifecycle *TargetLifecycle            `json:"target_lifecycle,omitempty"`
	WorkspaceInputs *WorkspaceInputs            `json:"workspace_inputs,omitempty"`
	PrivateSurfaces *PrivateSurfaceObservation  `json:"private_surfaces,omitempty"`
	BrowserFixtures *BrowserFixtureHostEvidence `json:"browser_fixtures,omitempty"`
	TrialScratch    *TrialScratchObservation    `json:"trial_scratch,omitempty"`
}

type privateReport struct {
	root        *os.Root
	dir         string
	staging     map[string]*trialStaging
	stagingRoot os.FileInfo
}

func newPrivateReport(parent, home, workspace, id string) (*privateReport, error) {
	physicalParent, err := privateHome(parent)
	physicalWorkspace, workspaceErr := filepath.EvalSymlinks(workspace)
	if err != nil || workspaceErr != nil || !pathWithin(home, physicalParent) ||
		physicalParent == physicalWorkspace || pathWithin(physicalWorkspace, physicalParent) ||
		pathWithin(physicalParent, physicalWorkspace) || !validID(id) {
		return nil, ErrReport
	}
	root, err := os.OpenRoot(physicalParent)
	if err != nil {
		return nil, ErrReport
	}
	defer func() { _ = root.Close() }()
	if root.Mkdir(id, 0o700) != nil {
		return nil, ErrReport
	}
	child, err := root.OpenRoot(id)
	if err != nil {
		return nil, ErrReport
	}
	return &privateReport{root: child, dir: filepath.Join(physicalParent, id)}, nil
}

func (report *privateReport) write(name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > 8*1024*1024 || !safeRelativePath(name) {
		return ErrReport
	}
	return report.writeBytes(name, append(data, '\n'))
}

func (report *privateReport) writeBytes(name string, data []byte) error {
	return report.writeBoundedFile(name, data, 8*1024*1024)
}

// Binary artifacts are private evidence, not model or JSON report contents.
func (report *privateReport) retainPDFArtifact(trialID string, data []byte) (string, error) {
	if !validID(trialID) || len(data) == 0 {
		return "", ErrReport
	}
	name := "artifact-" + trialID + ".pdf"
	if err := report.writeBoundedFile(name, data, maxPDFBytes); err != nil {
		return "", err
	}
	return name, nil
}

func (report *privateReport) writeBoundedFile(name string, data []byte, maximum int) error {
	if !safeRelativePath(name) || len(data) > maximum {
		return ErrReport
	}
	file, err := report.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrReport
	}
	count, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || count != len(data) || syncErr != nil || closeErr != nil {
		_ = report.root.Remove(name)
		return ErrReport
	}
	return nil
}

func (report *privateReport) finish(result RunReport) error {
	file, err := report.root.OpenFile("summary.md", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrReport
	}
	_, writeErr := fmt.Fprintf(file,
		"# Agent Eval Run\n\nRun: `%s`\n\nVerdict: **%s**\n\n"+
			"| Case | Trial | Verdict | Code | Evidence |\n| --- | --- | --- | --- | --- |\n",
		result.ID, result.Verdict,
	)
	for _, trial := range result.Trials {
		if writeErr != nil {
			break
		}
		repetition, repeat := trial.Repetition, result.Repeat
		if repeat == 0 {
			repetition, repeat = 1, 1
		}
		_, writeErr = fmt.Fprintf(file, "| %s | %d/%d | %s | %s | [%s](%s) |\n",
			trial.CaseID, repetition, repeat, trial.Verdict, trial.Code, trial.ID, trial.File)
	}
	if writeErr == nil {
		for _, usage := range result.RoleUsage {
			counts := "unknown"
			if usage.Coverage == "partial_returned_calls" || usage.Coverage == "returned_calls_only" {
				counts = CompactTokens(usage.InputTokens) + " in, " + CompactTokens(usage.OutputTokens) + " out"
			}
			_, writeErr = fmt.Fprintf(file, "\n%s: %s (%s); %d attempted calls, %d returned usage.\n",
				usage.Role, counts, usage.Coverage, usage.AttemptedCalls, usage.ReturnedUsage)
			if writeErr != nil {
				break
			}
		}
	}
	if writeErr == nil {
		if usage := result.TargetUsage; usage != nil {
			counts := "unknown"
			if usage.Coverage == "returned_finals_only" || usage.Coverage == "partial_returned_finals" {
				counts = CompactTokens(usage.InputTokens) + " in, " + CompactTokens(usage.OutputTokens) + " out"
			}
			_, writeErr = fmt.Fprintf(file,
				"\nTarget returned finals: %s (%s); %d/%d finals returned usage.\n",
				counts, usage.Coverage, usage.ReturnedUsage, usage.Finals)
		}
	}
	if writeErr == nil {
		if observation := result.NativeModels; observation != nil {
			usage, counts := observation.Totals, "unknown"
			if usage.Coverage == "observed_native_responses" || usage.Coverage == "partial_native_responses" {
				counts = CompactTokens(usage.InputTokens) + " in, " + CompactTokens(usage.OutputTokens) + " out"
			}
			_, writeErr = fmt.Fprintf(file,
				"\nRouted native model records: %s (%s, %s); %d requests, %d responses, %d observed usage.\n",
				counts, observation.Code, usage.Coverage, usage.Requests, usage.Responses, usage.UsageResponses)
		}
	}
	if writeErr == nil {
		if result.TrialScratch != nil {
			_, writeErr = fmt.Fprintf(file,
				"\nPrivate trial staging: %s; %d receipts; cleanup verdict: %s. Native resources are separate.\n",
				result.TrialScratch.Scope, len(result.TrialScratch.Trials), result.TrialScratch.verdict())
		}
	}
	if writeErr == nil {
		transport := "Local transport closure is not proof of remote cancellation."
		if result.Mode == "grade" {
			transport = "Judge-only regrade: no target or tool work; source remote state remains unchanged."
		}
		_, writeErr = io.WriteString(file,
			"\nOnly a complete run.json qualifies this summary as a finished report.\n"+
				"Target all-call usage: unknown; provider-internal role attempts: unknown.\n"+
				transport+" "+
				"Individual case results are not complete PDF/browser-suite or Telegram qualification.\n")
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = report.root.Remove("summary.md")
		return ErrReport
	}
	return report.write("run.json", result)
}
