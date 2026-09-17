package tui

import (
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

// PresentationDiagnostics is a content-free snapshot of renderer work. It is
// suitable for debug logs and performance tests: no prompt, tool argument,
// command output, path, diff, or transcript text is retained here.
type PresentationDiagnostics struct {
	FirstPaint                 time.Duration
	SnapshotUpdates            uint64
	CoalescedUpdates           uint64
	PresentationLatencySamples uint64
	PresentationLatencyTotal   time.Duration
	PresentationLatencyMax     time.Duration
	RenderPasses               uint64
	RenderDurationTotal        time.Duration
	RenderDurationMax          time.Duration
	RenderedBlocks             uint64
	ReusedBlocks               uint64
	OverlayBuilds              uint64
	OverlayBuildDurationTotal  time.Duration
	OverlayBuildDurationMax    time.Duration
	TranscriptSearches         uint64
	TranscriptSearchTotal      time.Duration
	TranscriptSearchMax        time.Duration
	TruncationObservations     uint64
	CurrentTruncatedSurfaces   int
	PeakTruncatedSurfaces      int
	HydrationResults           uint64
	HydrationFailures          uint64
	HydrationDurationTotal     time.Duration
	HydrationDurationMax       time.Duration
	CurrentHydratedEntries     int
	PeakHydratedEntries        int
	CurrentCells               int
	PeakCells                  int
	CurrentRenderedLines       int
	PeakRenderedLines          int
}

type presentationDiagnosticsState struct{ PresentationDiagnostics }

func (state *presentationDiagnosticsState) observeSnapshot(
	snapshot frontend.ThreadSnapshot,
	presentationLatency time.Duration,
	coalescedUpdates uint64,
) {
	state.SnapshotUpdates++
	state.CoalescedUpdates += coalescedUpdates
	state.PresentationLatencySamples++
	state.PresentationLatencyTotal += presentationLatency
	state.PresentationLatencyMax = max(state.PresentationLatencyMax, presentationLatency)
	truncated := snapshotTruncatedSurfaces(snapshot)
	state.CurrentTruncatedSurfaces = truncated
	state.PeakTruncatedSurfaces = max(state.PeakTruncatedSurfaces, truncated)
	state.TruncationObservations += uint64(truncated)
}

func (state *presentationDiagnosticsState) observeRender(
	duration time.Duration,
	document semanticViewportDocument,
	hydratedEntries int,
) {
	state.RenderPasses++
	state.RenderDurationTotal += duration
	state.RenderDurationMax = max(state.RenderDurationMax, duration)
	state.RenderedBlocks += uint64(document.renderedBlocks)
	state.ReusedBlocks += uint64(document.reusedBlocks)
	state.CurrentCells = len(document.blocks)
	state.PeakCells = max(state.PeakCells, state.CurrentCells)
	state.CurrentRenderedLines = document.lineCount
	state.PeakRenderedLines = max(state.PeakRenderedLines, document.lineCount)
	state.CurrentHydratedEntries = hydratedEntries
	state.PeakHydratedEntries = max(state.PeakHydratedEntries, hydratedEntries)
}

func (state *presentationDiagnosticsState) observeOverlayBuild(duration time.Duration) {
	state.OverlayBuilds++
	state.OverlayBuildDurationTotal += duration
	state.OverlayBuildDurationMax = max(state.OverlayBuildDurationMax, duration)
}

func (state *presentationDiagnosticsState) observeTranscriptSearch(duration time.Duration) {
	state.TranscriptSearches++
	state.TranscriptSearchTotal += duration
	state.TranscriptSearchMax = max(state.TranscriptSearchMax, duration)
}

func (state *presentationDiagnosticsState) observeHydration(
	duration time.Duration,
	entries []frontend.TranscriptEntry,
	failed bool,
) {
	state.HydrationResults++
	if failed {
		state.HydrationFailures++
	}
	state.HydrationDurationTotal += duration
	state.HydrationDurationMax = max(state.HydrationDurationMax, duration)
	for _, entry := range entries {
		if entry.Truncated {
			state.TruncationObservations++
		}
	}
}

func snapshotTruncatedSurfaces(snapshot frontend.ThreadSnapshot) int {
	count := 0
	for _, item := range snapshot.Items {
		switch {
		case item.Message != nil && item.Message.Truncated:
			count++
		case item.Plan != nil && item.Plan.Truncated:
			count++
		case item.Tool != nil && toolStateTruncated(*item.Tool):
			count++
		}
	}
	if snapshot.Workspace != nil && snapshot.Workspace.Truncated {
		count++
	}
	if snapshot.RepositoryStatus != nil && snapshot.RepositoryStatus.Snapshot.Truncated {
		count++
	}
	if snapshot.RepositoryDiff != nil && repositoryDiffTruncated(*snapshot.RepositoryDiff) {
		count++
	}
	if snapshot.Review != nil && snapshot.Review.Result != nil && snapshot.Review.Result.Truncated {
		count++
	}
	return count
}

func toolStateTruncated(tool frontend.ToolState) bool {
	return tool.OutputTruncated || tool.Command != nil && tool.Command.Truncated ||
		tool.Exploration != nil && tool.Exploration.Truncated ||
		tool.MCP != nil && tool.MCP.Truncated ||
		tool.RepositoryDiff != nil && repositoryDiffTruncated(*tool.RepositoryDiff)
}

func repositoryDiffTruncated(diff codingworkspace.DiffResult) bool {
	if diff.Truncated {
		return true
	}
	for _, file := range diff.Files {
		if file.Truncated {
			return true
		}
		for _, hunk := range file.Hunks {
			if hunk.Truncated {
				return true
			}
		}
	}
	return false
}
