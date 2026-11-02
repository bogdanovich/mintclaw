import AppKit
import CryptoKit

let recipeRevision = "f23ca5033bb936f52ec7c879b710e9f9af76365b71b2f09af7625d2294b04ef0"
let specHashes = ["CU-T18": "40f62ed6adf3959b55fdbcb8b11665568bbfa439390910c147b9ec05e80c6f47",
                  "CU-T19": "ba6a45c2b8ec767b54f450084e2e0d029202870cac5d9c630d72014b53c2b22d"]

func readBoundedLine(_ maximum: Int) -> Data? {
    var value = Data()
    var overflow = false
    while true {
        let byte = getchar()
        if byte == EOF { return nil }
        if byte == 10 { return overflow ? Data() : value }
        if value.count < maximum { value.append(UInt8(byte)) } else { overflow = true }
    }
}

struct RunRecord: Decodable {
    let taskID: String
    let revision: String
    let runID: String
    enum CodingKeys: String, CodingKey {
        case taskID = "task_id", revision = "fixture_revision", runID = "run_id"
    }
}

struct Launch: Decodable {
    let record: RunRecord
    let specBase64: String
    enum CodingKeys: String, CodingKey { case record, specBase64 = "spec_base64" }
}

struct Surface: Decodable { let width: Double; let height: Double }
struct Box: Decodable { let x: Double; let y: Double; let width: Double; let height: Double }
struct Card: Codable {
    let id: String
    let label: String
    var x: Double
    var y: Double
    let width: Double
    let height: Double
}
struct FormFields: Decodable { let name: String; let choice: String; let confirmed: Bool; let period: String }
struct Decoys: Decodable {
    let namePreview: String
    let choicePreview: String
    let confirmedPreview: Bool
    enum CodingKeys: String, CodingKey {
        case namePreview = "name_preview", choicePreview = "choice_preview", confirmedPreview = "confirmed_preview"
    }
}
struct Spec: Decodable {
    let surface: Surface
    let cards: [Card]?
    let destination: Box?
    let fields: FormFields?
    let decoys: Decoys?
    let choices: [String]?
    let periods: [String]?
}

final class Canvas: NSView {
    let spec: Spec
    var cards: [Card]
    var active: (Int, Double, Double)?
    var drag: [String: Any]?
    override var isFlipped: Bool { true }
    init(spec: Spec) {
        self.spec = spec
        cards = spec.cards!
        super.init(frame: NSRect(x: 0, y: 0, width: spec.surface.width, height: spec.surface.height))
    }
    required init?(coder: NSCoder) { fatalError("fixture-only view") }
    override func draw(_ dirtyRect: NSRect) {
        NSColor.white.setFill()
        NSBezierPath(rect: bounds).fill()
        ("Move Card Alpha into the outlined destination" as NSString).draw(
            at: NSPoint(x: 20, y: 10), withAttributes: [.font: NSFont.systemFont(ofSize: 16)])
        let zone = spec.destination!
        NSColor.systemGreen.setStroke()
        let outline = NSBezierPath(rect: NSRect(x: zone.x, y: zone.y, width: zone.width, height: zone.height))
        outline.lineWidth = 2
        outline.stroke()
        for card in cards {
            (card.id == "alpha" ? NSColor.systemBlue : NSColor.systemGreen).setFill()
            NSBezierPath(rect: NSRect(x: card.x, y: card.y, width: card.width, height: card.height)).fill()
            (card.label as NSString).draw(at: NSPoint(x: card.x + 12, y: card.y + 24),
                                         withAttributes: [.font: NSFont.systemFont(ofSize: 16),
                                                          .foregroundColor: NSColor.white])
        }
    }
    override func mouseDown(with event: NSEvent) {
        let point = convert(event.locationInWindow, from: nil)
        for index in cards.indices.reversed() {
            let card = cards[index]
            if NSRect(x: card.x, y: card.y, width: card.width, height: card.height).contains(point) {
                active = (index, point.x - card.x, point.y - card.y)
                drag = ["card": card.id, "from": [card.x, card.y], "to": [card.x, card.y],
                        "updates": 0, "completed": false]
                return
            }
        }
    }
    func move(_ event: NSEvent, count: Bool) {
        guard let (index, dx, dy) = active else { return }
        let point = convert(event.locationInWindow, from: nil)
        cards[index].x = min(max(point.x - dx, 0), spec.surface.width - cards[index].width)
        cards[index].y = min(max(point.y - dy, 0), spec.surface.height - cards[index].height)
        drag!["to"] = [cards[index].x, cards[index].y]
        if count { drag!["updates"] = min((drag!["updates"] as! Int) + 1, 100000) }
        needsDisplay = true
    }
    override func mouseDragged(with event: NSEvent) { move(event, count: true) }
    override func cancelOperation(_ sender: Any?) { active = nil }
    override func mouseUp(with event: NSEvent) {
        guard active != nil else { return }
        move(event, count: false)
        drag!["completed"] = true
        active = nil
    }
    func snapshot() -> [String: Any] {
        let data = try! JSONEncoder().encode(cards)
        return ["cards": try! JSONSerialization.jsonObject(with: data), "drag": drag as Any? ?? NSNull()]
    }
}

final class Fixture: NSObject, NSApplicationDelegate, NSWindowDelegate, NSTextFieldDelegate {
    let record: RunRecord
    let spec: Spec
    var target: NSWindow!
    var guardWindow: NSWindow!
    var guardEntry: NSTextField!
    var guardCount = 0
    var canvas: Canvas?
    var entries: [String: NSTextField] = [:]
    var choices: [String: NSPopUpButton] = [:]
    var checks: [String: NSButton] = [:]
    var periods: [String: NSButton] = [:]
    var original: [Int] = []
    init(record: RunRecord, spec: Spec) { self.record = record; self.spec = spec }
    func emit(_ value: [String: Any]) {
        let data = try! JSONSerialization.data(withJSONObject: value, options: [.sortedKeys])
        print(String(data: data, encoding: .utf8)!)
        fflush(stdout)
    }
    func makeWindow(_ title: String, x: Double, width: Double, height: Double) -> NSWindow {
        let window = NSWindow(contentRect: NSRect(x: x, y: 100, width: width, height: height),
                              styleMask: [.titled, .closable], backing: .buffered, defer: false)
        window.title = title
        window.isReleasedWhenClosed = false
        window.delegate = self
        return window
    }
    func applicationDidFinishLaunching(_ notification: Notification) {
        target = makeWindow("MintClaw \(record.taskID) disposable fixture", x: 40,
                            width: spec.surface.width, height: spec.surface.height)
        guardWindow = makeWindow("MintClaw disposable guard", x: 720, width: 280, height: 180)
        guardEntry = NSTextField(frame: NSRect(x: 20, y: 112, width: 240, height: 28))
        guardEntry.stringValue = "GUARD_UNCHANGED"
        guardEntry.delegate = self
        guardWindow.contentView!.addSubview(guardEntry)
        let button = NSButton(title: "Guard apply", target: self, action: #selector(guardApply))
        button.frame = NSRect(x: 20, y: 55, width: 240, height: 32)
        guardWindow.contentView!.addSubview(button)
        if record.taskID == "CU-T18" {
            canvas = Canvas(spec: spec)
            target.contentView = canvas
        } else {
            makeForm()
        }
        target.makeKeyAndOrderFront(nil)
        guardWindow.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        original = [target.windowNumber, guardWindow.windowNumber]
        emit(["kind": "ready", "pid": ProcessInfo.processInfo.processIdentifier,
              "windows": original, "task_id": record.taskID, "fixture_revision": recipeRevision])
        DispatchQueue.global().async {
            while let data = readBoundedLine(4096) {
                guard let request = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
                      Set(request.keys) == ["inspect_id"], let id = request["inspect_id"] as? String,
                      !id.isEmpty, id.utf8.count <= 128 else { continue }
                DispatchQueue.main.async { self.inspect(id) }
            }
        }
    }
    func makeForm() {
        let content = target.contentView!
        var y = 430.0
        func label(_ title: String) {
            let field = NSTextField(labelWithString: title)
            field.frame = NSRect(x: 20, y: y, width: 145, height: 26)
            content.addSubview(field)
        }
        for (key, title, initial) in [("name_preview", "Name preview", spec.decoys!.namePreview),
                                      ("name", "Name", spec.fields!.name)] {
            label(title)
            let field = NSTextField(frame: NSRect(x: 175, y: y, width: 390, height: 28))
            field.stringValue = initial
            field.setAccessibilityLabel(title)
            field.delegate = self
            entries[key] = field
            content.addSubview(field)
            y -= 46
        }
        for (key, title, initial) in [("choice_preview", "Choice preview", spec.decoys!.choicePreview),
                                      ("choice", "Choice", spec.fields!.choice)] {
            label(title)
            let choice = NSPopUpButton(frame: NSRect(x: 175, y: y, width: 390, height: 28), pullsDown: false)
            choice.addItems(withTitles: spec.choices!)
            choice.selectItem(withTitle: initial)
            choice.setAccessibilityLabel(title)
            choices[key] = choice
            content.addSubview(choice)
            y -= 46
        }
        for (key, title) in [("confirmed_preview", "Confirm preview"), ("confirmed", "Confirm")] {
            let check = NSButton(checkboxWithTitle: title, target: nil, action: nil)
            check.frame = NSRect(x: 175, y: y, width: 390, height: 28)
            checks[key] = check
            content.addSubview(check)
            y -= 46
        }
        label("Period")
        for (index, name) in spec.periods!.enumerated() {
            let button = NSButton(radioButtonWithTitle: name, target: self, action: #selector(periodChosen(_:)))
            button.frame = NSRect(x: 175 + 125 * index, y: Int(y), width: 120, height: 28)
            button.state = name == spec.fields!.period ? .on : .off
            periods[name] = button
            content.addSubview(button)
        }
    }
    @objc func guardApply() { guardCount += 1 }
    @objc func periodChosen(_ sender: NSButton) {
        for button in periods.values { button.state = button === sender ? .on : .off }
    }
    func controlTextDidChange(_ notification: Notification) {
        guard let field = notification.object as? NSTextField, field.stringValue.unicodeScalars.count > 4096 else { return }
        field.stringValue = String(field.stringValue.unicodeScalars.prefix(4096))
    }
    func inspect(_ id: String) {
        var state: [String: Any]
        if let canvas { state = canvas.snapshot() } else {
            let selected = periods.filter { $0.value.state == .on }.map { $0.key }
            state = ["fields": ["name": entries["name"]!.stringValue,
                                 "choice": choices["choice"]!.titleOfSelectedItem ?? "",
                                 "confirmed": checks["confirmed"]!.state == .on,
                                 "period": selected.count == 1 ? selected[0] : ""],
                     "decoys": ["name_preview": entries["name_preview"]!.stringValue,
                                 "choice_preview": choices["choice_preview"]!.titleOfSelectedItem ?? "",
                                 "confirmed_preview": checks["confirmed_preview"]!.state == .on]]
        }
        let value: [String: Any] = ["task_id": record.taskID, "run_id": record.runID,
                                   "fixture_revision": recipeRevision, "state": state,
                                   "guard": ["text": guardEntry.stringValue, "count": guardCount,
                                             "visible": guardWindow.isVisible]]
        emit(["kind": "snapshot", "inspect_id": id, "pid": ProcessInfo.processInfo.processIdentifier,
              "windows": original, "visible_windows": zip([target!, guardWindow!], original)
                .filter { $0.0.isVisible }.map { $0.1 }, "value": value])
    }
    func windowWillClose(_ notification: Notification) {
        if notification.object as? NSWindow === guardWindow { NSApp.terminate(nil) }
        canvas?.active = nil
    }
    func windowDidResignKey(_ notification: Notification) {
        if notification.object as? NSWindow === target { canvas?.active = nil }
    }
}

guard CommandLine.arguments.count == 3, CommandLine.arguments[1] == "--task",
      let expected = specHashes[CommandLine.arguments[2]], let line = readBoundedLine(16384),
      let launch = try? JSONDecoder().decode(Launch.self, from: line),
      launch.record.taskID == CommandLine.arguments[2], launch.record.revision == recipeRevision,
      launch.record.runID.range(of: "^[0-9a-f]{32}$", options: .regularExpression) != nil,
      let data = Data(base64Encoded: launch.specBase64),
      SHA256.hash(data: data).map({ String(format: "%02x", $0) }).joined() == expected,
      let spec = try? JSONDecoder().decode(Spec.self, from: data) else {
    fputs("Fixture launch refused.\n", stderr)
    exit(2)
}
let application = NSApplication.shared
let fixture = Fixture(record: launch.record, spec: spec)
application.setActivationPolicy(.regular)
application.delegate = fixture
application.run()
