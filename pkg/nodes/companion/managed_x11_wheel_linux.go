//go:build linux

package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/computer/desktop"
)

const (
	managedWheelLimit  = 32
	managedWheelBudget = 2 * time.Second
)

// ManagedX11WheelReceipt records the native press, routing and release
// evidence. The caller independently verifies any application effect.
type ManagedX11WheelReceipt struct {
	AttemptKnown     bool
	Attempted        bool
	DispatchVerified bool
	Neutral          bool
}

type x11WheelPoint struct {
	left, top, x, y int16
	width, height   uint16
}

func newX11WheelPoint(geometry computer.Geometry, point computer.InputPoint) (x11WheelPoint, error) {
	if _, _, err := geometry.DesktopPoint(point.X, point.Y); err != nil || geometry.Scale != 1 {
		return x11WheelPoint{}, computer.ErrInvalid
	}
	for _, v := range []float64{geometry.OriginX, geometry.OriginY, point.X, point.Y} {
		if v < math.MinInt16 || v > math.MaxInt16 || math.Trunc(v) != v {
			return x11WheelPoint{}, computer.ErrInvalid
		}
	}
	return x11WheelPoint{
		left: int16(geometry.OriginX), top: int16(geometry.OriginY), x: int16(point.X), y: int16(point.Y),
		width: uint16(geometry.Width), height: uint16(geometry.Height),
	}, nil
}

type x11WheelFrame struct {
	Sequence     uint64 `json:"request_seq"`
	Attempted    *bool  `json:"attempted"`
	Delivered    *bool  `json:"dispatch_verified"`
	Recovered    *bool  `json:"recovery_verified"`
	Cut          *bool  `json:"input_cut"`
	Acknowledged *bool  `json:"worker_acknowledged"`
}

func (frame x11WheelFrame) cut() bool {
	return frame.Cut != nil && *frame.Cut && frame.Acknowledged != nil && !*frame.Acknowledged &&
		frame.Sequence == 0 && frame.Attempted == nil && frame.Delivered == nil && frame.Recovered == nil
}

func (frame x11WheelFrame) receipt(sequence uint64, cut bool) (ManagedX11WheelReceipt, error) {
	if frame.Sequence != sequence || frame.Attempted == nil || frame.Delivered == nil || frame.Recovered == nil ||
		frame.Cut != nil || frame.Acknowledged != nil {
		return ManagedX11WheelReceipt{}, computer.ErrUnavailable
	}
	attempted, delivered, recovered := *frame.Attempted, *frame.Delivered, *frame.Recovered
	if cut && (!attempted || delivered) || delivered && !recovered || recovered && !attempted {
		return ManagedX11WheelReceipt{}, computer.ErrUnavailable
	}
	return ManagedX11WheelReceipt{
		AttemptKnown: true, Attempted: attempted, DispatchVerified: delivered, Neutral: !attempted || recovered,
	}, nil
}

// WheelDown sends one bounded native wheel step using the original desktop
// lease and trusted, freshly observed geometry. Once the request is written,
// the owner keeps a bounded cleanup window even if the caller cancels.
func (epoch *ManagedX11Pointer) WheelDown(
	ctx context.Context, lease *desktop.InputLease, geometry computer.Geometry, point computer.InputPoint,
) (ManagedX11WheelReceipt, error) {
	notSent := ManagedX11WheelReceipt{AttemptKnown: true, Neutral: true}
	nativePoint, err := newX11WheelPoint(geometry, point)
	if err != nil {
		return notSent, err
	}
	select {
	case epoch.gate <- struct{}{}:
		defer func() { <-epoch.gate }()
	default:
		return notSent, computer.ErrBusy
	}
	if epoch.seq >= managedWheelLimit {
		return notSent, computer.ErrUnavailable
	}
	if _, err = epoch.Binding(ctx); err != nil {
		return notSent, err
	}
	token, err := lease.X11Binding(ctx)
	if err != nil {
		return notSent, err
	}
	if token.ServerPID != uint32(epoch.server.ServerPID) || token.ServerUID != epoch.server.ServerUID {
		return notSent, computer.ErrStale
	}
	if err = ctx.Err(); err != nil {
		return notSent, err
	}
	select {
	case <-epoch.stopped:
		return notSent, computer.ErrStale
	default:
	}
	epoch.seq++
	receipt, err := epoch.wheel(token, nativePoint)
	if err != nil || !receipt.Neutral {
		epoch.revoke()
	}
	return receipt, err
}

func (epoch *ManagedX11Pointer) wheel(
	token desktop.X11InputBinding, point x11WheelPoint,
) (ManagedX11WheelReceipt, error) {
	deadline := time.Now().Add(managedWheelBudget)
	if epoch.stdout.SetReadDeadline(deadline) != nil || epoch.stdin.SetWriteDeadline(deadline) != nil {
		return ManagedX11WheelReceipt{}, computer.ErrUnavailable
	}
	if _, err := fmt.Fprintf(epoch.stdin, "%d %d %d %d %d %d %d %d %d %d %d %d 0\n",
		epoch.seq, token.Window.WindowID, token.Window.PID, point.left, point.top, point.width, point.height,
		point.x, point.y, token.LeaseWindow, token.LeasePID, token.Window.Generation); err != nil {
		return ManagedX11WheelReceipt{}, computer.ErrUnavailable
	}
	var reply x11WheelFrame
	if err := epoch.readFrame(&reply); err != nil {
		return ManagedX11WheelReceipt{}, err
	}
	cut := reply.cut()
	if cut {
		if _, err := io.WriteString(epoch.stdin, "recover\n"); err != nil {
			return ManagedX11WheelReceipt{}, computer.ErrUnavailable
		}
		reply = x11WheelFrame{}
		if err := epoch.readFrame(&reply); err != nil {
			return ManagedX11WheelReceipt{}, err
		}
	}
	return reply.receipt(epoch.seq, cut)
}

func (epoch *ManagedX11Pointer) readFrame(dst any) error {
	line, err := epoch.reader.ReadSlice('\n')
	if err != nil || len(line) > managedPointerFrame {
		return computer.ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return computer.ErrUnavailable
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return computer.ErrUnavailable
	}
	return nil
}
