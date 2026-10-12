import CryptoKit
import Darwin
import Foundation

enum VMFailure: Error { case unavailable }

let serviceName = "_mintclawcredvm"
let bootDirectory = "/Library/Application Support/MintClawCredentialVM/boot"
let stateDirectory = "/private/var/db/mintclaw-credential-vm"

func serviceIdentity() throws -> (uid_t, gid_t) {
    guard let account = getpwnam(serviceName) else { throw VMFailure.unavailable }
    let uid = account.pointee.pw_uid, gid = account.pointee.pw_gid
    guard let agent = getpwnam("ab"), uid != agent.pointee.pw_uid,
          uid != 0, uid != UInt32.max, gid != 0, gid != UInt32.max, gid != 20, gid != 80,
          getuid() == uid, geteuid() == uid, getgid() == gid, getegid() == gid,
          getgrnam(serviceName)?.pointee.gr_gid == gid else { throw VMFailure.unavailable }
    let count = getgroups(0, nil)
    guard count >= 0, count <= 128 else { throw VMFailure.unavailable }
    var groups = [gid_t](repeating: 0, count: Int(count))
    guard getgroups(count, &groups) == count, groups.allSatisfy({ $0 == gid }) else {
        throw VMFailure.unavailable
    }
    for key in ["OP_SERVICE_ACCOUNT_TOKEN", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH",
                "DYLD_FRAMEWORK_PATH", "SSH_AUTH_SOCK"] {
        guard getenv(key) == nil else { throw VMFailure.unavailable }
    }
    var limits = rlimit(rlim_cur: 0, rlim_max: 0)
    guard setrlimit(RLIMIT_CORE, &limits) == 0 else { throw VMFailure.unavailable }
    return (uid, gid)
}

// Root's standard deny-delete ACL is harmless. Any allow entry denies; startup
// never tries to repair ownership, ACLs or paths on behalf of an agent.
func denyOnlyACL(_ descriptor: Int32) -> Bool {
    errno = 0
    // Darwin reports ENOENT when this valid descriptor has no extended ACL.
    guard let acl = acl_get_fd_np(descriptor, ACL_TYPE_EXTENDED) else { return errno == ENOENT }
    defer { acl_free(UnsafeMutableRawPointer(acl)) }
    guard acl_valid(acl) == 0 else { return false }
    for index in 0..<170 {
        var entry: acl_entry_t?
        errno = 0
        let status = acl_get_entry(acl, Int32(index), &entry)
        if status == -1 && errno == EINVAL { return true }
        guard status == 0, let value = entry else { return false }
        var tag = ACL_UNDEFINED_TAG
        guard acl_get_tag_type(value, &tag) == 0, tag == ACL_EXTENDED_DENY else { return false }
    }
    return false
}

func rootDirectory(_ descriptor: Int32, serviceGID: gid_t) -> Bool {
    var info = stat()
    return fstat(descriptor, &info) == 0 && info.st_uid == 0 &&
        (info.st_gid == 0 || info.st_gid == 80 || info.st_gid == serviceGID) &&
        info.st_mode & mode_t(S_IFMT) == mode_t(S_IFDIR) &&
        info.st_mode & 0o7022 == 0 && denyOnlyACL(descriptor)
}

func pinDirectory(_ path: String, serviceGID: gid_t) throws -> FileHandle {
    guard path.hasPrefix("/"), path.utf8.count <= 256,
          URL(fileURLWithPath: path).standardized.path == path else { throw VMFailure.unavailable }
    let parts = path.split(separator: "/")
    guard parts.count <= 16 else { throw VMFailure.unavailable }
    var descriptor = open("/", O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
    guard descriptor >= 0 else { throw VMFailure.unavailable }
    do {
        for part in parts {
            guard rootDirectory(descriptor, serviceGID: serviceGID) else { throw VMFailure.unavailable }
            let next = openat(descriptor, String(part), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
            guard next >= 0 else { throw VMFailure.unavailable }
            close(descriptor)
            descriptor = next
        }
        var final = stat()
        guard rootDirectory(descriptor, serviceGID: serviceGID), fstat(descriptor, &final) == 0,
              final.st_gid == serviceGID, final.st_mode & 0o7777 == 0o750 else { throw VMFailure.unavailable }
        return FileHandle(fileDescriptor: descriptor, closeOnDealloc: true)
    } catch {
        close(descriptor)
        throw VMFailure.unavailable
    }
}

func pinFile(_ directory: FileHandle, name: String, uid: uid_t, gid: gid_t,
             mode: mode_t, writable: Bool, maximum: Int64) throws -> FileHandle {
    guard !name.isEmpty, !name.contains("/"), name != ".", name != ".." else { throw VMFailure.unavailable }
    let descriptor = openat(directory.fileDescriptor, name,
                            (writable ? O_RDWR : O_RDONLY) | O_NOFOLLOW | O_CLOEXEC | O_NONBLOCK)
    guard descriptor >= 0 else { throw VMFailure.unavailable }
    let file = FileHandle(fileDescriptor: descriptor, closeOnDealloc: true)
    var info = stat()
    guard fstat(descriptor, &info) == 0, info.st_mode & mode_t(S_IFMT) == mode_t(S_IFREG),
          info.st_uid == uid, info.st_gid == gid, info.st_mode & 0o7777 == mode,
          info.st_nlink == 1, info.st_size > 0, info.st_size <= maximum,
          denyOnlyACL(descriptor) else { throw VMFailure.unavailable }
    return file
}

func digest(_ file: FileHandle, maximum: Int64) throws -> String {
    var hasher = SHA256()
    var total: Int64 = 0
    try file.seek(toOffset: 0)
    while let data = try file.read(upToCount: 65536), !data.isEmpty {
        total += Int64(data.count)
        guard total <= maximum else { throw VMFailure.unavailable }
        hasher.update(data: data)
    }
    try file.seek(toOffset: 0)
    return hasher.finalize().map { String(format: "%02x", $0) }.joined()
}

struct BootManifest {
    let kernelDigest: String
    let initrdDigest: String
    let diskBytes: Int64

    init(_ data: Data) throws {
        guard data.count <= 512, let text = String(data: data, encoding: .ascii) else {
            throw VMFailure.unavailable
        }
        let lines = text.components(separatedBy: "\n")
        #if arch(x86_64)
        let architecture = "x86_64"
        #elseif arch(arm64)
        let architecture = "arm64"
        #else
        let architecture = "unsupported"
        #endif
        guard lines.count == 6, lines[0] == "mintclaw.credential_vm_boot.v1", lines[1] == architecture,
              [lines[2], lines[3]].allSatisfy({ value in
                  value.utf8.count == 64 && value.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
              }), let bytes = Int64(lines[4]), String(bytes) == lines[4],
              bytes >= 4 * 1024 * 1024 * 1024, bytes <= 128 * 1024 * 1024 * 1024,
              lines[5].isEmpty else { throw VMFailure.unavailable }
        kernelDigest = lines[2]
        initrdDigest = lines[3]
        diskBytes = bytes
    }
}

final class ProtectedInputs {
    let manifest: BootManifest
    let files: [FileHandle]

    init() throws {
        let (uid, gid) = try serviceIdentity()
        let boot = try pinDirectory(bootDirectory, serviceGID: gid)
        let state = try pinDirectory(stateDirectory, serviceGID: gid)
        let specification = try pinFile(boot, name: "manifest.v1", uid: 0, gid: gid,
                                        mode: 0o640, writable: false, maximum: 512)
        manifest = try BootManifest(try specification.readToEnd() ?? Data())
        let kernel = try pinFile(boot, name: "vmlinuz", uid: 0, gid: gid,
                                 mode: 0o440, writable: false, maximum: 256 * 1024 * 1024)
        let initrd = try pinFile(boot, name: "initrd", uid: 0, gid: gid,
                                 mode: 0o440, writable: false, maximum: 512 * 1024 * 1024)
        guard try digest(kernel, maximum: 256 * 1024 * 1024) == manifest.kernelDigest,
              try digest(initrd, maximum: 512 * 1024 * 1024) == manifest.initrdDigest else {
            throw VMFailure.unavailable
        }
        let disk = try pinFile(state, name: "guest.raw", uid: uid, gid: gid,
                               mode: 0o600, writable: true, maximum: manifest.diskBytes)
        var diskInfo = stat()
        guard fstat(disk.fileDescriptor, &diskInfo) == 0, diskInfo.st_size == manifest.diskBytes,
              flock(disk.fileDescriptor, LOCK_EX | LOCK_NB) == 0 else { throw VMFailure.unavailable }
        files = [boot, state, specification, kernel, initrd, disk]
    }
}
