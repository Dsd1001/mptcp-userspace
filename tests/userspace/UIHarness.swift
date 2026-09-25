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
        for (name,mode,tab) in [("userspace-connect","userspace_multipath",0),("native-connect","native_mptcp",0),("userspace-paths","userspace_multipath",2)] {
            model.mode=mode;model.tab=tab;model.problem=nil;model.running=false;model.busy=false
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
        print("PASS: legacy profile stays Native; schema 3 validation; weak-key and both-disabled rejection; three offscreen SwiftUI views")
    }
}
