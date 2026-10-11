import Darwin
import Foundation
import Virtualization

// Read-only public host facts. This program never creates a VM, reads a key,
// opens vault/guest storage, inspects process arguments, or qualifies isolation.
struct HostFacts: Encodable {
    let schema = "mintclaw.credential_mac_host_facts.v1"
    let architecture: String
    let osVersion: String
    let virtualizationSupported: Bool
    let physicalMemoryMiB: UInt64
    let minimumVirtualCPUs: Int
    let maximumVirtualCPUs: Int
    let minimumGuestMemoryMiB: UInt64
    let maximumGuestMemoryMiB: UInt64
    let proposedVirtualCPUs = 2
    let proposedGuestMemoryMiB: UInt64 = 4096
    let proposedResourcesWithinFrameworkLimits: Bool
    let currentUID: UInt32
    let currentGID: UInt32
    let currentProcessHasAdminGroup: Bool
    let proposedServiceAccountPresent: Bool
    let proposedServiceUID: UInt32?
    let proposedServiceGID: UInt32?
    let credentialHostQualified = false
    let administrationBoundary = "unqualified"
    let vmStorageConsoleAndBackups = "unqualified"
    let actualLinuxBrowserAndPhone = "unqualified"
}

#if arch(x86_64)
let architecture = "x86_64"
#elseif arch(arm64)
let architecture = "arm64"
#else
let architecture = "unsupported"
#endif

guard CommandLine.arguments.count == 1 else {
    fputs("credential Mac preflight accepts no arguments\n", stderr)
    exit(1)
}

let groupCount = getgroups(0, nil)
guard groupCount >= 0, groupCount <= 1024 else {
    fputs("credential Mac host facts unavailable\n", stderr)
    exit(1)
}
var groups = [gid_t](repeating: 0, count: Int(groupCount))
guard getgroups(groupCount, &groups) == groupCount else {
    fputs("credential Mac host facts unavailable\n", stderr)
    exit(1)
}
let adminGID = getgrnam("admin").map { $0.pointee.gr_gid }
let hasAdminGroup = adminGID.map { groups.contains($0) || getgid() == $0 } ?? false
let serviceIdentity = getpwnam("_mintclawcredvm").map { ($0.pointee.pw_uid, $0.pointee.pw_gid) }
let minimumMemory = VZVirtualMachineConfiguration.minimumAllowedMemorySize
let maximumMemory = VZVirtualMachineConfiguration.maximumAllowedMemorySize
let minimumCPUs = VZVirtualMachineConfiguration.minimumAllowedCPUCount
let maximumCPUs = VZVirtualMachineConfiguration.maximumAllowedCPUCount
let proposedMemory: UInt64 = 4096 * 1048576
let facts = HostFacts(
    architecture: architecture,
    osVersion: ProcessInfo.processInfo.operatingSystemVersionString,
    virtualizationSupported: VZVirtualMachine.isSupported,
    physicalMemoryMiB: ProcessInfo.processInfo.physicalMemory / 1048576,
    minimumVirtualCPUs: minimumCPUs,
    maximumVirtualCPUs: maximumCPUs,
    minimumGuestMemoryMiB: minimumMemory / 1048576,
    maximumGuestMemoryMiB: maximumMemory / 1048576,
    proposedResourcesWithinFrameworkLimits: minimumCPUs <= 2 && maximumCPUs >= 2 &&
        minimumMemory <= proposedMemory && maximumMemory >= proposedMemory,
    currentUID: getuid(),
    currentGID: getgid(),
    currentProcessHasAdminGroup: hasAdminGroup,
    proposedServiceAccountPresent: serviceIdentity != nil,
    proposedServiceUID: serviceIdentity?.0,
    proposedServiceGID: serviceIdentity?.1
)
do {
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
    let data = try encoder.encode(facts)
    FileHandle.standardOutput.write(data)
    FileHandle.standardOutput.write(Data([10]))
} catch {
    fputs("credential Mac host facts unavailable\n", stderr)
    exit(1)
}
