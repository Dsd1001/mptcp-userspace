import Foundation

@main struct ProvisioningHarness {
    static func expectFailure(_ name: String, _ work: () throws -> Void) {
        do { try work(); fatalError("expected failure: \(name)") }
        catch is ProfileError { }
        catch { fatalError("wrong error for \(name): \(error)") }
    }

    static func main() async throws {
        guard CommandLine.arguments.count == 2 else { throw ProfileError("usage: provisioning-harness endpoint") }
        let endpoint = CommandLine.arguments[1]
        let httpsURL = try RelayProvisioningClient.endpointURL("https://config.example.test/v1/config/token")
        let loopbackURL = try RelayProvisioningClient.endpointURL("http://127.0.0.1:8080/v1/config/token")
        precondition(httpsURL.scheme == "https")
        precondition(loopbackURL.scheme == "http")
        expectFailure("cleartext remote endpoint") { _ = try RelayProvisioningClient.endpointURL("http://example.com/v1/config/token") }
        expectFailure("userinfo endpoint") { _ = try RelayProvisioningClient.endpointURL("https://user:pass@example.com/v1/config/token") }
        expectFailure("fragment endpoint") { _ = try RelayProvisioningClient.endpointURL("https://example.com/v1/config/token#secret") }

        let payload = try await RelayProvisioningClient.fetch(endpoint: endpoint)
        let p = try payload.validatedProfile()
        precondition(payload.schema_version == 1)
        precondition(payload.display_name == "Synthetic")
        precondition(payload.background_resident == true)
        precondition(p.mode == "userspace_multipath")
        precondition(p.listen_port == 1081 && p.schedulerMode == "weighted")
        precondition(p.tcp_enabled == true && p.udp_enabled == true)
        precondition(p.relays.count == 2)
        precondition(p.relays[0].host == "192.0.2.10" && p.relays[0].port == 8849 && p.relays[0].download_mbps == 94)
        precondition(p.relays[1].host == "198.51.100.20" && p.relays[1].upload_mbps == nil)
        precondition(p.transport_key == String(repeating: "a", count: 64))
        let ordinary = try p.preferenceData()
        let object = try JSONSerialization.jsonObject(with: ordinary) as! [String: Any]
        precondition(object["transport_key"] == nil)

        let bad = RelayProvisioningPayload(schema_version:2, revision:nil, display_name:nil, mode:"userspace_multipath", listen_port:1081, scheduler_mode:"auto", tcp_enabled:true, udp_enabled:true, background_resident:nil, transport_key:String(repeating:"a",count:64), relays:p.relays)
        expectFailure("schema") { _ = try bad.validatedProfile() }
        let missingKey = RelayProvisioningPayload(schema_version:1, revision:nil, display_name:nil, mode:"userspace_multipath", listen_port:1081, scheduler_mode:"auto", tcp_enabled:true, udp_enabled:true, background_resident:nil, transport_key:nil, relays:p.relays)
        expectFailure("missing key") { _ = try missingKey.validatedProfile() }
        let native = RelayProvisioningPayload(schema_version:1, revision:nil, display_name:nil, mode:"native_mptcp", listen_port:1081, scheduler_mode:nil, tcp_enabled:true, udp_enabled:true, background_resident:false, transport_key:nil, relays:p.relays)
        let np = try native.validatedProfile(); precondition(!np.userspace && np.transport_key == nil)
        print("PASS: full Provisioning HTTPS policy, full-profile fetch, Userspace/Native validation and key-free preferences")
    }
}
