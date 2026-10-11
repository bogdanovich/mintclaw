package eval

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

func CalibratePreset(
	ctx context.Context, preset Preset, source string, options RunOptions,
) (CalibrationReport, error) {
	if options.SuiteTimeout == 0 {
		options.SuiteTimeout = 10 * time.Minute
	}
	if options.SuiteTimeout <= 0 || options.SuiteTimeout > 4*time.Hour {
		return CalibrationReport{}, ErrRunOptions
	}
	ctx, cancel := context.WithTimeout(ctx, options.SuiteTimeout)
	defer cancel()
	cfg, err := gradeConfig(preset, options.Home)
	if err != nil || preset.JudgeModel == "" {
		return CalibrationReport{}, ErrGrade
	}
	calibration, sha, err := LoadCalibration(source)
	if err != nil {
		return CalibrationReport{}, err
	}
	filter := cfg.SensitiveDataReplacer()
	if credentialJSONValue(preset, filter) || credentialJSONValue(calibration, filter) {
		return CalibrationReport{}, ErrGrade
	}
	judge, err := NewModelRole(cfg, preset.JudgeModel, "judge")
	if err != nil {
		return CalibrationReport{}, ErrGrade
	}
	defer judge.Close()
	if credentialJSONValue(judge.identity, filter) {
		return CalibrationReport{}, ErrGrade
	}
	home, _ := privateHome(options.Home)
	id := uuid.NewString()
	writer, err := newPrivateReport(options.OutputParent, home, preset.ExpectedWorkspace, id)
	if err != nil {
		return CalibrationReport{}, ErrReport
	}
	defer func() { _ = writer.root.Close() }()
	result, calibrationErr := Calibrate(ctx, judge, calibration, sha)
	result.ID, result.ReportDir = id, writer.dir
	calls := []RoleCall{}
	for _, example := range result.Examples {
		if example.Judge.Call != nil {
			calls = append(calls, *example.Judge.Call)
		}
	}
	usage := summarizeRoleCalls("judge", calls)
	result.RoleUsage = []RoleUsage{usage}
	if credentialJSONValue(result, filter) {
		return CalibrationReport{Version: Version, ID: id, Verdict: Fail, ReportDir: writer.dir}, ErrPrivateEvidence
	}
	summary := "# Judge Calibration\n\nNo target or tool work. Agreement on these labels is not target qualification.\n\n" +
		"| Example | Expected | Observed | Agreement |\n| --- | --- | --- | --- |\n"
	for _, example := range result.Examples {
		summary += fmt.Sprintf(
			"| %s | %s | %s | %s |\n",
			example.ID,
			example.Expected,
			example.Observed,
			example.Verdict,
		)
	}
	counts := "unknown"
	if usage.Coverage == "partial_returned_calls" || usage.Coverage == "returned_calls_only" {
		counts = CompactTokens(usage.InputTokens) + " in, " + CompactTokens(usage.OutputTokens) + " out"
	}
	summary += fmt.Sprintf("\nJudge: %s (%s); provider-internal attempts: unknown.\n", counts, usage.Coverage)
	summary += "\nOnly complete calibration.json marks a finished report.\n"
	if writer.writeBytes("summary.md", []byte(summary)) != nil || writer.write("calibration.json", result) != nil {
		result.Complete = false
		result.Verdict = combineVerdicts(result.Verdict, Error)
		return result, ErrReport
	}
	return result, calibrationErr
}
