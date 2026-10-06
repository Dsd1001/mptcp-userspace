import AppKit
import SwiftUI
import Foundation

// Synthetic profiles and the product's own offscreen views only. No Keychain,
// installed App, real Relay, proxy, or system network settings are accessed.
@main struct UOTHarness {
    static func expectFailure(_ name: String, _ work: () throws -> Void) {
        do { try work(); fatalError("expected failure: \(name)") }
        catch is ProfileError { }
        catch { fatalError("wrong error for \(name): \(error)") }
    }

    @MainActor static func main() throws {
        setenv("MPTCP_DESK_SMOKE_TEST", "1", 1)
        NSApplication.shared.setActivationPolicy(.prohibited)
        guard CommandLine.arguments.count == 2 else { throw ProfileError("usage: uot-harness output-directory") }
        let output = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
        try FileManager.default.createDirectory(at: output, withIntermediateDirectories: true)
        let relays = [RelayRow(host: "192.0.2.10", port: 24001), RelayRow(host: "198.51.100.20", port: 24001)]
        let key = String(repeating: "a", count: 64)
        let original = Profile(schema_version: 3, mode: "userspace_multipath", listen_port: 1081,
                               relays: relays, udp_enabled: true, tcp_enabled: true, transport_key: key)
        let legacyData = try JSONEncoder().encode(original)
        let legacyObject = try JSONSerialization.jsonObject(with: legacyData) as! [String: Any]
        precondition(legacyObject["uot_enabled"] == nil)
        let old = try JSONDecoder().decode(Profile.self, from: legacyData)
        try old.validate()
        precondition(!(old.uot_enabled ?? false))
        for tcp in [false, true] {
            for udp in [false, true] {
                for uot in [false, true] {
                    var p = original
                    p.tcp_enabled = tcp; p.udp_enabled = udp; p.uot_enabled = uot
                    if (udp && uot) || !(tcp || udp || uot) {
                        expectFailure("transport combination") { try p.validate() }
                    } else { try p.validate() }
                }
            }
        }
        var uot = original
        uot.tcp_enabled = false; uot.udp_enabled = false; uot.uot_enabled = true
        let ordinary = try uot.preferenceData()
        let object = try JSONSerialization.jsonObject(with: ordinary) as! [String: Any]
        precondition(object["transport_key"] == nil && object["uot_enabled"] as? Bool == true)
        var restored = try JSONDecoder().decode(Profile.self, from: ordinary)
        restored.transport_key = key
        try restored.validate()
        var native = uot
        native.mode = "native_mptcp"; native.tcp_enabled = true
        expectFailure("Native UoT") { try native.validate() }
        native.schema_version = 2; native.mode = "tcp_forward"
        expectFailure("legacy Native UoT") { try native.validate() }

        let model = Model.shared
        try model.loadProfileData(JSONEncoder().encode(uot))
        precondition(model.uotEnabled && !model.udpEnabled && !model.tcpEnabled)
        precondition(model.datagramEnabled && model.datagramLabel == "UoT")
        precondition(model.listeningStatus == "Userspace UoT 入口已启动")
        let engineConfig = try model.profile()
        precondition(engineConfig.uot_enabled == true && engineConfig.tcp_enabled == false)
        model.setNativeUDPEnabled(true)
        precondition(model.udpEnabled && !model.uotEnabled && !model.tcpEnabled)
        model.setUOTEnabled(true)
        precondition(model.uotEnabled && !model.udpEnabled && !model.tcpEnabled)
        model.tcpEnabled = true
        try model.profile().validate()
        model.running = true
        model.setNativeUDPEnabled(true)
        precondition(model.uotEnabled && !model.udpEnabled)
        model.running = false
        var invalid = uot; invalid.udp_enabled = true
        expectFailure("mutually exclusive import") { try model.loadProfileData(JSONEncoder().encode(invalid)) }
        precondition(model.uotEnabled && !model.udpEnabled)
        try model.loadProfileData(legacyData)
        precondition(!model.uotEnabled && model.udpEnabled)
        model.setUOTEnabled(true)
        model.mode = "native_mptcp"; model.applyModeSelection(model.mode)
        precondition(!model.uotEnabled && model.tcpEnabled)
        model.setUOTEnabled(true)
        precondition(!model.uotEnabled)

        let payload = RelayProvisioningPayload(schema_version: 1, profile_id: "uot", revision: "r1",
            display_name: "UoT only", mode: "userspace_multipath", listen_port: 1081, scheduler_mode: "auto",
            tcp_enabled: false, udp_enabled: false, uot_enabled: true, background_resident: false,
            transport_key: key, relays: relays)
        let decoded = try JSONDecoder().decode(RelayProvisioningPayload.self, from: JSONEncoder().encode(payload))
        let remote = try decoded.validatedProfile()
        precondition(remote.uot_enabled == true && remote.tcp_enabled == false)
        var invalidPayload = payload; invalidPayload.udp_enabled = true
        expectFailure("provisioned conflict") { _ = try invalidPayload.validatedProfile() }
        var bundle = RelayProvisioningBundlePayload(schema_version: 2, kind: "bundle", bundle_id: "u",
            revision: "1", display_name: "Synthetic UoT", mode: "parallel", profiles: [payload])
        try bundle.validate()
        let encodedBundle = try JSONEncoder().encode(bundle)
        let roundTrip = try JSONDecoder().decode(RelayProvisioningBundlePayload.self, from: encodedBundle)
        let selected = try roundTrip.selectedProfiles(ids: ["uot"])
        precondition(selected[0].uot_enabled == true)
        bundle.profiles = [invalidPayload]
        expectFailure("Bundle conflict") { try bundle.validate() }
        var nativePayload = payload; nativePayload.mode = "native_mptcp"; nativePayload.tcp_enabled = true
        expectFailure("provisioned Native UoT") { _ = try nativePayload.validatedProfile() }

        model.provisioningIsBundle = true; model.configurationSource = "remote"
        model.provisioningProfiles = [
            ProvisioningProfileChoice(id: "tcp", name: "TCP", listenPort: 1082, relayCount: 2,
                                      mode: "userspace_multipath", backgroundResident: false),
            ProvisioningProfileChoice(id: "uot", name: "UoT", listenPort: 1081, relayCount: 2,
                                      mode: "userspace_multipath", backgroundResident: false, uotEnabled: true)
        ]
        model.provisioningSelectedProfileIDs = ["tcp", "uot"]
        precondition(model.datagramEnabled && model.hasUOT && model.datagramLabel == "UoT")
        let stats = try JSONDecoder().decode(EngineEvent.self, from: Data(#"{"profile_id":"uot","bundle_id":"u","kind":"stats","connections":1,"sent":120,"received":240}"#.utf8))
        let udpStats = try JSONDecoder().decode(EngineEvent.self, from: Data(#"{"profile_id":"uot","bundle_id":"u","kind":"udp_stats","connections":1,"sent":100,"received":200,"dropped":2}"#.utf8))
        precondition(model.consumeBundleProfileEvent(stats))
        precondition(model.consumeBundleProfileEvent(udpStats))
        let diagnostic = model.provisioningDiagnostics["uot"]!
        precondition(diagnostic.sent == 120 && diagnostic.received == 240)
        precondition(diagnostic.udpSent == 100 && diagnostic.udpReceived == 200)
        precondition(diagnostic.udpConnections == 1 && diagnostic.udpDropped == 2)
        // Payload counters are a subset of carrier bytes, never added to totals.
        model.provisioningSelectedProfileIDs = ["tcp"]
        precondition(!model.datagramEnabled && !model.hasUOT)

        model.configurationSource = "local"; model.provisioningIsBundle = false
        try model.loadProfileData(JSONEncoder().encode(uot))
        model.sent = 120; model.received = 240
        model.udpConnections = 1; model.udpSent = 100; model.udpReceived = 200
        for (name, tab) in [("uot-configuration", 0), ("uot-paths", 2)] {
            model.tab = tab
            let frame = NSRect(x: 0, y: 0, width: 710, height: 850)
            let host = NSHostingView(rootView: DesktopView().environment(\.colorScheme, .light))
            let window = NSWindow(contentRect: frame, styleMask: .borderless, backing: .buffered, defer: false)
            window.contentView = host; host.frame = frame
            host.layoutSubtreeIfNeeded()
            RunLoop.main.run(until: Date(timeIntervalSinceNow: 0.3))
            host.layoutSubtreeIfNeeded(); host.displayIfNeeded()
            guard let bitmap = host.bitmapImageRepForCachingDisplay(in: host.bounds) else { throw ProfileError("UI bitmap unavailable") }
            host.cacheDisplay(in: host.bounds, to: bitmap)
            guard let png = bitmap.representation(using: .png, properties: [:]) else { throw ProfileError("UI PNG encoding failed") }
            try png.write(to: output.appendingPathComponent(name + ".png"), options: .atomic)
            window.contentView = nil
        }
        print("PASS: UoT-only/TCP+UoT combinations, mutual exclusion, legacy defaults, imports/preferences, Native rejection, provisioning/Bundle round-trip, telemetry accounting, locked controls and two offscreen views")
    }
}
