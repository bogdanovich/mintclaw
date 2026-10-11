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
	Verdict    Verdict `json:"verdict"`
	Code       string  `json:"code"`
	DurationMS int64   `json:"duration_ms"`
	File       string  `json:"file"`
}

type RunReport struct {
	Version    int                 `json:"version"`
	ID         string              `json:"id"`
	PresetID   string              `json:"preset_id"`
	Runtime    health.RuntimeProbe `json:"runtime"`
	Verdict    Verdict             `json:"verdict"`
	StartedAt  time.Time           `json:"started_at"`
	DurationMS int64               `json:"duration_ms"`
	Trials     []TrialSummary      `json:"trials"`
	Coverage   map[Verdict]int     `json:"coverage"`
	Usage      string              `json:"usage"`
	ReportDir  string              `json:"-"`
	Cleanup    string              `json:"cleanup"`
	Complete   bool                `json:"complete"`
}

type privateReport struct {
	root *os.Root
	dir  string
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
	if !safeRelativePath(name) || len(data) > 8*1024*1024 {
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
			"| Case | Verdict | Code | Evidence |\n| --- | --- | --- | --- |\n",
		result.ID, result.Verdict,
	)
	for _, trial := range result.Trials {
		if writeErr != nil {
			break
		}
		_, writeErr = fmt.Fprintf(file, "| %s | %s | %s | [%s](%s) |\n",
			trial.CaseID, trial.Verdict, trial.Code, trial.ID, trial.File)
	}
	if writeErr == nil {
		_, writeErr = io.WriteString(file,
			"\nOnly a complete run.json qualifies this summary as a finished report.\n"+
				"Usage: unknown (all-call collection not implemented in this stage).\n"+
				"Local transport closure is not proof of remote cancellation. "+
				"This run does not qualify PDF, browser, or Telegram behavior.\n")
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = report.root.Remove("summary.md")
		return ErrReport
	}
	return report.write("run.json", result)
}
