package tools

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestExecCodingStartObservationUsesOnlyTypedCommandFields(t *testing.T) {
	root := t.TempDir()
	tool, err := newTestExecTool(root, false)
	if err != nil {
		t.Fatal(err)
	}
	got := tool.CodingStartObservation(map[string]any{
		"action": "run", "command": "printf safe", "background": true,
		"ignored": "must not cross the observation boundary",
	})
	if got == nil || got.Command == nil {
		t.Fatal("typed exec start observation is missing")
	}
	command := got.Command
	if command.Action != "run" || command.Command != "printf safe" || command.CWD != root ||
		command.Source != "agent" || command.Status != "running" || !command.Background ||
		command.OwnsProcess {
		t.Fatalf("exec start observation = %+v", command)
	}
	if strings.Contains(command.Command+command.CWD+command.Input, "must not cross") {
		t.Fatalf("unknown argument entered observation: %+v", command)
	}
	for _, args := range []map[string]any{
		{"action": "list"},
		{"action": "unknown", "command": "echo unsafe"},
	} {
		if observation := tool.CodingStartObservation(args); observation != nil {
			t.Fatalf("unsupported action produced observation: %+v", observation)
		}
	}
}

func TestCommandObservationCaptureRedactsCredentialSplitAcrossWrites(t *testing.T) {
	var observations []toolshared.CommandObservation
	ctx := toolshared.WithCommandObservationSink(t.Context(), func(observation toolshared.CommandObservation) {
		observations = append(observations, observation)
	})
	capture := newCommandObservationCapture(ctx, toolshared.CommandObservation{Status: "running"})
	capture.append("stdout", []byte("sk-1234"))
	capture.append("stdout", []byte("56789abcdef"))
	capture.complete("succeeded", nil)

	if len(observations) != 3 {
		t.Fatalf("observations = %d, want 3: %+v", len(observations), observations)
	}
	first := observations[0].Transcript[0]
	second := observations[1].Transcript[0]
	if first.Sequence != second.Sequence || second.Text != "[REDACTED]" ||
		strings.Contains(second.Text, "123456789abcdef") {
		t.Fatalf("split credential progress = %+v", observations[:2])
	}
	terminal := observations[2]
	if len(terminal.Transcript) != 1 || terminal.Transcript[0].Text != "[REDACTED]" {
		t.Fatalf("split credential terminal transcript = %+v", terminal.Transcript)
	}
}

func TestCommandObservationCapturePublishesCausalDeltasAndTerminalSnapshot(t *testing.T) {
	var observations []toolshared.CommandObservation
	ctx := toolshared.WithCommandObservationSink(t.Context(), func(observation toolshared.CommandObservation) {
		observations = append(observations, observation)
	})
	capture := newCommandObservationCapture(ctx, toolshared.CommandObservation{
		Action: "run", Command: "test ./...", Source: "agent", Status: "running", OwnsProcess: true,
	})
	capture.append("stdout", []byte("one\n"))
	capture.append("stderr", []byte("two\n"))
	exitCode := 7
	capture.complete("failed", &exitCode)

	if len(observations) != 3 {
		t.Fatalf("progress observations = %d, want 3: %+v", len(observations), observations)
	}
	if observations[0].Transcript[0].Sequence != 1 || observations[0].Transcript[0].Text != "one\n" ||
		observations[1].Transcript[0].Sequence != 2 || observations[1].Transcript[0].Stream != "stderr" {
		t.Fatalf("causal deltas = %+v", observations[:2])
	}
	terminal := observations[2]
	if terminal.Status != "failed" || terminal.ExitCode == nil || *terminal.ExitCode != 7 ||
		terminal.Duration <= 0 || len(terminal.Transcript) != 2 {
		t.Fatalf("terminal snapshot = %+v", terminal)
	}
}

func TestCommandObservationCaptureRejectsProgressAfterCompletion(t *testing.T) {
	var observations []toolshared.CommandObservation
	ctx := toolshared.WithCommandObservationSink(t.Context(), func(observation toolshared.CommandObservation) {
		observations = append(observations, observation)
	})
	capture := newCommandObservationCapture(ctx, toolshared.CommandObservation{Status: "running"})
	exitCode := 0
	capture.complete("succeeded", &exitCode)
	capture.append("stdout", []byte("too late"))

	if len(observations) != 1 || observations[0].Status != "succeeded" ||
		len(observations[0].Transcript) != 0 {
		t.Fatalf("observations after terminal edge = %+v", observations)
	}
}

func TestProcessSessionWriteRecordsReadableKeyInputBeforeTerminal(t *testing.T) {
	var observations []toolshared.CommandObservation
	ctx := toolshared.WithCommandObservationSink(t.Context(), func(observation toolshared.CommandObservation) {
		observations = append(observations, observation)
	})
	capture := newCommandObservationCapture(ctx, toolshared.CommandObservation{Status: "running"})
	var raw string
	session := &ProcessSession{
		Status: "running",
		stdinWriter: sessionWriterFunc(func(data []byte) (int, error) {
			raw = string(data)
			return len(data), nil
		}),
		capture: capture,
	}
	if err := session.writeInput("\x1b[A", "up"); err != nil {
		t.Fatal(err)
	}
	session.complete(0, nil)

	if raw != "\x1b[A" || len(observations) != 2 || observations[0].Status != "running" ||
		len(observations[0].Transcript) != 1 || observations[0].Transcript[0].Text != "up" ||
		observations[1].Status != "succeeded" {
		t.Fatalf("session input lifecycle raw=%q observations=%+v", raw, observations)
	}
}

func TestProcessSessionPartialWriteRecordsDeliveredInputBeforeTerminal(t *testing.T) {
	var observations []toolshared.CommandObservation
	ctx := toolshared.WithCommandObservationSink(t.Context(), func(observation toolshared.CommandObservation) {
		observations = append(observations, observation)
	})
	capture := newCommandObservationCapture(ctx, toolshared.CommandObservation{Status: "running"})
	writerEntered := make(chan struct{})
	releaseWriter := make(chan struct{})
	writeErr := errors.New("partial write")
	session := &ProcessSession{
		Status: "running",
		stdinWriter: sessionWriterFunc(func([]byte) (int, error) {
			close(writerEntered)
			<-releaseWriter
			return 3, writeErr
		}),
		capture: capture,
	}

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- session.writeInput("abcdef", "readable-full-input")
	}()
	<-writerEntered
	completionDone := make(chan struct{})
	go func() {
		session.complete(0, nil)
		close(completionDone)
	}()
	close(releaseWriter)

	if err := <-writeDone; !errors.Is(err, writeErr) {
		t.Fatalf("partial write error = %v, want %v", err, writeErr)
	}
	<-completionDone
	if len(observations) != 2 || observations[0].Status != "running" ||
		len(observations[0].Transcript) != 1 || observations[0].Transcript[0].Stream != "input" ||
		observations[0].Transcript[0].Text != "abc" {
		t.Fatalf("partial input progress = %+v", observations)
	}
	terminal := observations[1]
	if terminal.Status != "succeeded" || len(terminal.Transcript) != 1 ||
		terminal.Transcript[0].Text != "abc" || strings.Contains(terminal.Transcript[0].Text, "readable") {
		t.Fatalf("partial input terminal observation = %+v", terminal)
	}
}

func TestProcessSessionNilErrorShortWriteReturnsErrShortWriteAndRecordsPrefix(t *testing.T) {
	var observations []toolshared.CommandObservation
	ctx := toolshared.WithCommandObservationSink(t.Context(), func(observation toolshared.CommandObservation) {
		observations = append(observations, observation)
	})
	session := &ProcessSession{
		Status: "running",
		stdinWriter: sessionWriterFunc(func([]byte) (int, error) {
			return 2, nil
		}),
		capture: newCommandObservationCapture(ctx, toolshared.CommandObservation{Status: "running"}),
	}

	if err := session.writeInput("abcdef", "readable-full-input"); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v, want %v", err, io.ErrShortWrite)
	}
	session.complete(0, nil)
	if len(observations) != 2 || observations[0].Status != "running" ||
		len(observations[0].Transcript) != 1 || observations[0].Transcript[0].Text != "ab" ||
		observations[1].Status != "succeeded" || len(observations[1].Transcript) != 1 ||
		observations[1].Transcript[0].Text != "ab" {
		t.Fatalf("short input observations = %+v", observations)
	}
}

func TestProcessSessionReservesBufferedOutputBeforeConcurrentInput(t *testing.T) {
	var observations []toolshared.CommandObservation
	ctx := toolshared.WithCommandObservationSink(t.Context(), func(observation toolshared.CommandObservation) {
		observations = append(observations, observation)
	})
	capture := newCommandObservationCapture(ctx, toolshared.CommandObservation{Status: "running"})
	writerEntered := make(chan struct{})
	session := &ProcessSession{
		Status:       "running",
		outputBuffer: &bytes.Buffer{},
		stdinWriter: sessionWriterFunc(func(data []byte) (int, error) {
			close(writerEntered)
			return len(data), nil
		}),
		capture: capture,
	}

	// Hold the publication lock so appendOutput can only proceed after it has
	// reserved the output while retaining the session lifecycle lock.
	capture.publishMu.Lock()
	publicationLocked := true
	defer func() {
		if publicationLocked {
			capture.publishMu.Unlock()
		}
	}()
	outputDone := make(chan struct{})
	go func() {
		session.appendOutput("stdout", []byte("output-before-input\n"))
		close(outputDone)
	}()

	deadline := time.Now().Add(time.Second)
	for session.mu.TryLock() {
		session.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("output admission did not acquire the session lifecycle lock")
		}
		runtime.Gosched()
	}
	inputDone := make(chan error, 1)
	go func() {
		inputDone <- session.writeInput("input-after-output\n", "input-after-output\n")
	}()
	select {
	case <-writerEntered:
		capture.publishMu.Unlock()
		publicationLocked = false
		t.Fatal("input write overtook buffered output admission")
	case <-time.After(50 * time.Millisecond):
	}

	capture.publishMu.Unlock()
	publicationLocked = false
	<-outputDone
	if err := <-inputDone; err != nil {
		t.Fatal(err)
	}
	session.complete(0, nil)

	if len(observations) != 3 {
		t.Fatalf("output/input lifecycle observations = %+v", observations)
	}
	terminal := observations[len(observations)-1]
	if terminal.Status != "succeeded" || len(terminal.Transcript) != 2 ||
		terminal.Transcript[0].Stream != "stdout" ||
		terminal.Transcript[0].Text != "output-before-input\n" ||
		terminal.Transcript[1].Stream != "input" ||
		terminal.Transcript[1].Text != "input-after-output\n" {
		t.Fatalf("causal output/input transcript = %+v", terminal)
	}
}

func TestCommandObservationCaptureKeepsBoundedHeadAndTail(t *testing.T) {
	progressCount := 0
	ctx := toolshared.WithCommandObservationSink(context.Background(), func(toolshared.CommandObservation) {
		progressCount++
	})
	capture := newCommandObservationCapture(ctx, toolshared.CommandObservation{Status: "running"})
	capture.append("stdout", []byte("HEAD:"+strings.Repeat("h", 40<<10)))
	for range 1000 {
		capture.append("stderr", []byte(strings.Repeat("m", 4096)))
	}
	capture.append("stdout", []byte(strings.Repeat("t", 40<<10)+":TAIL"))

	observation := capture.snapshot()
	if !observation.Truncated || len(observation.Transcript) > maxCommandTranscriptEntries {
		t.Fatalf("bounded capture metadata = %+v", observation)
	}
	var text strings.Builder
	for _, entry := range observation.Transcript {
		text.WriteString(entry.Text)
	}
	if text.Len() > maxCommandTranscriptBytes || !strings.Contains(text.String(), "HEAD:") ||
		!strings.Contains(text.String(), commandTranscriptOmission) || !strings.Contains(text.String(), ":TAIL") {
		t.Fatalf("head-tail capture len=%d text=%q", text.Len(), text.String())
	}
	if progressCount > 16 {
		t.Fatalf("unbounded live progress events = %d", progressCount)
	}
}

func TestShellCommandObservationIncludesMetadataTranscriptAndDuration(t *testing.T) {
	if testing.Short() {
		t.Skip("executes a local shell command")
	}
	tool, err := newTestExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	var progress []toolshared.CommandObservation
	ctx := toolshared.WithCommandObservationSink(t.Context(), func(observation toolshared.CommandObservation) {
		progress = append(progress, observation)
	})
	result := tool.Execute(ctx, map[string]any{
		"action": "run", "command": "printf out; printf err >&2",
	})
	if result.IsError || result.Observation == nil || result.Observation.Command == nil {
		t.Fatalf("exec result = %+v", result)
	}
	command := result.Observation.Command
	if command.Action != "run" || command.Command == "" || command.CWD == "" || command.Source != "agent" ||
		command.Status != "succeeded" || !command.OwnsProcess || len(command.Transcript) < 2 {
		t.Fatalf("command observation = %+v", command)
	}
	if len(progress) < 3 || progress[len(progress)-1].Status != "succeeded" ||
		progress[len(progress)-1].Duration <= 0 {
		t.Fatalf("command progress = %+v", progress)
	}
	streams := map[string]bool{}
	for _, entry := range command.Transcript {
		streams[entry.Stream] = true
	}
	if !streams["stdout"] || !streams["stderr"] {
		t.Fatalf("command transcript streams = %+v", command.Transcript)
	}
}

func TestBackgroundCompletionPublishesAfterOutputAndOwnsLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("executes a local background command")
	}
	tool, err := newTestExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	var observations []toolshared.CommandObservation
	ctx := toolshared.WithCommandObservationSink(t.Context(), func(observation toolshared.CommandObservation) {
		observations = append(observations, observation)
	})
	result := tool.Execute(ctx, map[string]any{
		"action": "run", "command": "printf background-output", "background": true,
	})
	if result.IsError || result.Observation == nil || result.Observation.Command == nil {
		t.Fatalf("background result = %+v", result)
	}
	session, err := tool.sessionManager.Get(result.Observation.Command.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err = session.waitForCompletion(); err != nil {
		t.Fatalf("background command completion: %v", err)
	}
	if len(observations) < 2 {
		t.Fatalf("background observations = %+v", observations)
	}
	terminal := observations[len(observations)-1]
	if terminal.Status != "succeeded" || !terminal.Background || !terminal.OwnsProcess ||
		terminal.SessionID == "" || terminal.Duration <= 0 || len(terminal.Transcript) == 0 ||
		!strings.Contains(terminal.Transcript[len(terminal.Transcript)-1].Text, "background-output") {
		t.Fatalf("background terminal observation = %+v", terminal)
	}
}

func TestBackgroundWriteThatCausesExitPublishesTerminalObservationLast(t *testing.T) {
	if testing.Short() {
		t.Skip("executes a local background command")
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell assertion uses POSIX read syntax")
	}
	tool, err := newTestExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	var observations []toolshared.CommandObservation
	ctx := toolshared.WithCommandObservationSink(t.Context(), func(observation toolshared.CommandObservation) {
		observations = append(observations, observation)
	})
	result := tool.Execute(ctx, map[string]any{
		"action": "run", "command": "read value; printf 'seen:%s\\n' \"$value\"", "background": true,
	})
	if result.IsError || result.Observation == nil || result.Observation.Command == nil {
		t.Fatalf("background result = %+v", result)
	}
	sessionID := result.Observation.Command.SessionID
	session, err := tool.sessionManager.Get(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	written := tool.Execute(ctx, map[string]any{
		"action": "write", "sessionId": sessionID, "data": "go\n",
	})
	if written.IsError {
		t.Fatalf("write result = %+v", written)
	}
	if err = session.waitForCompletion(); err != nil {
		t.Fatalf("background command completion: %v", err)
	}
	if len(observations) < 2 {
		t.Fatalf("background observations = %+v", observations)
	}
	for index, observation := range observations[:len(observations)-1] {
		if observation.Status != "running" {
			t.Fatalf(
				"observation %d preceded terminal edge with status %q: %+v",
				index,
				observation.Status,
				observations,
			)
		}
	}
	terminal := observations[len(observations)-1]
	if terminal.Status != "succeeded" || terminal.ExitCode == nil || *terminal.ExitCode != 0 {
		t.Fatalf("last observation is not successful terminal state: %+v", observations)
	}
	var transcript strings.Builder
	for _, entry := range terminal.Transcript {
		transcript.WriteString(entry.Text)
	}
	if !strings.Contains(transcript.String(), "go\n") || !strings.Contains(transcript.String(), "seen:go") {
		t.Fatalf("terminal transcript omits causal input/output: %q", transcript.String())
	}
}

func TestBackgroundKillPublishesOneCanceledTerminalObservation(t *testing.T) {
	if testing.Short() {
		t.Skip("executes a local background command")
	}
	tool, err := newTestExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	var observations []toolshared.CommandObservation
	ctx := toolshared.WithCommandObservationSink(t.Context(), func(observation toolshared.CommandObservation) {
		observations = append(observations, observation)
	})
	result := tool.Execute(ctx, map[string]any{
		"action": "run", "command": "sleep 30", "background": true,
	})
	if result.IsError || result.Observation == nil || result.Observation.Command == nil {
		t.Fatalf("background result = %+v", result)
	}
	sessionID := result.Observation.Command.SessionID
	killed := tool.Execute(t.Context(), map[string]any{"action": "kill", "sessionId": sessionID})
	if killed.IsError {
		t.Fatalf("kill result = %+v", killed)
	}
	terminal := make([]toolshared.CommandObservation, 0, 1)
	for _, observation := range observations {
		if observation.Status != "running" {
			terminal = append(terminal, observation)
		}
	}
	if len(terminal) != 1 || terminal[0].Status != "canceled" || !terminal[0].Canceled {
		t.Fatalf("terminal observations = %+v", terminal)
	}
}
