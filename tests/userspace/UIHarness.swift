import AppKit
import SwiftUI
import Foundation

// Renders only this project's own synthetic view, never the user's desktop.
// No real profiles, Keychain items, process launch, proxy or kernel settings.
@main struct UIHarness {
    @MainActor static func main() throws {
        setenv("MPTCP_DESK_SMOKE_TEST", "1", 1)
        let app = NSApplication.shared
        app.setActivationPolicy(.prohibited)
        let output = URL(fileURLWithPath:CommandLine.arguments[1],isDirectory:true)
        try FileManager.default.createDirectory(at:output,withIntermediateDirectories:true)
        let relays = [RelayRow(host:"192.0.2.10",port:24001),RelayRow(host:"198.51.100.20",port:24001)]

        let cacheDir = output.appendingPathComponent("managed-cache", isDirectory:true)
        setenv("MPTCP_DESK_CACHE_DIR", cacheDir.path, 1)
        let cacheEndpoint = "https://config.example.test/v1/bundle/" + String(repeating:"a",count:64)
        let cacheA = RelayProvisioningPayload(
            schema_version:1, profile_id:"cache-a", revision:"r1", display_name:"Cache A",
            mode:"native_mptcp", listen_port:1181, scheduler_mode:nil,
            tcp_enabled:true, udp_enabled:false, background_resident:false, transport_key:nil, relays:relays
        )
        var cacheB = cacheA
        cacheB.profile_id = "cache-b"; cacheB.display_name = "Cache B"; cacheB.listen_port = 1182
        let cacheBundle = RelayProvisioningBundlePayload(
            schema_version:2, kind:"bundle", bundle_id:"cache-bundle", revision:"r1",
            display_name:"Cached Bundle", mode:"parallel", profiles:[cacheA,cacheB]
        )
        let cacheResponse = try JSONEncoder().encode(cacheBundle)
        let cacheFetchedAt = Date(timeIntervalSince1970:1_800_000_000)
        try ManagedProvisioningCacheStore.save(
            responseData: cacheResponse,
            endpoint: cacheEndpoint,
            selectedProfileIDs:["cache-a"],
            fetchedAt: cacheFetchedAt
        )
        let cached = try ManagedProvisioningCacheStore.load(endpoint: cacheEndpoint)
        precondition(cached != nil)
        precondition(cached?.selectedProfileIDs == ["cache-a"])
        precondition(cached?.fetchedAt == cacheFetchedAt)
        try ManagedProvisioningCacheStore.updateSelection(endpoint: cacheEndpoint, selectedProfileIDs:["cache-b"])
        let updatedCached = try ManagedProvisioningCacheStore.load(endpoint: cacheEndpoint)
        precondition(updatedCached?.selectedProfileIDs == ["cache-b"])
        try ManagedProvisioningCacheStore.updateSelection(endpoint: cacheEndpoint, selectedProfileIDs:["cache-a"])
        if case .bundle(let loadedBundle)? = cached?.document {
            precondition(loadedBundle.bundle_id == "cache-bundle")
            precondition(loadedBundle.profiles.count == 2)
        } else { fatalError("managed Bundle cache did not round-trip") }
        let otherEndpoint = "https://config.example.test/v1/bundle/" + String(repeating:"b",count:64)
        let otherCached = try ManagedProvisioningCacheStore.load(endpoint: otherEndpoint)
        precondition(otherCached == nil)
        precondition(!ManagedProvisioningCachePolicy.refreshDue(
            fetchedAt: cacheFetchedAt,
            now: cacheFetchedAt.addingTimeInterval(ManagedProvisioningCachePolicy.refreshInterval - 1)
        ))
        precondition(ManagedProvisioningCachePolicy.refreshDue(
            fetchedAt: cacheFetchedAt,
            now: cacheFetchedAt.addingTimeInterval(ManagedProvisioningCachePolicy.refreshInterval)
        ))
        precondition(ManagedProvisioningCachePolicy.retryDelay(attempt:0) == 60)
        precondition(ManagedProvisioningCachePolicy.retryDelay(attempt:99) == 3 * 60 * 60)
        precondition(ManagedProvisioningCachePolicy.shouldApplyRefreshImmediately(runtimeActive:false))
        precondition(!ManagedProvisioningCachePolicy.shouldApplyRefreshImmediately(runtimeActive:true))
        let cacheAttributes = try FileManager.default.attributesOfItem(atPath: ManagedProvisioningCacheStore.cacheURL().path)
        precondition((cacheAttributes[.posixPermissions] as? NSNumber)?.intValue == 0o600)

        let legacy = Profile(schema_version:2,mode:"tcp_forward",listen_port:1081,relays:relays,udp_enabled:nil,tcp_enabled:nil,transport_key:nil)
        try legacy.validate();precondition(!legacy.userspace)
        var modern = Profile(schema_version:3,mode:"userspace_multipath",listen_port:1081,relays:relays,udp_enabled:true,tcp_enabled:true,transport_key:String(repeating:"a",count:64))
        try modern.validate()
        let encoded = try JSONEncoder().encode(modern)
        try JSONDecoder().decode(Profile.self,from:encoded).validate()
        modern.transport_key="password"
        do { try modern.validate();fatalError("weak key accepted") } catch is ProfileError {}
        modern.transport_key=String(repeating:"a",count:64);modern.tcp_enabled=false;modern.udp_enabled=false
        do { try modern.validate();fatalError("both disabled accepted") } catch is ProfileError {}
        let model = Model.shared
        model.configurationSource = "remote"
        model.provisioningURL = cacheEndpoint
        let cachePlan = try model.managedCacheLaunchSummaryForTests()
        precondition(cachePlan?.action == "run-bundle")
        precondition(cachePlan?.selectedCount == 1)
        precondition(cachePlan?.fetchedAt == cacheFetchedAt)
        model.configurationSource = "local"
        model.provisioningURL = ""
        model.relays=relays;model.transportKey=String(repeating:"a",count:64)
        model.tcpPaths=[
            PathMetric(id:1,address:"192.0.2.10:24001",connected:true,sent:2000000,received:4000000,rtt_ms:23.4,goodput_bps:2097152,outstanding_bytes:65536,queue_bytes:32768,errors:0,last_error:nil),
            PathMetric(id:2,address:"198.51.100.20:24001",connected:false,sent:1000000,received:3000000,rtt_ms:78.2,goodput_bps:1048576,outstanding_bytes:0,queue_bytes:0,errors:2,last_error:"Synthetic test: carrier unavailable, reconnecting; no real network address is used")
        ]
        model.udpPaths=model.tcpPaths;model.reorderBytes=32768;model.reorderPeak=524288;model.pendingBytes=65536;model.retransmits=3;model.udpDropped=2
        let diagnostic = """
        {"kind":"stats","resources":{"active_streams":512,"stream_limit":512,"pending_frames":288,"pending_frame_limit":6144,"pending_bytes":1050624,"pending_byte_limit":33685504,"receive_credit_bytes":29360128,"receive_credit_limit_bytes":33554432,"receive_allocated_bytes":4194304,"receive_allocated_limit_bytes":33554432,"admission_reserve_bytes":4194304,"waiting_opens":2,"waits":{},"rejections":{},"first_limit_at":"2026-09-22T00:00:00Z","last_limit_at":"2026-09-22T00:00:01Z","last_reason":"data_window","bootstrap_credit_bytes":8388608,"bootstrap_credit_limit_bytes":8388608,"growth_credit_bytes":20971520,"growth_credit_limit_bytes":25165824,"data_pending_frames":256,"data_pending_frame_limit":4096,"data_pending_bytes":1048576,"control_pending_frames":32,"control_pending_frame_limit":2048,"control_pending_bytes":2048,"window_blocked_writers":5,"open_receive_credit_waits":0,"opened_streams":1200,"closed_streams":688,"idle_streams":332,"small_streams":177,"bulk_streams":3,"idle_irrevocable_growth_bytes":0},"lifecycle":{"session_tag":"synthetic","created_at":"2026-09-22T00:00:00Z","closed":false,"event_sequence":1,"events":[{"sequence":1,"at":"2026-09-22T00:00:00Z","kind":"carrier_connected","path":1}]}}
        """
        let decodedEvent = try JSONDecoder().decode(EngineEvent.self, from: Data(diagnostic.utf8))
        precondition(decodedEvent.resources?.waiting_opens == 2)
        precondition(decodedEvent.resources?.stream_limit == 512)
        precondition(decodedEvent.resources?.open_receive_credit_waits == 0)
        precondition(decodedEvent.resources?.bootstrap_credit_limit_bytes == 8<<20)
        precondition(decodedEvent.resources?.rejections.isEmpty == true)
        precondition(decodedEvent.lifecycle?.closed == false)
        model.resources = decodedEvent.resources; model.lifecycle = decodedEvent.lifecycle
        precondition(model.localStreamResourceExpanded == false)
        precondition(model.localWindowResourceExpanded == false)
        precondition(model.profileStreamResourceExpanded.isEmpty)
        precondition(model.profileWindowResourceExpanded.isEmpty)
        let remoteDiagnosticA = """
        {"profile_id":"a","profile_name":"HKBN","bundle_id":"synthetic-bundle","kind":"stats","configured_scheduler_mode":"weighted","effective_scheduler_mode":"weighted","mode_switches":2,"last_mode_reason":"configured weighted capacity","paths":2,"connections":7,"sent":2097152,"received":4194304,"reorder_bytes":16384,"reorder_peak":131072,"pending_bytes":32768,"retransmits":4,"path_stats":[{"id":0,"address":"203.0.113.10:8849","connected":true,"sent":100,"received":200,"rtt_ms":18.5,"goodput_bps":3145728,"outstanding_bytes":4096,"queue_bytes":2048,"errors":0}],"resources":{"active_streams":11,"stream_limit":512,"pending_frames":18,"pending_frame_limit":6144,"pending_bytes":65536,"pending_byte_limit":33685504,"receive_credit_bytes":1048576,"receive_credit_limit_bytes":33554432,"receive_allocated_bytes":2097152,"receive_allocated_limit_bytes":33554432,"admission_reserve_bytes":4194304,"waiting_opens":1,"waits":{},"rejections":{},"bootstrap_credit_bytes":1048576,"bootstrap_credit_limit_bytes":8388608,"growth_credit_bytes":1048576,"growth_credit_limit_bytes":25165824,"data_pending_frames":12,"data_pending_frame_limit":4096,"control_pending_frames":6,"control_pending_frame_limit":2048,"window_blocked_writers":2,"open_receive_credit_waits":3,"local_connections":7,"occupied_stream_slots":12,"closing_streams":1,"lifecycle_open_bidirectional":9,"lifecycle_opening":1,"lifecycle_half_closed":1,"lifecycle_wait_local_final_ack":0,"lifecycle_wait_peer_final":0,"lifecycle_both_final_wait_close":0,"lifecycle_wait_final_consumed":0,"lifecycle_closing_other":0,"data_idle_over_30s":1,"data_idle_over_1m":0,"data_idle_over_5m":0,"data_idle_over_10m":0,"oldest_data_idle_seconds":35,"idle_streams":5,"small_streams":5,"bulk_streams":1,"capability_revision":4,"session_tx_unconsumed_bytes":65536,"idle_actual_data_bytes":32768}}
        """
        let remoteDiagnosticB = """
        {"profile_id":"b","profile_name":"HKT","bundle_id":"synthetic-bundle","kind":"stats","configured_scheduler_mode":"weighted","effective_scheduler_mode":"protect","mode_switches":5,"last_mode_reason":"path degraded","paths":1,"connections":3,"sent":524288,"received":1048576,"reorder_bytes":4096,"reorder_peak":65536,"pending_bytes":8192,"retransmits":9,"path_stats":[{"id":0,"address":"198.51.100.20:8848","connected":false,"sent":0,"received":0,"rtt_ms":0,"goodput_bps":0,"outstanding_bytes":0,"queue_bytes":0,"errors":1,"last_error":"dial 198.51.100.20:8848: timeout"}],"resources":{"active_streams":3,"stream_limit":512,"pending_frames":4,"pending_frame_limit":6144,"pending_bytes":8192,"pending_byte_limit":33685504,"receive_credit_bytes":262144,"receive_credit_limit_bytes":33554432,"receive_allocated_bytes":524288,"receive_allocated_limit_bytes":33554432,"admission_reserve_bytes":4194304,"waiting_opens":0,"waits":{},"rejections":{},"bootstrap_credit_bytes":524288,"bootstrap_credit_limit_bytes":8388608,"growth_credit_bytes":0,"growth_credit_limit_bytes":25165824,"data_pending_frames":3,"data_pending_frame_limit":4096,"control_pending_frames":1,"control_pending_frame_limit":2048,"window_blocked_writers":0,"open_receive_credit_waits":0,"local_connections":3,"occupied_stream_slots":3,"closing_streams":0,"lifecycle_open_bidirectional":3,"lifecycle_opening":0,"lifecycle_half_closed":0,"lifecycle_wait_local_final_ack":0,"lifecycle_wait_peer_final":0,"lifecycle_both_final_wait_close":0,"lifecycle_wait_final_consumed":0,"lifecycle_closing_other":0,"data_idle_over_30s":0,"data_idle_over_1m":0,"data_idle_over_5m":0,"data_idle_over_10m":0,"oldest_data_idle_seconds":5,"idle_streams":2,"small_streams":1,"bulk_streams":0,"capability_revision":4,"session_tx_unconsumed_bytes":8192,"idle_actual_data_bytes":4096}}
        """
        let remoteEventA = try JSONDecoder().decode(EngineEvent.self,from:Data(remoteDiagnosticA.utf8))
        let remoteEventB = try JSONDecoder().decode(EngineEvent.self,from:Data(remoteDiagnosticB.utf8))
        precondition(model.consumeBundleProfileEvent(remoteEventA))
        precondition(model.consumeBundleProfileEvent(remoteEventB))
        precondition(model.provisioningDiagnostics["a"]?.connections == 7)
        precondition(model.provisioningDiagnostics["a"]?.resources?.active_streams == 11)
        precondition(model.provisioningDiagnostics["a"]?.configuredSchedulerMode == "weighted")
        precondition(model.provisioningDiagnostics["b"]?.connections == 3)
        precondition(model.provisioningDiagnostics["b"]?.resources?.active_streams == 3)
        precondition(model.provisioningDiagnostics["b"]?.effectiveSchedulerMode == "protect")
        model.append("Userspace 认证通过；实际带宽叠加取决于链路容量，不作为测速结论")
        precondition(model.logs.last?.contains("认证通过") == true)
        precondition(model.logs.last?.contains("测速") == false)
        for (name,mode,tab,api) in [("userspace-connect","userspace_multipath",0,false),("userspace-api","userspace_multipath",0,true),("userspace-bundle","userspace_multipath",0,true),("native-connect","native_mptcp",0,false),("userspace-paths","userspace_multipath",2,false),("userspace-paths-expanded","userspace_multipath",2,false),("userspace-bundle-paths","userspace_multipath",2,true),("userspace-bundle-paths-expanded","userspace_multipath",2,true)] {
            model.mode=mode;model.tab=tab;model.problem=nil;model.running=false;model.busy=false
            model.configurationSource=api ? "remote" : "local"
            model.provisioningURL=api ? "https://config.example.test/v1/bundle/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" : ""
            model.provisioningStatus=api ? "Synthetic Profile · 已同步 · 1" : "手动配置"
            model.provisioningRevision=api ? "1" : ""
            model.provisioningDisplayName=api ? "Synthetic Profile" : ""
            let isBundle = name == "userspace-bundle" || name == "userspace-bundle-paths"
            model.provisioningIsBundle = isBundle
            model.provisioningBundleMode = isBundle ? "parallel" : ""
            model.provisioningBundleID = isBundle ? "synthetic-bundle" : ""
            model.provisioningProfiles = isBundle ? [
                ProvisioningProfileChoice(id:"a",name:"HKBN",listenPort:1081,relayCount:5,mode:"userspace_multipath",backgroundResident:true),
                ProvisioningProfileChoice(id:"b",name:"HKT",listenPort:1082,relayCount:8,mode:"userspace_multipath",backgroundResident:true)
            ] : []
            model.provisioningSelectedProfileIDs = isBundle ? ["a","b"] : []
            model.provisioningRuntimeStatus = isBundle ? ["a":"已启动","b":"错误"] : [:]
            model.provisioningRuntimeError = isBundle ? ["b":"连接超时"] : [:]
            if isBundle { model.provisioningDisplayName="Synthetic Bundle"; model.provisioningRevision="r3" }
            model.localStreamResourceExpanded = name == "userspace-paths-expanded"
            model.localWindowResourceExpanded = name == "userspace-paths-expanded"
            model.profileStreamResourceExpanded = name == "userspace-bundle-paths-expanded" ? ["a"] : []
            model.profileWindowResourceExpanded = name == "userspace-bundle-paths-expanded" ? ["a"] : []
            let host=NSHostingView(rootView:DesktopView().environment(\.colorScheme,.light))
            let frame=NSRect(x:0,y:0,width:710,height:850)
            let window=NSWindow(contentRect:frame,styleMask:.borderless,backing:.buffered,defer:false)
            window.contentView=host;host.frame=frame;host.layoutSubtreeIfNeeded()
            RunLoop.main.run(until:Date(timeIntervalSinceNow:0.35))
            host.layoutSubtreeIfNeeded();host.displayIfNeeded()
            guard let bitmap=host.bitmapImageRepForCachingDisplay(in:host.bounds) else { throw ProfileError("UI bitmap unavailable") }
            host.cacheDisplay(in:host.bounds,to:bitmap)
            guard let png=bitmap.representation(using:.png,properties:[:]) else { throw ProfileError("UI PNG encoding failed") }
            try png.write(to:output.appendingPathComponent(name+".png"),options:.atomic)
            print("Rendered \(name): \(bitmap.pixelsWide)x\(bitmap.pixelsHigh), \(png.count) bytes")
            window.contentView=nil
        }
        print("PASS: persistent managed cache/fingerprint/48h policy/cache-first launch plan, full per-Profile diagnostics isolation, hidden remote endpoints, resource disclosures, and eight offscreen SwiftUI views")
    }
}
