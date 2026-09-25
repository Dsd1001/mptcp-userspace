import SwiftUI
import AppKit

final class Model: ObservableObject {
    static let shared = Model()
    @Published var relays = [RelayRow(host: "", port: 21001), RelayRow(host: "", port: 21002)]
    @Published var listenPort = "1081"
    @Published var mode = "userspace_multipath"
    @Published var schedulerMode = "auto"
    @Published var configuredSchedulerMode = ""
    @Published var effectiveSchedulerMode = ""
    @Published var schedulerModeSwitches: UInt64 = 0
    @Published var lastSchedulerModeReason = ""
    var configurationLocked: Bool { busy || running }
    var schedulerExplanation: String { SchedulerPolicy(rawValue:schedulerMode)?.explanation ?? "调度策略无效" }
    static func schedulerTitle(_ value: String) -> String { SchedulerPolicy(rawValue:value)?.title ?? "等待引擎回报" }
    @Published var transportKey = ""
    @Published var tcpEnabled = true
    @Published var udpEnabled = true
    @Published var tcpPaths: [PathMetric] = []
    @Published var udpPaths: [PathMetric] = []
    @Published var udpHealthyPaths = 0
    @Published var reorderBytes = 0
    @Published var reorderPeak = 0
    @Published var pendingBytes = 0
    @Published var retransmits: UInt64 = 0
    @Published var udpDropped: UInt64 = 0
    @Published var resources: ResourceMetric?
    @Published var lifecycle: LifecycleMetric?
    private var lastTransportEvent: UInt64 = 0
    var userspace: Bool { mode == "userspace_multipath" }
    @Published var udpConnections = 0
    @Published var udpSent: Int64 = 0
    @Published var udpReceived: Int64 = 0
    @Published var running = false
    @Published var busy = false
    @Published var status = "未连接"
    @Published var problem: String?
    @Published var logs: [String] = []
    @Published var paths = 0
    @Published var connections = 0
    @Published var sent: Int64 = 0
    @Published var received: Int64 = 0
    @Published var tab = 0
    private var process: Process?
    private var reader: FileHandle?
    private var pending = Data()
    private var forwardingActivity: NSObjectProtocol?

    private func endForwardingActivity() {
        if let activity = forwardingActivity {
            ProcessInfo.processInfo.endActivity(activity)
            forwardingActivity = nil
        }
    }

    init() {
        if ProcessInfo.processInfo.environment["MPTCP_DESK_SMOKE_TEST"] == "1" { return }
        if let data = UserDefaults.standard.data(forKey: "multipath-profile-v2"),
           var profile = try? JSONDecoder().decode(Profile.self, from: data) {
            apply(profile)
            do {
                if profile.userspace { profile.transport_key = try TransportKeyStore.load() }
                apply(profile)
                try profile.validate()
            } catch { problem = error.localizedDescription }
        } else if let data = UserDefaults.standard.data(forKey: "tcp-forward-profile-v1"),
                  let profile = try? JSONDecoder().decode(Profile.self, from: data), (try? profile.validate()) != nil {
            apply(profile)
            append("已载入 0.5.1 配置并保持 Native MPTCP；切换 Userspace 必须改用新版 Landing/Relay 入口")
        }
    }
    func apply(_ p: Profile) {
        guard !configurationLocked else { return }
        relays = p.relays; listenPort = String(p.listen_port)
        udpEnabled = p.udp_enabled ?? false
        tcpEnabled = p.tcp_enabled ?? true
        mode = p.userspace ? "userspace_multipath" : "native_mptcp"
        schedulerMode = p.userspace ? p.schedulerMode : "auto"
        transportKey = p.transport_key ?? ""
    }
    func profile() throws -> Profile {
        guard let port = Int(listenPort) else {throw Message("请输入本地 TCP 端口")}
        let cleaned = relays.map {RelayRow(host:$0.host.trimmingCharacters(in:.whitespacesAndNewlines),port:$0.port,download_mbps:$0.download_mbps,upload_mbps:$0.upload_mbps)}
        let p = Profile(schema_version:3,mode:mode,listen_port:port,relays:cleaned,udp_enabled:udpEnabled,tcp_enabled:tcpEnabled,transport_key:userspace ? transportKey.trimmingCharacters(in:.whitespacesAndNewlines) : nil,scheduler_mode:userspace ? schedulerMode : nil)
        try p.validate()
        return p
    }
    struct Message: LocalizedError { var text: String; init(_ text: String) {self.text = text}; var errorDescription: String? {text} }
    func save() {
        guard !configurationLocked else { return }
        do {
            let p = try profile()
            if p.userspace { try TransportKeyStore.save(p.transport_key ?? "") }
            UserDefaults.standard.set(try p.preferenceData(), forKey: "multipath-profile-v2")
            problem = nil
            append(userspace ? "Userspace 配置已保存；传输密钥保存在本机钥匙串" : "Native 配置已保存；旧 0.5.1 偏好记录未覆盖")
        } catch { problem = error.localizedDescription }
    }
    func loadProfileData(_ data: Data) throws {
        guard !configurationLocked else { throw Message("运行期间配置已锁定，请先停止转发") }
        guard data.count <= 32768 else { throw Message("配置文件过大") }
        let p = try JSONDecoder().decode(Profile.self, from:data)
        try p.validate()
        apply(p)
    }
    func receiveSchedulerEvent(_ event: EngineEvent) {
        if let value = event.configured_scheduler_mode { configuredSchedulerMode = value }
        if let value = event.effective_scheduler_mode { effectiveSchedulerMode = value }
        if let value = event.mode_switches { schedulerModeSwitches = value }
        if let value = event.last_mode_reason { lastSchedulerModeReason = value }
    }
    func importProfile() {
        guard !configurationLocked else { return }
        let panel = NSOpenPanel()
        panel.allowedContentTypes = [.json]
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = false
        if panel.runModal() == .OK, let url = panel.url {
            do {
                let data = try Data(contentsOf: url)
                try loadProfileData(data); save()
            } catch {problem = "无法导入配置：\(error.localizedDescription)"}
        }
    }
    func append(_ line: String) {
        let stamp = DateFormatter.localizedString(from: Date(), dateStyle: .none, timeStyle: .medium)
        logs.append("\(stamp)  \(line)")
        if logs.count > 150 {logs.removeFirst(logs.count - 150)}
    }
    private func keepDiagnostic(_ line: Data) {
        guard ProcessInfo.processInfo.environment["MPTCP_DESK_SMOKE_TEST"] != "1", line.count <= 131072 else { return }
        // Only decoded engine telemetry enters this file; profile/stdin and
        // transport credentials are never written. Keep just one bounded snapshot.
        do {
            let folder = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Logs/MPTCPDesk", isDirectory: true)
            try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            let file = folder.appendingPathComponent("latest-transport.json")
            try line.write(to: file, options: .atomic)
            try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
        } catch { /* Diagnostics must not terminate or delay forwarding. */ }
    }
    func launch(_ requestedAction: String) {
        guard !busy && !running else {return}
        let action = requestedAction == "doctor" && userspace ? "doctor-userspace" : requestedAction
        let checking = action.hasPrefix("doctor")
        guard let engine = Bundle.main.url(forResource: "mptcp-desktop-engine", withExtension: nil) else {problem = "安装包缺少传输引擎";return}
        do {
            var data = Data()
            if action == "run" { data = try JSONEncoder().encode(profile()); save(); if problem != nil {return} }
            problem = nil; busy = true; status = checking ? "检查环境中" : "连接中"
            udpConnections = 0; udpSent = 0; udpReceived = 0
            paths = 0; connections = 0; sent = 0; received = 0
            tcpPaths = []; udpPaths = []; udpHealthyPaths = 0
            reorderBytes = 0; reorderPeak = 0; pendingBytes = 0; retransmits = 0; udpDropped = 0
            resources = nil; lifecycle = nil; lastTransportEvent = 0
            configuredSchedulerMode = ""; effectiveSchedulerMode = ""; schedulerModeSwitches = 0; lastSchedulerModeReason = ""
            let child = Process(); child.executableURL = engine; child.arguments = [action]
            let output = Pipe(), input = Pipe(), errors = Pipe()
            child.standardOutput = output; child.standardInput = input; child.standardError = errors
            pending = Data(); reader = output.fileHandleForReading
            output.fileHandleForReading.readabilityHandler = { [weak self] handle in
                let bytes = handle.availableData
                guard !bytes.isEmpty else {handle.readabilityHandler = nil;return}
                DispatchQueue.main.async { self?.consume(bytes, process: child) }
            }
            errors.fileHandleForReading.readabilityHandler = { handle in _ = handle.availableData }
            child.terminationHandler = { [weak self] stopped in
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.15) {
                    guard let self = self, self.process === stopped else {return}
                    self.reader?.readabilityHandler = nil; self.reader = nil
                    self.process = nil; self.busy = false; self.running = false
                    self.endForwardingActivity()
                    self.connections = 0
                    self.udpConnections = 0
                    if stopped.terminationStatus != 0 && self.problem == nil {self.problem = "传输引擎已退出，请检查日志"}
                    self.status = self.problem == nil ? (checking ? "引擎环境检查通过" : "已停止") : "需要处理"
                }
            }
            process = child
            try child.run()
            if action == "run" {
                forwardingActivity = ProcessInfo.processInfo.beginActivity(options: .userInitiatedAllowingIdleSystemSleep, reason: "TCP / UDP forwarding")
            }
            if !data.isEmpty {try input.fileHandleForWriting.write(contentsOf: data)}
            try input.fileHandleForWriting.close()
        } catch {
            if let child = process, child.isRunning { child.terminate() }
            endForwardingActivity()
            process = nil;busy = false;running = false;status = "启动失败";problem = error.localizedDescription
        }
    }
    private func consume(_ bytes: Data, process child: Process) {
        guard process === child else {return}
        pending.append(bytes)
        if pending.count > 131072 {pending = Data();return}
        while let end = pending.firstIndex(of: 10) {
            let line = pending.prefix(upTo: end); pending.removeSubrange(...end)
            guard let event = try? JSONDecoder().decode(EngineEvent.self, from: line) else {continue}
            receiveSchedulerEvent(event)
            switch event.kind {
            case "listening": running = true; busy = false; status = userspace ? (tcpEnabled ? "Userspace 入口已启动" : "Userspace UDP 入口已启动") : "Native 入口已启动"
            case "error": problem = event.message;status = "连接失败"
            case "connecting": status = event.message ?? "连接中"
            case "ready": status = event.message ?? "环境可用"
            default: break
            }
            if event.kind == "stats" || event.kind == "listening" {
                paths = event.paths ?? 0;connections = event.connections ?? 0
                sent = event.sent ?? 0;received = event.received ?? 0
                tcpPaths = event.path_stats ?? []
                reorderBytes = event.reorder_bytes ?? 0; reorderPeak = event.reorder_peak ?? 0
                pendingBytes = event.pending_bytes ?? 0; retransmits = event.retransmits ?? 0
            }
            if let current = event.resources { resources = current }
            if let current = event.lifecycle {
                lifecycle = current
                for item in current.events ?? [] where item.sequence > lastTransportEvent {
                    if ["carrier_disconnected", "carrier_dial_failed", "session_closed", "stream_admission_timeout", "scheduler_mode_changed", "scheduler_path_role"].contains(item.kind) {
                        append("\(item.at) · \(item.kind) · 路径 \(item.path ?? 0) · \(item.reason ?? "")")
                    }
                }
                lastTransportEvent = current.event_sequence
            }
            if event.kind == "stats" || event.kind == "transport_closed" { keepDiagnostic(Data(line)) }
            if event.kind == "udp_stats" {
                udpConnections = event.connections ?? 0
                udpSent = event.sent ?? 0; udpReceived = event.received ?? 0
                udpPaths = event.path_stats ?? []; udpHealthyPaths = event.paths ?? 0
                udpDropped = event.dropped ?? 0
            }
            if event.kind != "stats", let text = event.message {append(text)}
        }
    }
    func stop() {
        guard let child = process else {return}
        status = "停止中"; busy = true
        child.terminate()
        DispatchQueue.main.asyncAfter(deadline: .now() + 2) { if child.isRunning {kill(child.processIdentifier, SIGKILL)} }
    }
    func quit() {
        defer { endForwardingActivity() }
        guard let child = process, child.isRunning else {return}
        child.terminate()
        for _ in 0..<20 {if !child.isRunning {return};usleep(50000)}
        kill(child.processIdentifier, SIGKILL)
    }
    func changeAggregation(enabled: Bool) {
        let alert = NSAlert()
        alert.messageText = enabled ? "开启 macOS 开发者聚合开关？" : "关闭 macOS 开发者聚合开关？"
        alert.informativeText = "将修改系统级 net.inet.mptcp.allow_aggregate，影响本机其他 MPTCP 程序，需要管理员授权。不会修改系统代理或创建 TUN。"
        alert.addButton(withTitle: "继续");alert.addButton(withTitle: "取消")
        guard alert.runModal() == .alertFirstButtonReturn else {return}
        let value = enabled ? "1" : "0"
        let script = NSAppleScript(source: "do shell script \"/usr/sbin/sysctl -w net.inet.mptcp.allow_aggregate=\(value)\" with administrator privileges")
        var error: NSDictionary?
        script?.executeAndReturnError(&error)
        if error != nil {problem = "系统设置未更改，可能取消了授权"}
        else {problem = nil;append(enabled ? "系统聚合开关已开启" : "系统聚合开关已关闭");launch("doctor")}
    }
}

struct DesktopView: View {
    @ObservedObject var model = Model.shared
    var locked: Bool {model.configurationLocked}
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(spacing: 12) {
                Image(systemName: "network").font(.system(size: 30)).foregroundColor(.teal)
                VStack(alignment: .leading, spacing: 3) {
                    Text("MPTCP Desk").font(.system(size: 23, weight: .semibold))
                    HStack {Circle().fill(model.running ? Color.green : Color.secondary).frame(width: 7, height: 7);Text(model.status).font(.system(size: 12)).foregroundColor(.secondary)}
                }
                Spacer()
                Text("Multipath 0.9.4 · Weighted 调度").font(.system(size: 11)).foregroundColor(.secondary)
            }
            Picker("视图", selection: $model.tab) {Text("连接").tag(0);Text("日志").tag(1);Text("路径诊断").tag(2)}.pickerStyle(.segmented)
            if model.tab == 0 {
                VStack(alignment: .leading, spacing: 12) {
                    Picker("传输模式", selection: $model.mode) {
                        Text("Userspace Multipath").tag("userspace_multipath")
                        Text("Native MPTCP（兼容）").tag("native_mptcp")
                    }.pickerStyle(.segmented).onChange(of: model.mode) { value in
                        if value == "native_mptcp" { model.tcpEnabled = true }
                    }
                    if model.userspace {
                        VStack(alignment:.leading,spacing:5) {
                            Picker("调度策略", selection:$model.schedulerMode) {
                                ForEach(SchedulerPolicy.allCases) { policy in Text(policy.title).tag(policy.rawValue) }
                            }.pickerStyle(.segmented).accessibilityIdentifier("scheduler-policy")
                            Text(model.schedulerExplanation + "  这是 Userspace 策略，不是 macOS 系统聚合开关。")
                                .font(.system(size:11)).foregroundColor(.secondary).fixedSize(horizontal:false,vertical:true)
                        }
                        SecureField("Landing 传输密钥（64 位十六进制，不是 SS 密码）", text: $model.transportKey)
                        Text("本版支持 MPX/3 Rev5 Weighted。Weighted 需要 Mac 与 Landing 均为 0.9.4；Auto / Aggregate / Protect 继续使用兼容的 Rev4 hello。不要连接 Native 或 SS 入口。")
                            .font(.system(size:11)).foregroundColor(.secondary).fixedSize(horizontal:false,vertical:true)
                    }
                    HStack {Text("本地转发入口").frame(width: 120, alignment: .leading);Text("127.0.0.1").foregroundColor(.secondary);TextField("端口", text: $model.listenPort).frame(width: 85);Spacer();Button {let p = NSPasteboard.general;p.clearContents();p.setString("127.0.0.1:\(model.listenPort)",forType:.string)} label:{Image(systemName:"doc.on.doc")}.help("复制本地 TCP 入口")}
                    HStack {
                        Toggle("TCP", isOn:$model.tcpEnabled).toggleStyle(.switch).disabled(!model.userspace)
                        Toggle(model.userspace ? "UDP 独立多路径" : "UDP 逐包轮询（旧版）", isOn:$model.udpEnabled).toggleStyle(.switch)
                        Spacer();Text("127.0.0.1:\(model.listenPort)").font(.system(size:12,design:.monospaced)).foregroundColor(.secondary)
                    }
                    Divider()
                    HStack {Text("Relay 路径").font(.headline);Spacer();Button{model.relays.append(RelayRow(host:"",port:21001))}label:{Image(systemName:"plus")}.help("添加 Relay").disabled(locked || model.relays.count >= 8)}
                    if model.userspace && model.schedulerMode == SchedulerPolicy.weighted.rawValue {
                        Text("Weighted：每条下行 Mbps 必填；上行 Mbps 可留空，留空时该方向继续自动估算。实时 RTT、故障与超时保护仍生效；UDP 不使用此权重。")
                            .font(.system(size:10)).foregroundColor(.secondary).fixedSize(horizontal:false,vertical:true)
                    }
                    ScrollView {
                        VStack(spacing: 8) {
                            ForEach($model.relays) { $relay in
                                HStack {
                                    Image(systemName:"server.rack").foregroundColor(.secondary)
                                    TextField("Relay IPv4",text:$relay.host)
                                    TextField("端口",value:$relay.port,formatter: Self.portFormatter).frame(width:85)
                                    if model.userspace && model.schedulerMode == SchedulerPolicy.weighted.rawValue {
                                        TextField("下行 Mbps*",value:$relay.download_mbps,formatter: Self.bandwidthFormatter).frame(width:95)
                                        TextField("上行 Mbps",value:$relay.upload_mbps,formatter: Self.bandwidthFormatter).frame(width:95)
                                    }
                                    Button{model.relays.removeAll{$0.id == relay.id}}label:{Image(systemName:"minus.circle")}.help("移除 Relay").disabled(model.relays.count <= 2)
                                }.disabled(locked)
                            }
                        }
                    }.frame(height: 150)
                    Divider()
                }.disabled(locked)
                HStack(spacing:20) {
                    metric(model.userspace ? "TCP 载路" : "Native 子流",model.paths < 0 ? "未知" : String(model.paths));metric("连接",String(model.connections))
                    metric("上传",ByteCountFormatter.string(fromByteCount:model.sent,countStyle:.binary))
                    metric("下载",ByteCountFormatter.string(fromByteCount:model.received,countStyle:.binary))
                }.padding(.vertical,5)
                if model.udpEnabled {
                    HStack(spacing:20) {
                        metric(model.userspace ? "UDP 载路 / 映射" : "UDP 映射",model.userspace ? "\(model.udpHealthyPaths) / \(model.udpConnections)" : String(model.udpConnections))
                        metric("UDP 上传",ByteCountFormatter.string(fromByteCount:model.udpSent,countStyle:.binary))
                        metric("UDP 下载",ByteCountFormatter.string(fromByteCount:model.udpReceived,countStyle:.binary))
                    }
                }
            } else if model.tab == 1 {
                ScrollView {Text(model.logs.joined(separator:"\n")).font(.system(size:11,design:.monospaced)).textSelection(.enabled).frame(maxWidth:.infinity,alignment:.topLeading)}
                    .frame(maxWidth:.infinity,maxHeight:.infinity)
            } else {
                VStack(alignment:.leading,spacing:12) {
                    if model.userspace {
                        Text("配置策略：\(Model.schedulerTitle(model.configuredSchedulerMode)) · 当前策略：\(Model.schedulerTitle(model.effectiveSchedulerMode)) · 自动切换：\(model.schedulerModeSwitches)")
                            .font(.system(size:12,weight:.medium)).accessibilityIdentifier("scheduler-status")
                        Text("本端发送方向 · \(model.lastSchedulerModeReason.isEmpty ? "等待引擎诊断" : model.lastSchedulerModeReason)")
                            .font(.system(size:11)).foregroundColor(.secondary).fixedSize(horizontal:false,vertical:true)
                        HStack(spacing:16) {
                            metric("当前重排",Self.bytes(model.reorderBytes))
                            metric("重排峰值",Self.bytes(model.reorderPeak))
                            metric("等待确认",Self.bytes(model.pendingBytes))
                        }
                        if let resource = model.resources {
                            Text("入口 TCP \(resource.local_connections ?? 0) · MPX 占槽 \(resource.occupied_stream_slots ?? resource.active_streams)/\(resource.stream_limit) · 活跃身份 \(resource.active_streams) · closing \(resource.closing_streams ?? 0)")
                                .font(.system(size:11,design:.monospaced))
                            Text("双向开放 \(resource.lifecycle_open_bidirectional ?? 0) · 建流中 \(resource.lifecycle_opening ?? 0) · 半关闭 \(resource.lifecycle_half_closed ?? 0) · 等本端终态 ACK \(resource.lifecycle_wait_local_final_ack ?? 0)")
                                .font(.system(size:11,design:.monospaced))
                            Text("等对端终态 \(resource.lifecycle_wait_peer_final ?? 0) · 双向终态待 Close \(resource.lifecycle_both_final_wait_close ?? 0) · 等 FINAL_CONSUMED \(resource.lifecycle_wait_final_consumed ?? 0) · 其他 closing \(resource.lifecycle_closing_other ?? 0)")
                                .font(.system(size:11,design:.monospaced)).foregroundColor(.secondary)
                            Text("DATA 静默 >30s / >1m / >5m / >10m：\(resource.data_idle_over_30s ?? 0) / \(resource.data_idle_over_1m ?? 0) / \(resource.data_idle_over_5m ?? 0) / \(resource.data_idle_over_10m ?? 0) · 最老静默 \(resource.oldest_data_idle_seconds ?? 0)s")
                                .font(.system(size:11,design:.monospaced)).foregroundColor(.secondary)
                            Text("待确认帧 \(resource.pending_frames)/\(resource.pending_frame_limit) · 建流中 \(resource.waiting_opens)")
                                .font(.system(size:11,design:.monospaced))
                            Text("接收未消费 DATA \(Self.bytes(resource.receive_credit_bytes))/\(Self.bytes(resource.receive_credit_limit_bytes)) · 实际分页 \(Self.bytes(resource.receive_allocated_bytes))/\(Self.bytes(resource.receive_allocated_limit_bytes))")
                                .font(.system(size:11,design:.monospaced)).foregroundColor(.secondary)
                            Text("实际基础占用 \(Self.bytes(resource.bootstrap_credit_bytes ?? 0))/\(Self.bytes(resource.bootstrap_credit_limit_bytes ?? 0)) · 实际增长占用 \(Self.bytes(resource.growth_credit_bytes ?? 0))/\(Self.bytes(resource.growth_credit_limit_bytes ?? 0))")
                                .font(.system(size:11,design:.monospaced)).foregroundColor(.secondary)
                            Text("DATA 帧 \(resource.data_pending_frames ?? 0)/\(resource.data_pending_frame_limit ?? 0) · 控制帧 \(resource.control_pending_frames ?? 0)/\(resource.control_pending_frame_limit ?? 0) · 等窗口写入 \(resource.window_blocked_writers ?? 0)")
                                .font(.system(size:11,design:.monospaced))
                            Text("窗口需求 idle / small / bulk：\(resource.idle_streams ?? 0) / \(resource.small_streams ?? 0) / \(resource.bulk_streams ?? 0) · OPEN 信用等待：\(resource.open_receive_credit_waits ?? 0)")
                                .font(.system(size:11,design:.monospaced)).foregroundColor(.secondary)
                            if let reason = resource.last_reason {
                                Text("最近资源事件：\(reason) · \(resource.last_limit_at ?? "")")
                                    .font(.system(size:10)).foregroundColor(.secondary).textSelection(.enabled)
                            }
                        }
                        if let resource = model.resources, let rev = resource.capability_revision, rev >= 2 {
                            Text("Rev\(rev) · 待结算 \(resource.closing_streams ?? 0) 流 · 发送未消费 \(Self.bytes(resource.session_tx_unconsumed_bytes ?? 0)) · 空闲 DATA \(Self.bytes(resource.idle_actual_data_bytes ?? 0))")
                                .font(.system(size:10,design:.monospaced)).foregroundColor(.secondary)
                        }
                        Text("TCP 重传：\(model.retransmits) · UDP 丢弃/超时事件：\(model.udpDropped)")
                            .font(.system(size:12)).foregroundColor(.secondary)
                        Text("Goodput 为确认数据估计值，包含启动估计，不是测速结果；UDP 不重传丢失报文。")
                            .font(.system(size:11)).foregroundColor(.secondary)
                        ScrollView {
                            VStack(alignment:.leading,spacing:14) {
                                pathSection("TCP 长期载路",model.tcpPaths)
                                if model.udpEnabled { pathSection("UDP 独立数据报路径",model.udpPaths) }
                            }.frame(maxWidth:.infinity,alignment:.leading)
                        }
                    } else {
                        Text("Native 模式沿用 0.5.1 的内核子流统计；MPX 的 RTT、Goodput 和重排统计仅用于 Userspace 模式。")
                            .font(.system(size:13)).foregroundColor(.secondary)
                    }
                }.frame(maxWidth:.infinity,maxHeight:.infinity,alignment:.topLeading)
            }
            if let problem = model.problem {
                Label(problem,systemImage:"exclamationmark.triangle.fill").font(.system(size:12)).foregroundColor(.red).fixedSize(horizontal:false,vertical:true)
            }
            Spacer(minLength:0)
            Divider()
            HStack {
                Button{model.importProfile()}label:{Image(systemName:"square.and.arrow.down")}.help("导入配置").disabled(locked)
                Button{model.save()}label:{Image(systemName:"square.and.arrow.down.on.square")}.help("保存配置").disabled(locked)
                Menu {Button("开启系统聚合…"){model.changeAggregation(enabled:true)};Button("关闭系统聚合…"){model.changeAggregation(enabled:false)}} label:{Image(systemName:"gearshape")}.frame(width:42).help("仅 Native 模式需要系统聚合设置").disabled(locked || model.userspace)
                Spacer()
                Button("检查环境"){model.launch("doctor")}.disabled(locked)
                if model.running || model.busy {
                    Button{model.stop()}label:{Label("停止",systemImage:"stop.fill")}
                } else {
                    Button{model.launch("run")}label:{Label("启动",systemImage:"play.fill")}.buttonStyle(.borderedProminent)
                }
            }
        }.padding(22).frame(minWidth:650,idealWidth:710,maxWidth:900,minHeight:800,idealHeight:850)
    }
    func metric(_ label:String,_ value:String)->some View {VStack(alignment:.leading,spacing:4){Text(label).font(.system(size:11)).foregroundColor(.secondary);Text(value).font(.system(size:16,weight:.medium,design:.monospaced))}.frame(maxWidth:.infinity,alignment:.leading)}
    static func bytes(_ count:Int)->String { ByteCountFormatter.string(fromByteCount:Int64(count),countStyle:.binary) }
    func pathSection(_ title:String,_ paths:[PathMetric])->some View {
        VStack(alignment:.leading,spacing:8) {
            Text(title).font(.headline)
            if paths.isEmpty { Text("尚无路径数据；启动后自动更新").font(.system(size:12)).foregroundColor(.secondary) }
            ForEach(paths) { path in
                VStack(alignment:.leading,spacing:4) {
                    HStack {
                        Circle().fill(path.connected ? Color.green : Color.secondary).frame(width:7,height:7)
                        Text("\(path.id) · \(path.address)" + (path.role.map { " · " + $0.uppercased() } ?? "")).font(.system(size:12,design:.monospaced))
                        Spacer();Text(path.connected ? "在线" : "离线/重连中").font(.system(size:11)).foregroundColor(.secondary)
                    }
                    Text(String(format:"RTT %.1f ms · Goodput %.2f MiB/s%@ · 队列 %@ · 在途 %@ · 错误 %llu",path.rtt_ms,path.goodput_bps/1048576,(path.configured_rate_bps ?? 0) > 0 ? String(format:" · Weighted %.1f Mbps",(path.configured_rate_bps ?? 0)*8/1000000) : "",Self.bytes(path.queue_bytes),Self.bytes(path.outstanding_bytes),path.errors))
                        .font(.system(size:11,design:.monospaced)).foregroundColor(.secondary)
                    if let error = path.last_error, !error.isEmpty {
                        Text(error).font(.system(size:10)).foregroundColor(.secondary).lineLimit(2).help(error)
                    }
                }.padding(8).background(Color.secondary.opacity(0.06)).cornerRadius(6)
            }
        }
    }
    static let portFormatter: NumberFormatter = {let f=NumberFormatter();f.numberStyle = .none;f.minimum=1;f.maximum=65535;f.allowsFloats=false;return f}()
    static let bandwidthFormatter: NumberFormatter = {let f=NumberFormatter();f.numberStyle = .decimal;f.minimum=0.1;f.maximum=6553.5;f.minimumFractionDigits=0;f.maximumFractionDigits=1;f.allowsFloats=true;return f}()
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationShouldTerminateAfterLastWindowClosed(_ sender:NSApplication)->Bool {false}
    func applicationWillTerminate(_ notification:Notification){Model.shared.quit()}
}

struct StatusMenu: View {
    @ObservedObject var model = Model.shared
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        Text(model.status)
        if let problem = model.problem { Text(problem) }
        if model.running {
            Text("TCP 连接：\(model.connections)")
            if model.udpEnabled { Text("UDP 映射：\(model.udpConnections)") }
        }
        Divider()
        Button("打开主窗口") {
            openWindow(id: "main")
            NSApplication.shared.activate(ignoringOtherApps: true)
        }
        if model.running || model.busy {
            Button("停止转发") { model.stop() }.disabled(model.status == "停止中")
        } else {
            Button("启动转发") { model.launch("run") }
        }
        Divider()
        Button("退出 MPTCP Desk") { NSApplication.shared.terminate(nil) }.keyboardShortcut("q")
    }
}

#if !UI_TEST
@main struct MPTCPDesktop: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var delegate
    var body: some Scene {
        Window("MPTCP Desk", id: "main") {DesktopView()}.windowResizability(.contentMinSize)
            .commands {CommandGroup(replacing:.newItem) {};CommandGroup(replacing:.appInfo) {Button("关于 MPTCP Desk"){NSApplication.shared.orderFrontStandardAboutPanel()}}}
        MenuBarExtra("MPTCP Desk", systemImage: "network") { StatusMenu() }
    }
}
#endif
