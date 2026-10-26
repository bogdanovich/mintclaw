package eval

import "github.com/bogdanovich/mintclaw/pkg/config"

// Use the native default workspace store, never an operator-provided path.
// These fixed limits bound retained files, not completeness or provider cost.
func evalModelMetadataCapture() config.DiagnosticTraceCaptureConfig {
	return config.DiagnosticTraceCaptureConfig{
		Enabled: true, ContentMode: "metadata_only",
		MaxTraceBytes: 512 * 1024, MaxRecords: 512, MaxRecordBytes: 16 * 1024,
		RetentionHours: 1, MaxTraces: 16,
	}
}

func evalTargetMetadataCapture(preset Preset) config.DiagnosticTraceCaptureConfig {
	capture := evalModelMetadataCapture()
	if hasCompaction(preset) {
		// Five inputs and at most three repetitions also produce native
		// compaction-attempt traces. Keep those from evicting root turns.
		capture.MaxTraces = 64
	}
	return capture
}

func admittedModelMetadataPolicy(capture config.DiagnosticTraceCaptureConfig, preset Preset) bool {
	// A disabled seed is allowed: preparation applies the opt-in only to its
	// fresh output. Owned startup separately requires the exact enabled policy.
	return !capture.Enabled || preset.CaptureModelMetadata && capture == evalTargetMetadataCapture(preset)
}
