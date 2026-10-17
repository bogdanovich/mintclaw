//go:build darwin

package desktop

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ebitengine/purego"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

var (
	macObservers        sync.Map
	macObserverSequence atomic.Uint64
	macObserverCallback = sync.OnceValue(func() uintptr {
		// One native trampoline per process, not one unreclaimable callback per
		// admission. The refcon is a numeric registry token, never a Go pointer.
		return purego.NewCallback(func(_ uintptr, _ uintptr, _ uintptr, token uintptr) {
			if value, ok := macObservers.Load(token); ok {
				value.(*darwinWindow).lost.Store(true)
			}
		})
	})
)

type macProbe struct {
	ctx    context.Context
	result chan bool
}

type darwinWindow struct {
	lost      atomic.Bool
	probes    chan macProbe
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// All AX/CF objects and the run loop belong to one locked native thread.
// Callers can cancel a probe without transferring native ownership to themselves.
type macWindowResources struct {
	api          *macWindowAPI
	process      *darwinProcess
	application  uintptr
	element      uintptr
	observer     uintptr
	notification uintptr
	mode         uintptr
	loop         uintptr
	source       uintptr
	token        uintptr
	pid          uint32
	window       uint32
}

func watchNativeWindow(ctx context.Context, request WindowRequest) (windowBackend, error) {
	if request.Display != "" || request.XAuthority != "" {
		return nil, computer.ErrInvalid
	}
	report, err := Inspect(ctx)
	if err != nil {
		return nil, err
	}
	if !report.Ready {
		return nil, computer.ErrDenied
	}
	openCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	guard := &darwinWindow{probes: make(chan macProbe), stop: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan error)
	go guard.run(openCtx, request, ready)
	select {
	case err = <-ready:
		if err != nil {
			_ = guard.close()
			return nil, err
		}
		return guard, nil
	case <-openCtx.Done():
		_ = guard.close()
		return nil, openCtx.Err()
	}
}

func (guard *darwinWindow) run(ctx context.Context, request WindowRequest, ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(guard.done)
	resources := &macWindowResources{pid: request.PID, window: request.WindowID}
	defer resources.close()
	err := resources.open(ctx, guard)
	if err == nil && !resources.alive(ctx, guard) {
		err = computer.ErrStale
	}
	select {
	case ready <- err:
		if err != nil {
			return
		}
	case <-ctx.Done():
		return
	case <-guard.stop:
		return
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-guard.stop:
			return
		case probe := <-guard.probes:
			probe.result <- resources.alive(probe.ctx, guard)
		case <-ticker.C:
			resources.drain(guard)
		}
	}
}

func (guard *darwinWindow) alive(ctx context.Context) bool {
	if guard.lost.Load() || ctx.Err() != nil {
		return false
	}
	probe := macProbe{ctx: ctx, result: make(chan bool, 1)}
	select {
	case guard.probes <- probe:
	case <-ctx.Done():
		return false
	case <-guard.done:
		return false
	}
	select {
	case valid := <-probe.result:
		return valid && ctx.Err() == nil && !guard.lost.Load()
	case <-ctx.Done():
		return false
	case <-guard.done:
		return false
	}
}

func (guard *darwinWindow) close() error {
	guard.lost.Store(true)
	guard.closeOnce.Do(func() { close(guard.stop) })
	<-guard.done
	return nil
}

func (resources *macWindowResources) open(ctx context.Context, guard *darwinWindow) error {
	var err error
	resources.process, err = watchDarwinProcess(resources.pid)
	if err != nil {
		return err
	}
	resources.api, err = loadMacWindowAPI()
	if err != nil {
		return err
	}
	api := resources.api
	resources.application = api.createApp(int32(resources.pid))
	if resources.application == 0 || api.setTimeout(resources.application, 0.25) != 0 {
		return computer.ErrUnavailable
	}
	windows, code := api.attribute(resources.application, "AXWindows")
	if windows != 0 {
		defer api.release(windows)
	}
	if code != 0 || windows == 0 || api.typeID(windows) != api.arrayType() {
		return computer.ErrDenied
	}
	count := api.arrayCount(windows)
	if count < 1 || count > 128 {
		return computer.ErrDenied
	}
	for index := int64(0); index < count; index++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		candidate := api.arrayValue(windows, index)
		if candidate == 0 || api.typeID(candidate) != api.elementType() || api.setTimeout(candidate, 0.25) != 0 {
			return computer.ErrUnavailable
		}
		var window uint32
		if api.elementWindow(candidate, &window) == 0 && window == resources.window {
			if resources.element != 0 {
				return computer.ErrDenied
			}
			resources.element = api.retain(candidate)
		}
	}
	if resources.element == 0 {
		return computer.ErrStale
	}
	resources.notification = api.makeString(0, "AXUIElementDestroyed", 0x08000100)
	resources.mode = api.makeString(0, "MintClawWindowGuardMode", 0x08000100)
	if resources.notification == 0 || resources.mode == 0 {
		return computer.ErrUnavailable
	}
	resources.token, err = registerMacObserver(guard)
	if err != nil {
		return err
	}
	if api.observerCreate(int32(resources.pid), macObserverCallback(), &resources.observer) != 0 ||
		resources.observer == 0 {
		return ErrUnsupported
	}
	if api.observerAdd(resources.observer, resources.element, resources.notification, resources.token) != 0 {
		return ErrUnsupported
	}
	resources.loop = api.currentLoop()
	resources.source = api.observerSource(resources.observer)
	if resources.loop == 0 || resources.source == 0 {
		return ErrUnsupported
	}
	api.addSource(resources.loop, resources.source, resources.mode)
	return nil
}

func registerMacObserver(guard *darwinWindow) (uintptr, error) {
	for {
		previous := macObserverSequence.Load()
		if previous == ^uint64(0) {
			return 0, computer.ErrUnavailable
		}
		if macObserverSequence.CompareAndSwap(previous, previous+1) {
			token := uintptr(previous + 1)
			macObservers.Store(token, guard)
			return token, nil
		}
	}
}

func (resources *macWindowResources) drain(guard *darwinWindow) {
	// Each call handles at most one source, and the total work is bounded. A
	// notification flood invalidates admission instead of starving release.
	for range 512 {
		switch resources.api.runMode(resources.mode, 0, true) {
		case 3: // Timed out: the current observer queue is drained.
			return
		case 4: // Handled one source; continue the bounded drain.
		default:
			guard.lost.Store(true)
			return
		}
	}
	guard.lost.Store(true)
}

func (resources *macWindowResources) alive(ctx context.Context, guard *darwinWindow) bool {
	if ctx.Err() != nil || guard.lost.Load() {
		return false
	}
	resources.drain(guard)
	if guard.lost.Load() || !resources.process.alive() {
		guard.lost.Store(true)
		return false
	}
	api := resources.api
	var pid int32
	var window uint32
	if api.elementPID(resources.element, &pid) != 0 || pid != int32(resources.pid) ||
		api.elementWindow(resources.element, &window) != 0 || window != resources.window {
		guard.lost.Store(true)
		return false
	}
	role, code := api.attribute(resources.element, "AXRole")
	if role != 0 {
		defer api.release(role)
	}
	if code != 0 || api.text(role) != "AXWindow" {
		guard.lost.Store(true)
		return false
	}
	resources.drain(guard)
	if !resources.process.alive() {
		guard.lost.Store(true)
	}
	return ctx.Err() == nil && !guard.lost.Load()
}

func (resources *macWindowResources) close() {
	api := resources.api
	if api != nil {
		if resources.loop != 0 && resources.source != 0 && resources.mode != 0 {
			api.removeSource(resources.loop, resources.source, resources.mode)
		}
		if resources.observer != 0 {
			if resources.element != 0 && resources.notification != 0 {
				_ = api.observerRemove(resources.observer, resources.element, resources.notification)
			}
			api.release(resources.observer)
		}
		if resources.token != 0 {
			macObservers.Delete(resources.token)
		}
		for _, value := range []uintptr{resources.element, resources.application, resources.notification, resources.mode} {
			if value != 0 {
				api.release(value)
			}
		}
		api.close()
	}
	if resources.process != nil {
		_ = resources.process.close()
	}
}
