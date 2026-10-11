package computer

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"time"
	"unicode/utf8"
)

const (
	MaxCaptureBytes       = 16 * 1024 * 1024
	MaxAccessibilityBytes = 32 * 1024
	MaxCaptureDimension   = 16384
	MaxCapturePixels      = 64 * 1024 * 1024
)

// Geometry binds original capture pixels to desktop logical coordinates. A
// resized/cropped image requires its own correctly transformed geometry.
type Geometry struct {
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	OriginX float64 `json:"origin_x"`
	OriginY float64 `json:"origin_y"`
	Scale   float64 `json:"scale"`
}

func (geometry Geometry) Validate() error {
	if geometry.Width <= 0 || geometry.Height <= 0 || geometry.Width > MaxCaptureDimension ||
		geometry.Height > MaxCaptureDimension || int64(geometry.Width)*int64(geometry.Height) > MaxCapturePixels ||
		!finite(geometry.OriginX) || !finite(geometry.OriginY) || !finite(geometry.Scale) || geometry.Scale <= 0 ||
		!finite(geometry.OriginX+float64(geometry.Width)/geometry.Scale) ||
		!finite(geometry.OriginY+float64(geometry.Height)/geometry.Scale) {
		return ErrInvalid
	}
	return nil
}

func (geometry Geometry) DesktopPoint(x, y float64) (float64, float64, error) {
	if geometry.Validate() != nil || !finite(x) || !finite(y) || x < 0 || y < 0 ||
		x >= float64(geometry.Width) || y >= float64(geometry.Height) {
		return 0, 0, ErrInvalid
	}
	return geometry.OriginX + x/geometry.Scale, geometry.OriginY + y/geometry.Scale, nil
}

// Observation's protected payload is live-only. Wire/artifact adapters must use
// scoped transfer and safe durable projections, not JSON or extra history.
// Its timestamps are in the validating host's clock domain; transport adapters
// must establish freshness without treating an untrusted remote clock as proof.
type Observation struct {
	ID                string    `json:"id"`
	Binding           Binding   `json:"binding"`
	Geometry          Geometry  `json:"geometry"`
	CapturedAt        time.Time `json:"captured_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	ContentType       string    `json:"content_type,omitempty"`
	Screenshot        []byte    `json:"-"`
	AccessibilityText string    `json:"-"`
}

func (observation Observation) Validate(session Session, now time.Time) error {
	if err := session.AgentAuthority(now); err != nil {
		return err
	}
	if observation.Binding != session.Binding {
		return ErrStale
	}
	if !validIdentifier(observation.ID) || observation.Geometry.Validate() != nil ||
		observation.CapturedAt.IsZero() || observation.CapturedAt.After(now) ||
		!observation.ExpiresAt.After(observation.CapturedAt) || observation.ExpiresAt.After(session.ExpiresAt) {
		return ErrInvalid
	}
	if !now.Before(observation.ExpiresAt) {
		return ErrExpired
	}
	if len(observation.Screenshot) > MaxCaptureBytes || len(observation.AccessibilityText) > MaxAccessibilityBytes ||
		!utf8.ValidString(observation.AccessibilityText) ||
		len(observation.Screenshot) == 0 && observation.AccessibilityText == "" {
		return ErrInvalid
	}
	if len(observation.Screenshot) == 0 {
		if observation.ContentType != "" {
			return ErrInvalid
		}
		return nil
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(observation.Screenshot))
	if err != nil || config.Width != observation.Geometry.Width || config.Height != observation.Geometry.Height ||
		format != "png" && format != "jpeg" || observation.ContentType != "image/"+format {
		return ErrInvalid
	}
	return nil
}

// String and GoString avoid exposing protected pixels/text through ordinary
// formatted diagnostics. Callers must still never log the payload fields.
func (observation Observation) String() string {
	return fmt.Sprintf("computer observation (%dx%d; protected payload omitted)",
		observation.Geometry.Width, observation.Geometry.Height)
}

func (observation Observation) GoString() string { return observation.String() }

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
