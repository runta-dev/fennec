import Darwin
import Foundation
import SystemConfiguration

// Computers › This Mac: one sample of the Mac's CPU, memory and disk for the panel.
// CPU usage is the share of busy ticks since the previous sample, so the first
// sample after launch reports none.
final class HostMetrics {
    private var previousTicks: (busy: UInt64, total: UInt64)?
    private lazy var fixed: [String: Any] = {
        let version = ProcessInfo.processInfo.operatingSystemVersion
        var osVersion = "\(version.majorVersion).\(version.minorVersion)"
        if version.patchVersion > 0 { osVersion += ".\(version.patchVersion)" }
        var info: [String: Any] = [
            "os_name": "macOS",
            "os_version": osVersion,
            "cpu_count": ProcessInfo.processInfo.activeProcessorCount,
            "memory_total_bytes": ProcessInfo.processInfo.physicalMemory,
        ]
        if let name = SCDynamicStoreCopyComputerName(nil, nil) as String?, !name.isEmpty { info["name"] = name }
        if let chip = HostMetrics.sysctlString("machdep.cpu.brand_string"), !chip.isEmpty { info["chip"] = chip }
        if let model = HostMetrics.sysctlString("hw.model"), !model.isEmpty { info["model"] = model }
        return info
    }()

    func sample() -> [String: Any] {
        var payload = fixed
        if let cpu = cpuPercent() { payload["cpu_percent"] = cpu }
        if let used = memoryUsedBytes() { payload["memory_used_bytes"] = used }
        let keys: Set<URLResourceKey> = [.volumeTotalCapacityKey, .volumeAvailableCapacityForImportantUsageKey]
        if let values = try? URL(fileURLWithPath: "/").resourceValues(forKeys: keys),
           let total = values.volumeTotalCapacity, total > 0,
           let available = values.volumeAvailableCapacityForImportantUsage {
            payload["disk_total_bytes"] = total
            payload["disk_used_bytes"] = max(0, Int64(total) - available)
        }
        return payload
    }

    private func cpuPercent() -> Double? {
        var load = host_cpu_load_info()
        var count = mach_msg_type_number_t(MemoryLayout<host_cpu_load_info>.size / MemoryLayout<integer_t>.size)
        let result = withUnsafeMutablePointer(to: &load) {
            $0.withMemoryRebound(to: integer_t.self, capacity: Int(count)) {
                host_statistics(mach_host_self(), HOST_CPU_LOAD_INFO, $0, &count)
            }
        }
        guard result == KERN_SUCCESS else { return nil }
        let user = UInt64(load.cpu_ticks.0), system = UInt64(load.cpu_ticks.1)
        let idle = UInt64(load.cpu_ticks.2), nice = UInt64(load.cpu_ticks.3)
        let busy = user + system + nice
        let total = busy + idle
        defer { previousTicks = (busy, total) }
        guard let previous = previousTicks, total > previous.total else { return nil }
        return Double(busy &- previous.busy) / Double(total - previous.total) * 100
    }

    // Activity Monitor's "Memory Used": app memory + wired + compressed.
    private func memoryUsedBytes() -> UInt64? {
        var stats = vm_statistics64()
        var count = mach_msg_type_number_t(MemoryLayout<vm_statistics64>.size / MemoryLayout<integer_t>.size)
        let result = withUnsafeMutablePointer(to: &stats) {
            $0.withMemoryRebound(to: integer_t.self, capacity: Int(count)) {
                host_statistics64(mach_host_self(), HOST_VM_INFO64, $0, &count)
            }
        }
        guard result == KERN_SUCCESS else { return nil }
        let page = UInt64(vm_kernel_page_size)
        let anonymous = UInt64(stats.internal_page_count), purgeable = UInt64(stats.purgeable_count)
        let app = anonymous > purgeable ? anonymous - purgeable : 0
        return (app + UInt64(stats.wire_count) + UInt64(stats.compressor_page_count)) * page
    }

    private static func sysctlString(_ name: String) -> String? {
        var size = 0
        guard sysctlbyname(name, nil, &size, nil, 0) == 0, size > 0 else { return nil }
        var buffer = [CChar](repeating: 0, count: size)
        guard sysctlbyname(name, &buffer, &size, nil, 0) == 0 else { return nil }
        return String(cString: buffer).trimmingCharacters(in: .whitespaces)
    }
}
