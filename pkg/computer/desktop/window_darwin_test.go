//go:build darwin

package desktop

import (
	"errors"
	"os"
	"testing"

	"github.com/ebitengine/purego"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

func TestMacWindowNativeSymbolsDoNotPromptOrCapture(t *testing.T) {
	for range 3 {
		api, err := loadMacWindowAPI()
		if err != nil {
			t.Fatal("required exact-window native APIs unavailable", err)
		}
		application := api.createApp(int32(os.Getpid()))
		if application == 0 || api.typeID(application) != api.elementType() || api.setTimeout(application, 0.25) != 0 {
			t.Fatal("native AX handle or messaging-timeout ABI failed")
		}
		api.release(application)
		api.close()
	}
}

func TestMacWindowNotificationUsesOneTrampolineAndNoGoPointer(t *testing.T) {
	callback := macObserverCallback()
	var invoke func(uintptr, uintptr, uintptr, uintptr)
	purego.RegisterFunc(&invoke, callback)
	for range 2500 {
		guard := &darwinWindow{}
		token, err := registerMacObserver(guard)
		if err != nil {
			t.Fatal(err)
		}
		invoke(0, 0, 0, token)
		macObservers.Delete(token)
		if !guard.lost.Load() || macObserverCallback() != callback {
			t.Fatal("notification did not invalidate exact registered admission")
		}
		guard.lost.Store(false)
		invoke(0, 0, 0, token)
		if guard.lost.Load() {
			t.Fatal("released callback registration was resurrected")
		}
	}
	macObservers.Range(func(_, _ any) bool {
		t.Error("native callback registration leaked")
		return true
	})
}

func TestMacWindowAdmissionDoesNotBypassActualReadiness(t *testing.T) {
	report, err := Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Skip("this process already has actual companion GUI grants")
	}
	guard, err := WatchWindow(t.Context(), WindowRequest{PID: uint32(os.Getpid()), WindowID: 1})
	if guard != nil || !errors.Is(err, computer.ErrDenied) {
		t.Fatal("unready caller attempted native window admission", err)
	}
}

func TestMacWindowEventDrainIsBoundedAndFailClosed(t *testing.T) {
	for _, result := range []int32{1, 2, 3, 4, 99} {
		guard := &darwinWindow{}
		calls := 0
		resources := &macWindowResources{api: &macWindowAPI{
			runMode: func(uintptr, float64, bool) int32 { calls++; return result },
		}}
		resources.drain(guard)
		if guard.lost.Load() != (result != 3) || calls > 512 || calls < 1 {
			t.Fatal("invalid run-loop state or notification flood was not bounded", result, calls)
		}
	}
}

func TestMacWindowResourceReleaseUnregistersWithoutApplicationActions(t *testing.T) {
	guard := &darwinWindow{}
	token, err := registerMacObserver(guard)
	if err != nil {
		t.Fatal(err)
	}
	released := make(map[uintptr]int)
	removedSource, removedNotification := false, false
	resources := &macWindowResources{
		api: &macWindowAPI{
			nativeAPI: &nativeAPI{release: func(value uintptr) { released[value]++ }},
			removeSource: func(loop, source, mode uintptr) {
				removedSource = loop == 1 && source == 2 && mode == 3
			},
			observerRemove: func(observer, element, notification uintptr) int32 {
				removedNotification = observer == 4 && element == 5 && notification == 6
				return 0
			},
		},
		loop: 1, source: 2, mode: 3, observer: 4, element: 5, notification: 6, application: 7, token: token,
	}
	resources.close()
	if !removedSource || !removedNotification || len(released) != 5 {
		t.Fatal("native observer resources were not released", released)
	}
	for _, value := range []uintptr{3, 4, 5, 6, 7} {
		if released[value] != 1 {
			t.Fatal("owned native reference not released exactly once", value)
		}
	}
	if _, ok := macObservers.Load(token); ok {
		t.Fatal("released observer token leaked")
	}
}
