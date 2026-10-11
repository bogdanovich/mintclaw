import Darwin
import Foundation
import Virtualization

func unavailable() -> Never {
    fputs("credential Mac VM unavailable\n", stderr)
    exit(EXIT_FAILURE)
}

func configuration(_ inputs: ProtectedInputs) throws -> VZVirtualMachineConfiguration {
    guard VZVirtualMachine.isSupported else { throw VMFailure.unavailable }
    let result = VZVirtualMachineConfiguration()
    result.cpuCount = 2
    result.memorySize = 4096 * 1024 * 1024
    result.platform = VZGenericPlatformConfiguration()
    let boot = VZLinuxBootLoader(kernelURL: URL(fileURLWithPath: bootDirectory + "/vmlinuz"))
    boot.initialRamdiskURL = URL(fileURLWithPath: bootDirectory + "/initrd")
    boot.commandLine = "root=/dev/vda rw panic=1"
    result.bootLoader = boot
    let attachment = try VZDiskImageStorageDeviceAttachment(
        url: URL(fileURLWithPath: stateDirectory + "/guest.raw"), readOnly: false,
        cachingMode: .cached, synchronizationMode: .fsync)
    let disk = VZVirtioBlockDeviceConfiguration(attachment: attachment)
    result.storageDevices = [disk]
    let network = VZVirtioNetworkDeviceConfiguration()
    guard let address = VZMACAddress(string: "02:00:00:ab:00:01") else { throw VMFailure.unavailable }
    network.macAddress = address
    network.attachment = VZNATNetworkDeviceAttachment()
    result.networkDevices = [network]
    result.entropyDevices = [VZVirtioEntropyDeviceConfiguration()]
    // No host directories, serial/console, graphics, clipboard, display,
    // keyboard/mouse, vsock controls, USB redirection or saved machine state.
    try result.validate()
    return result
}

final class VMDriver: NSObject, VZVirtualMachineDelegate {
    let inputs: ProtectedInputs
    let machine: VZVirtualMachine
    var signals: [DispatchSourceSignal] = []

    init(inputs: ProtectedInputs, configuration: VZVirtualMachineConfiguration) {
        self.inputs = inputs
        machine = VZVirtualMachine(configuration: configuration, queue: .main)
        super.init()
        machine.delegate = self
    }

    func run() {
        for number in [SIGTERM, SIGINT] {
            signal(number, SIG_IGN)
            let source = DispatchSource.makeSignalSource(signal: number, queue: .main)
            source.setEventHandler { [weak self] in
                guard let self = self else { unavailable() }
                self.machine.stop { error in
                    if error != nil { unavailable() }
                    exit(EXIT_SUCCESS)
                }
            }
            signals.append(source)
            source.resume()
        }
        machine.start { result in
            if case .failure = result { unavailable() }
        }
    }

    func guestDidStop(_ virtualMachine: VZVirtualMachine) { exit(EXIT_SUCCESS) }
    func virtualMachine(_ virtualMachine: VZVirtualMachine, didStopWithError error: Error) { unavailable() }
}

@main
struct CredentialVM {
    static func main() {
        guard CommandLine.arguments.count == 2,
              ["validate", "serve"].contains(CommandLine.arguments[1]) else { unavailable() }
        do {
            let inputs = try ProtectedInputs()
            let config = try configuration(inputs)
            if CommandLine.arguments[1] == "validate" {
                print("credential Mac VM configuration valid; host boundary remains unqualified")
                return
            }
            let driver = VMDriver(inputs: inputs, configuration: config)
            driver.run()
            withExtendedLifetime(driver) { RunLoop.main.run() }
            unavailable()
        } catch { unavailable() }
    }
}
