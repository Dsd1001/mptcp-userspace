import SwiftUI
import Foundation
import AppKit
import Network
import ServiceManagement

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
    var configurationLocked: Bool { busy || running || provisioningSyncing }
    var schedulerExplanation: String { SchedulerPolicy(rawValue:schedulerMode)?.explanation ?? "调度策略无效" }
    static func schedulerTitle(_ value: String) -> String { SchedulerPolicy(rawValue:value)?.title ?? "等待引擎回报" }
    @Published var transportKey = ""
    @Published var configurationSource = "local"
    @Published var provisioningURL = ""
    @Published var provisioningSyncing = false
    @Published var provisioningStatus = "手动配置"
    @Published var provisioningRevision = ""
    @Published var provisioningDisplayName = ""
    @Published var provisioningIsBundle = false
    @Published var provisioningBundleMode = ""
    @Published var provisioningBundleID = ""
    @Published var provisioningProfiles: [ProvisioningProfileChoice] = []
    @Published var provisioningSelectedProfileIDs = Set<String>()
    @Published var provisioningRuntimeStatus: [String:String] = [:]
    @Published var provisioningRuntimeError: [String:String] = [:]
    @Published var provisioningTCPPaths: [String:[PathMetric]] = [:]
    @Published var provisioningUDPPaths: [String:[PathMetric]] = [:]
    private var lastProvisioningBundle: RelayProvisioningBundlePayload?
    private static let bundleSelectionPrefix = "provisioning-bundle-selection-v1."
    private static let configurationSourceKey = "configuration-source-v1"
    var remoteConfigurationSelected: Bool { configurationSource == "remote" }
    var provisioningManaged: Bool { remoteConfigurationSelected && !provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
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
    @Published var backgroundResident = false
    @Published var backgroundResidentStatus = "关闭"
    private let networkMonitor = NWPathMonitor()
    private let networkQueue = DispatchQueue(label: "org.mptcp.desktop.network-monitor")
    private var workspaceObservers: [NSObjectProtocol] = []
    private var recoveryWorkItem: DispatchWorkItem?
    private var recoveryAttempt = 0
    private var wantsForwarding = false
    private var sleeping = false
    private var quitting = false
    private var manualStopRequested = false
    private var needsRecovery = false
    private var networkAvailable = false
    private static let residentKey = "background-resident-v1"
    private static let wantsForwardingKey = "background-resident-wants-forwarding-v1"

    private func endForwardingActivity() {
        if let activity = forwardingActivity {
            ProcessInfo.processInfo.endActivity(activity)
            forwardingActivity = nil
        }
    }

    private func setWantsForwarding(_ value: Bool) {
        wantsForwarding = value
        UserDefaults.standard.set(value, forKey: Self.wantsForwardingKey)
    }
    private var shouldRecover: Bool {
        BackgroundRecoveryPolicy.shouldRecover(
            backgroundResident: backgroundResident,
            wantsForwarding: wantsForwarding,
            sleeping: sleeping,
            quitting: quitting,
            manualStopRequested: manualStopRequested
        )
    }
    private func refreshLoginItemStatus() {
        switch SMAppService.mainApp.status {
        case .enabled: backgroundResidentStatus = "登录自启已启用"
        case .requiresApproval: backgroundResidentStatus = "需在系统设置允许登录项"
        case .notRegistered: backgroundResidentStatus = backgroundResident ? "登录项未注册" : "关闭"
        case .notFound: backgroundResidentStatus = "登录项不可用"
        @unknown default: backgroundResidentStatus = "登录项状态未知"
        }
    }
    private func registerLoginItem() {
        do {
            if SMAppService.mainApp.status != .enabled { try SMAppService.mainApp.register() }
            refreshLoginItemStatus()
        } catch {
            backgroundResidentStatus = "登录项注册失败"
            problem = "后台常驻已开启，但登录自启注册失败：\(error.localizedDescription)"
        }
    }
    func setBackgroundResident(_ enabled: Bool) {
        backgroundResident = enabled
        UserDefaults.standard.set(enabled, forKey: Self.residentKey)
        recoveryWorkItem?.cancel(); recoveryWorkItem = nil
        if enabled {
            if running || busy { setWantsForwarding(true) }
            registerLoginItem()
            append("后台常驻已开启：登录自启、唤醒恢复和引擎异常恢复已启用")
            if wantsForwarding {
                needsRecovery = true
                requestRecovery(reason: "后台常驻恢复", immediate: true)
            }
        } else {
            needsRecovery = false
            setWantsForwarding(false)
            backgroundResidentStatus = "正在关闭登录项"
            Task { @MainActor [weak self] in
                do { try await SMAppService.mainApp.unregister() }
                catch { self?.problem = "关闭登录自启失败：\(error.localizedDescription)" }
                self?.refreshLoginItemStatus()
            }
            append("后台常驻已关闭；当前转发不会被强制停止，但之后不再自动恢复")
        }
    }
    private func startLifecycleObservers() {
        let center = NSWorkspace.shared.notificationCenter
        workspaceObservers.append(center.addObserver(forName: NSWorkspace.willSleepNotification, object: nil, queue: .main) { [weak self] _ in
            self?.handleWillSleep()
        })
        workspaceObservers.append(center.addObserver(forName: NSWorkspace.didWakeNotification, object: nil, queue: .main) { [weak self] _ in
            self?.handleDidWake()
        })
        networkMonitor.pathUpdateHandler = { [weak self] path in
            DispatchQueue.main.async {
                guard let self else { return }
                let wasAvailable = self.networkAvailable
                self.networkAvailable = path.status == .satisfied
                if self.networkAvailable && !wasAvailable && self.needsRecovery {
                    self.append("网络已恢复，准备重建转发")
                    self.requestRecovery(reason: "网络恢复", immediate: true)
                }
            }
        }
        networkMonitor.start(queue: networkQueue)
    }
    private func handleWillSleep() {
        sleeping = true
        recoveryWorkItem?.cancel(); recoveryWorkItem = nil
        if backgroundResident && wantsForwarding {
            needsRecovery = true
            append("系统即将睡眠；唤醒并恢复网络后将重建转发")
        }
    }
    private func handleDidWake() {
        sleeping = false
        guard backgroundResident && wantsForwarding else { return }
        recoveryAttempt = 0
        needsRecovery = true
        networkAvailable = false
        status = "唤醒后等待网络"
        append("系统已唤醒；将丢弃睡眠前连接并重建 Userspace/Native 转发")
        requestRecovery(reason: "系统唤醒", forceRestart: true)
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.0) { [weak self] in
            guard let self, self.shouldRecover else { return }
            self.networkAvailable = self.networkMonitor.currentPath.status == .satisfied
            if self.networkAvailable { self.requestRecovery(reason: "唤醒网络确认", immediate: true) }
            else { self.status = "等待网络恢复" }
        }
    }
    private func requestRecovery(reason: String, immediate: Bool = false, forceRestart: Bool = false) {
        guard shouldRecover else { return }
        guard BackgroundRecoveryPolicy.canRetry(attempt: recoveryAttempt) else {
            needsRecovery = false
            status = "需要处理"
            problem = "后台自动恢复连续失败，请检查配置、钥匙串、本地端口和网络后手动启动"
            append("后台自动恢复已达到本轮 5 次上限，已停止重试")
            return
        }
        needsRecovery = true
        if forceRestart, let child = process, child.isRunning {
            status = "正在重建转发"
            child.terminate()
            DispatchQueue.main.asyncAfter(deadline: .now() + 2) { if child.isRunning { kill(child.processIdentifier, SIGKILL) } }
            return
        }
        guard networkAvailable else { status = "等待网络恢复"; return }
        guard process == nil && !busy && !running else { return }
        recoveryWorkItem?.cancel()
        let delay = immediate ? 0.25 : BackgroundRecoveryPolicy.retryDelay(attempt: recoveryAttempt)
        let item = DispatchWorkItem { [weak self] in
            guard let self, self.shouldRecover, self.networkAvailable, self.process == nil, !self.busy, !self.running else { return }
            self.status = "后台恢复中"
            self.append("后台常驻自动启动转发 · \(reason)")
            self.startForwarding(automatic: true)
        }
        recoveryWorkItem = item
        DispatchQueue.main.asyncAfter(deadline: .now() + delay, execute: item)
    }

    init() {
        if ProcessInfo.processInfo.environment["MPTCP_DESK_SMOKE_TEST"] == "1" { return }
        do { provisioningURL = try ProvisioningURLStore.load() ?? "" }
        catch { problem = error.localizedDescription }
        if let saved = UserDefaults.standard.string(forKey: Self.configurationSourceKey), ["local","remote"].contains(saved) {
            configurationSource = saved
        } else {
            configurationSource = provisioningURL.isEmpty ? "local" : "remote"
        }
        provisioningStatus = provisioningManaged ? "远端配置 · 启动时同步" : (remoteConfigurationSelected ? "请填写 API 地址" : "本地配置")
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
        backgroundResident = UserDefaults.standard.bool(forKey: Self.residentKey)
        wantsForwarding = UserDefaults.standard.bool(forKey: Self.wantsForwardingKey)
        if !backgroundResident { wantsForwarding = false }
        startLifecycleObservers()
        if backgroundResident {
            registerLoginItem()
            if wantsForwarding {
                needsRecovery = true
                status = "等待网络恢复"
            }
        } else {
            refreshLoginItemStatus()
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
    private func resetProvisioningBundleState() {
        provisioningIsBundle = false
        provisioningBundleMode = ""
        provisioningBundleID = ""
        provisioningProfiles = []
        provisioningSelectedProfileIDs = []
        provisioningRuntimeStatus = [:]
        provisioningRuntimeError = [:]
        provisioningTCPPaths = [:]
        provisioningUDPPaths = [:]
        lastProvisioningBundle = nil
    }
    private func selectionKey(_ bundleID: String) -> String { Self.bundleSelectionPrefix + bundleID }
    private func savedBundleSelection(_ bundleID: String) -> Set<String> {
        Set(UserDefaults.standard.stringArray(forKey: selectionKey(bundleID)) ?? [])
    }
    private func saveBundleSelection(_ bundleID: String, ids: Set<String>) {
        UserDefaults.standard.set(Array(ids).sorted(), forKey: selectionKey(bundleID))
    }
    private func resolvedSelection(_ bundle: RelayProvisioningBundlePayload) throws -> Set<String> {
        let available = Set(bundle.profiles.compactMap(\.profile_id))
        var selected = savedBundleSelection(bundle.bundle_id).intersection(available)
        if bundle.mode == "single_select" {
            if selected.count != 1 { selected = bundle.profiles.first?.profile_id.map { [$0] } ?? [] }
        } else if selected.isEmpty {
            selected = available
        }
        _ = try bundle.selectedProfiles(ids: selected)
        saveBundleSelection(bundle.bundle_id, ids: selected)
        return selected
    }
    private func orderedSelectedPayloads(_ bundle: RelayProvisioningBundlePayload, ids: Set<String>) throws -> [RelayProvisioningPayload] {
        let validated = try bundle.selectedProfiles(ids: ids)
        let selected = Set(validated.compactMap(\.profile_id))
        return bundle.profiles.filter { $0.profile_id.map(selected.contains) ?? false }
    }
    private func applyProvisionedSummary(_ payload: RelayProvisioningPayload) throws {
        let provisioned = try payload.validatedProfile()
        relays = provisioned.relays
        listenPort = String(provisioned.listen_port)
        udpEnabled = provisioned.udp_enabled ?? false
        tcpEnabled = provisioned.tcp_enabled ?? true
        mode = provisioned.userspace ? "userspace_multipath" : "native_mptcp"
        schedulerMode = provisioned.userspace ? provisioned.schedulerMode : "auto"
        transportKey = provisioned.transport_key ?? ""
    }
    private func applyBundleSelection(_ bundle: RelayProvisioningBundlePayload, ids: Set<String>, updateResident: Bool = true) throws {
        let selected = try orderedSelectedPayloads(bundle, ids: ids)
        guard let first = selected.first else { throw Message("至少选择 1 个 Profile") }
        try applyProvisionedSummary(first)
        provisioningSelectedProfileIDs = ids
        provisioningProfiles = bundle.profiles.compactMap { payload in
            guard let id = payload.profile_id else { return nil }
            return ProvisioningProfileChoice(id: id, name: payload.display_name ?? id, listenPort: payload.listen_port, relayCount: payload.relays.count, mode: payload.mode, backgroundResident: payload.background_resident ?? false)
        }
        let resident = selected.contains { $0.background_resident ?? false }
        if updateResident && resident != backgroundResident { setBackgroundResident(resident) }
    }
    func setProvisioningProfileSelected(_ id: String, selected: Bool) {
        guard !configurationLocked, let bundle = lastProvisioningBundle else { return }
        do {
            var next = provisioningSelectedProfileIDs
            if bundle.mode == "single_select" {
                guard selected else { return }
                next = [id]
            } else {
                if selected { next.insert(id) } else { next.remove(id) }
                guard !next.isEmpty else { throw Message("多配置并行至少保留 1 个启用 Profile") }
            }
            _ = try bundle.selectedProfiles(ids: next)
            saveBundleSelection(bundle.bundle_id, ids: next)
            try applyBundleSelection(bundle, ids: next)
            provisioningRuntimeStatus = [:]
            provisioningRuntimeError = [:]
            provisioningTCPPaths = [:]
            provisioningUDPPaths = [:]
            let count = next.count
            provisioningStatus = bundle.mode == "parallel" ? "\(bundle.display_name) · 已选择 \(count) 个 Profile" : "\(bundle.display_name) · 已选择 1 个 Profile"
            problem = nil
        } catch { problem = error.localizedDescription }
    }

    func setConfigurationSource(_ source: String) {
        guard !configurationLocked, ["local", "remote"].contains(source) else { return }
        configurationSource = source
        UserDefaults.standard.set(source, forKey: Self.configurationSourceKey)
        problem = nil
        provisioningStatus = source == "local" ? "本地配置" : (provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "请填写 API 地址" : "远端配置 · 启动时同步")
    }

    func saveProvisioningURL() {
        guard !configurationLocked else { return }
        do {
            let cleaned = provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines)
            if cleaned.isEmpty {
                try ProvisioningURLStore.delete()
                provisioningStatus = "请填写 API 地址"
                provisioningRevision = ""
                provisioningDisplayName = ""
                resetProvisioningBundleState()
                append("远端配置 API 已清除")
                return
            }
            _ = try RelayProvisioningClient.endpointURL(cleaned)
            try ProvisioningURLStore.save(cleaned)
            provisioningURL = cleaned
            configurationSource = "remote"
            UserDefaults.standard.set("remote", forKey: Self.configurationSourceKey)
            resetProvisioningBundleState()
            provisioningStatus = "远端配置 · 启动时同步"
            append("远端配置 API 已保存")
            problem = nil
        } catch { problem = error.localizedDescription }
    }

    func clearProvisioningURL() {
        guard !configurationLocked else { return }
        do {
            try ProvisioningURLStore.delete()
            provisioningURL = ""
            provisioningStatus = "请填写 API 地址"
            provisioningRevision = ""
            provisioningDisplayName = ""
            resetProvisioningBundleState()
            problem = nil
            append("远端配置 API 已清除")
        } catch { problem = error.localizedDescription }
    }

    func syncProvisioning(startAfterSync: Bool = false, automatic: Bool = false) {
        guard !busy && !running && !provisioningSyncing else { return }
        let endpoint = provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !endpoint.isEmpty else { problem = "请先填写 Provisioning API 地址"; return }
        do {
            _ = try RelayProvisioningClient.endpointURL(endpoint)
            try ProvisioningURLStore.save(endpoint)
            provisioningURL = endpoint
        } catch { problem = error.localizedDescription; return }
        provisioningSyncing = true
        provisioningStatus = "正在获取完整配置…"
        problem = nil
        Task { @MainActor [weak self] in
            guard let self else { return }
            do {
                let document = try await RelayProvisioningClient.fetch(endpoint: endpoint)
                switch document {
                case .profile(let payload):
                    self.resetProvisioningBundleState()
                    let provisioned = try payload.validatedProfile()
                    let preferenceData = try provisioned.preferenceData()
                    if provisioned.userspace { try TransportKeyStore.save(provisioned.transport_key ?? "") }
                    try self.applyProvisionedSummary(payload)
                    UserDefaults.standard.set(preferenceData, forKey: "multipath-profile-v2")
                    self.provisioningRevision = payload.revision ?? ""
                    self.provisioningDisplayName = payload.display_name ?? ""
                    let title = self.provisioningDisplayName.isEmpty ? "API 配置" : self.provisioningDisplayName
                    self.provisioningStatus = payload.revision.map { "\(title) · 已同步 · \($0)" } ?? "\(title) · 已同步"
                    self.append("Provisioning 同步成功：已应用单 Profile（\(payload.relays.count) 条 Relay）")
                    self.provisioningSyncing = false
                    if let resident = payload.background_resident, resident != self.backgroundResident { self.setBackgroundResident(resident) }
                    self.problem = nil
                    if startAfterSync {
                        if automatic && payload.background_resident == false {
                            self.append("Provisioning 已关闭后台常驻，本次自动恢复取消")
                        } else {
                            self.launch("run", automatic: automatic)
                        }
                    }

                case .bundle(let bundle):
                    try bundle.validate()
                    let selection = try self.resolvedSelection(bundle)
                    let selected = try self.orderedSelectedPayloads(bundle, ids: selection)
                    self.lastProvisioningBundle = bundle
                    self.provisioningIsBundle = true
                    self.provisioningBundleMode = bundle.mode
                    self.provisioningBundleID = bundle.bundle_id
                    self.provisioningRevision = bundle.revision
                    self.provisioningDisplayName = bundle.display_name
                    self.provisioningRuntimeStatus = [:]
                    self.provisioningRuntimeError = [:]
                    self.provisioningTCPPaths = [:]
                    self.provisioningUDPPaths = [:]
                    try self.applyBundleSelection(bundle, ids: selection)
                    self.provisioningStatus = bundle.mode == "parallel"
                        ? "\(bundle.display_name) · 已同步 · \(selection.count) 个 Profile 启用"
                        : "\(bundle.display_name) · 已同步 · 单配置选择"
                    self.append("Provisioning Bundle 同步成功：包含 \(bundle.profiles.count) 个 Profile，本机启用 \(selection.count) 个")
                    self.provisioningSyncing = false
                    self.problem = nil
                    let resident = selected.contains { $0.background_resident ?? false }
                    if startAfterSync {
                        if automatic && !resident {
                            self.append("所选 Bundle Profile 均关闭后台常驻，本次自动恢复取消")
                        } else {
                            let bundleData = try JSONEncoder().encode(bundle)
                            let orderedIDs = selected.compactMap(\.profile_id)
                            self.launch("run-bundle", automatic: automatic, stdinData: bundleData, extraArguments: orderedIDs)
                        }
                    }
                }
            } catch {
                self.provisioningSyncing = false
                self.provisioningStatus = "同步失败 · 未启动"
                self.problem = "Provisioning API 同步失败：\(error.localizedDescription)"
                self.append("Provisioning API 同步失败；API 托管模式不会使用旧缓存配置启动")
            }
        }
    }

    func startForwarding(automatic: Bool = false) {
        guard !busy && !running && !provisioningSyncing else { return }
        if remoteConfigurationSelected {
            guard !provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { problem = "请先填写远端配置 API 地址"; return }
            syncProvisioning(startAfterSync: true, automatic: automatic)
        } else {
            launch("run", automatic: automatic)
        }
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
    private func customerFacingRemoteError(_ message: String?) -> String {
        let value = (message ?? "").lowercased()
        if value.contains("认证") || value.contains("auth") || value.contains("key") { return "认证失败" }
        if value.contains("timeout") || value.contains("超时") || value.contains("deadline") { return "连接超时" }
        if value.contains("refused") || value.contains("拒绝") { return "连接被拒绝" }
        return "连接失败"
    }
    private func customerLogLine(_ line: String) -> String? {
        if line.contains("实际带宽叠加") || line.contains("不作为测速结论") || line.contains("不是测速结果") {
            if line.contains("认证通过") { return "认证通过" }
            return nil
        }
        if line.contains("正在建立 Userspace 会话") { return "正在连接" }
        if line.contains("不使用内核 MPTCP") { return nil }
        if line.contains("MPX/4 Draft") && !line.contains("错误") { return nil }
        return line
    }
    func append(_ line: String) {
        guard let visible = customerLogLine(line), !visible.isEmpty else { return }
        let stamp = DateFormatter.localizedString(from: Date(), dateStyle: .none, timeStyle: .medium)
        logs.append("\(stamp)  \(visible)")
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
    func launch(_ requestedAction: String, automatic: Bool = false, stdinData: Data? = nil, extraArguments: [String] = []) {
        guard !busy && !running else {return}
        let action = requestedAction == "doctor" && userspace ? "doctor-userspace" : requestedAction
        let checking = action.hasPrefix("doctor")
        let forwarding = action == "run" || action == "run-bundle"
        guard let engine = Bundle.main.url(forResource: "mptcp-desktop-engine", withExtension: nil) else {problem = "安装包缺少传输引擎";return}
        do {
            var data = stdinData ?? Data()
            if action == "run" && stdinData == nil {
                data = try JSONEncoder().encode(profile())
                if !automatic { save(); if problem != nil { return } }
            } else if action == "run-bundle" && stdinData == nil {
                throw Message("Bundle 启动缺少刚同步的 Provisioning 数据")
            }
            problem = nil; busy = true; status = checking ? "检查环境中" : "连接中"
            if action == "run-bundle" { provisioningRuntimeStatus = [:]; provisioningRuntimeError = [:] }
            udpConnections = 0; udpSent = 0; udpReceived = 0
            paths = 0; connections = 0; sent = 0; received = 0
            tcpPaths = []; udpPaths = []; udpHealthyPaths = 0
            reorderBytes = 0; reorderPeak = 0; pendingBytes = 0; retransmits = 0; udpDropped = 0
            resources = nil; lifecycle = nil; lastTransportEvent = 0
            configuredSchedulerMode = ""; effectiveSchedulerMode = ""; schedulerModeSwitches = 0; lastSchedulerModeReason = ""
            let child = Process(); child.executableURL = engine; child.arguments = [action] + extraArguments
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
                    let recover = forwarding && self.shouldRecover
                    if recover {
                        self.needsRecovery = true
                        self.recoveryAttempt += 1
                        self.problem = nil
                        self.status = self.networkAvailable ? "后台恢复中" : "等待网络恢复"
                        self.append("传输引擎已退出；后台常驻将自动恢复")
                        self.requestRecovery(reason: "引擎退出")
                    } else {
                        if stopped.terminationStatus != 0 && self.problem == nil && !self.manualStopRequested && !self.quitting {
                            self.problem = "传输引擎已退出，请检查日志"
                        }
                        self.status = self.problem == nil ? (checking ? "引擎环境检查通过" : "已停止") : "需要处理"
                    }
                    self.manualStopRequested = false
                }
            }
            process = child
            try child.run()
            if forwarding {
                if !automatic && backgroundResident { setWantsForwarding(true) }
                needsRecovery = false
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

            if let profileID = event.profile_id, event.bundle_id != nil {
                let title = event.profile_name ?? profileID
                switch event.kind {
                case "connecting":
                    provisioningRuntimeStatus[profileID] = "连接中"
                    provisioningRuntimeError.removeValue(forKey: profileID)
                case "ready":
                    provisioningRuntimeStatus[profileID] = "认证通过"
                    provisioningRuntimeError.removeValue(forKey: profileID)
                case "listening":
                    provisioningRuntimeStatus[profileID] = "已启动"
                    provisioningRuntimeError.removeValue(forKey: profileID)
                case "error":
                    provisioningRuntimeStatus[profileID] = "错误"
                    provisioningRuntimeError[profileID] = customerFacingRemoteError(event.message)
                case "transport_closed":
                    if provisioningRuntimeStatus[profileID] != "错误" { provisioningRuntimeStatus[profileID] = "已停止" }
                default: break
                }
                if event.kind == "stats" { provisioningTCPPaths[profileID] = event.path_stats ?? [] }
                if event.kind == "udp_stats" { provisioningUDPPaths[profileID] = event.path_stats ?? [] }
                if event.kind == "error" { append("[\(title)] \(customerFacingRemoteError(event.message))") }
                else if ["connecting","ready","listening","transport_closed"].contains(event.kind) {
                    let label: String
                    switch event.kind { case "connecting": label = "正在连接"; case "ready": label = "认证通过"; case "listening": label = "已启动"; default: label = "已停止" }
                    append("[\(title)] \(label)")
                }
                continue
            }

            receiveSchedulerEvent(event)
            switch event.kind {
            case "bundle_listening":
                running = true; busy = false
                recoveryAttempt = 0; needsRecovery = false
                let failed = event.failed_profiles ?? 0
                if failed > 0 {
                    status = "部分配置运行中"
                    problem = event.message ?? "部分 Profile 不可用；可用配置继续运行"
                } else {
                    problem = nil
                    status = (event.connecting_profiles ?? 0) > 0 ? "部分配置已启动" : "多配置入口已启动"
                }
            case "bundle_degraded":
                running = (event.active_profiles ?? 0) > 0; busy = false
                recoveryAttempt = 0; needsRecovery = false
                status = running ? "部分配置运行中" : "等待可用配置"
                problem = event.message ?? "部分 Profile 不可用"
            case "bundle_connecting":
                status = event.message ?? "部分配置连接中"
            case "bundle_failed":
                running = false; busy = false
                status = "全部配置不可用"
                problem = event.message ?? "所有选中 Profile 均不可用"
            case "listening":
                running = true; busy = false
                recoveryAttempt = 0; needsRecovery = false; problem = nil
                status = userspace ? (tcpEnabled ? "Userspace 入口已启动" : "Userspace UDP 入口已启动") : "Native 入口已启动"
            case "error": problem = remoteConfigurationSelected ? customerFacingRemoteError(event.message) : event.message; status = "连接失败"
            case "connecting": status = event.message ?? "连接中"
            case "ready": status = event.message ?? "环境可用"
            default: break
            }
            if event.kind == "stats" || event.kind == "listening" || event.kind == "bundle_stats" {
                paths = event.paths ?? 0;connections = event.connections ?? 0
                sent = event.sent ?? 0;received = event.received ?? 0
                if event.kind != "bundle_stats" { tcpPaths = event.path_stats ?? [] }
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
            if event.kind == "stats" || event.kind == "transport_closed" || event.kind == "bundle_stats" { keepDiagnostic(Data(line)) }
            if event.kind == "udp_stats" || event.kind == "bundle_udp_stats" {
                udpConnections = event.connections ?? 0
                udpSent = event.sent ?? 0; udpReceived = event.received ?? 0
                if event.kind != "bundle_udp_stats" { udpPaths = event.path_stats ?? [] }
                udpHealthyPaths = event.paths ?? 0
                udpDropped = event.dropped ?? 0
            }
            if event.kind != "stats" && event.kind != "bundle_stats" && event.kind != "bundle_udp_stats", let text = event.message {append(text)}
        }
    }

    func stop() {
        manualStopRequested = true
        needsRecovery = false
        recoveryWorkItem?.cancel(); recoveryWorkItem = nil
        setWantsForwarding(false)
        guard let child = process else {
            busy = false; running = false; status = "已停止"
            manualStopRequested = false
            return
        }
        status = "停止中"; busy = true
        child.terminate()
        DispatchQueue.main.asyncAfter(deadline: .now() + 2) { if child.isRunning {kill(child.processIdentifier, SIGKILL)} }
    }
    func quit() {
        quitting = true
        recoveryWorkItem?.cancel(); recoveryWorkItem = nil
        if !backgroundResident { setWantsForwarding(false) }
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
                Text("0.10.2").font(.system(size: 11)).foregroundColor(.secondary)
            }
            Picker("视图", selection: $model.tab) {Text("连接").tag(0);Text("日志").tag(1);Text("路径诊断").tag(2)}.pickerStyle(.segmented)
            HStack(spacing:12) {
                Text("配置").font(.system(size:12,weight:.medium))
                Picker("配置", selection: Binding(get:{model.configurationSource}, set:{model.setConfigurationSource($0)})) {
                    Text("本地配置").tag("local")
                    Text("远端配置").tag("remote")
                }.pickerStyle(.segmented).labelsHidden().frame(maxWidth:320).disabled(locked)
                Spacer()
            }
            if model.tab == 0 {
                VStack(alignment: .leading, spacing: 12) {
                    if model.remoteConfigurationSelected {
                        VStack(alignment:.leading,spacing:8) {
                            SecureField("请输入 Provisioning API 地址", text:$model.provisioningURL)
                                .textFieldStyle(.roundedBorder)
                            HStack(spacing:8) {
                                Button("保存") { model.saveProvisioningURL() }.disabled(model.provisioningSyncing)
                                Button { model.syncProvisioning() } label: {
                                    if model.provisioningSyncing { ProgressView().controlSize(.small) } else { Label("同步配置",systemImage:"arrow.clockwise") }
                                }.disabled(model.provisioningSyncing || model.provisioningURL.trimmingCharacters(in:.whitespacesAndNewlines).isEmpty)
                                if !model.provisioningURL.isEmpty { Button("清除") { model.clearProvisioningURL() }.disabled(model.provisioningSyncing) }
                                Spacer()
                                Text(model.provisioningStatus).font(.system(size:11)).foregroundColor(.secondary)
                            }
                        }
                    }
                    if model.remoteConfigurationSelected && model.provisioningManaged {
                        VStack(alignment:.leading,spacing:8) {
                            Text(model.provisioningIsBundle ? "远端配置组" : "远端配置").font(.headline)
                            if !model.provisioningDisplayName.isEmpty { Text(model.provisioningDisplayName).font(.system(size:12,weight:.medium)) }
                            if !model.provisioningRevision.isEmpty { Text("Revision：\(model.provisioningRevision)").font(.system(size:11,design:.monospaced)).foregroundColor(.secondary) }
                            if model.provisioningIsBundle {
                                Text(model.provisioningBundleMode == "parallel" ? "多配置并行" : "单配置选择")
                                    .font(.system(size:11,weight:.medium)).foregroundColor(.secondary)
                                ForEach(model.provisioningProfiles) { choice in
                                    let selected = model.provisioningSelectedProfileIDs.contains(choice.id)
                                    Button { model.setProvisioningProfileSelected(choice.id, selected: !selected) } label: {
                                        HStack(spacing:9) {
                                            Image(systemName: model.provisioningBundleMode == "parallel" ? (selected ? "checkmark.square.fill" : "square") : (selected ? "largecircle.fill.circle" : "circle"))
                                                .foregroundColor(selected ? .accentColor : .secondary)
                                            VStack(alignment:.leading,spacing:2) {
                                                Text(choice.name).font(.system(size:12,weight:.medium)).foregroundColor(.primary)
                                                Text("127.0.0.1:\(choice.listenPort) · \(choice.mode == "userspace_multipath" ? "MPX/4" : "Native") · \(choice.relayCount) Relays")
                                                    .font(.system(size:10,design:.monospaced)).foregroundColor(.secondary)
                                                if let detail = model.provisioningRuntimeError[choice.id], !detail.isEmpty {
                                                    Text(detail).font(.system(size:10)).foregroundColor(.red).lineLimit(2).help(detail)
                                                }
                                            }
                                            Spacer()
                                            if let state = model.provisioningRuntimeStatus[choice.id] { Text(state).font(.system(size:10)).foregroundColor(state == "错误" ? .red : .secondary) }
                                        }.contentShape(Rectangle())
                                    }.buttonStyle(.plain).disabled(model.running || model.busy || model.provisioningSyncing)
                                }
                                if model.provisioningBundleMode == "parallel" {
                                    Text("端口冲突时无法启动；单个配置连接失败不会影响其他可用配置。")
                                        .font(.system(size:10)).foregroundColor(.secondary)
                                }
                            } else {
                                Text("\(model.userspace ? "Userspace Multipath" : "Native MPTCP") · 127.0.0.1:\(model.listenPort) · \(model.userspace ? Model.schedulerTitle(model.schedulerMode) : "Native") · TCP \(model.tcpEnabled ? "开" : "关") · UDP \(model.udpEnabled ? "开" : "关") · \(model.relays.count) 条 Relay")
                                    .font(.system(size:11)).foregroundColor(.secondary)
                            }
                        }.padding(10).background(Color.secondary.opacity(0.06)).cornerRadius(8)
                    } else if !model.remoteConfigurationSelected {
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
                            }
                            SecureField("Transport Key", text: $model.transportKey)
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
                            Text("Weighted 模式需填写每条 Relay 的下行带宽。")
                                .font(.system(size:10)).foregroundColor(.secondary)
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
                    }
                    Divider()
                }.disabled(locked)
                HStack {
                    Toggle("后台常驻", isOn: Binding(get:{model.backgroundResident}, set:{model.setBackgroundResident($0)})).toggleStyle(.switch).disabled(model.remoteConfigurationSelected)
                    Spacer()
                    Text(model.backgroundResidentStatus).font(.system(size:11)).foregroundColor(.secondary)
                }
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
                    if model.remoteConfigurationSelected && model.provisioningIsBundle {
                        HStack(spacing:16) {
                            metric("配置", String(model.provisioningSelectedProfileIDs.count))
                            metric("TCP 路径", String(model.paths))
                            metric("连接", String(model.connections))
                        }
                        ScrollView {
                            VStack(alignment:.leading,spacing:14) {
                                ForEach(model.provisioningProfiles.filter { model.provisioningSelectedProfileIDs.contains($0.id) }) { choice in
                                    VStack(alignment:.leading,spacing:8) {
                                        HStack {
                                            Text(choice.name).font(.headline)
                                            Spacer()
                                            let state = model.provisioningRuntimeStatus[choice.id] ?? "等待启动"
                                            Text(state).font(.system(size:11)).foregroundColor(state == "错误" ? .red : .secondary)
                                        }
                                        Text("127.0.0.1:\(choice.listenPort)").font(.system(size:11,design:.monospaced)).foregroundColor(.secondary)
                                        if let detail = model.provisioningRuntimeError[choice.id], !detail.isEmpty {
                                            Text(detail).font(.system(size:11)).foregroundColor(.red)
                                        }
                                        pathSection("TCP 路径", model.provisioningTCPPaths[choice.id] ?? [], hideEndpoint:true)
                                        if model.udpEnabled { pathSection("UDP 路径", model.provisioningUDPPaths[choice.id] ?? [], hideEndpoint:true) }
                                    }.padding(10).background(Color.secondary.opacity(0.04)).cornerRadius(8)
                                }
                            }.frame(maxWidth:.infinity,alignment:.leading)
                        }
                    } else if model.userspace {
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
                        ScrollView {
                            VStack(alignment:.leading,spacing:14) {
                                if model.remoteConfigurationSelected && model.provisioningIsBundle {
                                    ForEach(model.provisioningProfiles.filter { model.provisioningSelectedProfileIDs.contains($0.id) }) { choice in
                                        VStack(alignment:.leading,spacing:8) {
                                            HStack {
                                                Text(choice.name).font(.headline)
                                                Spacer()
                                                let state = model.provisioningRuntimeStatus[choice.id] ?? "等待启动"
                                                Text(state).font(.system(size:11)).foregroundColor(state == "错误" ? .red : .secondary)
                                            }
                                            if let detail = model.provisioningRuntimeError[choice.id], !detail.isEmpty {
                                                Text(detail).font(.system(size:11)).foregroundColor(.red)
                                            }
                                            pathSection("TCP 路径", model.provisioningTCPPaths[choice.id] ?? [], hideEndpoint:true)
                                            if model.udpEnabled { pathSection("UDP 路径", model.provisioningUDPPaths[choice.id] ?? [], hideEndpoint:true) }
                                        }.padding(10).background(Color.secondary.opacity(0.04)).cornerRadius(8)
                                    }
                                } else {
                                    pathSection("TCP 路径", model.tcpPaths, hideEndpoint:model.remoteConfigurationSelected)
                                    if model.udpEnabled { pathSection("UDP 路径", model.udpPaths, hideEndpoint:model.remoteConfigurationSelected) }
                                }
                            }.frame(maxWidth:.infinity,alignment:.leading)
                        }
                    } else {
                        Text("Native MPTCP 路径统计由系统提供。")
                            .font(.system(size:12)).foregroundColor(.secondary)
                    }
                }.frame(maxWidth:.infinity,maxHeight:.infinity,alignment:.topLeading)
            }
            if let problem = model.problem {
                Label(problem,systemImage:"exclamationmark.triangle.fill").font(.system(size:12)).foregroundColor(.red).fixedSize(horizontal:false,vertical:true)
            }
            Spacer(minLength:0)
            Divider()
            HStack {
                Button{model.importProfile()}label:{Image(systemName:"square.and.arrow.down")}.help("导入配置").disabled(locked || model.remoteConfigurationSelected)
                Button{model.save()}label:{Image(systemName:"square.and.arrow.down.on.square")}.help("保存配置").disabled(locked || model.remoteConfigurationSelected)
                Menu {Button("开启系统聚合…"){model.changeAggregation(enabled:true)};Button("关闭系统聚合…"){model.changeAggregation(enabled:false)}} label:{Image(systemName:"gearshape")}.frame(width:42).help("仅 Native 模式需要系统聚合设置").disabled(locked || model.userspace)
                Spacer()
                Button("检查环境"){model.launch("doctor")}.disabled(locked)
                if model.running || model.busy {
                    Button{model.stop()}label:{Label("停止",systemImage:"stop.fill")}
                } else {
                    Button{model.startForwarding()}label:{Label("启动",systemImage:"play.fill")}.buttonStyle(.borderedProminent)
                }
            }
        }.padding(22).frame(minWidth:650,idealWidth:710,maxWidth:900,minHeight:800,idealHeight:850)
    }
    func metric(_ label:String,_ value:String)->some View {VStack(alignment:.leading,spacing:4){Text(label).font(.system(size:11)).foregroundColor(.secondary);Text(value).font(.system(size:16,weight:.medium,design:.monospaced))}.frame(maxWidth:.infinity,alignment:.leading)}
    static func bytes(_ count:Int)->String { ByteCountFormatter.string(fromByteCount:Int64(count),countStyle:.binary) }
    func pathSection(_ title:String,_ paths:[PathMetric],hideEndpoint:Bool=false)->some View {
        VStack(alignment:.leading,spacing:8) {
            Text(title).font(.headline)
            if paths.isEmpty { Text("尚无路径数据；启动后自动更新").font(.system(size:12)).foregroundColor(.secondary) }
            ForEach(paths) { path in
                VStack(alignment:.leading,spacing:4) {
                    HStack {
                        Circle().fill(path.connected ? Color.green : Color.secondary).frame(width:7,height:7)
                        Text((hideEndpoint ? "路径 \(path.id + 1)" : "\(path.id) · \(path.address)") + (path.role.map { " · " + $0.uppercased() } ?? "")).font(.system(size:12,design:.monospaced))
                        Spacer();Text(path.connected ? "在线" : "离线/重连中").font(.system(size:11)).foregroundColor(.secondary)
                    }
                    Text(String(format:"RTT %.1f ms · Goodput %.2f MiB/s%@ · 队列 %@ · 在途 %@ · 错误 %llu",path.rtt_ms,path.goodput_bps/1048576,(path.configured_rate_bps ?? 0) > 0 ? String(format:" · Weighted %.1f Mbps",(path.configured_rate_bps ?? 0)*8/1000000) : "",Self.bytes(path.queue_bytes),Self.bytes(path.outstanding_bytes),path.errors))
                        .font(.system(size:11,design:.monospaced)).foregroundColor(.secondary)
                    if let error = path.last_error, !error.isEmpty {
                        if hideEndpoint { Text("路径连接异常").font(.system(size:10)).foregroundColor(.secondary) }
                        else { Text(error).font(.system(size:10)).foregroundColor(.secondary).lineLimit(2).help(error) }
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
        Text("后台常驻：\(model.backgroundResident ? model.backgroundResidentStatus : "关闭")")
        Divider()
        Button("打开主窗口") {
            openWindow(id: "main")
            NSApplication.shared.activate(ignoringOtherApps: true)
        }
        if model.running || model.busy {
            Button("停止转发") { model.stop() }.disabled(model.status == "停止中")
        } else {
            Button("启动转发") { model.startForwarding() }
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
