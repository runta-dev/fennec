import AppKit

// onMain runs f on the main thread in the common run loop modes, so it is
// served during menu tracking and modal loops too. It returns nil when the
// main thread did not get to it within timeout (the app is frozen).
final class ResultBox<T> {
    var value: T?
    let lock = NSLock()
}

func onMain<T>(_ timeout: TimeInterval, _ f: @escaping () -> T) -> T? {
    let box = ResultBox<T>()
    let sem = DispatchSemaphore(value: 0)
    CFRunLoopPerformBlock(CFRunLoopGetMain(), CFRunLoopMode.commonModes.rawValue) {
        let v = MainActor.assumeIsolated { f() }
        box.lock.lock()
        box.value = v
        box.lock.unlock()
        sem.signal()
    }
    CFRunLoopWakeUp(CFRunLoopGetMain())
    if sem.wait(timeout: .now() + timeout) == .timedOut { return nil }
    box.lock.lock()
    defer { box.lock.unlock() }
    return box.value
}

// ControlServer answers line-delimited JSON requests on a unix socket.
final class ControlServer {
    let path: String
    private var fd: Int32 = -1

    init(path: String) { self.path = path }

    func start() throws {
        var st = stat()
        if lstat(path, &st) == 0 {
            guard (st.st_mode & S_IFMT) == S_IFSOCK else {
                throw NSError(domain: "fixture", code: 1, userInfo: [NSLocalizedDescriptionKey: "\(path) exists and is not a socket"])
            }
            unlink(path)
        }
        fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw posixError("socket") }
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let bytes = Array(path.utf8)
        let cap = MemoryLayout.size(ofValue: addr.sun_path)
        guard bytes.count < cap else {
            throw NSError(domain: "fixture", code: 2, userInfo: [NSLocalizedDescriptionKey: "socket path longer than \(cap - 1) bytes"])
        }
        withUnsafeMutableBytes(of: &addr.sun_path) { raw in
            for (i, b) in bytes.enumerated() { raw[i] = b }
            raw[bytes.count] = 0
        }
        let old = umask(0o177)
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                bind(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        umask(old)
        guard rc == 0 else { throw posixError("bind") }
        chmod(path, 0o600)
        guard listen(fd, 16) == 0 else { throw posixError("listen") }
        let lfd = fd
        Thread.detachNewThread {
            while true {
                let c = accept(lfd, nil, nil)
                if c < 0 {
                    if errno == EINTR { continue }
                    return
                }
                Thread.detachNewThread { ControlServer.serve(c) }
            }
        }
    }

    func stop() {
        if fd >= 0 { close(fd) }
        unlink(path)
    }

    private func posixError(_ op: String) -> NSError {
        NSError(domain: "fixture", code: Int(errno), userInfo: [NSLocalizedDescriptionKey: "\(op): \(String(cString: strerror(errno)))"])
    }

    static func serve(_ c: Int32) {
        defer { close(c) }
        var pending = Data()
        var buf = [UInt8](repeating: 0, count: 65536)
        while true {
            let n = read(c, &buf, buf.count)
            if n <= 0 { return }
            pending.append(contentsOf: buf[0..<n])
            while let nl = pending.firstIndex(of: 0x0A) {
                let line = pending[pending.startIndex..<nl]
                pending.removeSubrange(pending.startIndex...nl)
                if line.allSatisfy({ $0 == 0x20 || $0 == 0x0D || $0 == 0x09 }) { continue }
                var out = encode(handle(Data(line)))
                out.append(0x0A)
                let ok = out.withUnsafeBytes { p -> Bool in
                    var off = 0
                    while off < p.count {
                        let w = write(c, p.baseAddress! + off, p.count - off)
                        if w <= 0 { return false }
                        off += w
                    }
                    return true
                }
                if !ok { return }
                if quitting { onQuit() }
            }
        }
    }

    static var quitting = false
    static var onQuit: () -> Void = {}

    static func encode(_ v: [String: Any]) -> Data {
        if JSONSerialization.isValidJSONObject(v), let d = try? JSONSerialization.data(withJSONObject: v, options: [.sortedKeys]) { return d }
        return Data(#"{"ok":false,"error":"state is not JSON-encodable"}"#.utf8)
    }

    static func fail(_ msg: String) -> [String: Any] { ["ok": false, "error": msg] }

    static func handle(_ line: Data) -> [String: Any] {
        guard let obj = try? JSONSerialization.jsonObject(with: line), let req = obj as? [String: Any] else {
            return fail("request is not a JSON object")
        }
        guard let op = req["op"] as? String else { return fail("missing op") }
        let timeout = ((req["timeout_ms"] as? NSNumber)?.doubleValue ?? 10000) / 1000
        switch op {
        case "scenes":
            return ["ok": true, "scenes": sceneNames]
        case "reset":
            guard let name = req["scene"] as? String else { return fail("reset needs a scene") }
            guard sceneNames.contains(name) else { return fail("unknown scene \(name)") }
            guard let err = onMain(timeout, { resetScene(name) }) else { return fail("app is busy: main thread did not answer") }
            if let err { return fail(err) }
            // Some scenes settle asynchronously (full-screen Spaces, page loads).
            let deadline = Date().addingTimeInterval(max(timeout, 20))
            while Date() < deadline {
                let ok = onMain(1) { Model.shared.ready?() ?? true } ?? false
                if ok { return ["ok": true] }
                Thread.sleep(forTimeInterval: 0.1)
            }
            return fail("scene \(name) did not become ready")
        case "state":
            guard let s = onMain(timeout, { Model.shared.snapshot() }) else {
                return ["ok": false, "error": "app is busy: main thread did not answer", "busy": true]
            }
            return ["ok": true, "state": s]
        case "freeze":
            let secs = min(max((req["seconds"] as? NSNumber)?.doubleValue ?? 5, 0), 60)
            DispatchQueue.main.async { freezeMain(secs, source: "socket") }
            return ["ok": true]
        case "quit":
            quitting = true
            onQuit = {
                _ = onMain(2) { cleanupBeforeExit() }
                exit(0)
            }
            return ["ok": true]
        default:
            return fail("unknown op \(op)")
        }
    }
}

// freezeMain blocks the main thread to simulate an unresponsive app.
func freezeMain(_ seconds: Double, source: String) {
    Model.shared.freezes.append(["seconds": seconds, "source": source])
    Thread.sleep(forTimeInterval: seconds)
}

// socketAnswers says whether something accepts connections on path.
func socketAnswers(_ path: String) -> Bool {
    let fd = socket(AF_UNIX, SOCK_STREAM, 0)
    guard fd >= 0 else { return false }
    defer { close(fd) }
    var addr = sockaddr_un()
    addr.sun_family = sa_family_t(AF_UNIX)
    let bytes = Array(path.utf8)
    guard bytes.count < MemoryLayout.size(ofValue: addr.sun_path) else { return false }
    withUnsafeMutableBytes(of: &addr.sun_path) { raw in
        for (i, b) in bytes.enumerated() { raw[i] = b }
        raw[bytes.count] = 0
    }
    let rc = withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
        }
    }
    return rc == 0
}
