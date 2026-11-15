import AppKit
import SwiftUI
import Foundation
import CryptoKit

// Only synthetic data and this App's offscreen NSHostingViews are used. Never
// opens the real GUI, reads user profiles/Keychain items, or launches an engine.
@main struct TunnelUIHarness {
    static func envelope(_ profile: ManagedTunnelProfile, token: String) throws -> Data {
        let characters = Array(token)
        let raw = stride(from:0,to:characters.count,by:2).map { UInt8(String(characters[$0...($0+1)]),radix:16)! }
        let secret = SymmetricKey(data: Data(raw))
        let material = HMAC<SHA256>.authenticationCode(for: Data("mpx-provision-config-envelope-v1".utf8), using: secret)
        let key = SymmetricKey(data: Data(material))
        let sealed = try AES.GCM.seal(try JSONEncoder().encode(profile), using: key, authenticating: Data("mpx-provision-envelope-v1".utf8))
        func b64(_ data: Data) -> String { data.base64EncodedString().replacingOccurrences(of:"+",with:"-").replacingOccurrences(of:"/",with:"_").replacingOccurrences(of:"=",with:"") }
        return try JSONSerialization.data(withJSONObject: ["v":1,"n":b64(Data(sealed.nonce)),"d":b64(sealed.ciphertext+sealed.tag)])
    }
    static func rejected(_ name: String, _ body: () throws -> Void) throws {
        do { try body(); throw NSError(domain:"TunnelUIHarness",code:1,userInfo:[NSLocalizedDescriptionKey:"invalid case accepted: "+name]) }
        catch is ProfileError { return }
    }
    @MainActor static func main() throws {
        guard CommandLine.arguments.count == 2 else { fatalError("usage: TunnelUIHarness output-directory") }
        setenv("MPTCP_DESK_SMOKE_TEST","1",1)
        let app = NSApplication.shared
        app.setActivationPolicy(.prohibited)
        let output = URL(fileURLWithPath:CommandLine.arguments[1],isDirectory:true)
        try FileManager.default.createDirectory(at:output,withIntermediateDirectories:true)
        setenv("MPTCP_DESK_CACHE_DIR",output.appendingPathComponent("synthetic-cache").path,1)
        let token = String(repeating:"2",count:64)
        let endpoint = "https://controller.example.invalid/v1/tunnel/"+token
        let key = String(repeating:"a",count:64)
        let configuration = TunnelConfig(tunnel:TunnelTransport(transport_key:key,
            relays:(1...6).map { RelayRow(host:"192.0.2.\($0)",port:24001,download_mbps:100,upload_mbps:50) },scheduler_mode:"aggregate"),
            inlets:[TunnelInlet(id:"hkbn",name:"HKBN",listen_port:1081,route_id:1),TunnelInlet(id:"hkt",name:"HKT",listen_port:1082,route_id:2,uot_enabled:true),TunnelInlet(id:"icable",name:"iCable",listen_port:1083,route_id:3)])
        let configData = try JSONEncoder().encode(configuration)
        let decoded = try TunnelConfig.decode(configData)
        precondition(decoded.inlets.count == 3 && decoded.tunnel.relays.count == 6)
        let one = try decoded.selecting(["hkt"]); precondition(one.inlets.count == 1)
        try rejected("empty selection") { _ = try decoded.selecting([]) }
        try rejected("unknown selection") { _ = try decoded.selecting(["missing"]) }
        try rejected("legacy profile") { _ = try TunnelConfig.decode(Data(#"{"schema_version":3,"mode":"userspace_multipath"}"#.utf8)) }
        var wrong = configuration
        wrong.inlets[1].route_id = 1
        try rejected("duplicate route") { try wrong.validate() }
        wrong = configuration;wrong.tunnel.relays[0].host = "0.0.0.0"
        try rejected("unspecified relay") { try wrong.validate() }
        wrong = configuration;wrong.inlets[0].tcp_enabled = false
        try rejected("disabled inlet") { try wrong.validate() }
        wrong.inlets[0].uot_enabled = true
        try wrong.validate()
        var object = try JSONSerialization.jsonObject(with:configData) as! [String:Any]
        object["legacy_profiles"] = []
        try rejected("unknown field") { _ = try TunnelConfig.decode(JSONSerialization.data(withJSONObject:object)) }
        let redacted = try configuration.preferenceData()
        precondition(!String(decoding:redacted,as:UTF8.self).contains(key))
        let profile = ManagedTunnelProfile(schema_version:4,kind:"tunnel_profile",profile_id:"synthetic-tunnel",revision:"r1",display_name:"三入口共享隧道",background_resident:false,config:configuration)
        let encrypted = try envelope(profile,token:token)
        if case .tunnel(let remote) = try RelayProvisioningClient.decodeResponse(encrypted,endpoint:endpoint) {
            precondition(remote.config.inlets.count == 3 && remote.profile_id == "synthetic-tunnel")
        } else { fatalError("expected managed tunnel") }
        try rejected("plaintext remote") { _ = try RelayProvisioningClient.decodeResponse(JSONEncoder().encode(profile),endpoint:endpoint) }
        try rejected("bad URL query") { _ = try RelayProvisioningClient.endpointURL(endpoint+"?x=1") }
        let fetchedAt = Date(timeIntervalSince1970:1800000000)
        try ManagedProvisioningCacheStore.save(responseData:encrypted,endpoint:endpoint,selectedProfileIDs:["hkbn","hkt"],fetchedAt:fetchedAt)
        let model = Model.shared
        model.configurationSource = "remote";model.provisioningURL = endpoint
        let launch = try model.managedCacheLaunchSummaryForTests()
        precondition(launch?.action == "run-tunnel" && launch?.selectedCount == 2 && launch?.fetchedAt == fetchedAt)
        let other = try ManagedProvisioningCacheStore.load(endpoint:"https://other.example.invalid/v1/tunnel/"+token)
        precondition(other == nil)
        model.setProvisioningProfileSelected("icable",selected:true)
        precondition(model.provisioningSelectedProfileIDs.count == 3)
        model.remoteManagementEnabled = false
        model.applyRemoteDesiredState(RemoteDesiredState(revision:99,desired_state:"running",assignment_type:"bundle",assignment_id:"old",provisioning_url:endpoint,restart_generation:0,sync_generation:0,update_generation:0,desired_version:nil))
        precondition(model.remoteControlRevision == 0 && !model.running)
        model.applyTunnel(configuration)
        model.running = false;model.busy = false;model.problem = nil
        model.status = "已停止";model.paths = 6;model.connections = 18;model.sent = 42<<20;model.received = 120<<20
        model.configuredSchedulerMode = "aggregate";model.effectiveSchedulerMode = "aggregate"
        let paths: [[String:Any]] = (1...6).map { i in ["id":i,"address":"192.0.2.\(i):24001","connected":true,"sent":7<<20,"received":20<<20,"rtt_ms":12.0+Double(i),"goodput_bps":10000000,"outstanding_bytes":65536,"queue_bytes":32768,"errors":0] }
        model.tcpPaths = try JSONDecoder().decode([PathMetric].self,from:JSONSerialization.data(withJSONObject:paths))
        model.tunnelRouteMetrics = try JSONDecoder().decode([TunnelRouteMetric].self,from:JSONSerialization.data(withJSONObject:(1...3).map { i in ["route_id":i,"weight":1,"streams":6,"ready_data_bytes":32768,"dispatched_payload_bytes":14000000,"receive_allocated_bytes":81920,"receive_limit_bytes":67108864,"receive_pressure_resets":0] }))
        model.tunnelInletMetrics = try JSONDecoder().decode([TunnelInletMetric].self,from:JSONSerialization.data(withJSONObject:configuration.inlets.map { i in ["id":i.id,"route_id":Int(i.route_id),"listen_port":i.listen_port,"active_connections":6,"accepted_connections":24,"rejected_connections":0,"udp_connections":i.id=="hkt" ? 2 : 0,"udp_sent_bytes":4096,"udp_received_bytes":4096,"udp_dropped":0] }))
        model.logs = ["共享隧道已认证；6 条 Carrier 可用","HKBN / HKT / iCable 共用一个 Session","未连接生产机器；此页面为验收用合成数据"]
        for (name,tab,remote,dark) in [("tunnel-local",0,false,false),("tunnel-remote",0,true,false),("tunnel-runtime",1,false,false),("tunnel-management",2,false,false),("tunnel-logs",3,false,false),("tunnel-dark",1,false,true)] {
            model.tunnelSelectedTab = tab;model.configurationSource = remote ? "remote" : "local"
            let host = NSHostingView(rootView:TunnelDesktopView().environment(\.colorScheme,dark ? .dark:.light))
            let frame = NSRect(x:0,y:0,width:1180,height:1000)
            let window = NSWindow(contentRect:frame,styleMask:.borderless,backing:.buffered,defer:false)
            window.contentView = host;host.frame = frame;host.layoutSubtreeIfNeeded()
            RunLoop.main.run(until:Date(timeIntervalSinceNow:0.4))
            host.layoutSubtreeIfNeeded();host.displayIfNeeded()
            guard let bitmap = host.bitmapImageRepForCachingDisplay(in:host.bounds) else {throw ProfileError("missing bitmap")}
            host.cacheDisplay(in:host.bounds,to:bitmap)
            guard let png = bitmap.representation(using:.png,properties:[:]) else {throw ProfileError("PNG encode failed")}
            try png.write(to:output.appendingPathComponent(name+".png"),options:.atomic)
            print("Rendered \(name) \(bitmap.pixelsWide)x\(bitmap.pixelsHigh)")
            window.contentView = nil
        }
        print("PASS: shared tunnel validation, legacy rejection, encrypted remote config, endpoint-bound cache, selected inlets, local-only remote authorization and six offscreen GUI views")
    }
}
