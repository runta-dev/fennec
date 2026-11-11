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
    func text(_ key: String) -> String { L10n.text(key, language: Locale.preferredLanguages.first ?? "en-US") }
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
        panel.prompt = text("installation.install_here")
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
                alert.messageText = text("installation.fennec_is_installed")
                alert.informativeText = text("installation.open_fennec_from_your_applications_folder")
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
        progress.title = text("installation.install_fennec")
        let spinner = NSProgressIndicator(frame:NSRect(x:148,y:58,width:24,height:24))
        spinner.style = .spinning; spinner.isIndeterminate = true; spinner.startAnimation(nil)
        let label = NSTextField(labelWithString:text("installation.installing_fennec"))
        label.frame = NSRect(x:20,y:22,width:280,height:22); label.alignment = .center
        progress.contentView?.addSubview(spinner); progress.contentView?.addSubview(label)
        progress.center(); progress.makeKeyAndOrderFront(nil)
        run(args) { result in
            progress.close()
            guard result?["success"] as? Bool == true, let path = result?["application"] as? String else {
                let alert = NSAlert()
                alert.messageText = text("installation.could_not_install_fennec")
                let unverified = result?["error"] as? String == "unverified_app"
                alert.informativeText = unverified ? text("installation.this_copy_could_not_be_verified_download_a_fresh_copy") : text("installation.choose_another_installation_folder")
                alert.addButton(withTitle:unverified ? text("installation.download_fennec") : text("installation.choose_folder"))
                alert.addButton(withTitle:text("installation.quit"))
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
            alert.messageText = text("installation.this_copy_of_fennec_could_not_start")
            alert.informativeText = text("installation.download_a_fresh_copy_of_fennec")
            alert.addButton(withTitle:text("installation.download_fennec"))
            alert.addButton(withTitle:text("installation.quit"))
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
        alert.messageText = existing ? text("installation.replace_the_installed_fennec") : text("installation.install_fennec")
        alert.informativeText = existing ? text("installation.your_settings_and_cloud_sessions_will_be_kept") : text("installation.install_in_applications_to_get_started")
        alert.addButton(withTitle:existing ? text("installation.replace_and_open") : text("installation.install_and_open"))
        alert.addButton(withTitle:text("installation.choose_folder"))
        alert.addButton(withTitle:text("installation.quit"))
        NSApp.activate(ignoringOtherApps:true)
        switch alert.runModal() {
        case .alertFirstButtonReturn: install(destination,replace:existing)
        case .alertSecondButtonReturn: chooseFolder { folder in if let folder { install(folder,replace:false) } else { NSApp.terminate(nil) } }
        default: NSApp.terminate(nil)
        }
    }
}
