import Foundation
import Darwin

struct TunnelTransport: Codable {
    var transport_key: String
    var relays: [RelayRow]
    var scheduler_mode: String? = nil
    var schedulerMode: String { scheduler_mode ?? "auto" }
}

struct TunnelInlet: Codable, Identifiable {
    var id: String
    var name: String? = nil
    var listen_port: Int
    var route_id: UInt32
    var weight: Int? = nil
    var max_connections: Int? = nil
    var tcp_enabled: Bool? = nil
    var uot_enabled: Bool? = nil
    var tcpEnabled: Bool { tcp_enabled ?? true }
    var uotEnabled: Bool { uot_enabled ?? false }
    var displayName: String { name?.isEmpty == false ? name! : id }
}

struct TunnelConfig: Codable {
    var schema_version: Int = 1
    var kind: String = "shared_mpx4_tunnel"
    var tunnel: TunnelTransport
    var inlets: [TunnelInlet]

    static func validID(_ value: String) -> Bool {
        !value.isEmpty && value.utf8.count <= 64 && value.utf8.allSatisfy {
            (48...57).contains($0) || (65...90).contains($0) || (97...122).contains($0) || $0 == 45 || $0 == 95
        }
    }
    static func ipKind(_ host: String) -> (valid: Bool, unicast: Bool, loopback: Bool)? {
        var v4 = in_addr()
        if host.withCString({ inet_pton(AF_INET, $0, &v4) }) == 1 {
            let n = UInt32(bigEndian: v4.s_addr)
            return (true, n != 0 && n != UInt32.max && (n >> 28) != 14, (n >> 24) == 127)
        }
        var v6 = in6_addr()
        if host.withCString({ inet_pton(AF_INET6, $0, &v6) }) == 1 {
            let bytes = withUnsafeBytes(of: &v6) { Array($0) }
            let zero = bytes.allSatisfy { $0 == 0 }
            let loop = bytes.dropLast().allSatisfy { $0 == 0 } && bytes.last == 1
            // IPv4-mapped addresses must obey IPv4 unicast and loopback rules.
            if bytes.prefix(10).allSatisfy({ $0 == 0 }) && bytes[10] == 255 && bytes[11] == 255 {
                let n = bytes[12...15].reduce(UInt32(0)) { ($0 << 8) | UInt32($1) }
                return (true, n != 0 && n != UInt32.max && (n >> 28) != 14, (n >> 24) == 127)
            }
            return (true, !zero && bytes[0] != 255, loop)
        }
        return nil
    }
    static func validHost(_ host: String) -> Bool {
        if let ip = ipKind(host) { return ip.unicast }
        guard !host.isEmpty && host.utf8.count <= 253 else { return false }
        let value = host.hasSuffix(".") ? String(host.dropLast()) : host
        return value.split(separator: ".", omittingEmptySubsequences: false).allSatisfy { label in
            !label.isEmpty && label.utf8.count <= 63 && !label.hasPrefix("-") && !label.hasSuffix("-") && label.utf8.allSatisfy {
                (48...57).contains($0) || (65...90).contains($0) || (97...122).contains($0) || $0 == 45
            }
        }
    }
    func validate() throws {
        guard schema_version == 1 && kind == "shared_mpx4_tunnel" else {
            throw ProfileError("1.2.0 只接受共享隧道配置；旧的独立 Profile/多组 MPX Bundle 不会自动转换")
        }
        let key = tunnel.transport_key
        guard key.utf8.count == 64 && key.utf8.allSatisfy({ (48...57).contains($0) || (65...70).contains($0) || (97...102).contains($0) }) else {
            throw ProfileError("隧道传输密钥必须是 64 位十六进制字符")
        }
        guard SchedulerPolicy(rawValue: tunnel.schedulerMode) != nil else { throw ProfileError("隧道调度策略无效") }
        guard (2...8).contains(tunnel.relays.count) else { throw ProfileError("共享隧道需要 2–8 条 Relay 路径") }
        guard (1...64).contains(inlets.count) else { throw ProfileError("共享隧道需要 1–64 个业务入口") }
        var ids = Set<String>(), ports = Set<Int>(), routes = Set<UInt32>()
        for inlet in inlets {
            guard Self.validID(inlet.id), ids.insert(inlet.id).inserted else { throw ProfileError("入口 ID 必须唯一，使用 1–64 个英文字母、数字、- 或 _") }
            guard (inlet.name ?? "").utf8.count <= 128 else { throw ProfileError("入口名称过长") }
            guard (1024...65535).contains(inlet.listen_port), ports.insert(inlet.listen_port).inserted else { throw ProfileError("入口端口必须唯一，范围为 1024–65535") }
            guard inlet.route_id != 0, routes.insert(inlet.route_id).inserted else { throw ProfileError("路由编号必须非零且唯一") }
            guard (0...16).contains(inlet.weight ?? 0), (0...2048).contains(inlet.max_connections ?? 0) else { throw ProfileError("入口权重或连接限额无效") }
            guard inlet.tcpEnabled || inlet.uotEnabled else { throw ProfileError("每个入口至少启用 TCP 或 UoT") }
        }
        var addresses = Set<String>()
        for (index, relay) in tunnel.relays.enumerated() {
            let host = relay.host
            let identity = "\(host.lowercased()):\(relay.port)"
            guard Self.validHost(host), (1...65535).contains(relay.port), addresses.insert(identity).inserted else { throw ProfileError("Relay 需要有效且不重复的单播 IP/域名和端口") }
            let localhost = host.lowercased().trimmingCharacters(in: CharacterSet(charactersIn: ".")) == "localhost"
            if (localhost || Self.ipKind(host)?.loopback == true) && ports.contains(relay.port) { throw ProfileError("Relay 不能指向本地业务入口") }
            if let n = relay.download_mbps, !Profile.validWeightedMbps(n) { throw ProfileError("Relay \(index + 1) 下行容量无效") }
            if let n = relay.upload_mbps, !Profile.validWeightedMbps(n) { throw ProfileError("Relay \(index + 1) 上行容量无效") }
            if tunnel.schedulerMode == "weighted" && relay.download_mbps == nil { throw ProfileError("Weighted 模式需要填写每条 Relay 的下行容量") }
        }
    }
    func selecting(_ ids: Set<String>) throws -> TunnelConfig {
        try validate()
        guard !ids.isEmpty else { throw ProfileError("至少启用一个业务入口") }
        guard ids.isSubset(of: Set(inlets.map(\.id))) else { throw ProfileError("所选入口不存在") }
        var selected = self
        selected.inlets = inlets.filter { ids.contains($0.id) }
        return selected
    }
    func preferenceData() throws -> Data {
        var redacted = self
        redacted.tunnel.transport_key = ""
        return try JSONEncoder().encode(redacted)
    }
    static func checkedObject(_ value: Any?, allowed: Set<String>, label: String) throws -> [String: Any] {
        guard let object = value as? [String: Any], Set(object.keys).isSubset(of: allowed) else { throw ProfileError("\(label) 包含未知字段或不是 JSON 对象") }
        return object
    }
    static func checkJSON(_ value: Any?) throws {
        let c = try checkedObject(value, allowed: ["schema_version", "kind", "tunnel", "inlets"], label: "隧道配置")
        let t = try checkedObject(c["tunnel"], allowed: ["transport_key", "relays", "scheduler_mode"], label: "隧道")
        guard let relays = t["relays"] as? [Any], let inlets = c["inlets"] as? [Any] else { throw ProfileError("缺少 Relay 或入口数组") }
        for r in relays { _ = try checkedObject(r, allowed: ["host", "port", "download_mbps", "upload_mbps"], label: "Relay") }
        for inlet in inlets { _ = try checkedObject(inlet, allowed: ["id", "name", "listen_port", "route_id", "weight", "max_connections", "tcp_enabled", "uot_enabled"], label: "入口") }
    }
    static func decode(_ data: Data) throws -> TunnelConfig {
        guard !data.isEmpty && data.count <= 65536 else { throw ProfileError("隧道配置为空或超过 64 KiB") }
        try checkJSON(JSONSerialization.jsonObject(with: data))
        let result = try JSONDecoder().decode(TunnelConfig.self, from: data)
        try result.validate()
        return result
    }
}

struct ManagedTunnelProfile: Codable {
    var schema_version: Int
    var kind: String
    var profile_id: String
    var revision: String
    var display_name: String
    var background_resident: Bool
    var config: TunnelConfig
    func validate() throws {
        guard schema_version == 4 && kind == "tunnel_profile", TunnelConfig.validID(profile_id), !revision.isEmpty, revision.utf8.count <= 128, display_name.utf8.count <= 256 else { throw ProfileError("1.2.0 远端配置需要 schema_version=4、kind=tunnel_profile；请在新控制器中重新配置共享隧道") }
        try config.validate()
    }
    static func decode(_ data: Data) throws -> ManagedTunnelProfile {
        guard !data.isEmpty && data.count <= 65536 else { throw ProfileError("远端隧道配置超过 64 KiB") }
        let o = try TunnelConfig.checkedObject(JSONSerialization.jsonObject(with: data), allowed: ["schema_version", "kind", "profile_id", "revision", "display_name", "background_resident", "config"], label: "远端配置")
        try TunnelConfig.checkJSON(o["config"])
        let result = try JSONDecoder().decode(ManagedTunnelProfile.self, from: data)
        try result.validate()
        return result
    }
}

struct TunnelRouteMetric: Decodable, Identifiable {
    var route_id: UInt32
    var id: UInt32 { route_id }
    var weight: Int
    var streams: Int
    var closing_streams: Int?
    var ready_data_bytes: Int
    var dispatched_payload_bytes: UInt64
    var receive_unconsumed_bytes: Int?
    var receive_allocated_bytes: Int?
    var receive_limit_bytes: Int?
    var receive_pressure_resets: UInt64?
}
struct TunnelInletMetric: Decodable, Identifiable {
    var id: String
    var route_id: UInt32
    var listen_port: Int
    var active_connections: Int64
    var accepted_connections: UInt64
    var rejected_connections: UInt64
    var udp_connections: Int?
    var udp_sent_bytes: UInt64?
    var udp_received_bytes: UInt64?
    var udp_dropped: UInt64?
}
