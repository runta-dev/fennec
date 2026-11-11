import AppKit

import Foundation
import ServiceManagement

// Thin OS adapter called by Go. It never owns business services or credentials.
func runLoginItemAdapter(_ operation: String) -> Bool {
    var value: [String:Any] = ["enabled":false,"supported":false,"approvalRequired":false,"error":false]
    if #available(macOS 13.0, *) {
        do {
            switch operation {
	        case "mark-initialized": UserDefaults.standard.set(true,forKey:"loginItemDefaultApplied")
            case "on", "off":
                UserDefaults.standard.set(true,forKey:"loginItemDefaultApplied")
                if operation == "on" { try SMAppService.mainApp.register() }
                else { try SMAppService.mainApp.unregister() }
            case "settings": SMAppService.openSystemSettingsLoginItems()
            case "status": break
            default: value["error"] = true
            }
        } catch { value["error"] = true }
        let status = SMAppService.mainApp.status
        value["supported"] = true
        value["enabled"] = status == .enabled || status == .requiresApproval
        value["approvalRequired"] = status == .requiresApproval
	    value["initialized"] = UserDefaults.standard.bool(forKey:"loginItemDefaultApplied")
    }
    if let data = try? JSONSerialization.data(withJSONObject:value) { FileHandle.standardOutput.write(data) }
    return value["error"] as? Bool != true
}

// Installation policy and filesystem changes stay in Go. This adapter only
// presents the user's choice and opens the verified installed application.
func prepareApplicationInstallation(_ completion: @escaping (String?) -> Void) {
    var installedHint: String?
    if let index = CommandLine.arguments.firstIndex(of:"--installed-application"), index + 1 < CommandLine.arguments.count { installedHint = CommandLine.arguments[index + 1] }
    let chinese = Locale.preferredLanguages.first?.hasPrefix("zh") == true
    func text(_ zh: String, _ en: String) -> String { chinese ? zh : en }
    func run(_ arguments: [String], _ finished: @escaping ([String:Any]?) -> Void) {
        DispatchQueue.global(qos: .userInitiated).async {
            let process = Process(), pipe = Pipe()
            process.executableURL = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/fen")
            process.arguments = arguments
            process.standardInput = FileHandle.nullDevice
            process.standardOutput = pipe
            process.standardError = FileHandle.nullDevice
            var result: [String:Any]?
            do {
                try process.run()
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                process.waitUntilExit()
                if data.count <= 65536 {
                    result = try JSONSerialization.jsonObject(with: data) as? [String:Any]
                    result?["success"] = process.terminationStatus == 0
                }
            } catch {}
            let value = result
            DispatchQueue.main.async { finished(value) }
        }
    }
    func chooseFolder(_ selected: @escaping (String?) -> Void) {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true; panel.canChooseFiles = false
        panel.allowsMultipleSelection = false; panel.canCreateDirectories = true
        panel.prompt = text("安装到此处", "Install Here")
        selected(panel.runModal() == .OK ? panel.url?.appendingPathComponent("Fennec.app").path : nil)
    }
    func openInstalled(_ path: String) {
        // Existing windows close only after the verified copy is published.
        // The independent Go service and remote sessions survive UI shutdown.
        for application in NSRunningApplication.runningApplications(withBundleIdentifier:"com.runta.fennec") where application.processIdentifier != ProcessInfo.processInfo.processIdentifier {
            application.terminate()
        }
        let configuration = NSWorkspace.OpenConfiguration()
        configuration.createsNewApplicationInstance = true
        configuration.arguments = ["--installed-application",path]
        NSWorkspace.shared.openApplication(at:URL(fileURLWithPath:path),configuration:configuration) { _, error in
            DispatchQueue.main.async {
                if error == nil { NSApp.terminate(nil) }
                else {
                let alert = NSAlert()
                alert.messageText = text("Fennec 已安装", "Fennec is installed")
                alert.informativeText = text("请从 Applications 文件夹打开 Fennec。", "Open Fennec from your Applications folder.")
                alert.runModal(); NSApp.terminate(nil)
                }
            }
        }
    }
    func install(_ destination: String, replace: Bool) {
        var args = ["install", "--destination", destination]
        if replace { args.append("--replace") }
        let progress = NSPanel(contentRect:NSRect(x:0,y:0,width:320,height:110),styleMask:[.titled],backing:.buffered,defer:false)
        progress.isReleasedWhenClosed = false
        progress.title = text("安装 Fennec", "Install Fennec")
        let spinner = NSProgressIndicator(frame:NSRect(x:148,y:58,width:24,height:24))
        spinner.style = .spinning; spinner.isIndeterminate = true; spinner.startAnimation(nil)
        let label = NSTextField(labelWithString:text("正在安装 Fennec…", "Installing Fennec…"))
        label.frame = NSRect(x:20,y:22,width:280,height:22); label.alignment = .center
        progress.contentView?.addSubview(spinner); progress.contentView?.addSubview(label)
        progress.center(); progress.makeKeyAndOrderFront(nil)
        run(args) { result in
            progress.close()
            guard result?["success"] as? Bool == true, let path = result?["application"] as? String else {
                let alert = NSAlert()
                alert.messageText = text("暂时无法安装 Fennec", "Could not install Fennec")
                let unverified = result?["error"] as? String == "unverified_app"
                alert.informativeText = unverified ? text("无法验证这份安装包，请重新下载。", "This copy could not be verified. Download a fresh copy.") : text("请选择其他安装文件夹。", "Choose another installation folder.")
                alert.addButton(withTitle:unverified ? text("下载 Fennec", "Download Fennec") : text("选择文件夹…", "Choose Folder…"))
                alert.addButton(withTitle:text("退出", "Quit"))
                if alert.runModal() == .alertFirstButtonReturn {
                    if unverified {
                        if let url = URL(string:"https://github.com/runta-dev/fennec/releases/latest") { NSWorkspace.shared.open(url) }
                        NSApp.terminate(nil); return
                    }
                    chooseFolder { folder in
                        if let folder { install(folder,replace:false) } else { NSApp.terminate(nil) }
                    }
                } else { NSApp.terminate(nil) }
                return
            }
            openInstalled(path)
        }
    }
    var inspection = ["install", "--check"]
    if let installedHint { inspection += ["--installed-application",installedHint] }
    run(inspection) { state in
        guard let state, state["success"] as? Bool == true, let required = state["required"] as? Bool else {
            let alert = NSAlert()
            alert.messageText = text("无法打开这份 Fennec", "This copy of Fennec could not start")
            alert.informativeText = text("请重新下载安装包。", "Download a fresh copy of Fennec.")
            alert.addButton(withTitle:text("下载 Fennec", "Download Fennec"))
            alert.addButton(withTitle:text("退出", "Quit"))
            if alert.runModal() == .alertFirstButtonReturn, let url = URL(string:"https://github.com/runta-dev/fennec/releases/latest") { NSWorkspace.shared.open(url) }
            NSApp.terminate(nil); return
        }
        if !required {
            let application = state["application"] as? String ?? Bundle.main.bundlePath
            if application != Bundle.main.bundlePath && installedHint != application { openInstalled(application); return }
            completion(application); return
        }
        guard let destination = state["destination"] as? String else { completion(nil); return }
        let existing = state["existing"] as? Bool == true
        let alert = NSAlert()
        alert.messageText = existing ? text("替换已安装的 Fennec？", "Replace the installed Fennec?") : text("安装 Fennec？", "Install Fennec?")
        alert.informativeText = existing ? text("设置和云端会话会保留。", "Your settings and cloud sessions will be kept.") : text("安装到 Applications 后开始使用。", "Install in Applications to get started.")
        alert.addButton(withTitle:existing ? text("替换并打开", "Replace and Open") : text("安装并打开", "Install and Open"))
        alert.addButton(withTitle:text("选择文件夹…", "Choose Folder…"))
        alert.addButton(withTitle:text("退出", "Quit"))
        NSApp.activate(ignoringOtherApps:true)
        switch alert.runModal() {
        case .alertFirstButtonReturn: install(destination,replace:existing)
        case .alertSecondButtonReturn: chooseFolder { folder in if let folder { install(folder,replace:false) } else { NSApp.terminate(nil) } }
        default: NSApp.terminate(nil)
        }
    }
}
