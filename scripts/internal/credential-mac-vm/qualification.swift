import Darwin
import Foundation

func require(_ value: Bool) throws {
    guard value else { throw VMFailure.unavailable }
}

func denied(_ operation: () throws -> Void) throws {
    do { try operation() } catch { return }
    throw VMFailure.unavailable
}

func chmodACL(_ arguments: [String]) throws {
    let command = Process()
    command.executableURL = URL(fileURLWithPath: "/bin/chmod")
    command.arguments = arguments
    command.standardOutput = FileHandle.nullDevice
    command.standardError = FileHandle.nullDevice
    try command.run()
    command.waitUntilExit()
    try require(command.terminationStatus == 0)
}

@main
struct Qualification {
    static func main() {
        var stage = "private fixture directory"
        do {
            let directory = FileManager.default.temporaryDirectory.appendingPathComponent(
                "mintclaw-vm-inputs-" + UUID().uuidString)
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false,
                                                    attributes: [.posixPermissions: 0o700])
            defer { try? FileManager.default.removeItem(at: directory) }
            let descriptor = open(directory.path, O_RDONLY | O_DIRECTORY | O_CLOEXEC | O_NOFOLLOW)
            try require(descriptor >= 0)
            let pin = FileHandle(fileDescriptor: descriptor, closeOnDealloc: true)
            stage = "directory ownership denial"
            try require(!rootDirectory(descriptor, serviceGID: getgid()))
            let rootDescriptor = open("/", O_RDONLY | O_DIRECTORY | O_CLOEXEC | O_NOFOLLOW)
            try require(rootDescriptor >= 0)
            defer { close(rootDescriptor) }
            try require(rootDirectory(rootDescriptor, serviceGID: getgid()))
            let path = directory.appendingPathComponent("input").path
            defer { try? chmodACL(["-N", path]) }
            try Data("synthetic".utf8).write(to: URL(fileURLWithPath: path))
            try require(chmod(path, 0o600) == 0)
            stage = "ordinary file admission"
            let file = try pinFile(pin, name: "input", uid: getuid(), gid: getgid(),
                                   mode: 0o600, writable: false, maximum: 1024)
            try require(fcntl(file.fileDescriptor, F_GETFD) & FD_CLOEXEC != 0)
            let hash = try digest(file, maximum: 1024)
            try require(hash.count == 64 && (try file.offset()) == 0)
            try denied { _ = try digest(file, maximum: 1) }
            try denied { _ = try pinFile(pin, name: "../input", uid: getuid(), gid: getgid(),
                                         mode: 0o600, writable: false, maximum: 1024) }
            stage = "owner and mode denial"
            try denied { _ = try pinFile(pin, name: "input", uid: getuid() + 1, gid: getgid(),
                                         mode: 0o600, writable: false, maximum: 1024) }
            try denied { _ = try pinFile(pin, name: "input", uid: getuid(), gid: getgid(),
                                         mode: 0o400, writable: false, maximum: 1024) }
            try require(symlink("input", directory.appendingPathComponent("link").path) == 0)
            stage = "symlink denial"
            try denied { _ = try pinFile(pin, name: "link", uid: getuid(), gid: getgid(),
                                         mode: 0o600, writable: false, maximum: 1024) }
            let hardlink = directory.appendingPathComponent("hardlink").path
            stage = "hardlink denial"
            try require(link(path, hardlink) == 0)
            try denied { _ = try pinFile(pin, name: "input", uid: getuid(), gid: getgid(),
                                         mode: 0o600, writable: false, maximum: 1024) }
            try require(unlink(hardlink) == 0)
            stage = "deny ACL admission"
            try chmodACL(["+a", "everyone deny delete", path])
            try require(denyOnlyACL(file.fileDescriptor))
            try chmodACL(["-N", path])
            stage = "allow ACL denial"
            let username = NSUserName()
            try chmodACL(["+a", "user:" + username + " allow read,write", path])
            try require(!denyOnlyACL(file.fileDescriptor))
            try denied { _ = try pinFile(pin, name: "input", uid: getuid(), gid: getgid(),
                                         mode: 0o600, writable: false, maximum: 1024) }
            try chmodACL(["-N", path])
            stage = "ordinary caller denial"
            try denied { _ = try serviceIdentity() }
            #if arch(x86_64)
            let architecture = "x86_64"
            #else
            let architecture = "arm64"
            #endif
            let valid = "mintclaw.credential_vm_boot.v1\n" + architecture + "\n" + hash + "\n" + hash + "\n4294967296\n"
            stage = "manifest admission and denial"
            _ = try BootManifest(Data(valid.utf8))
            for invalid in [valid + "\n", valid.replacingOccurrences(of: hash, with: hash.uppercased()),
                            valid.replacingOccurrences(of: "4294967296", with: "04294967296"),
                            valid.replacingOccurrences(of: "4294967296", with: "1")] {
                try denied { _ = try BootManifest(Data(invalid.utf8)) }
            }
            print("PASS: credential Mac VM public input qualification")
        } catch {
            fputs("credential Mac VM input qualification failed at " + stage + "\n", stderr)
            exit(EXIT_FAILURE)
        }
    }
}
