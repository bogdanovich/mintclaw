import AppKit

final class Fixture: NSObject, NSApplicationDelegate, NSTextFieldDelegate {
    var windows: [NSWindow] = []
    var fields: [NSTextField] = []
    var counts = [0, 0]
    var lastValues: [String] = []

    func emit(_ value: [String: Any]) {
        let data = try! JSONSerialization.data(withJSONObject: value, options: [.sortedKeys])
        print(String(data: data, encoding: .utf8)!)
        fflush(stdout)
    }

    func state() { emit(["kind": "state", "values": fields.map { $0.stringValue }, "counts": counts]) }

    func applicationDidFinishLaunching(_ notification: Notification) {
        for index in 0..<2 {
            let window = NSWindow(contentRect: NSRect(x: 100 + 460 * index, y: 300, width: 440, height: 240),
                                  styleMask: [.titled, .closable], backing: .buffered, defer: false)
            window.title = "MintClaw disposable fixture \(index)"
            window.isReleasedWhenClosed = false
            let heading = NSTextField(labelWithString: "MintClaw qualification fixture")
            heading.frame = NSRect(x: 25, y: 180, width: 390, height: 24)
            window.contentView!.addSubview(heading)
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
        Timer.scheduledTimer(withTimeInterval: 0.2, repeats: true) { _ in
            let values = self.fields.map { $0.stringValue }
            if values != self.lastValues {
                self.lastValues = values
                self.state()
            }
        }
    }

    func controlTextDidChange(_ notification: Notification) { state() }
    @objc func apply(_ button: NSButton) { counts[button.tag] += 1; state() }
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }
}

let app = NSApplication.shared
let fixture = Fixture()
app.setActivationPolicy(.regular)
app.delegate = fixture
app.run()
