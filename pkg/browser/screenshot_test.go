package browser

import (
	"context"
	"errors"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestBrokerScreenshotRequiresExactFreshObservationAndReturnsCopy(t *testing.T) {
	broker, worker, session := openActionTestBroker(t, NewMemoryStore())
	observation, err := broker.Observe(
		context.Background(), testOwner(), session.ID, session.TabID,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := ScreenshotRequest{
		Owner: testOwner(), RequestID: "request_1", SessionID: session.ID,
		TabID: session.TabID, SnapshotID: observation.SnapshotID,
		SnapshotGeneration: observation.SnapshotGeneration,
	}
	wrongOwner := request
	wrongOwner.Owner.ActorID = "actor_2"
	if _, err = broker.CaptureScreenshot(context.Background(), wrongOwner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-owner CaptureScreenshot() error = %v, want ErrNotFound", err)
	}
	capture, err := broker.CaptureScreenshot(context.Background(), request)
	if err != nil || capture.SessionID != session.ID || capture.TabID != session.TabID ||
		capture.SnapshotID != observation.SnapshotID || capture.ContentType != "image/png" ||
		capture.PolicyRevision == "" || len(capture.Data) == 0 {
		t.Fatalf("CaptureScreenshot() = %+v, %v", capture, err)
	}
	capture.Data[0] = 0
	if worker.screenshot.Data[0] != pngSignature[0] {
		t.Fatal("CaptureScreenshot() exposed worker-owned bytes")
	}
	if _, err = broker.Observe(context.Background(), testOwner(), session.ID, session.TabID); err != nil {
		t.Fatal(err)
	}
	if _, err = broker.CaptureScreenshot(context.Background(), request); !errors.Is(err, ErrStale) {
		t.Fatalf("stale CaptureScreenshot() error = %v, want ErrStale", err)
	}
}

func TestBrokerScreenshotRejectsWrongTypeSignatureAndSize(t *testing.T) {
	root := admittedBrowserConfig()
	root.Tools.Browser.Limits.ScreenshotBytes = 16
	broker, worker, session := openActionTestBrokerWithConfig(t, root, NewMemoryStore())
	observation, err := broker.Observe(
		context.Background(), testOwner(), session.ID, session.TabID,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := ScreenshotRequest{
		Owner: testOwner(), RequestID: "request_1", SessionID: session.ID,
		TabID: session.TabID, SnapshotID: observation.SnapshotID,
		SnapshotGeneration: observation.SnapshotGeneration,
	}
	tests := []DriverScreenshot{
		{Data: append([]byte(nil), pngSignature...), ContentType: "image/jpeg"},
		{Data: []byte("not-png"), ContentType: "image/png"},
		{Data: append(append([]byte(nil), pngSignature...), make([]byte, 9)...), ContentType: "image/png"},
	}
	for _, screenshot := range tests {
		worker.screenshot = screenshot
		if _, err = broker.CaptureScreenshot(context.Background(), request); !errors.Is(err, ErrDriverIncompatible) {
			t.Fatalf("CaptureScreenshot(%q, %d) error = %v", screenshot.ContentType, len(screenshot.Data), err)
		}
	}
}

func TestBrokerElementScreenshotRequiresFreshSemanticReference(t *testing.T) {
	broker, worker, session := openActionTestBroker(t, NewMemoryStore())
	observation, err := broker.Observe(t.Context(), testOwner(), session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	ref := stableElementRef(observation.SnapshotID, worker.observation.Elements[0].Target)
	request := ScreenshotRequest{
		Owner: testOwner(), RequestID: "capture_element_1", SessionID: session.ID,
		TabID: session.TabID, SnapshotID: observation.SnapshotID,
		SnapshotGeneration: observation.SnapshotGeneration,
		Target:             ScreenshotTargetElement, Ref: ref,
	}
	capture, err := broker.CaptureScreenshot(t.Context(), request)
	if err != nil || capture.CaptureTarget != ScreenshotTargetElement ||
		capture.SnapshotID != observation.SnapshotID ||
		worker.screenshotElement != worker.observation.Elements[0] {
		t.Fatalf("CaptureScreenshot() = %+v, %v; element = %+v", capture, err, worker.screenshotElement)
	}

	wrongRef := request
	wrongRef.Ref = "element_wrong"
	if _, err = broker.CaptureScreenshot(t.Context(), wrongRef); !errors.Is(err, ErrStale) {
		t.Fatalf("wrong-ref CaptureScreenshot() error = %v, want ErrStale", err)
	}
	pageWithRef := request
	pageWithRef.Target = ScreenshotTargetPage
	if _, err = broker.CaptureScreenshot(t.Context(), pageWithRef); !errors.Is(err, ErrInvalid) {
		t.Fatalf("page-with-ref CaptureScreenshot() error = %v, want ErrInvalid", err)
	}
	partialContext := request
	partialContext.FrameID = "frame_1"
	if _, err = broker.CaptureScreenshot(t.Context(), partialContext); !errors.Is(err, ErrInvalid) {
		t.Fatalf("partial-context CaptureScreenshot() error = %v, want ErrInvalid", err)
	}
}

func TestBrowserScreenshotLimitUsesBoundedEffectiveValue(t *testing.T) {
	limits := config.BrowserLimitsConfig{}.Effective()
	if limits.ScreenshotBytes != config.BrowserMaxScreenshotBytes {
		t.Fatalf("ScreenshotBytes = %d", limits.ScreenshotBytes)
	}
}
