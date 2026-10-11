package eval

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"time"
)

type Calibration struct {
	Version       int                  `json:"version"`
	PromptVersion string               `json:"prompt_version"`
	Examples      []CalibrationExample `json:"examples"`
}

type CalibrationExample struct {
	ID       string      `json:"id"`
	Task     string      `json:"task"`
	Rubric   []Criterion `json:"rubric"`
	Evidence []Event     `json:"evidence"`
	Expected Verdict     `json:"expected"`
}

type CalibrationFinding struct {
	ID       string    `json:"id"`
	Expected Verdict   `json:"expected"`
	Observed Verdict   `json:"observed"`
	Verdict  Verdict   `json:"verdict"`
	Judge    Judgement `json:"judge"`
}

type CalibrationReport struct {
	Version       int                  `json:"version"`
	PromptVersion string               `json:"prompt_version"`
	SHA256        string               `json:"sha256"`
	Verdict       Verdict              `json:"verdict"`
	Examples      []CalibrationFinding `json:"examples"`
}

func LoadCalibration(name string) (Calibration, string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return Calibration{}, "", ErrModelContract
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return Calibration{}, "", ErrModelContract
	}
	defer func() { _ = root.Close() }()
	data, err := readBoundedFile(root, filepath.Base(absolute), MaxManifestBytes)
	if err != nil {
		return Calibration{}, "", ErrModelContract
	}
	result, err := DecodeManifest[Calibration](data)
	if err != nil || result.Validate() != nil {
		return Calibration{}, "", ErrModelContract
	}
	return result, digest(data), nil
}

func (calibration Calibration) Validate() error {
	if calibration.Version != Version || calibration.PromptVersion != JudgePromptVersion ||
		len(calibration.Examples) == 0 || len(calibration.Examples) > 16 {
		return ErrModelContract
	}
	seen := make(map[string]bool, len(calibration.Examples))
	for _, example := range calibration.Examples {
		if !validID(example.ID) || seen[example.ID] ||
			!slices.Contains([]Verdict{Pass, Fail, Inconclusive}, example.Expected) {
			return ErrModelContract
		}
		if _, _, err := judgeInput(example.conversationCase(), example.Evidence); err != nil {
			return ErrModelContract
		}
		seen[example.ID] = true
	}
	return nil
}

func (example CalibrationExample) conversationCase() Case {
	return Case{
		Version:     Version,
		ID:          "calibration." + example.ID,
		Suite:       "calibration",
		Tags:        []string{},
		Description: "Manually labeled judge calibration, not a target trial.",
		Task:        example.Task,
		Requires:    []string{},
		Limits:      DefaultLimits(),
		Driver: Driver{
			Mode:  "scripted",
			Steps: []Step{{Action: Action{Kind: "stop"}, WaitFor: Expectation{Kind: "final"}}},
		},
		Checks: []Check{{ID: "outcome", Kind: "last_outcome", Expected: "final"}},
		Rubric: example.Rubric,
	}
}

// Labels are compared after judging; they are never included in judge input.
// A successful calibration is agreement on this corpus, not proof of target quality.
func Calibrate(
	ctx context.Context,
	judge *ModelRole,
	calibration Calibration,
	sha256 string,
) (CalibrationReport, error) {
	if calibration.Validate() != nil || !validDigest(sha256) {
		return CalibrationReport{}, ErrModelContract
	}
	result := CalibrationReport{
		Version: Version, PromptVersion: JudgePromptVersion, SHA256: sha256,
		Verdict: Pass, Examples: []CalibrationFinding{},
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for _, example := range calibration.Examples {
		observed := judgeConversation(ctx, judge, example.conversationCase(), example.Evidence)
		verdict := Fail
		if observed.Code != "judge_completed" {
			verdict = combineVerdicts(Error, observed.Verdict)
		} else if observed.Verdict == example.Expected {
			verdict = Pass
		}
		result.Examples = append(result.Examples, CalibrationFinding{
			ID: example.ID, Expected: example.Expected, Observed: observed.Verdict, Verdict: verdict, Judge: observed,
		})
		result.Verdict = combineVerdicts(result.Verdict, verdict)
	}
	if result.Verdict != Pass {
		return result, ErrNotPassed
	}
	return result, nil
}
