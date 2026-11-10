import Foundation
import Darwin

// A local terminal transport, not a remote session supervisor.
final class TerminalPTY {
    private let process = Process()
    private var master: FileHandle?
    private var slave: FileHandle?

    func start(executable: URL, arguments: [String], cols: UInt16, rows: UInt16,
               output: @escaping (Data) -> Void, exited: @escaping (Int32) -> Void) throws {
        guard master == nil, cols > 0, rows > 0 else {
            throw NSError(domain: "Fennec.Terminal", code: 1)
        }
        var masterFD: Int32 = -1
        var slaveFD: Int32 = -1
        var size = winsize(ws_row: rows, ws_col: cols, ws_xpixel: 0, ws_ypixel: 0)
        guard openpty(&masterFD, &slaveFD, nil, nil, &size) == 0 else {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
        let reader = FileHandle(fileDescriptor: masterFD, closeOnDealloc: true)
        let terminal = FileHandle(fileDescriptor: slaveFD, closeOnDealloc: true)
        master = reader; slave = terminal
        process.executableURL = executable
        process.arguments = arguments
        var env = ProcessInfo.processInfo.environment
        env["TERM"] = "xterm-256color"
        process.environment = env
        process.standardInput = terminal
        process.standardOutput = terminal
        process.standardError = terminal
        reader.readabilityHandler = { handle in
            let data = handle.availableData
            if data.isEmpty { handle.readabilityHandler = nil; return }
            output(data)
        }
        process.terminationHandler = { child in exited(child.terminationStatus) }
        do {
            try process.run()
            // Only the child retains the slave, allowing EOF after disconnect.
            try terminal.close(); slave = nil
        } catch {
            close()
            throw error
        }
    }

    func write(_ data: Data) throws {
        guard let master = master else { throw POSIXError(.ENOTCONN) }
        try master.write(contentsOf: data)
    }

    func resize(cols: UInt16, rows: UInt16) throws {
        guard let master = master, cols > 0, rows > 0 else { throw POSIXError(.EINVAL) }
        var size = winsize(ws_row: rows, ws_col: cols, ws_xpixel: 0, ws_ypixel: 0)
        guard ioctl(master.fileDescriptor, TIOCSWINSZ, &size) == 0 else {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
        if process.isRunning { Darwin.kill(process.processIdentifier, SIGWINCH) }
    }

    func close() {
        master?.readabilityHandler = nil
        if process.isRunning { process.terminate() }
        try? master?.close(); master = nil
        try? slave?.close(); slave = nil
    }

    deinit { close() }
}

// Credits count bytes not yet acknowledged by the terminal renderer.
final class TerminalOutputWindow {
    private let condition = NSCondition()
    private var outstanding = 0
    private var closed = false
    let capacity = 256 * 1024

    func reserve(_ count: Int) -> Bool {
        guard count > 0, count <= capacity else { return false }
        condition.lock(); defer { condition.unlock() }
        while !closed && outstanding + count > capacity { condition.wait() }
        guard !closed else { return false }
        outstanding += count
        return true
    }
    func acknowledge(_ count: Int) {
        condition.lock(); defer { condition.unlock() }
        guard count > 0, count <= outstanding else { return }
        outstanding -= count
        condition.broadcast()
    }
    func close() {
        condition.lock(); closed = true; condition.broadcast(); condition.unlock()
    }
}
