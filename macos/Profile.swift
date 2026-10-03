import Foundation
import Security

struct ProfileError: LocalizedError {
    var text: String
    init(_ text: String) { self.text = text }
    var errorDescription: String? { text }
}

struct RelayRow: Codable, Identifiable {
    var id: UUID = UUID()
    var host: String
    var port: Int
    var download_mbps: Double? = nil
    var upload_mbps: Double? = nil
    enum CodingKeys: String, CodingKey { case host, port, download_mbps, upload_mbps }
}

enum SchedulerPolicy: String, CaseIterable, Identifiable {
    case auto, aggregate, protect, weighted
    var id: String { rawValue }
    var title: String {
        switch self { case .auto: return "Auto"; case .aggregate: return "Aggregate"; case .protect: return "Protect"; case .weighted: return "Weighted" }
    }
    var explanation: String {
        switch self {
        case .auto: return "自动判断线路质量，在聚合与保护策略间切换。"
        case .aggregate: return "适合质量接近的线路，优先聚合吞吐。"
        case .protect: return "隔离明显慢路，优先有序吞吐与稳定性。"
        case .weighted: return "按每条 Relay 配置的带宽能力分配；故障、RTT 和队列保护仍实时生效。"
        }
    }
}

struct Profile: Codable {
    var schema_version: Int
    var mode: String
    var listen_port: Int
    var relays: [RelayRow]
    var udp_enabled: Bool?
    var tcp_enabled: Bool?
    var transport_key: String?
    var scheduler_mode: String? = nil
    var schedulerMode: String { scheduler_mode ?? SchedulerPolicy.auto.rawValue }
    var userspace: Bool { schema_version == 3 && mode == "userspace_multipath" }
    func validate() throws {
        let legacy = schema_version == 2 && mode == "tcp_forward"
        guard legacy || (schema_version == 3 && ["userspace_multipath", "native_mptcp"].contains(mode)) else {
            throw ProfileError("需要 schema 2 Native 配置或 schema 3 双模式配置，不支持旧 SOCKS5 配置")
        }
        guard (1024...65535).contains(listen_port), (2...8).contains(relays.count) else {
            throw ProfileError("检查本地端口和 Relay 数量（2–8 条）")
        }
        if userspace {
            guard SchedulerPolicy(rawValue:schedulerMode) != nil else { throw ProfileError("调度策略仅支持 auto、aggregate、protect、weighted") }
            let key = transport_key ?? ""
            guard key.utf8.count == 64, key.utf8.allSatisfy({ (48...57).contains($0) || (65...70).contains($0) || (97...102).contains($0) }) else {
                throw ProfileError("请输入 Landing 生成的 64 位十六进制传输密钥，不是 SS 密码")
            }
        }
        guard (tcp_enabled ?? true) || (udp_enabled ?? false) else { throw ProfileError("TCP 和 UDP 不能同时关闭") }
        guard userspace || (tcp_enabled ?? true) else { throw ProfileError("Native 兼容模式需保留 TCP") }
        var identities = Set<String>()
        for (index, relay) in relays.enumerated() {
            let parts = relay.host.split(separator: ".", omittingEmptySubsequences: false)
            let identity = userspace ? "\(relay.host):\(relay.port)" : relay.host
            guard parts.count == 4,
                  parts.allSatisfy({UInt8($0) != nil && ($0 == "0" || !$0.hasPrefix("0"))}),
                  let first = parts.first.flatMap({UInt8($0)}), first < 224,
                  relay.host != "0.0.0.0", (1...65535).contains(relay.port), identities.insert(identity).inserted else {
                throw ProfileError("Relay 需要有效且不重复的单播 IPv4:端口；Native 模式还要求 IP 不重复")
            }
            guard !(relay.host.hasPrefix("127.") && relay.port == listen_port) else { throw ProfileError("Relay 不能指向本地转发入口") }
            if let down = relay.download_mbps {
                guard Self.validWeightedMbps(down) else { throw ProfileError("Relay \(index + 1) 下行带宽需为 0.1–6553.5 Mbps，最多 1 位小数") }
            }
            if let up = relay.upload_mbps {
                guard Self.validWeightedMbps(up) else { throw ProfileError("Relay \(index + 1) 上行带宽需为 0.1–6553.5 Mbps，最多 1 位小数") }
            }
            if userspace && schedulerMode == SchedulerPolicy.weighted.rawValue && relay.download_mbps == nil {
                throw ProfileError("Weighted 模式下 Relay \(index + 1) 的下行 Mbps 必填；上行留空时自动估算")
            }
        }
    }
    static func validWeightedMbps(_ value: Double) -> Bool {
        guard value.isFinite, value >= 0.1, value <= 6553.5 else { return false }
        return abs((value * 10).rounded() - value * 10) < 0.000001
    }
}

extension Profile {
    // The same key-free representation is used by save and the preference tests.
    func preferenceData() throws -> Data {
        var ordinary = self
        ordinary.transport_key = nil
        return try JSONEncoder().encode(ordinary)
    }
}

struct PathMetric: Decodable, Identifiable {
    var id: Int
    var address: String
    var connected: Bool
    var sent: UInt64
    var received: UInt64
    var rtt_ms: Double
    var goodput_bps: Double
    var configured_rate_bps: Double? = nil
    var outstanding_bytes: Int
    var queue_bytes: Int
    var errors: UInt64
    var last_error: String?
    var role: String? = nil
    var role_reason: String? = nil
}
struct ResourceMetric: Decodable {
    var capability_revision: Int?
    var credit_accounting: String?
    var session_tx_unconsumed_bytes: Int?
    var session_tx_growth_bytes: Int?
    var closing_streams: Int?
    var occupied_stream_slots: Int?
    var receive_page_count: Int?
    var idle_actual_data_bytes: Int?
    var stream_window_entitlement_bytes: UInt64?
    var bootstrap_credit_bytes: Int?
    var bootstrap_credit_limit_bytes: Int?
    var growth_credit_bytes: Int?
    var growth_credit_limit_bytes: Int?
    var data_pending_frames: Int?
    var data_pending_frame_limit: Int?
    var data_pending_bytes: Int?
    var control_pending_frames: Int?
    var control_pending_frame_limit: Int?
    var control_pending_bytes: Int?
    var window_blocked_writers: Int?
    var open_receive_credit_waits: UInt64?
    var opened_streams: UInt64?
    var closed_streams: UInt64?
    var local_connections: Int?
    var lifecycle_opening: Int?
    var lifecycle_open_bidirectional: Int?
    var lifecycle_half_closed: Int?
    var lifecycle_wait_local_final_ack: Int?
    var lifecycle_wait_peer_final: Int?
    var lifecycle_both_final_wait_close: Int?
    var lifecycle_wait_final_consumed: Int?
    var lifecycle_closing_other: Int?
    var data_idle_over_30s: Int?
    var data_idle_over_1m: Int?
    var data_idle_over_5m: Int?
    var data_idle_over_10m: Int?
    var oldest_stream_age_seconds: Int?
    var oldest_data_idle_seconds: Int?
    var idle_streams: Int?
    var small_streams: Int?
    var bulk_streams: Int?
    var idle_irrevocable_growth_bytes: Int?

    var active_streams: Int
    var stream_limit: Int
    var pending_frames: Int
    var pending_frame_limit: Int
    var pending_bytes: Int
    var pending_byte_limit: Int
    var receive_credit_bytes: Int
    var receive_credit_limit_bytes: Int
    var receive_allocated_bytes: Int
    var receive_allocated_limit_bytes: Int
    var admission_reserve_bytes: Int
    var waiting_opens: Int
    var waits: [String: UInt64]
    var rejections: [String: UInt64]
    var first_limit_at: String?
    var last_limit_at: String?
    var last_reason: String?
}
struct TransportEventMetric: Decodable {
    var sequence: UInt64
    var at: String
    var kind: String
    var reason: String?
    var path: Int?
    var stream: UInt64?
}
struct LifecycleMetric: Decodable {
    var session_tag: String
    var created_at: String
    var closed: Bool
    var closed_at: String?
    var close_reason: String?
    var event_sequence: UInt64
    var events: [TransportEventMetric]?
}
struct EngineEvent: Decodable {
    var profile_id: String?
    var profile_name: String?
    var bundle_id: String?
    var bundle_name: String?
    var listen_port: Int?
    var configured_scheduler_mode: String?
    var effective_scheduler_mode: String?
    var mode_switches: UInt64?
    var last_mode_reason: String?
    var kind: String
    var message: String?
    var paths: Int?
    var connections: Int?
    var sent: Int64?
    var received: Int64?
    var mode: String?
    var path_stats: [PathMetric]?
    var reorder_bytes: Int?
    var reorder_peak: Int?
    var pending_bytes: Int?
    var retransmits: UInt64?
    var dropped: UInt64?
    var resource_reason: String?
    var resources: ResourceMetric?
    var lifecycle: LifecycleMetric?
}

// Only the transport secret goes in Keychain. Ordinary Relay/UI preferences
// remain in UserDefaults without the key. No silent plaintext fallback exists.
enum TransportKeyStore {
    private static let query: [String: Any] = [
        kSecClass as String: kSecClassGenericPassword,
        kSecAttrService as String: "MPTCPDesk.UserspaceTransport",
        kSecAttrAccount as String: "active-profile"
    ]
    static func save(_ value: String) throws {
        let data = Data(value.utf8)
        var status = SecItemUpdate(query as CFDictionary, [kSecValueData as String: data] as CFDictionary)
        if status == errSecItemNotFound {
            var item = query
            item[kSecValueData as String] = data
            item[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
            status = SecItemAdd(item as CFDictionary, nil)
        }
        guard status == errSecSuccess else { throw ProfileError("钥匙串保存失败（\(status)）；密钥没有写入普通偏好设置") }
    }
    static func load() throws -> String? {
        var item = query
        item[kSecReturnData as String] = true
        item[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(item as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = result as? Data, let value = String(data: data, encoding: .utf8) else {
            throw ProfileError("钥匙串读取失败（\(status)）；请解锁钥匙串或重新输入 Landing 密钥")
        }
        return value
    }
}

// Versioned full-client provisioning response. The provisioning URL and
// transport key are credentials; neither is stored in UserDefaults or logs.
struct RelayProvisioningPayload: Codable {
    var schema_version: Int
    var profile_id: String? = nil
    var revision: String?
    var display_name: String?
    var mode: String
    var listen_port: Int
    var scheduler_mode: String?
    var tcp_enabled: Bool
    var udp_enabled: Bool
    var background_resident: Bool?
    var transport_key: String?
    var relays: [RelayRow]

    func validatedProfile() throws -> Profile {
        guard schema_version == 1 else { throw ProfileError("Provisioning API schema_version 仅支持 1") }
        if let revision, revision.utf8.count > 128 { throw ProfileError("Provisioning revision 过长") }
        if let display_name, display_name.utf8.count > 128 { throw ProfileError("Provisioning display_name 过长") }
        guard ["userspace_multipath", "native_mptcp"].contains(mode) else { throw ProfileError("Provisioning mode 仅支持 userspace_multipath 或 native_mptcp") }
        let p = Profile(
            schema_version: 3,
            mode: mode,
            listen_port: listen_port,
            relays: relays,
            udp_enabled: udp_enabled,
            tcp_enabled: tcp_enabled,
            transport_key: mode == "userspace_multipath" ? transport_key : nil,
            scheduler_mode: mode == "userspace_multipath" ? (scheduler_mode ?? SchedulerPolicy.auto.rawValue) : nil
        )
        try p.validate()
        return p
    }
}


struct RelayProvisioningBundlePayload: Codable {
    var schema_version: Int
    var kind: String
    var bundle_id: String
    var revision: String
    var display_name: String
    var mode: String
    var profiles: [RelayProvisioningPayload]

    func validate() throws {
        guard schema_version == 2, kind == "bundle" else { throw ProfileError("Provisioning Bundle 仅支持 schema_version=2 / kind=bundle") }
        guard !bundle_id.isEmpty, bundle_id.utf8.count <= 128, revision.utf8.count <= 128, display_name.utf8.count <= 128 else { throw ProfileError("Provisioning Bundle 标识、名称或 Revision 无效") }
        guard mode == "single_select" || mode == "parallel" else { throw ProfileError("Bundle mode 仅支持 single_select 或 parallel") }
        guard (1...32).contains(profiles.count) else { throw ProfileError("Bundle 需要包含 1–32 个 Profile") }
        var ids = Set<String>()
        var ports: [Int:String] = [:]
        for payload in profiles {
            guard payload.schema_version == 1, let id = payload.profile_id, !id.isEmpty, id.utf8.count <= 128, ids.insert(id).inserted else { throw ProfileError("Bundle Profile ID 必须非空且唯一") }
            _ = try payload.validatedProfile()
            if mode == "parallel" {
                if let other = ports[payload.listen_port] { throw ProfileError("并行 Bundle 端口冲突：\(other) 与 \(payload.display_name ?? id) 都使用 \(payload.listen_port)") }
                ports[payload.listen_port] = payload.display_name ?? id
            }
        }
    }

    func selectedProfiles(ids: Set<String>) throws -> [RelayProvisioningPayload] {
        try validate()
        var wanted = ids
        if wanted.isEmpty {
            if mode == "single_select", let first = profiles.first?.profile_id { wanted = [first] }
            else { wanted = Set(profiles.compactMap(\.profile_id)) }
        }
        if mode == "single_select" && wanted.count != 1 { throw ProfileError("单配置选择模式必须且只能选择 1 个 Profile") }
        guard !wanted.isEmpty else { throw ProfileError("至少选择 1 个 Profile") }
        let available = Set(profiles.compactMap(\.profile_id))
        guard wanted.isSubset(of: available) else { throw ProfileError("本机保存的 Profile 选择已不在当前 Bundle 中，请重新选择") }
        var ports: [Int:String] = [:]
        let result = profiles.filter { payload in payload.profile_id.map(wanted.contains) ?? false }
        for payload in result {
            let title = payload.display_name ?? payload.profile_id ?? "Profile"
            if let other = ports[payload.listen_port] { throw ProfileError("所选 Profile 端口冲突：\(other) 与 \(title) 都使用 \(payload.listen_port)") }
            ports[payload.listen_port] = title
        }
        return result
    }
}

enum RelayProvisioningDocument {
    case profile(RelayProvisioningPayload)
    case bundle(RelayProvisioningBundlePayload)
}

struct ProvisioningProfileChoice: Identifiable {
    var id: String
    var name: String
    var listenPort: Int
    var relayCount: Int
    var mode: String
    var backgroundResident: Bool
}

private final class RelayProvisioningNoRedirectDelegate: NSObject, URLSessionTaskDelegate {
    func urlSession(
        _ session: URLSession,
        task: URLSessionTask,
        willPerformHTTPRedirection response: HTTPURLResponse,
        newRequest request: URLRequest,
        completionHandler: @escaping (URLRequest?) -> Void
    ) { completionHandler(nil) }
}

enum RelayProvisioningClient {
    static let maximumProfileResponseBytes = 64 * 1024
    static let maximumResponseBytes = 512 * 1024

    static func endpointURL(_ raw: String) throws -> URL {
        let value = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !value.isEmpty, var parts = URLComponents(string: value), let scheme = parts.scheme?.lowercased(), let host = parts.host?.lowercased() else {
            throw ProfileError("请输入有效的 Provisioning API URL")
        }
        guard parts.user == nil, parts.password == nil, parts.fragment == nil else {
            throw ProfileError("Provisioning URL 不能包含账号密码或 fragment")
        }
        let loopback = host == "localhost" || host == "127.0.0.1" || host == "::1"
        guard scheme == "https" || (scheme == "http" && loopback) else {
            throw ProfileError("Provisioning API 必须使用 HTTPS；仅 localhost 调试允许 HTTP")
        }
        parts.scheme = scheme
        guard let url = parts.url else { throw ProfileError("Provisioning API URL 无效") }
        return url
    }

    static func fetch(endpoint: String) async throws -> RelayProvisioningDocument {
        let url = try endpointURL(endpoint)
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 10)
        request.httpMethod = "GET"
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue("MPTCP-Desk/provisioning", forHTTPHeaderField: "User-Agent")

        let config = URLSessionConfiguration.ephemeral
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        config.urlCache = nil
        config.httpCookieStorage = nil
        config.urlCredentialStorage = nil
        config.timeoutIntervalForRequest = 10
        config.timeoutIntervalForResource = 15
        let delegate = RelayProvisioningNoRedirectDelegate()
        let session = URLSession(configuration: config, delegate: delegate, delegateQueue: nil)
        defer { session.invalidateAndCancel() }
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw ProfileError("Provisioning API 返回的不是 HTTP 响应") }
        guard http.statusCode == 200 else { throw ProfileError("Provisioning API HTTP \(http.statusCode)") }
        guard !data.isEmpty, data.count <= maximumResponseBytes else { throw ProfileError("Provisioning API 响应为空或超过 512 KiB") }
        struct Header: Decodable { var schema_version: Int; var kind: String? }
        do {
            let header = try JSONDecoder().decode(Header.self, from: data)
            if header.schema_version == 1 {
                guard data.count <= maximumProfileResponseBytes else { throw ProfileError("单 Profile Provisioning 响应超过 64 KiB") }
                return .profile(try JSONDecoder().decode(RelayProvisioningPayload.self, from: data))
            }
            if header.schema_version == 2, header.kind == "bundle" {
                let bundle = try JSONDecoder().decode(RelayProvisioningBundlePayload.self, from: data)
                try bundle.validate()
                return .bundle(bundle)
            }
            throw ProfileError("Provisioning API schema/kind 不受支持")
        } catch let error as ProfileError { throw error }
        catch { throw ProfileError("Provisioning API JSON 无效：\(error.localizedDescription)") }
    }
}

// The provisioning URL contains the high-entropy bearer token in its path and
// therefore lives in Keychain just like the MPX transport key.
enum ProvisioningURLStore {
    private static let query: [String: Any] = [
        kSecClass as String: kSecClassGenericPassword,
        kSecAttrService as String: "MPTCPDesk.Provisioning",
        kSecAttrAccount as String: "active-url"
    ]
    static func save(_ value: String) throws {
        let cleaned = value.trimmingCharacters(in: .whitespacesAndNewlines)
        if cleaned.isEmpty { try delete(); return }
        _ = try RelayProvisioningClient.endpointURL(cleaned)
        let data = Data(cleaned.utf8)
        var status = SecItemUpdate(query as CFDictionary, [kSecValueData as String: data] as CFDictionary)
        if status == errSecItemNotFound {
            var item = query
            item[kSecValueData as String] = data
            item[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
            status = SecItemAdd(item as CFDictionary, nil)
        }
        guard status == errSecSuccess else { throw ProfileError("Provisioning URL 钥匙串保存失败（\(status)）") }
    }
    static func load() throws -> String? {
        var item = query
        item[kSecReturnData as String] = true
        item[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(item as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = result as? Data, let value = String(data: data, encoding: .utf8) else {
            throw ProfileError("Provisioning URL 钥匙串读取失败（\(status)）")
        }
        return value
    }
    static func delete() throws {
        let status = SecItemDelete(query as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw ProfileError("Provisioning URL 钥匙串删除失败（\(status)）") }
    }
}
