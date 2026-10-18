import CoreFoundation
import CoreGraphics
import Darwin
import Foundation

func qualificationBoolean(_ value: Any?) -> Bool? {
    guard let number = value as? NSNumber, CFGetTypeID(number) == CFBooleanGetTypeID() else { return nil }
    return number.boolValue
}

func qualificationGUIBlocker(_ session: [String: Any]?, uid: uid_t) -> String? {
    guard let session = session,
          qualificationBoolean(session["kCGSSessionOnConsoleKey"]) == true,
          qualificationBoolean(session["kCGSessionLoginDoneKey"]) == true,
          let owner = session["kCGSSessionUserIDKey"] as? NSNumber,
          CFGetTypeID(owner) == CFNumberGetTypeID(), owner.int64Value == Int64(uid)
    else { return "Native qualification needs this user's active GUI login." }
    // This non-public OS hint is only a refusal gate. Its absence is not window
    // admission or permission evidence; the original native suite still runs.
    if let locked = session["CGSSessionScreenIsLocked"], qualificationBoolean(locked) != false {
        return "Native qualification needs an unlocked desktop. Unlock this user's Mac and retry."
    }
    return nil
}

func qualificationExitStatus(_ status: Int32, reason: Process.TerminationReason) -> Int32 {
    return reason == .exit ? status : 128 + status
}

func qualificationChildStatus(_ executable: String, arguments: [String]) throws -> Int32 {
    let child = Process()
    child.executableURL = URL(fileURLWithPath: executable)
    child.arguments = arguments
    child.standardInput = FileHandle.nullDevice
    child.standardOutput = FileHandle.standardOutput
    child.standardError = FileHandle.standardError
    try child.run()
    child.waitUntilExit()
    return qualificationExitStatus(child.terminationStatus, reason: child.terminationReason)
}

func qualificationWriteReceipt(_ status: Int32, path: String) throws {
    try "\(status)\n".write(toFile: path, atomically: true, encoding: .utf8)
}

#if MINTCLAW_QUALIFICATION_SELF_TEST
let healthy: [String: Any] = [
    "kCGSSessionOnConsoleKey": true,
    "kCGSessionLoginDoneKey": true,
    "kCGSSessionUserIDKey": 501
]
precondition(qualificationGUIBlocker(healthy, uid: 501) == nil)
precondition(qualificationGUIBlocker(nil, uid: 501) != nil)
precondition(qualificationGUIBlocker(healthy, uid: 502) != nil)
for value: Any in [true, "501"] {
    var session = healthy
    session["kCGSSessionUserIDKey"] = value
    precondition(qualificationGUIBlocker(session, uid: 501) != nil)
}
var missingOwner = healthy
missingOwner.removeValue(forKey: "kCGSSessionUserIDKey")
precondition(qualificationGUIBlocker(missingOwner, uid: 501) != nil)
for key in ["kCGSSessionOnConsoleKey", "kCGSessionLoginDoneKey"] {
    for value: Any in [false, "true", NSNumber(value: 1)] {
        var session = healthy
        session[key] = value
        precondition(qualificationGUIBlocker(session, uid: 501) != nil)
    }
    var missing = healthy
    missing.removeValue(forKey: key)
    precondition(qualificationGUIBlocker(missing, uid: 501) != nil)
}
for value: Any in [true, "false", NSNumber(value: 0)] {
    var session = healthy
    session["CGSSessionScreenIsLocked"] = value
    precondition(qualificationGUIBlocker(session, uid: 501) != nil)
}
var unlocked = healthy
unlocked["CGSSessionScreenIsLocked"] = false
precondition(qualificationGUIBlocker(unlocked, uid: 501) == nil)
precondition(qualificationExitStatus(0, reason: .exit) == 0)
precondition(qualificationExitStatus(7, reason: .exit) == 7)
precondition(qualificationExitStatus(9, reason: .uncaughtSignal) == 137)
precondition(try! qualificationChildStatus("/usr/bin/true", arguments: []) == 0)
precondition(try! qualificationChildStatus("/usr/bin/false", arguments: []) == 1)
let receiptRoot = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
try FileManager.default.createDirectory(at: receiptRoot, withIntermediateDirectories: false)
defer { try? FileManager.default.removeItem(at: receiptRoot) }
let receiptPath = receiptRoot.appendingPathComponent("exit.txt").path
for status: Int32 in [0, 1, 75, 137] {
    try qualificationWriteReceipt(status, path: receiptPath)
    let value = try String(contentsOfFile: receiptPath, encoding: .utf8)
    precondition(value == "\(status)\n")
}
print("Mac qualification preflight and exit-status self-tests passed.")
#else
let environment = ProcessInfo.processInfo.environment
guard let executable = environment["MINTCLAW_QUALIFICATION_EXECUTABLE"], executable.hasPrefix("/"),
      FileManager.default.isExecutableFile(atPath: executable),
      let receipt = environment["MINTCLAW_QUALIFICATION_EXIT_FILE"], receipt.hasPrefix("/")
else { exit(64) }
var status: Int32 = 70
do {
    let session = CGSessionCopyCurrentDictionary() as? [String: Any]
    if let blocker = qualificationGUIBlocker(session, uid: getuid()) {
        fputs(blocker + "\n", stderr)
        status = 75
    } else {
        status = try qualificationChildStatus(executable, arguments: Array(CommandLine.arguments.dropFirst()))
    }
    try qualificationWriteReceipt(status, path: receipt)
} catch {
    fputs("GUI qualification launcher failed: \(error)\n", stderr)
    exit(70)
}
exit(status)
#endif
