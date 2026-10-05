import Foundation
import Security
import Darwin

private let protocolVersion = 1
private let parentRequirement = "identifier \"org.mptcp.desktop\" and certificate root = H\"d60f6edc71041789131273af4abe708db2cf0dd7\""
private let maximumRequestBytes = 1024 * 1024
private let maximumSecretBytes = 512 * 1024

private enum Slot: String, Codable {
    case remoteControl
    case userspaceTransport
    case provisioning
    case validation

    var service: String {
        switch self {
        case .remoteControl: return "MPTCPDesk.RemoteControl"
        case .userspaceTransport: return "MPTCPDesk.UserspaceTransport"
        case .provisioning: return "MPTCPDesk.Provisioning"
        case .validation: return "MPTCPDesk.KeychainBroker.Validation.v1"
        }
    }

    var account: String {
        switch self {
        case .remoteControl: return "device-credential"
        case .userspaceTransport: return "active-profile"
        case .provisioning: return "active-url"
        case .validation: return "probe"
        }
    }

    var accessibility: CFString {
        switch self {
        case .remoteControl:
            return kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        case .userspaceTransport, .provisioning, .validation:
            return kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        }
    }
}

private struct Request: Codable {
    var version: Int
    var operation: String
    var slot: Slot?
    var valueBase64: String?
}

private struct Response: Codable {
    var ok: Bool
    var valueBase64: String? = nil
    var error: String? = nil
}

private func writeResponse(_ response: Response, status: Int32 = 0) -> Never {
    if let data = try? JSONEncoder().encode(response) {
        FileHandle.standardOutput.write(data)
        FileHandle.standardOutput.write(Data([0x0A]))
    }
    exit(status)
}

private func verifyParent() -> Bool {
    let pid = getppid()
    guard pid > 1 else { return false }
    let attributes = [kSecGuestAttributePid as String: NSNumber(value: pid)] as CFDictionary
    var code: SecCode?
    guard SecCodeCopyGuestWithAttributes(nil, attributes, SecCSFlags(rawValue: 0), &code) == errSecSuccess,
          let code else { return false }
    var requirement: SecRequirement?
    guard SecRequirementCreateWithString(parentRequirement as CFString, SecCSFlags(rawValue: 0), &requirement) == errSecSuccess,
          let requirement else { return false }
    return SecCodeCheckValidity(code, SecCSFlags(rawValue: kSecCSStrictValidate), requirement) == errSecSuccess
}

private func baseQuery(_ slot: Slot) -> [String: Any] {
    [
        kSecClass as String: kSecClassGenericPassword,
        kSecAttrService as String: slot.service,
        kSecAttrAccount as String: slot.account,
    ]
}

private func load(_ slot: Slot) throws -> Data? {
    var query = baseQuery(slot)
    query[kSecReturnData as String] = true
    query[kSecMatchLimit as String] = kSecMatchLimitOne
    var result: CFTypeRef?
    let status = SecItemCopyMatching(query as CFDictionary, &result)
    if status == errSecItemNotFound { return nil }
    guard status == errSecSuccess, let data = result as? Data else {
        throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
    }
    return data
}

private func save(_ data: Data, slot: Slot) throws {
    guard data.count <= maximumSecretBytes else {
        throw NSError(domain: "MPTCPKeychainBroker", code: 1)
    }
    let query = baseQuery(slot)
    var status = SecItemUpdate(query as CFDictionary, [kSecValueData as String: data] as CFDictionary)
    if status == errSecItemNotFound {
        var item = query
        item[kSecValueData as String] = data
        item[kSecAttrAccessible as String] = slot.accessibility
        status = SecItemAdd(item as CFDictionary, nil)
    }
    guard status == errSecSuccess else {
        throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
    }
}

private func delete(_ slot: Slot) throws {
    let status = SecItemDelete(baseQuery(slot) as CFDictionary)
    guard status == errSecSuccess || status == errSecItemNotFound else {
        throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
    }
}

guard verifyParent() else {
    writeResponse(Response(ok: false, error: "unauthorized-parent"), status: 77)
}

let input = FileHandle.standardInput.readDataToEndOfFile()
guard !input.isEmpty, input.count <= maximumRequestBytes else {
    writeResponse(Response(ok: false, error: "invalid-request"), status: 64)
}

do {
    let request = try JSONDecoder().decode(Request.self, from: input)
    guard request.version == protocolVersion else {
        writeResponse(Response(ok: false, error: "unsupported-version"), status: 65)
    }
    if request.operation == "ping" {
        writeResponse(Response(ok: true))
    }
    guard let slot = request.slot else {
        writeResponse(Response(ok: false, error: "missing-slot"), status: 64)
    }
    switch request.operation {
    case "get":
        let data = try load(slot)
        writeResponse(Response(ok: true, valueBase64: data?.base64EncodedString()))
    case "set":
        guard let encoded = request.valueBase64,
              let data = Data(base64Encoded: encoded),
              data.count <= maximumSecretBytes else {
            writeResponse(Response(ok: false, error: "invalid-value"), status: 64)
        }
        try save(data, slot: slot)
        writeResponse(Response(ok: true))
    case "delete":
        try delete(slot)
        writeResponse(Response(ok: true))
    default:
        writeResponse(Response(ok: false, error: "unsupported-operation"), status: 64)
    }
} catch let error as NSError {
    let code = error.domain == NSOSStatusErrorDomain ? "keychain-\(error.code)" : "operation-failed"
    writeResponse(Response(ok: false, error: code), status: 1)
} catch {
    writeResponse(Response(ok: false, error: "operation-failed"), status: 1)
}
