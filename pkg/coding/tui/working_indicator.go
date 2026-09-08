package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const motionEnvironmentVariable = "MINTCLAW_TUI_MOTION"

// MotionMode controls time-varying terminal decoration without hiding live
// task state. Its zero value selects animated motion.
type MotionMode string

const (
	MotionAnimated MotionMode = "animated"
	MotionReduced  MotionMode = "reduced"
	MotionDisabled MotionMode = "disabled"
)

const (
	animatedTickInterval = 300 * time.Millisecond
	clockTickInterval    = time.Second
)

type keyMap struct {
	interrupt key.Binding
}

func newKeyMap(interruptKeys []string) keyMap {
	keys := make([]string, 0, len(interruptKeys))
	for _, value := range interruptKeys {
		if value = strings.TrimSpace(value); value != "" {
			keys = append(keys, value)
		}
	}
	if len(keys) == 0 {
		keys = []string{"ctrl+c"}
	}
	return keyMap{interrupt: key.NewBinding(
		key.WithKeys(keys...),
		key.WithHelp(keys[0], "interrupt"),
	)}
}

func resolveMotionMode(configured MotionMode, environment []string) (MotionMode, error) {
	value := strings.TrimSpace(string(configured))
	if value == "" {
		value = environmentValue(environment, motionEnvironmentVariable)
	}
	if value == "" {
		value = os.Getenv(motionEnvironmentVariable)
	}
	switch MotionMode(strings.ToLower(value)) {
	case "", MotionAnimated:
		return MotionAnimated, nil
	case MotionReduced:
		return MotionReduced, nil
	case MotionDisabled:
		return MotionDisabled, nil
	default:
		return "", fmt.Errorf(
			"coding TUI motion mode must be %q, %q, or %q",
			MotionAnimated,
			MotionReduced,
			MotionDisabled,
		)
	}
}

func environmentValue(environment []string, name string) string {
	for index := len(environment) - 1; index >= 0; index-- {
		key, value, found := strings.Cut(environment[index], "=")
		if found && key == name {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type workingPhase string

const (
	workingPhaseWorking    workingPhase = "Working"
	workingPhaseExploring  workingPhase = "Exploring"
	workingPhaseRunning    workingPhase = "Running"
	workingPhaseBackground workingPhase = "Waiting for background terminal"
	workingPhaseCompacting workingPhase = "Compacting"
	workingPhaseReviewing  workingPhase = "Reviewing"
	workingPhaseStopping   workingPhase = "Interrupting"
)

type workingTickMsg struct {
	generation uint64
}

type workingIndicator struct {
	mode           MotionMode
	now            func() time.Time
	phase          workingPhase
	identity       string
	elapsed        time.Duration
	lastResumeAt   time.Time
	running        bool
	frame          uint64
	tickPending    bool
	tickGeneration uint64
}

func newWorkingIndicator(mode MotionMode, now func() time.Time) workingIndicator {
	if mode == "" {
		mode = MotionAnimated
	}
	if now == nil {
		now = time.Now
	}
	return workingIndicator{mode: mode, now: now, phase: workingPhaseWorking}
}

func (indicator *workingIndicator) sync(snapshot frontend.ThreadSnapshot, initialTurnPending bool) {
	now := indicator.now()
	active := activeWork(snapshot.Activity) || initialTurnPending
	identity := foregroundWorkIdentity(snapshot, initialTurnPending)
	if active {
		switch {
		case !indicator.running && indicator.identity == identity && identity != "":
			indicator.lastResumeAt = now
		case indicator.identity == "pending" && identity != "":
			indicator.identity = identity
		case indicator.identity != identity:
			indicator.elapsed = 0
			indicator.lastResumeAt = now
			indicator.identity = identity
		case !indicator.running:
			indicator.lastResumeAt = now
		}
		indicator.running = true
		indicator.phase = phaseForSnapshot(snapshot, initialTurnPending)
		return
	}

	if snapshot.Activity == frontend.ActivityWaitingInput && indicator.identity == identity && identity != "" {
		indicator.pauseAt(now)
		return
	}
	indicator.reset()
}

func (indicator *workingIndicator) pauseAt(now time.Time) {
	if indicator.running {
		indicator.elapsed += max(time.Duration(0), now.Sub(indicator.lastResumeAt))
		indicator.running = false
	}
	indicator.stopTicks()
}

func (indicator *workingIndicator) reset() {
	indicator.identity = ""
	indicator.elapsed = 0
	indicator.running = false
	indicator.phase = workingPhaseWorking
	indicator.frame = 0
	indicator.stopTicks()
}

func (indicator *workingIndicator) stopTicks() {
	if indicator.tickPending {
		indicator.tickGeneration++
	}
	indicator.tickPending = false
}

func (indicator *workingIndicator) elapsedAt(now time.Time) time.Duration {
	elapsed := indicator.elapsed
	if indicator.running {
		elapsed += max(time.Duration(0), now.Sub(indicator.lastResumeAt))
	}
	return elapsed
}

func (indicator *workingIndicator) schedule(ctx context.Context, visible bool, focused bool) tea.Cmd {
	if !indicator.running || !visible || !focused {
		indicator.stopTicks()
		return nil
	}
	if indicator.tickPending {
		return nil
	}
	indicator.tickPending = true
	indicator.tickGeneration++
	generation := indicator.tickGeneration
	interval := clockTickInterval
	if indicator.mode == MotionAnimated {
		interval = animatedTickInterval
	}
	return func() tea.Msg {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		select {
		case <-timer.C:
			return workingTickMsg{generation: generation}
		case <-ctx.Done():
			return nil
		}
	}
}

func (indicator *workingIndicator) acceptTick(message workingTickMsg) bool {
	if !indicator.tickPending || message.generation != indicator.tickGeneration {
		return false
	}
	indicator.tickPending = false
	indicator.frame++
	return true
}

func (indicator *workingIndicator) line(interruptKey string) string {
	if !indicator.running {
		return ""
	}
	prefix := ""
	switch indicator.mode {
	case MotionAnimated:
		if indicator.frame%2 == 0 {
			prefix = "• "
		} else {
			prefix = "◦ "
		}
	case MotionReduced:
		prefix = "• "
	}
	return fmt.Sprintf(
		"%s%s (%s • %s to interrupt)",
		prefix,
		indicator.phase,
		formatWorkingElapsed(indicator.elapsedAt(indicator.now())),
		boundedSingleLine(interruptKey, 32),
	)
}

func formatWorkingElapsed(elapsed time.Duration) string {
	seconds := uint64(max(time.Duration(0), elapsed) / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 60*60 {
		return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%dh %02dm %02ds", seconds/3600, seconds%3600/60, seconds%60)
}

func foregroundWorkIdentity(snapshot frontend.ThreadSnapshot, initialTurnPending bool) string {
	if initialTurnPending && !activeWork(snapshot.Activity) {
		return "pending"
	}
	if turnID := strings.TrimSpace(snapshot.ActiveTurnID); turnID != "" {
		return "turn:" + turnID
	}
	if snapshot.Activity == frontend.ActivityReviewing && snapshot.Review != nil {
		return "review:" + snapshot.Review.ReviewID
	}
	if snapshot.Activity == frontend.ActivityCompacting && snapshot.LastCompaction != nil {
		return "compaction:" + snapshot.LastCompaction.AttemptID
	}
	for index := len(snapshot.Items) - 1; index >= 0; index-- {
		if turnID := strings.TrimSpace(snapshot.Items[index].TurnID); turnID != "" {
			return "turn:" + turnID
		}
	}
	if activeWork(snapshot.Activity) {
		return "foreground"
	}
	return ""
}

func phaseForSnapshot(snapshot frontend.ThreadSnapshot, initialTurnPending bool) workingPhase {
	if initialTurnPending && !activeWork(snapshot.Activity) {
		return workingPhaseWorking
	}
	switch snapshot.Activity {
	case frontend.ActivityCompacting:
		return workingPhaseCompacting
	case frontend.ActivityReviewing:
		return workingPhaseReviewing
	case frontend.ActivityInterrupting:
		return workingPhaseStopping
	case frontend.ActivityRunning:
		return runningPhase(snapshot.Items)
	default:
		return workingPhaseWorking
	}
}

func runningPhase(items []frontend.PresentationItem) workingPhase {
	activeTools := make([]frontend.ToolState, 0, 2)
	backgroundSessions := make(map[string]frontend.CommandStatus)
	for _, item := range items {
		if item.Tool == nil {
			continue
		}
		if command := item.Tool.Command; command != nil && command.Background {
			if sessionID := strings.TrimSpace(command.SessionID); sessionID != "" {
				backgroundSessions[sessionID] = command.Status
			}
		}
		if item.Lifecycle == frontend.PresentationActive {
			activeTools = append(activeTools, *item.Tool)
		}
	}
	backgroundRunning := false
	for _, status := range backgroundSessions {
		if status == frontend.CommandRunning {
			backgroundRunning = true
			break
		}
	}
	if len(activeTools) == 0 {
		if backgroundRunning {
			return workingPhaseBackground
		}
		return workingPhaseWorking
	}
	allExploration := true
	allExec := true
	for _, tool := range activeTools {
		if tool.Command != nil && tool.Command.Status == frontend.CommandRunning {
			if tool.Command.Background {
				return workingPhaseBackground
			}
			return workingPhaseRunning
		}
		if !nativeExplorationTool(tool.Name) {
			allExploration = false
		}
		if strings.TrimSpace(tool.Name) != "exec" {
			allExec = false
		}
	}
	if backgroundRunning && allExec {
		return workingPhaseBackground
	}
	if allExploration {
		return workingPhaseExploring
	}
	return workingPhaseRunning
}

func nativeExplorationTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "list_dir", "read_file", "search_files", "repository_status", "repository_diff", "load_image":
		return true
	default:
		return false
	}
}

func (m *Model) syncWorkingIndicator() {
	m.working.sync(m.snapshot, m.initialTurnPending)
}

func (m *Model) workingSurfaceVisible() bool {
	return m.height > 4 && m.working.running
}

func (m *Model) scheduleWorkingTick() tea.Cmd {
	return m.working.schedule(m.ctx, m.workingSurfaceVisible(), m.focused)
}

func (m *Model) workingLine() string {
	if !m.workingSurfaceVisible() {
		return ""
	}
	return m.working.line(m.keys.interrupt.Help().Key)
}
