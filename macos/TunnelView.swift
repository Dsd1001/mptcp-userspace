import SwiftUI
import AppKit

private struct TunnelPanel<Content: View>: View {
    let title: String
    let subtitle: String
    let content: Content
    init(_ title: String, subtitle: String = "", @ViewBuilder content: () -> Content) {
        self.title = title; self.subtitle = subtitle; self.content = content()
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            VStack(alignment: .leading, spacing: 4) {
                Text(title).font(.headline)
                if !subtitle.isEmpty { Text(subtitle).font(.caption).foregroundStyle(.secondary) }
            }
            content
        }
        .padding(18).frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 14))
        .overlay(RoundedRectangle(cornerRadius: 14).stroke(Color.secondary.opacity(0.15)))
    }
}
private struct TunnelMetricTile: View {
    let title: String
    let value: String
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title).font(.caption).foregroundStyle(.secondary)
            Text(value).font(.system(size: 24, weight: .semibold, design: .rounded)).monospacedDigit()
        }.frame(maxWidth: .infinity, alignment: .leading).padding(15)
            .background(Color.accentColor.opacity(0.07), in: RoundedRectangle(cornerRadius: 12))
    }
}

struct TunnelDesktopView: View {
    @ObservedObject var model = Model.shared
    @ObservedObject var updater = AppUpdater.shared
    private var locked: Bool { model.configurationLocked }
    private var remote: Bool { model.remoteConfigurationSelected }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            HStack(spacing:8) {
                navigationButton("共享隧道", symbol:"point.3.connected.trianglepath.dotted", tab:0)
                navigationButton("运行状态", symbol:"waveform.path.ecg", tab:1)
                navigationButton("远程管理", symbol:"desktopcomputer", tab:2)
                navigationButton("日志", symbol:"text.alignleft", tab:3)
                Spacer()
            }.padding(.horizontal,24).padding(.vertical,12)
            Divider()
            Group {
                switch model.tunnelSelectedTab {
                case 1: ScrollView { diagnostics.padding(22) }
                case 2: ScrollView { management.padding(22) }
                case 3: logs
                default: ScrollView { configuration.padding(22) }
                }
            }.frame(maxWidth:.infinity,maxHeight:.infinity)

        }
        .frame(minWidth: 1040, minHeight: 740)
        .background(Color(nsColor: .windowBackgroundColor))
    }
    private func navigationButton(_ title: String, symbol: String, tab: Int) -> some View {
        Button { model.tunnelSelectedTab = tab } label: {
            Label(title,systemImage:symbol)
                .font(.subheadline.weight(model.tunnelSelectedTab == tab ? .semibold:.regular))
                .padding(.horizontal,15).padding(.vertical,9)
                .foregroundStyle(model.tunnelSelectedTab == tab ? Color.accentColor:Color.primary)
                .background(model.tunnelSelectedTab == tab ? Color.accentColor.opacity(0.12):Color.clear,in:RoundedRectangle(cornerRadius:9))
        }.buttonStyle(.plain)
    }
    private var header: some View {
        HStack(spacing: 15) {
            Image(systemName: "point.3.connected.trianglepath.dotted").font(.system(size: 30)).foregroundStyle(Color.accentColor)
            VStack(alignment: .leading, spacing: 4) {
                Text("MPTCP Tunnel").font(.title2.bold())
                Text("一条共享隧道 · 多业务入口 · MPX/4 多路径聚合").font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            VStack(alignment: .trailing, spacing: 4) {
                Text(model.status).font(.subheadline.weight(.semibold))
                Text("\(model.paths) / \(model.relays.count) 条 Carrier").font(.caption.monospacedDigit()).foregroundStyle(.secondary)
            }
            if model.running || model.busy {
                Button("停止隧道", role: .destructive) { model.stop() }.disabled(model.status == "停止中")
            } else {
                Button("连接隧道") { model.startForwarding() }.buttonStyle(.borderedProminent).disabled(model.provisioningSyncing)
            }
        }.padding(.horizontal, 24).padding(.vertical, 18)
    }
    private var configuration: some View {
        VStack(alignment: .leading, spacing: 18) {
            if let problem = model.problem {
                Label(problem, systemImage: "exclamationmark.triangle.fill").font(.callout).foregroundStyle(.orange)
                    .padding(12).frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color.orange.opacity(0.08), in: RoundedRectangle(cornerRadius: 10))
            }
            HStack {
                Picker("配置来源", selection: Binding(get: { model.configurationSource }, set: { model.setConfigurationSource($0) })) {
                    Text("本地配置").tag("local")
                    Text("远端控制器").tag("remote")
                }.pickerStyle(.segmented).frame(width: 300).disabled(locked)
                Spacer()
                if !remote {
                    Button("导入隧道 JSON") { model.importProfile() }.disabled(locked)
                    Button("保存") { model.save() }.disabled(locked)
                }
            }
            if remote { remoteConfiguration }
            TunnelPanel("共享传输", subtitle: "所有业务入口共享同一 Session。Relay 只转发 Carrier，最终汇聚到同一个隧道出口。") {
                HStack {
                    Text("Transport Key").frame(width: 110, alignment: .leading)
                    Group {
                        if model.tunnelRevealKey { TextField("共同隧道出口的 64 位密钥", text: $model.transportKey) }
                        else { SecureField("共同隧道出口的 64 位密钥", text: $model.transportKey) }
                    }.textFieldStyle(.roundedBorder).font(.system(.body, design: .monospaced)).disabled(locked || remote)
                    Button { model.tunnelRevealKey.toggle() } label: { Image(systemName: model.tunnelRevealKey ? "eye.slash" : "eye") }.help("显示或隐藏密钥")
                }
                HStack {
                    Picker("路径调度", selection: $model.schedulerMode) {
                        ForEach(SchedulerPolicy.allCases) { Text($0.title).tag($0.rawValue) }
                    }.frame(width: 270).disabled(locked || remote)
                    Text(model.schedulerExplanation).font(.caption).foregroundStyle(.secondary)
                }
                relayEditor
            }
            TunnelPanel("业务入口", subtitle: "入口使用普通 TCP；UDP 经 UoT 复用同一组 Carrier。路由编号必须与共同出口的白名单一致。") {
                if remote {
                    if model.provisioningProfiles.isEmpty {
                        Text("同步远端隧道后可选择业务入口。").foregroundStyle(.secondary)
                    } else {
                        ForEach(model.provisioningProfiles) { inlet in
                            HStack {
                                Toggle(isOn: Binding(get: { model.provisioningSelectedProfileIDs.contains(inlet.id) }, set: { model.setProvisioningProfileSelected(inlet.id, selected: $0) })) {
                                    Text(inlet.name).fontWeight(.medium)
                                }.disabled(locked)
                                Spacer()
                                Text("127.0.0.1:\(String(inlet.listenPort))").font(.system(.body, design: .monospaced))
                                Text(inlet.uotEnabled ? "TCP / UoT" : "TCP").font(.caption).foregroundStyle(.secondary)
                            }.padding(.vertical, 4)
                        }
                        Text("所选 \(model.provisioningSelectedProfileIDs.count) 个入口共用 \(model.relays.count) 条 Carrier，不按入口重复建立隧道。").font(.caption).foregroundStyle(.secondary)
                    }
                } else {
                    inletEditor
                    Button { model.addTunnelInlet() } label: { Label("添加业务入口", systemImage: "plus") }.disabled(locked || model.tunnelInlets.count >= 64)
                }
            }
            HStack {
                Toggle("后台常驻 / 登录恢复", isOn: Binding(get: { model.backgroundResident }, set: { model.setBackgroundResident($0) }))
                Text(model.backgroundResidentStatus).font(.caption).foregroundStyle(.secondary)
                Spacer()
                Button("环境检查") { model.launch("doctor") }.disabled(locked)
                Button("检查更新") { updater.checkForUpdates() }
            }
            Text("1.2.0 仅支持共享隧道配置。旧的独立 Profile 和多组 MPX Bundle 不自动转换；本地密钥保存在钥匙串。")
                .font(.caption).foregroundStyle(.secondary)
        }
    }
    private var remoteConfiguration: some View {
        TunnelPanel("远端隧道配置", subtitle: "API 地址包含访问凭据，请勿公开。缓存启动与后台同步不会建立第二套传输隧道。") {
            HStack {
                Group {
                    if model.tunnelRevealURL { TextField("https://controller/v1/tunnel/…", text: $model.provisioningURL) }
                    else { SecureField("https://controller/v1/tunnel/…", text: $model.provisioningURL) }
                }.textFieldStyle(.roundedBorder).disabled(locked)
                Button { model.tunnelRevealURL.toggle() } label: { Image(systemName: model.tunnelRevealURL ? "eye.slash" : "eye") }
                Button("保存地址") { model.saveProvisioningURL() }.disabled(locked)
                Button(model.provisioningSyncing ? "同步中…" : "同步配置") { model.syncProvisioning() }.disabled(model.provisioningSyncing)
            }
            Text(model.provisioningStatus).font(.caption).foregroundStyle(.secondary)
            if model.provisioningUpdatePending {
                Label("新配置已缓存；停止并重新连接后生效，不会中断当前流量。", systemImage: "arrow.triangle.2.circlepath").font(.caption).foregroundStyle(.orange)
            }
        }
    }
    private var relayEditor: some View {
        VStack(spacing: 9) {
            HStack {
                Text("Relay 地址 / 域名").frame(maxWidth: .infinity, alignment: .leading)
                Text("端口").frame(width: 85)
                Text("下行 Mbps").frame(width: 110)
                Text("上行 Mbps").frame(width: 110)
                if !remote { Color.clear.frame(width: 28, height: 1) }
            }.font(.caption).foregroundStyle(.secondary)
            ForEach(Array(model.relays.indices), id: \.self) { index in
                HStack(spacing: 9) {
                    TextField("relay.example.com", text: $model.relays[index].host).textFieldStyle(.roundedBorder)
                    TextField("端口", value: $model.relays[index].port, format: .number.grouping(.never)).textFieldStyle(.roundedBorder).frame(width: 85)
                    TextField("自动", value: $model.relays[index].download_mbps, formatter: Self.capacityFormatter).textFieldStyle(.roundedBorder).frame(width: 110)
                    TextField("自动", value: $model.relays[index].upload_mbps, formatter: Self.capacityFormatter).textFieldStyle(.roundedBorder).frame(width: 110)
                    if !remote {
                        Button { model.relays.remove(at: index) } label: { Image(systemName: "minus.circle") }.buttonStyle(.borderless).disabled(model.relays.count <= 2)
                    }
                }.disabled(locked || remote)
            }
            if !remote {
                HStack {
                    Button { model.relays.append(RelayRow(host: "", port: 24001)) } label: { Label("添加 Relay", systemImage: "plus") }.disabled(locked || model.relays.count >= 8)
                    Spacer()
                    Text("\(model.relays.count) 条共享路径 / 最多 8 条").font(.caption).foregroundStyle(.secondary)
                }
            }
        }
    }
    private var inletEditor: some View {
        VStack(spacing: 12) {
            ForEach(Array(model.tunnelInlets.indices), id: \.self) { index in
                VStack(alignment: .leading, spacing: 9) {
                    HStack {
                        TextField("入口 ID", text: $model.tunnelInlets[index].id).frame(width: 135)
                        TextField("显示名称", text: Binding(get: { model.tunnelInlets[index].name ?? "" }, set: { model.tunnelInlets[index].name = $0 }))
                        Toggle("TCP", isOn: Binding(get: { model.tunnelInlets[index].tcpEnabled }, set: { model.tunnelInlets[index].tcp_enabled = $0 })).toggleStyle(.checkbox)
                        Toggle("UDP / UoT", isOn: Binding(get: { model.tunnelInlets[index].uotEnabled }, set: { model.tunnelInlets[index].uot_enabled = $0 })).toggleStyle(.checkbox)
                        Button { model.tunnelInlets.remove(at: index) } label: { Image(systemName: "trash") }.buttonStyle(.borderless).disabled(model.tunnelInlets.count <= 1)
                    }
                    HStack(spacing: 12) {
                        Text("本地端口").font(.caption)
                        TextField("端口", value: $model.tunnelInlets[index].listen_port, format: .number.grouping(.never)).frame(width: 80)
                        Text("路由 ID").font(.caption)
                        TextField("路由", value: $model.tunnelInlets[index].route_id, format: .number.grouping(.never)).frame(width: 90)
                        Text("权重").font(.caption)
                        TextField("1", value: Binding(get: { model.tunnelInlets[index].weight ?? 1 }, set: { model.tunnelInlets[index].weight = $0 }), format: .number.grouping(.never)).frame(width: 55)
                        Text("连接上限").font(.caption)
                        TextField("512", value: Binding(get: { model.tunnelInlets[index].max_connections ?? 512 }, set: { model.tunnelInlets[index].max_connections = $0 }), format: .number.grouping(.never)).frame(width: 70)
                        Spacer()
                    }
                }.textFieldStyle(.roundedBorder).padding(12)
                    .background(Color.secondary.opacity(0.045), in: RoundedRectangle(cornerRadius: 10)).disabled(locked)
            }
        }
    }
    private var diagnostics: some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack(spacing: 12) {
                TunnelMetricTile(title: "共享 Carrier", value: "\(model.paths) / \(model.relays.count)")
                TunnelMetricTile(title: "业务 TCP 连接", value: "\(model.connections)")
                TunnelMetricTile(title: "发送有效载荷", value: Self.bytes(model.sent))
                TunnelMetricTile(title: "接收有效载荷", value: Self.bytes(model.received))
            }
            TunnelPanel("Carrier 路径", subtitle: "所有入口都可以使用全部可用路径；不把入口固定分配给某台 Relay。") {
                if model.tcpPaths.isEmpty { Text("连接后显示各路径状态。").foregroundStyle(.secondary) }
                ForEach(model.tcpPaths) { path in
                    HStack {
                        Circle().fill(path.connected ? Color.green : Color.secondary).frame(width: 7, height: 7)
                        Text("#\(path.id)  \(path.address)").font(.system(.callout, design: .monospaced)).frame(maxWidth: .infinity, alignment: .leading)
                        Text(String(format: "%.1f ms", path.rtt_ms)).monospacedDigit().frame(width: 85, alignment: .trailing)
                        Text("↑ \(Self.bytes(Int64(clamping: path.sent)))").frame(width: 115, alignment: .trailing)
                        Text("↓ \(Self.bytes(Int64(clamping: path.received)))").frame(width: 115, alignment: .trailing)
                    }.font(.caption)
                }
            }
            TunnelPanel("入口隔离与队列", subtitle: "按路由公平发送，接收按实际占用计数。资源极限只终止超限业务连接，不关闭整条隧道。") {
                ForEach(model.tunnelInlets) { inlet in
                    let route = model.tunnelRouteMetrics.first { $0.route_id == inlet.route_id }
                    let runtime = model.tunnelInletMetrics.first { $0.id == inlet.id }
                    HStack {
                        VStack(alignment: .leading, spacing: 3) {
                            Text(inlet.displayName).fontWeight(.medium)
                            Text("127.0.0.1:\(String(inlet.listen_port)) → 路由 \(inlet.route_id)").font(.caption).foregroundStyle(.secondary)
                        }.frame(maxWidth: .infinity, alignment: .leading)
                        Text("\(runtime?.active_connections ?? 0) TCP / \(runtime?.udp_connections ?? 0) UDP").font(.caption).frame(width: 140)
                        VStack(alignment: .trailing, spacing: 3) {
                            Text("待调度 \(Self.bytes(Int64(route?.ready_data_bytes ?? 0)))")
                            Text("接收内存 \(Self.bytes(Int64(route?.receive_allocated_bytes ?? 0)))")
                        }.font(.caption).frame(width: 150)
                        Text("保护重置 \(route?.receive_pressure_resets ?? 0)").font(.caption).frame(width: 100)
                    }.padding(.vertical, 5)
                }
            }
            Text("重注入 \(model.retransmits) · 待确认 \(Self.bytes(Int64(model.pendingBytes))) · 调度 \(Model.schedulerTitle(model.effectiveSchedulerMode))")
                .font(.caption).foregroundStyle(.secondary)
        }
    }
    private var management: some View {
        VStack(alignment: .leading, spacing: 18) {
            TunnelPanel("远程设备管理", subtitle: "仅在本机授权后启用；支持配置分配、启动/停止、同步、重启和软件更新，不提供远程 Shell。") {
                Toggle("允许此设备接受远端控制", isOn: Binding(get: { model.remoteManagementEnabled }, set: { model.setRemoteManagementEnabled($0) }))
                HStack {
                    TextField("https://controller.example.com", text: $model.remoteControlServer).textFieldStyle(.roundedBorder)
                    Button("保存服务器") { model.saveRemoteControlServer() }
                }
                HStack {
                    SecureField("一次性配对码", text: $model.remotePairingCode).textFieldStyle(.roundedBorder)
                    Button("配对") { model.pairRemoteManagement() }
                    Button("解除配对", role: .destructive) { model.unpairRemoteManagement() }
                }
                Text("\(model.remoteControlStatus) · 设备 \(model.remoteDeviceID.isEmpty ? "未配对" : model.remoteDeviceID)").font(.caption).textSelection(.enabled)
            }
            TunnelPanel("后台与更新") {
                Toggle("后台常驻 / 登录后恢复", isOn: Binding(get: { model.backgroundResident }, set: { model.setBackgroundResident($0) }))
                Text(model.backgroundResidentStatus).font(.caption).foregroundStyle(.secondary)
                Button("检查签名更新") { updater.checkForUpdates() }
            }
        }
    }
    private var logs: some View {
        VStack(alignment: .leading) {
            HStack { Text("传输与设备事件").font(.headline); Spacer(); Button("清空") { model.logs.removeAll() } }.padding(.horizontal, 22).padding(.top, 16)
            ScrollView {
                Text(model.logs.isEmpty ? "暂无日志" : model.logs.joined(separator: "\n"))
                    .font(.system(.caption, design: .monospaced)).textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading).padding(22)
            }
        }
    }
    private static func bytes(_ count: Int64) -> String { let f = ByteCountFormatter(); f.countStyle = .binary; f.allowsNonnumericFormatting = false; return f.string(fromByteCount:count) }
    private static let capacityFormatter: NumberFormatter = {
        let f = NumberFormatter(); f.numberStyle = .decimal; f.minimum = 0.1; f.maximum = 6553.5; f.maximumFractionDigits = 1; return f
    }()
}
