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
