//go:build darwin

package desktop

import (
	"bytes"
	"os"

	"github.com/ebitengine/purego"
)

// Apple system frameworks only; no external command, permission prompt or TCC
// database access. purego keeps companion cross-builds independent of cgo.
type nativeAPI struct {
	handles         []uintptr
	release         func(uintptr)
	typeID          func(uintptr) uintptr
	stringType      func() uintptr
	booleanType     func() uintptr
	numberType      func() uintptr
	makeString      func(uintptr, string, uint32) uintptr
	stringBytes     func(uintptr, *byte, int64, uint32) bool
	dictionaryValue func(uintptr, uintptr) uintptr
	booleanValue    func(uintptr) bool
	numberValue     func(uintptr, int32, *int64) bool
	mainBundle      func() uintptr
	bundleID        func(uintptr) uintptr
	bundleValue     func(uintptr, uintptr) uintptr
	currentSession  func() uintptr
	accessibility   func() bool
	screenRecording func() bool
}

type nativeFunction struct {
	name   string
	target any
}

func loadNative(permissions bool) (*nativeAPI, error) {
	api := &nativeAPI{}
	frameworks := []struct {
		name      string
		functions []nativeFunction
	}{
		{"CoreFoundation", []nativeFunction{
			{"CFRelease", &api.release},
			{"CFGetTypeID", &api.typeID},
			{"CFStringGetTypeID", &api.stringType},
			{"CFBooleanGetTypeID", &api.booleanType},
			{"CFNumberGetTypeID", &api.numberType},
			{"CFStringCreateWithCString", &api.makeString},
			{"CFStringGetCString", &api.stringBytes},
			{"CFDictionaryGetValue", &api.dictionaryValue},
			{"CFBooleanGetValue", &api.booleanValue},
			{"CFNumberGetValue", &api.numberValue},
			{"CFBundleGetMainBundle", &api.mainBundle},
			{"CFBundleGetIdentifier", &api.bundleID},
			{"CFBundleGetValueForInfoDictionaryKey", &api.bundleValue},
		}},
		{"CoreGraphics", []nativeFunction{
			{"CGSessionCopyCurrentDictionary", &api.currentSession},
			{"CGPreflightScreenCaptureAccess", &api.screenRecording},
		}},
		{"ApplicationServices", []nativeFunction{
			{"AXIsProcessTrusted", &api.accessibility},
		}},
	}
	for _, framework := range frameworks {
		if !permissions && framework.name != "CoreFoundation" {
			continue
		}
		path := "/System/Library/Frameworks/" + framework.name + ".framework/" + framework.name
		handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			api.close()
			return nil, ErrUnsupported
		}
		api.handles = append(api.handles, handle)
		for _, function := range framework.functions {
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

func (api *nativeAPI) close() {
	for i := len(api.handles) - 1; i >= 0; i-- {
		_ = purego.Dlclose(api.handles[i])
	}
}

func (api *nativeAPI) text(value uintptr) string {
	if value == 0 || api.typeID(value) != api.stringType() {
		return ""
	}
	var data [4096]byte
	if !api.stringBytes(value, &data[0], int64(len(data)), 0x08000100) {
		return ""
	}
	end := bytes.IndexByte(data[:], 0)
	if end < 0 {
		return ""
	}
	return string(data[:end])
}

func (api *nativeAPI) keyed(object uintptr, name string, get func(uintptr, uintptr) uintptr) uintptr {
	key := api.makeString(0, name, 0x08000100)
	if key == 0 {
		return 0
	}
	defer api.release(key)
	return get(object, key)
}

func (api *nativeAPI) application() Application {
	bundle := api.mainBundle()
	if bundle == 0 {
		return Application{}
	}
	return Application{
		BundleID:   api.text(api.bundleID(bundle)),
		ConfigPath: api.text(api.keyed(bundle, "MintClawNodeConfigPath", api.bundleValue)),
	}
}

func CurrentApplication() (Application, error) {
	api, err := loadNative(false)
	if err != nil {
		return Application{}, err
	}
	defer api.close()
	return api.application(), nil
}

func inspectNative() (Readiness, error) {
	api, err := loadNative(true)
	if err != nil {
		return Readiness{}, err
	}
	defer api.close()
	report := Readiness{OS: "darwin", BundleID: api.application().BundleID}
	session := api.currentSession()
	if session != 0 {
		defer api.release(session)
		onConsole := api.keyed(session, "kCGSSessionOnConsoleKey", api.dictionaryValue)
		uidValue := api.keyed(session, "kCGSSessionUserIDKey", api.dictionaryValue)
		var uid int64
		report.ActiveGUI = onConsole != 0 && api.typeID(onConsole) == api.booleanType() &&
			api.booleanValue(onConsole) && uidValue != 0 && api.typeID(uidValue) == api.numberType() &&
			api.numberValue(uidValue, 4, &uid) && uid == int64(os.Getuid())
	}
	// These exact non-prompting functions report this process's real TCC context.
	report.Accessibility = api.accessibility()
	report.ScreenRecording = api.screenRecording()
	return report, nil
}
