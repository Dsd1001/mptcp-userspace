import Foundation
import CryptoKit
import Security

internal enum KeychainBrokerSlot: String, Codable {
    case remoteControl
    case userspaceTransport
    case provisioning
}

private struct KeychainBrokerRequest: Codable {
    var version: Int = 1
    var operation: String
    var slot: KeychainBrokerSlot? = nil
    var valueBase64: String? = nil
}

private struct KeychainBrokerResponse: Codable {
    var ok: Bool
    var valueBase64: String?
    var error: String?
}

internal enum KeychainBrokerClient {
    static let protocolVersion = 1
    static let expectedBrokerSHA256 = "5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9"
    static let brokerRequirement = "identifier \"org.mptcp.desktop.keychainbroker.v1\" and certificate root = H\"d60f6edc71041789131273af4abe708db2cf0dd7\""
    static let resourceName = "MPTCPKeychainBroker.v1"
    static let resourceExtension = "b64"
    private static let installLock = NSLock()

    private static func sha256(_ data: Data) -> String {
        SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
    }

    private static func verifyBrokerSignature(at url: URL) throws {
        var code: SecStaticCode?
        guard SecStaticCodeCreateWithPath(url as CFURL, SecCSFlags(rawValue: 0), &code) == errSecSuccess,
              let code else {
            throw ProfileError("Keychain Broker 代码签名无法读取")
        }
        var requirement: SecRequirement?
        guard SecRequirementCreateWithString(brokerRequirement as CFString, SecCSFlags(rawValue: 0), &requirement) == errSecSuccess,
              let requirement else {
            throw ProfileError("Keychain Broker 签名要求无效")
        }
        let status = SecStaticCodeCheckValidity(code, SecCSFlags(rawValue: kSecCSStrictValidate), requirement)
        guard status == errSecSuccess else {
            throw ProfileError("Keychain Broker 签名校验失败（\(status)）")
        }
    }

    private static func brokerDirectory() throws -> URL {
        guard let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first else {
            throw ProfileError("无法定位 Application Support 目录")
        }
        return base
            .appendingPathComponent("MPTCP Desk", isDirectory: true)
            .appendingPathComponent("KeychainBroker", isDirectory: true)
            .appendingPathComponent("v1", isDirectory: true)
    }

    private static func embeddedBrokerData() throws -> Data {
        guard let url = Bundle.main.url(forResource: resourceName, withExtension: resourceExtension) else {
            throw ProfileError("安装包缺少 Keychain Broker 资源")
        }
        let encoded = try Data(contentsOf: url)
        guard let text = String(data: encoded, encoding: .utf8),
              let data = Data(base64Encoded: text, options: .ignoreUnknownCharacters),
              sha256(data) == expectedBrokerSHA256 else {
            throw ProfileError("Keychain Broker 资源完整性校验失败")
        }
        return data
    }

    static func installedBrokerURL() throws -> URL {
        installLock.lock()
        defer { installLock.unlock() }

        let fileManager = FileManager.default
        let directory = try brokerDirectory()
        let installed = directory.appendingPathComponent("MPTCPKeychainBroker", isDirectory: false)
        let embedded = try embeddedBrokerData()

        if fileManager.fileExists(atPath: installed.path) {
            let existing = try Data(contentsOf: installed, options: [.mappedIfSafe])
            guard sha256(existing) == expectedBrokerSHA256 else {
                throw ProfileError("已安装的 Keychain Broker 与固定版本不一致；不会自动覆盖")
            }
            try verifyBrokerSignature(at: installed)
            return installed
        }

        try fileManager.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        try fileManager.setAttributes([.posixPermissions: 0o700], ofItemAtPath: directory.path)
        let temporary = directory.appendingPathComponent(".MPTCPKeychainBroker." + UUID().uuidString)
        defer { try? fileManager.removeItem(at: temporary) }
        try embedded.write(to: temporary, options: [.atomic])
        try fileManager.setAttributes([.posixPermissions: 0o700], ofItemAtPath: temporary.path)
        let written = try Data(contentsOf: temporary, options: [.mappedIfSafe])
        guard sha256(written) == expectedBrokerSHA256 else {
            throw ProfileError("Keychain Broker 安装写入校验失败")
        }
        try verifyBrokerSignature(at: temporary)
        try fileManager.moveItem(at: temporary, to: installed)
        try fileManager.setAttributes([.posixPermissions: 0o700], ofItemAtPath: installed.path)
        return installed
    }

    private static func invoke(_ request: KeychainBrokerRequest) throws -> KeychainBrokerResponse {
        let executable = try installedBrokerURL()
        let input = Pipe()
        let output = Pipe()
        let process = Process()
        process.executableURL = executable
        process.standardInput = input
        process.standardOutput = output
        process.standardError = FileHandle.nullDevice
        let payload = try JSONEncoder().encode(request)
        try process.run()
        input.fileHandleForWriting.write(payload)
        try input.fileHandleForWriting.close()
        process.waitUntilExit()
        let responseData = output.fileHandleForReading.readDataToEndOfFile()
        guard !responseData.isEmpty,
              let response = try? JSONDecoder().decode(KeychainBrokerResponse.self, from: responseData) else {
            throw ProfileError("Keychain Broker 无有效响应（exit \(process.terminationStatus)）")
        }
        guard response.ok else {
            throw ProfileError("Keychain Broker 操作失败（\(response.error ?? "unknown")）")
        }
        return response
    }

    static func ping() throws {
        _ = try invoke(KeychainBrokerRequest(operation: "ping"))
    }

    static func load(_ slot: KeychainBrokerSlot) throws -> Data? {
        let response = try invoke(KeychainBrokerRequest(operation: "get", slot: slot))
        guard let encoded = response.valueBase64 else { return nil }
        guard let data = Data(base64Encoded: encoded) else {
            throw ProfileError("Keychain Broker 返回了无效数据")
        }
        return data
    }

    static func save(_ data: Data, slot: KeychainBrokerSlot) throws {
        _ = try invoke(KeychainBrokerRequest(operation: "set", slot: slot, valueBase64: data.base64EncodedString()))
    }

    static func delete(_ slot: KeychainBrokerSlot) throws {
        _ = try invoke(KeychainBrokerRequest(operation: "delete", slot: slot))
    }


}
