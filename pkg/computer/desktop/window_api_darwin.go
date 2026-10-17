//go:build darwin

package desktop

import "github.com/ebitengine/purego"

type macWindowAPI struct {
	*nativeAPI
	retain         func(uintptr) uintptr
	arrayType      func() uintptr
	arrayCount     func(uintptr) int64
	arrayValue     func(uintptr, int64) uintptr
	elementType    func() uintptr
	createApp      func(int32) uintptr
	copyAttribute  func(uintptr, uintptr, *uintptr) int32
	elementPID     func(uintptr, *int32) int32
	elementWindow  func(uintptr, *uint32) int32
	setTimeout     func(uintptr, float32) int32
	observerCreate func(int32, uintptr, *uintptr) int32
	observerAdd    func(uintptr, uintptr, uintptr, uintptr) int32
	observerRemove func(uintptr, uintptr, uintptr) int32
	observerSource func(uintptr) uintptr
	currentLoop    func() uintptr
	addSource      func(uintptr, uintptr, uintptr)
	removeSource   func(uintptr, uintptr, uintptr)
	runMode        func(uintptr, float64, bool) int32
}

func loadMacWindowAPI() (*macWindowAPI, error) {
	base, err := loadNative(true)
	if err != nil {
		return nil, err
	}
	api := &macWindowAPI{nativeAPI: base}
	libraries := []struct {
		name      string
		functions []nativeFunction
	}{
		{"CoreFoundation", []nativeFunction{
			{"CFRetain", &api.retain},
			{"CFArrayGetTypeID", &api.arrayType},
			{"CFArrayGetCount", &api.arrayCount},
			{"CFArrayGetValueAtIndex", &api.arrayValue},
			{"CFRunLoopGetCurrent", &api.currentLoop},
			{"CFRunLoopAddSource", &api.addSource},
			{"CFRunLoopRemoveSource", &api.removeSource},
			{"CFRunLoopRunInMode", &api.runMode},
		}},
		{"ApplicationServices", []nativeFunction{
			{"AXUIElementGetTypeID", &api.elementType},
			{"AXUIElementCreateApplication", &api.createApp},
			{"AXUIElementCopyAttributeValue", &api.copyAttribute},
			{"AXUIElementGetPid", &api.elementPID},
			// This SPI is the exact CG-window/AX-element mapping. Absence is
			// unsupported: never substitute titles, geometry or list position.
			{"_AXUIElementGetWindow", &api.elementWindow},
			{"AXUIElementSetMessagingTimeout", &api.setTimeout},
			{"AXObserverCreate", &api.observerCreate},
			{"AXObserverAddNotification", &api.observerAdd},
			{"AXObserverRemoveNotification", &api.observerRemove},
			{"AXObserverGetRunLoopSource", &api.observerSource},
		}},
	}
	for _, library := range libraries {
		handle, openErr := purego.Dlopen("/System/Library/Frameworks/"+library.name+".framework/"+library.name,
			purego.RTLD_NOW|purego.RTLD_LOCAL)
		if openErr != nil {
			api.close()
			return nil, ErrUnsupported
		}
		api.handles = append(api.handles, handle)
		for _, function := range library.functions {
			symbol, lookupErr := purego.Dlsym(handle, function.name)
			if lookupErr != nil {
				api.close()
				return nil, ErrUnsupported
			}
			purego.RegisterFunc(function.target, symbol)
		}
	}
	return api, nil
}

func (api *macWindowAPI) attribute(element uintptr, name string) (uintptr, int32) {
	key := api.makeString(0, name, 0x08000100)
	if key == 0 {
		return 0, -25204
	}
	defer api.release(key)
	var value uintptr
	code := api.copyAttribute(element, key, &value)
	return value, code
}
