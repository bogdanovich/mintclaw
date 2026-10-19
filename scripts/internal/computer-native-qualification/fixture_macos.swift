import AppKit
import CoreFoundation

struct PreparedFixtureText {
    let value: String
    let utf16Caret: Int

    init?(_ input: Any?) {
        guard let prepared = input as? [String: Any],
              let value = prepared["value"] as? String,
              value.utf8.count <= 4096,
              let number = prepared["caret"] as? NSNumber,
              CFGetTypeID(number) != CFBooleanGetTypeID(),
              !CFNumberIsFloatType(number),
              let caret = prepared["caret"] as? Int,
              caret >= 0, caret <= value.unicodeScalars.count else { return nil }
        self.value = value
        self.utf16Caret = String(value.unicodeScalars.prefix(caret)).utf16.count
    }
}

final class Fixture: NSObject, NSApplicationDelegate, NSWindowDelegate, NSTextFieldDelegate {
    var windows: [NSWindow] = []
    var fields: [NSTextField] = []
    var headings: [NSTextField] = []
    var counts = [0, 0]
    var lastValues: [String] = []

    func emit(_ value: [String: Any]) {
        let data = try! JSONSerialization.data(withJSONObject: value, options: [.sortedKeys])
        print(String(data: data, encoding: .utf8)!)
        fflush(stdout)
    }

    func state() { emit(["kind": "state", "values": fields.map { $0.stringValue }, "counts": counts]) }

    // Controller-only fixture seeding, never an evaluated model action. The
    // protocol counts Unicode scalars, while AppKit selection ranges use UTF-16.
    func prepareText(_ inspectID: String, prepared: PreparedFixtureText) {
        guard windows[0].makeFirstResponder(nil) else {
            emit(["kind": "fixture_error", "inspect_id": inspectID, "error_code": "editor_resign_failed"])
            return
        }
        fields[0].stringValue = prepared.value
        guard windows[0].makeFirstResponder(fields[0]),
              let editor = fields[0].currentEditor() as? NSTextView,
              editor.string == prepared.value else {
            emit(["kind": "fixture_error", "inspect_id": inspectID, "error_code": "editor_unavailable"])
            return
        }
        let selection = NSRange(location: prepared.utf16Caret, length: 0)
        editor.setSelectedRange(selection)
        guard editor.selectedRange() == selection else {
            emit(["kind": "fixture_error", "inspect_id": inspectID, "error_code": "caret_setup_failed"])
            return
        }
        emit(["kind": "prepared_text", "inspect_id": inspectID,
              "values": fields.map { $0.stringValue }, "counts": counts])
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        for index in 0..<2 {
            let window = NSWindow(contentRect: NSRect(x: 100 + 460 * index, y: 300, width: 440, height: 240),
                                  styleMask: [.titled, .closable], backing: .buffered, defer: false)
            window.title = "MintClaw disposable fixture \(index)"
            window.isReleasedWhenClosed = false
            window.delegate = self
            let heading = NSTextField(labelWithString: "MintClaw qualification fixture")
            heading.frame = NSRect(x: 25, y: 180, width: 390, height: 24)
            window.contentView!.addSubview(heading)
            headings.append(heading)
            let field = NSTextField(frame: NSRect(x: 25, y: 125, width: 390, height: 32))
            field.stringValue = index == 0 ? "" : "GUARD_UNCHANGED"
            field.setAccessibilityLabel("Qualification text")
            field.delegate = self
            window.contentView!.addSubview(field)
            fields.append(field)
            let button = NSButton(title: "Apply fixture", target: self, action: #selector(apply(_:)))
            button.frame = NSRect(x: 25, y: 55, width: 390, height: 35)
            button.tag = index
            window.contentView!.addSubview(button)
            windows.append(window)
            window.makeKeyAndOrderFront(nil)
        }
        NSApp.activate(ignoringOtherApps: true)
        emit(["kind": "ready", "pid": ProcessInfo.processInfo.processIdentifier,
              "windows": windows.map { $0.windowNumber }])
        state()
        DispatchQueue.global().async {
            while let line = readLine() {
                guard let data = line.data(using: .utf8),
                      let request = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
                      let inspectID = request["inspect_id"] as? String,
                      !inspectID.isEmpty, inspectID.utf8.count <= 128 else { continue }
                if request["prepare_text"] != nil {
                    guard let prepared = PreparedFixtureText(request["prepare_text"]) else { continue }
                    DispatchQueue.main.async { self.prepareText(inspectID, prepared: prepared) }
                    continue
                }
                DispatchQueue.main.async {
                    if request["advance_visual"] as? Bool == true {
                        self.headings[0].stringValue = "MintClaw fresh resumed frame"
                        self.windows[0].display()
                    }
                    if request["close_target"] as? Bool == true,
                       ProcessInfo.processInfo.environment["MINTCLAW_FIXTURE_KEEP_GUARD"] == "1" {
                        self.windows[0].close()
                    }
                    self.emit(["kind": "lifetime", "inspect_id": inspectID,
                               "pid": ProcessInfo.processInfo.processIdentifier,
                               "windows": self.windows.filter { $0.isVisible }.map { $0.windowNumber }])
                }
            }
        }
        if ProcessInfo.processInfo.environment["MINTCLAW_FIXTURE_CLOSE_AFTER_READY"] == "1" {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.2) { self.windows[0].performClose(nil) }
        }
        Timer.scheduledTimer(withTimeInterval: 0.2, repeats: true) { _ in
            let values = self.fields.map { $0.stringValue }
            if values != self.lastValues {
                self.lastValues = values
                self.state()
            }
        }
    }

    func controlTextDidChange(_ notification: Notification) { state() }
    func windowWillClose(_ notification: Notification) {
        if ProcessInfo.processInfo.environment["MINTCLAW_FIXTURE_KEEP_GUARD"] == "1",
           let window = notification.object as? NSWindow, window === windows.first { return }
        NSApp.terminate(nil)
    }
    @objc func apply(_ button: NSButton) { counts[button.tag] += 1; state() }
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }
}

#if MINTCLAW_FIXTURE_TEXT_SELF_TEST
let text = "A👩‍💻e\u{0301}|Z"
// Exact Unicode-scalar boundaries, including inside the ZWJ and combining
// sequences. Neither Swift Character counts nor byte offsets are AppKit ranges.
for (caret, utf16Caret) in [0, 1, 3, 4, 6, 7, 8, 9, 10].enumerated() {
    let prepared = PreparedFixtureText(["value": text, "caret": caret])
    precondition(prepared?.value == text && prepared?.utf16Caret == utf16Caret)
}
for caret: Any in [-1, 10, true, false, 1.5, NSNumber(value: 1.0), "1", NSNull()] {
    precondition(PreparedFixtureText(["value": text, "caret": caret]) == nil)
}
precondition(PreparedFixtureText(nil) == nil)
precondition(PreparedFixtureText(["value": 1, "caret": 0]) == nil)
precondition(PreparedFixtureText(["value": "", "caret": 0])?.utf16Caret == 0)
precondition(PreparedFixtureText(["value": String(repeating: "é", count: 2049), "caret": 0]) == nil)
print("Mac fixture text protocol and scalar/UTF-16 caret self-tests passed.")
#else
let app = NSApplication.shared
let fixture = Fixture()
app.setActivationPolicy(.regular)
app.delegate = fixture
app.run()
#endif
