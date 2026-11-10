import Foundation
import Security
import CryptoKit

func certificate(_ code: SecCode) -> Data? {
    var information: CFDictionary?
    var staticCode: SecStaticCode?
    guard SecCodeCopyStaticCode(code, [], &staticCode) == errSecSuccess, let staticCode,
          SecCodeCopySigningInformation(staticCode, SecCSFlags(rawValue: kSecCSSigningInformation), &information) == errSecSuccess,
          let certificates = (information as? [String: Any])?[kSecCodeInfoCertificates as String] as? [SecCertificate],
          let leaf = certificates.first else { return nil }
    return SecCertificateCopyData(leaf) as Data
}

func authorizedParent() -> Bool {
    var ownCode: SecCode?
    guard SecCodeCopySelf([], &ownCode) == errSecSuccess, let ownCode,
          let leaf = certificate(ownCode) else { return false }
    let fingerprint = Insecure.SHA1.hash(data: leaf).map { String(format: "%02x", $0) }.joined()
    var requirement: SecRequirement?
    let expression = "identifier \"com.runta.fennec\" and certificate leaf = H\"\(fingerprint)\""
    guard SecRequirementCreateWithString(expression as CFString, [], &requirement) == errSecSuccess else { return false }
    var parent: SecCode?
    let attributes = [kSecGuestAttributePid as String: NSNumber(value: getppid())]
    guard SecCodeCopyGuestWithAttributes(nil, attributes as CFDictionary, [], &parent) == errSecSuccess,
          let parent else { return false }
    var descriptor = stat()
    guard fstat(STDOUT_FILENO, &descriptor) == 0,
          descriptor.st_mode & S_IFMT == S_IFIFO else { return false }
    return SecCodeCheckValidity(parent, SecCSFlags(rawValue: kSecCSStrictValidate), requirement) == errSecSuccess
}

struct CredentialRequest: Decodable {
    let operation: String
    let token: String?
}

struct CredentialResponse: Encodable {
    let status: OSStatus
    let token: String?
}

guard authorizedParent() else { exit(77) }
let input = FileHandle.standardInput.readData(ofLength: 65537)
guard input.count <= 65536,
      let request = try? JSONDecoder().decode(CredentialRequest.self, from: input) else { exit(64) }
let query: [String: Any] = [
    kSecClass as String: kSecClassGenericPassword,
    kSecAttrService as String: "com.runta.fennec",
    kSecAttrAccount as String: "https://api.runta.com"
]
var result: OSStatus = errSecParam
var token: String?
switch request.operation {
case "read":
    var read = query
    read[kSecReturnData as String] = true
    var value: CFTypeRef?
    result = SecItemCopyMatching(read as CFDictionary, &value)
    if result == errSecSuccess {
        token = (value as? Data).flatMap { String(data: $0, encoding: .utf8) }
    }
case "write":
    if let value = request.token, !value.isEmpty {
        let data = Data(value.utf8)
        result = SecItemUpdate(query as CFDictionary, [kSecValueData as String: data] as CFDictionary)
        if result == errSecItemNotFound {
            var add = query
            add[kSecValueData as String] = data
            add[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
            result = SecItemAdd(add as CFDictionary, nil)
        }
    }
case "delete":
    result = SecItemDelete(query as CFDictionary)
default:
    break
}
let response = CredentialResponse(status: result, token: token)
guard let output = try? JSONEncoder().encode(response) else { exit(70) }
FileHandle.standardOutput.write(output)
