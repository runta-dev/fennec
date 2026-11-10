import Foundation
import Darwin

// Native transport and presentation adapter only. Business state and secrets
// remain in the Go daemon, independently of this process's lifetime.
final class ControlClient {
    private let queue = DispatchQueue(label: "com.runta.fennec.control", attributes: .concurrent)
    private let socketPath = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".config/fennec/ipc/control.sock").path
    private func failure(_ text: String) -> NSError { NSError(domain: "FennecControl", code: 1, userInfo: [NSLocalizedDescriptionKey: text]) }

    func start(_ completion: @escaping (Bool) -> Void) {
        queue.async {
            let process = Process()
            process.executableURL = Bundle.main.executableURL?.deletingLastPathComponent().appendingPathComponent("fennec-daemon")
            process.arguments = ["fen", "start"]
            process.standardInput = FileHandle.nullDevice
            process.standardOutput = FileHandle.nullDevice
            process.standardError = FileHandle.nullDevice
            var success = false
            do { try process.run(); process.waitUntilExit(); success = process.terminationStatus == 0 } catch {}
            DispatchQueue.main.async { completion(success) }
        }
    }

    func call(_ method: String, _ params: [String: Any] = [:], completion: @escaping (Result<Any, Error>) -> Void) {
        queue.async {
            let result: Result<Any, Error>
            do { result = .success(try self.request(method, params)) }
            catch { result = .failure(error) }
            DispatchQueue.main.async { completion(result) }
        }
    }

    private func request(_ method: String, _ params: [String: Any]) throws -> Any {
        let fd = Darwin.socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw failure("Unable to open daemon connection") }
        defer { Darwin.close(fd) }
        var noSignal: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSignal, socklen_t(MemoryLayout<Int32>.size))
        var timeout = timeval(tv_sec: 30, tv_usec: 0)
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, socklen_t(MemoryLayout<timeval>.size))
        setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &timeout, socklen_t(MemoryLayout<timeval>.size))
        var address = sockaddr_un()
        address.sun_family = sa_family_t(AF_UNIX)
        address.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
        let path = Array(socketPath.utf8CString)
        guard path.count <= MemoryLayout.size(ofValue: address.sun_path) else { throw failure("Daemon socket path is too long") }
        withUnsafeMutableBytes(of: &address.sun_path) { target in
            path.withUnsafeBytes { source in target.copyBytes(from: source) }
        }
        let connected = withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        guard connected == 0 else { throw failure("Fennec daemon is unavailable") }
        let identity = UUID().uuidString
        var payload = try JSONSerialization.data(withJSONObject: ["version": 1, "id": identity, "method": method, "params": params])
        payload.append(10)
        try payload.withUnsafeBytes { buffer in
            var offset = 0
            while offset < buffer.count {
                let written = Darwin.send(fd, buffer.baseAddress!.advanced(by: offset), buffer.count - offset, 0)
                if written < 0 && errno == EINTR { continue }
                guard written > 0 else { throw failure("Request delivery is uncertain; inspect state before retrying") }
                offset += written
            }
        }
        var response = Data(), buffer = [UInt8](repeating: 0, count: 16384)
        while response.count <= 8 * 1024 * 1024 {
            let count = Darwin.recv(fd, &buffer, buffer.count, 0)
            if count < 0 && errno == EINTR { continue }
            guard count > 0 else { throw failure("Daemon response unavailable; inspect state before retrying") }
            response.append(contentsOf: buffer.prefix(count))
            if response.contains(10) { break }
        }
        guard response.count <= 8 * 1024 * 1024,
              let object = try JSONSerialization.jsonObject(with: response) as? [String: Any],
              object["version"] as? Int == 1, object["id"] as? String == identity else { throw failure("Invalid daemon response") }
        if let error = object["error"] as? [String: Any] { throw failure(error["message"] as? String ?? "Daemon operation failed") }
        return object["result"] ?? NSNull()
    }
}
