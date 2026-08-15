package browser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// CaptureScreenshot captures one PNG only for the exact fresh observation
// owned by the caller. Artifact persistence and routed delivery remain gateway
// responsibilities outside the broker.
func (broker *Broker) CaptureScreenshot(
	ctx context.Context,
	request ScreenshotRequest,
) (ScreenshotCapture, error) {
	target := request.Target
	if target == "" {
		target = ScreenshotTargetPage
	}
	if request.Owner.Validate() != nil || !validIdentifier(request.RequestID) ||
		!validIdentifier(request.SessionID) ||
		!validIdentifier(request.TabID) || !validIdentifier(request.SnapshotID) ||
		request.SnapshotGeneration == 0 ||
		!validContextBinding(request.FrameID, request.ContextCatalogID, request.ContextGeneration) ||
		(target != ScreenshotTargetPage && target != ScreenshotTargetElement) ||
		(target == ScreenshotTargetPage && request.Ref != "") ||
		(target == ScreenshotTargetElement && !validIdentifier(request.Ref)) {
		return ScreenshotCapture{}, fmt.Errorf("%w: malformed screenshot request", ErrInvalid)
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	session, slot, worker, err := broker.actionSessionLocked(
		ctx, request.Owner, request.SessionID, request.TabID,
	)
	if err != nil {
		return ScreenshotCapture{}, err
	}
	if session.SnapshotID != request.SnapshotID ||
		session.SnapshotGeneration != request.SnapshotGeneration ||
		!sessionMatchesContextBinding(
			session, request.FrameID, request.ContextCatalogID, request.ContextGeneration,
		) {
		return ScreenshotCapture{}, ErrStale
	}
	var contextWorker ContextWorker
	if session.ContextAuthority != nil {
		var ok bool
		contextWorker, ok = worker.(ContextWorker)
		if !ok && session.FrameID != "" {
			return ScreenshotCapture{}, ErrDriverIncompatible
		}
		if ok {
			if err = broker.ensureContextFreshLocked(ctx, session, contextWorker); err != nil {
				return ScreenshotCapture{}, err
			}
		}
	}
	maximum := broker.config.Limits.Effective().ScreenshotBytes
	var screenshot DriverScreenshot
	if retainedWorker, ok := worker.(RetainedScreenshotWorker); ok {
		if request.Retention == nil {
			return ScreenshotCapture{}, ErrDenied
		}
		var element DriverElement
		if target == ScreenshotTargetElement {
			var found bool
			element, found = slot.refs[request.Ref]
			if !found || slot.navigationID == "" {
				return ScreenshotCapture{}, ErrStale
			}
		}
		if slot.navigationID == "" {
			return ScreenshotCapture{}, ErrStale
		}
		screenshot, err = retainedWorker.CaptureRetainedScreenshot(
			ctx, request, slot.navigationID, element, maximum,
		)
	} else if target == ScreenshotTargetElement {
		element, ok := slot.refs[request.Ref]
		if !ok || slot.navigationID == "" {
			return ScreenshotCapture{}, ErrStale
		}
		elementWorker, ok := worker.(ElementScreenshotWorker)
		if !ok {
			return ScreenshotCapture{}, ErrDriverIncompatible
		}
		screenshot, err = elementWorker.CaptureElementScreenshot(
			ctx, slot.navigationID, session.SnapshotOrigin, element, maximum,
		)
	} else {
		screenshotWorker, ok := worker.(BoundScreenshotWorker)
		if !ok {
			return ScreenshotCapture{}, ErrDriverIncompatible
		}
		if slot.navigationID == "" {
			return ScreenshotCapture{}, ErrStale
		}
		screenshot, err = screenshotWorker.CapturePageScreenshot(ctx, slot.navigationID, maximum)
	}
	if err != nil {
		return ScreenshotCapture{}, err
	}
	if contextWorker != nil {
		if err = broker.ensureContextFreshLocked(ctx, session, contextWorker); err != nil {
			return ScreenshotCapture{}, err
		}
	}
	if screenshot.ContentType != "image/png" {
		return ScreenshotCapture{}, ErrDriverIncompatible
	}
	if screenshot.Retained != nil {
		retained := screenshot.Retained
		if len(screenshot.Data) != 0 || retained.ContentType != "image/png" ||
			retained.Size < 1 || retained.Size > int64(maximum) ||
			retained.ExpiresAt < 1 || retained.Ref == "" || len(retained.Ref) > 256 ||
			len(retained.SHA256) != sha256.Size*2 {
			return ScreenshotCapture{}, ErrDriverIncompatible
		}
		if digest, decodeErr := hex.DecodeString(retained.SHA256); decodeErr != nil || len(digest) != sha256.Size {
			return ScreenshotCapture{}, ErrDriverIncompatible
		}
	} else if len(screenshot.Data) == 0 || len(screenshot.Data) > maximum ||
		!bytes.HasPrefix(screenshot.Data, pngSignature) {
		return ScreenshotCapture{}, ErrDriverIncompatible
	}
	return ScreenshotCapture{
		SessionID: session.ID, Target: session.Target, Profile: session.Profile,
		PolicyRevision: session.PolicyRevision, TabID: session.TabID,
		FrameID: session.FrameID, ContextCatalogID: request.ContextCatalogID,
		ContextGeneration: request.ContextGeneration,
		SnapshotID:        session.SnapshotID, SnapshotGeneration: session.SnapshotGeneration,
		CaptureTarget: target, Data: append([]byte(nil), screenshot.Data...), ContentType: screenshot.ContentType,
		Retained: screenshot.Retained,
	}, nil
}
