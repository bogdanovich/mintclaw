package tools

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const (
	maxCommandTranscriptBytes    = 64 << 10
	maxCommandTranscriptEntries  = 128
	maxCommandLiveRedactionBytes = maxCommandTranscriptBytes + (1 << 10)
	commandTranscriptOmission    = "[… command output omitted …]\n"
	commandTranscriptHeadBytes   = maxCommandTranscriptBytes / 2
	commandTranscriptHeadItems   = maxCommandTranscriptEntries / 2
	commandTranscriptTailBytes   = maxCommandTranscriptBytes - commandTranscriptHeadBytes -
		len(commandTranscriptOmission)
	commandTranscriptTailItems = maxCommandTranscriptEntries - commandTranscriptHeadItems - 1
)

// CodingStartObservation implements toolshared.CodingObservationProvider.
// Only the native exec schema is projected; arbitrary tool arguments never
// cross this boundary.
func (t *ExecTool) CodingStartObservation(args map[string]any) *toolshared.ToolObservation {
	action, _ := args["action"].(string)
	action = strings.TrimSpace(action)
	if action == "" || action == "list" {
		return nil
	}
	observation := toolshared.CommandObservation{Action: action, Source: "agent", Status: "running"}
	switch action {
	case "run":
		observation.Command, _ = args["command"].(string)
		observation.CWD, _ = args["cwd"].(string)
		if strings.TrimSpace(observation.CWD) == "" && t != nil {
			observation.CWD = t.workingDir
		}
		observation.Background = commandBoolArgument(args["background"])
		observation.OwnsProcess = !observation.Background
	case "poll", "read", "write", "kill", "send-keys":
		observation.SessionID, _ = args["sessionId"].(string)
		observation.Background = true
		switch action {
		case "write":
			observation.Input, _ = args["data"].(string)
		case "send-keys":
			observation.Input, _ = args["keys"].(string)
		}
		if t != nil && t.sessionManager != nil {
			if session, err := t.sessionManager.Get(observation.SessionID); err == nil {
				metadata := session.commandObservation(action)
				metadata.Input = observation.Input
				observation = metadata
			}
		}
	default:
		return nil
	}
	return toolshared.SanitizeToolObservation(&toolshared.ToolObservation{Command: &observation})
}

func commandBoolArgument(value any) bool {
	switch value := value.(type) {
	case bool:
		return value
	case string:
		return value == "true"
	default:
		return false
	}
}

type commandObservationCapture struct {
	mu           sync.Mutex
	publishMu    sync.Mutex
	completeOnce sync.Once
	completed    bool
	ctx          context.Context
	observation  toolshared.CommandObservation
	head         []toolshared.CommandTranscriptEntry
	tail         []toolshared.CommandTranscriptEntry
	next         uint64
	headBytes    int
	tailBytes    int
	truncated    bool
	startedAt    time.Time
	liveStream   string
	liveText     string
	liveSequence uint64
}

func newCommandObservationCapture(
	ctx context.Context,
	observation toolshared.CommandObservation,
) *commandObservationCapture {
	return &commandObservationCapture{ctx: ctx, observation: observation, startedAt: time.Now()}
}

func (capture *commandObservationCapture) append(stream string, data []byte) {
	publish := capture.reserveAppend(stream, data)
	if publish != nil {
		publish()
	}
}

// reserveAppend admits one fragment in causal order and returns the matching
// publication. The caller may reserve while holding its own lifecycle lock,
// release that lock, and then publish without allowing a later fragment or
// terminal edge to overtake it.
func (capture *commandObservationCapture) reserveAppend(stream string, data []byte) func() {
	if capture == nil || len(data) == 0 {
		return nil
	}
	capture.publishMu.Lock()
	if capture.completed {
		capture.publishMu.Unlock()
		return nil
	}
	capture.mu.Lock()
	wasTruncated := capture.truncated
	text := strings.ToValidUTF8(string(data), "�")
	capture.retainLocked(stream, text)
	truncated := capture.truncated
	base := capture.observation
	var progress toolshared.CommandTranscriptEntry
	if !wasTruncated {
		if capture.liveStream != stream {
			capture.liveSequence++
			capture.liveStream = stream
			capture.liveText = ""
		}
		remaining := max(0, maxCommandLiveRedactionBytes-len(capture.liveText))
		capture.liveText += validUTF8Prefix(text, remaining)
		truncated = truncated || len(text) > remaining
		progress = toolshared.CommandTranscriptEntry{
			Sequence: capture.liveSequence,
			Stream:   stream,
			Text:     capture.liveText,
		}
	}
	capture.mu.Unlock()
	if wasTruncated {
		// Once the live projection has reached its hard bound, keep only the
		// rolling tail locally and publish it with the terminal snapshot. This
		// caps event/log pressure for commands with unbounded output.
		return capture.publishUnlock
	}
	base.Transcript = []toolshared.CommandTranscriptEntry{progress}
	base.Truncated = base.Truncated || truncated
	return func() {
		defer capture.publishMu.Unlock()
		toolshared.PublishCommandObservation(capture.ctx, base)
	}
}

func (capture *commandObservationCapture) publishUnlock() {
	capture.publishMu.Unlock()
}

func (capture *commandObservationCapture) retainLocked(
	stream, text string,
) {
	if text == "" {
		return
	}
	if capture.headBytes < commandTranscriptHeadBytes && len(capture.head) < commandTranscriptHeadItems {
		prefix := validUTF8Prefix(text, commandTranscriptHeadBytes-capture.headBytes)
		if prefix != "" {
			capture.next++
			entry := toolshared.CommandTranscriptEntry{Sequence: capture.next, Stream: stream, Text: prefix}
			capture.head = append(capture.head, entry)
			capture.headBytes += len(prefix)
			text = text[len(prefix):]
		}
	}
	if text == "" {
		return
	}
	capture.next++
	entry := toolshared.CommandTranscriptEntry{Sequence: capture.next, Stream: stream, Text: text}
	capture.tail = append(capture.tail, entry)
	capture.tailBytes += len(text)
	capture.trimTailLocked()
}

func (capture *commandObservationCapture) trimTailLocked() {
	for len(capture.tail) > commandTranscriptTailItems {
		capture.tailBytes -= len(capture.tail[0].Text)
		capture.tail = capture.tail[1:]
		capture.truncated = true
	}
	for capture.tailBytes > commandTranscriptTailBytes && len(capture.tail) > 0 {
		overflow := capture.tailBytes - commandTranscriptTailBytes
		first := &capture.tail[0]
		if overflow >= len(first.Text) {
			capture.tailBytes -= len(first.Text)
			capture.tail = capture.tail[1:]
			capture.truncated = true
			continue
		}
		trimmed := validUTF8Suffix(first.Text, len(first.Text)-overflow)
		capture.tailBytes -= len(first.Text) - len(trimmed)
		first.Text = trimmed
		capture.truncated = true
	}
}

func (capture *commandObservationCapture) snapshot() toolshared.CommandObservation {
	if capture == nil {
		return toolshared.CommandObservation{}
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	observation := capture.observation
	observation.Transcript = make(
		[]toolshared.CommandTranscriptEntry,
		0,
		len(capture.head)+len(capture.tail)+1,
	)
	observation.Transcript = append(observation.Transcript, capture.head...)
	if capture.truncated {
		observation.Transcript = append(observation.Transcript, toolshared.CommandTranscriptEntry{
			Stream: "system", Text: commandTranscriptOmission,
		})
	}
	observation.Transcript = append(observation.Transcript, capture.tail...)
	observation.Truncated = observation.Truncated || capture.truncated
	return observation
}

func (capture *commandObservationCapture) complete(status string, exitCode *int) {
	if capture == nil {
		return
	}
	capture.completeOnce.Do(func() {
		capture.publishMu.Lock()
		defer capture.publishMu.Unlock()
		capture.completed = true
		observation := capture.snapshot()
		observation.Status = status
		observation.ExitCode = exitCode
		observation.Duration = time.Since(capture.startedAt)
		observation.Canceled = status == "canceled"
		observation.TimedOut = status == "timed_out"
		toolshared.PublishCommandObservation(capture.ctx, observation)
	})
}

type commandCaptureWriter struct {
	target  io.Writer
	capture *commandObservationCapture
	stream  string
}

func (writer commandCaptureWriter) Write(data []byte) (int, error) {
	written, err := writer.target.Write(data)
	if written > 0 {
		writer.capture.append(writer.stream, data[:written])
	}
	return written, err
}
