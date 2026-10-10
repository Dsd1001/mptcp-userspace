import SwiftUI
import Foundation
import AppKit
import Network
import ServiceManagement

private struct ManagedLaunchPlan {
    var action: String
    var stdinData: Data
    var extraArguments: [String]
    var resident: Bool
    var revision: String
    var displayName: String
    var selectedProfileIDs: Set<String>
}

struct ProfileDiagnosticState {
    var configuredSchedulerMode = ""
    var effectiveSchedulerMode = ""
    var schedulerModeSwitches: UInt64 = 0
    var lastSchedulerModeReason = ""
    var paths = 0
    var connections = 0
    var sent: Int64 = 0
    var received: Int64 = 0
    var reorderBytes = 0
    var reorderPeak = 0
    var pendingBytes = 0
    var retransmits: UInt64 = 0
    var udpDropped: UInt64 = 0
    var udpConnections = 0
    var udpSent: Int64 = 0
    var udpReceived: Int64 = 0
    var resources: ResourceMetric?
    var lifecycle: LifecycleMetric?
    var tcpPaths: [PathMetric] = []
    var udpPaths: [PathMetric] = []
}

final class Model: ObservableObject {
    static let shared = Model()
    @Published var relays = [RelayRow(host: "", port: 21001), RelayRow(host: "", port: 21002)]
    @Published var listenPort = "1081"
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
    @Published var provisioningDiagnostics: [String:ProfileDiagnosticState] = [:]
    @Published var provisioningLastSync: Date?
    @Published var provisioningUsingCache = false
    @Published var provisioningBackgroundRefreshing = false
    @Published var provisioningUpdatePending = false
    private var lastProvisioningBundle: RelayProvisioningBundlePayload?
    private var provisioningRefreshWorkItem: DispatchWorkItem?
    private var provisioningRefreshRetryAttempt = 0
    private static let bundleSelectionPrefix = "provisioning-bundle-selection-v1."
    private static let configurationSourceKey = "configuration-source-v1"
    var remoteConfigurationSelected: Bool { configurationSource == "remote" }
    var provisioningManaged: Bool { remoteConfigurationSelected && !provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
    @Published var tcpEnabled = true
    @Published var udpEnabled = true
    @Published var uotEnabled = false
    var activeDatagramProfiles: [ProvisioningProfileChoice] {
        provisioningProfiles.filter { provisioningSelectedProfileIDs.contains($0.id) }
    }
    var datagramEnabled: Bool {
        remoteConfigurationSelected && provisioningIsBundle
            ? activeDatagramProfiles.contains { $0.udpEnabled || $0.uotEnabled }
            : udpEnabled || uotEnabled
    }
    var hasUOT: Bool {
        remoteConfigurationSelected && provisioningIsBundle
            ? activeDatagramProfiles.contains { $0.uotEnabled } : uotEnabled
    }
    var datagramLabel: String {
        if remoteConfigurationSelected && provisioningIsBundle {
            let native = activeDatagramProfiles.contains { $0.udpEnabled }
            return hasUOT ? (native ? "UDP / UoT" : "UoT") : "UDP"
        }
        return uotEnabled ? "UoT" : "UDP"
    }
    var listeningStatus: String {
        tcpEnabled ? "Userspace 入口已启动" : "Userspace \(datagramLabel) 入口已启动"
    }
    func setNativeUDPEnabled(_ enabled: Bool) {
        guard !configurationLocked else { return }
        udpEnabled = enabled
        if enabled { uotEnabled = false }
    }
    func setUOTEnabled(_ enabled: Bool) {
        guard !configurationLocked else { return }
        uotEnabled = enabled
        if enabled { udpEnabled = false }
    }
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
    @Published var localStreamResourceExpanded = false
    @Published var localWindowResourceExpanded = false
    @Published var profileStreamResourceExpanded = Set<String>()
    @Published var profileWindowResourceExpanded = Set<String>()
    private var lastTransportEvent: UInt64 = 0
    var userspace: Bool { true } // v1.1.2: MPX/4 Userspace is the only mode.
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
    @Published var remoteManagementEnabled = false
    @Published var remoteControlServer = ""
    @Published var remotePairingCode = ""
    @Published var remoteControlStatus = "关闭"
    @Published var remoteControlConnected = false
    @Published var remoteDeviceID = ""
    @Published var remoteControlLastSeen: Date?
    var remoteControlRevision: UInt64 = 0
    var remoteRestartGeneration: UInt64 = 0
    var remoteSyncGeneration: UInt64 = 0
    var remoteUpdateGeneration: UInt64 = 0
    var remoteControlClient: RemoteControlClient?
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
    func refreshLoginItemStatus() {
        switch SMAppService.mainApp.status {
        case .enabled: backgroundResidentStatus = "登录自启已启用"
        case .requiresApproval: backgroundResidentStatus = "需在系统设置允许登录项"
        case .notRegistered: backgroundResidentStatus = backgroundResident ? "登录项未注册" : "关闭"
        case .notFound: backgroundResidentStatus = "登录项不可用"
        @unknown default: backgroundResidentStatus = "登录项状态未知"
        }
    }
    func registerLoginItem() {
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
            if remoteManagementEnabled {
                backgroundResidentStatus = "远程管理保持登录自启"
                refreshLoginItemStatus()
            } else {
                unregisterLoginItemIfUnused()
            }
            append("后台常驻已关闭；当前转发不会被强制停止，但之后不再自动恢复")
        }
    }
    func unregisterLoginItemIfUnused() {
        guard !backgroundResident && !remoteManagementEnabled else { refreshLoginItemStatus(); return }
        backgroundResidentStatus = "正在关闭登录项"
        Task { @MainActor [weak self] in
            do { try await SMAppService.mainApp.unregister() }
            catch { self?.problem = "关闭登录自启失败：\(error.localizedDescription)" }
            self?.refreshLoginItemStatus()
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
        provisioningStatus = provisioningManaged ? "远端配置 · 本地缓存优先" : (remoteConfigurationSelected ? "请填写 API 地址" : "本地配置")
        if let data = UserDefaults.standard.data(forKey: "multipath-profile-v2"),
           var profile = try? JSONDecoder().decode(Profile.self, from: data) {
            do {
                guard profile.userspace else {
                    throw Message("检测到旧 Native MPTCP 配置。v1.1.5 只支持 Userspace，请重新填写 Transport Key 与 Relay 后保存")
                }
                profile.transport_key = try TransportKeyStore.load()
                try profile.validate()
                apply(profile)
            } catch {
                problem = error.localizedDescription
                UserDefaults.standard.set(false, forKey: Self.wantsForwardingKey)
            }
        } else if UserDefaults.standard.data(forKey: "tcp-forward-profile-v1") != nil {
            problem = "检测到旧 Native MPTCP 配置。请创建 MPX/4 Userspace 配置；不会尝试使用旧 Native 隧道启动"
            UserDefaults.standard.set(false, forKey: Self.wantsForwardingKey)
        }
        backgroundResident = UserDefaults.standard.bool(forKey: Self.residentKey)
        wantsForwarding = UserDefaults.standard.bool(forKey: Self.wantsForwardingKey)
        if !backgroundResident { wantsForwarding = false }
        if remoteConfigurationSelected, !provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            do {
                if let cached = try ManagedProvisioningCacheStore.load(endpoint: provisioningURL) {
                    provisioningLastSync = cached.fetchedAt
                    _ = try applyManagedDocument(cached.document, preferredSelection: cached.selectedProfileIDs, updateResident: false)
                    provisioningUsingCache = true
                    provisioningStatus = managedStatus(prefix: "本地缓存")
                }
            } catch {
                append("远端配置本地缓存不可用，将在需要时重新同步 API")
            }
        }
        startLifecycleObservers()
        scheduleProvisioningRefreshFromCache()
        if backgroundResident {
            registerLoginItem()
            if wantsForwarding {
                needsRecovery = true
                status = "等待网络恢复"
            }
        } else {
            refreshLoginItemStatus()
        }
        initializeRemoteManagement()
    }
    func apply(_ p: Profile) {
        guard !configurationLocked, p.userspace else { return }
        relays = p.relays; listenPort = String(p.listen_port)
        udpEnabled = p.udp_enabled ?? false
        uotEnabled = p.uot_enabled ?? false
        tcpEnabled = p.tcp_enabled ?? true
        schedulerMode = p.schedulerMode
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
        provisioningDiagnostics = [:]
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
        uotEnabled = provisioned.uot_enabled ?? false
        tcpEnabled = provisioned.tcp_enabled ?? true
        schedulerMode = provisioned.schedulerMode
        transportKey = provisioned.transport_key ?? ""
    }
    private func applyBundleSelection(_ bundle: RelayProvisioningBundlePayload, ids: Set<String>, updateResident: Bool = true) throws {
        let selected = try orderedSelectedPayloads(bundle, ids: ids)
        guard let first = selected.first else { throw Message("至少选择 1 个 Profile") }
        try applyProvisionedSummary(first)
        provisioningSelectedProfileIDs = ids
        provisioningProfiles = bundle.profiles.compactMap { payload in
            guard let id = payload.profile_id else { return nil }
            return ProvisioningProfileChoice(id: id, name: payload.display_name ?? id, listenPort: payload.listen_port, relayCount: payload.relays.count, mode: payload.mode, backgroundResident: payload.background_resident ?? false, udpEnabled: payload.udp_enabled, uotEnabled: payload.uot_enabled ?? false)
        }
        let resident = selected.contains { $0.background_resident ?? false }
        if updateResident && resident != backgroundResident { setBackgroundResident(resident) }
    }
    private func managedStatus(prefix: String) -> String {
        let title = provisioningDisplayName.isEmpty ? "远端配置" : provisioningDisplayName
        if provisioningRevision.isEmpty { return "\(title) · \(prefix)" }
        return "\(title) · \(prefix) · \(provisioningRevision)"
    }

    private func applyManagedDocument(
        _ document: RelayProvisioningDocument,
        preferredSelection: Set<String>? = nil,
        updateResident: Bool = true
    ) throws -> ManagedLaunchPlan {
        switch document {
        case .profile(let payload):
            resetProvisioningBundleState()
            let provisioned = try payload.validatedProfile()
            if provisioned.userspace { try TransportKeyStore.save(provisioned.transport_key ?? "") }
            try applyProvisionedSummary(payload)
            UserDefaults.standard.set(try provisioned.preferenceData(), forKey: "multipath-profile-v2")
            provisioningRevision = payload.revision ?? ""
            provisioningDisplayName = payload.display_name ?? ""
            let resident = payload.background_resident ?? false
            if updateResident && resident != backgroundResident { setBackgroundResident(resident) }
            return ManagedLaunchPlan(
                action: "run",
                stdinData: try JSONEncoder().encode(provisioned),
                extraArguments: [],
                resident: resident,
                revision: provisioningRevision,
                displayName: provisioningDisplayName,
                selectedProfileIDs: []
            )

        case .bundle(let bundle):
            try bundle.validate()
            let available = Set(bundle.profiles.compactMap(\.profile_id))
            var selection = (preferredSelection ?? savedBundleSelection(bundle.bundle_id)).intersection(available)
            if bundle.mode == "single_select" {
                if selection.count != 1 { selection = bundle.profiles.first?.profile_id.map { [$0] } ?? [] }
            } else if selection.isEmpty {
                selection = available
            }
            _ = try bundle.selectedProfiles(ids: selection)
            saveBundleSelection(bundle.bundle_id, ids: selection)
            lastProvisioningBundle = bundle
            provisioningIsBundle = true
            provisioningBundleMode = bundle.mode
            provisioningBundleID = bundle.bundle_id
            provisioningRevision = bundle.revision
            provisioningDisplayName = bundle.display_name
            provisioningRuntimeStatus = [:]
            provisioningRuntimeError = [:]
            provisioningTCPPaths = [:]
            provisioningUDPPaths = [:]
            provisioningDiagnostics = [:]
            try applyBundleSelection(bundle, ids: selection, updateResident: updateResident)
            let selected = try orderedSelectedPayloads(bundle, ids: selection)
            let resident = selected.contains { $0.background_resident ?? false }
            return ManagedLaunchPlan(
                action: "run-bundle",
                stdinData: try JSONEncoder().encode(bundle),
                extraArguments: selected.compactMap(\.profile_id),
                resident: resident,
                revision: bundle.revision,
                displayName: bundle.display_name,
                selectedProfileIDs: selection
            )
        }
    }

    private func selectedIDsForFetchedDocument(_ document: RelayProvisioningDocument) throws -> Set<String> {
        switch document {
        case .profile:
            return []
        case .bundle(let bundle):
            let available = Set(bundle.profiles.compactMap(\.profile_id))
            var selected: Set<String>
            if provisioningBundleID == bundle.bundle_id, !provisioningSelectedProfileIDs.isEmpty {
                selected = provisioningSelectedProfileIDs.intersection(available)
            } else {
                selected = savedBundleSelection(bundle.bundle_id).intersection(available)
            }
            if bundle.mode == "single_select" {
                if selected.count != 1 { selected = bundle.profiles.first?.profile_id.map { [$0] } ?? [] }
            } else if selected.isEmpty {
                selected = available
            }
            _ = try bundle.selectedProfiles(ids: selected)
            saveBundleSelection(bundle.bundle_id, ids: selected)
            return selected
        }
    }

    private func managedLaunchPlanFromCache() throws -> (ManagedProvisioningCachedDocument, ManagedLaunchPlan)? {
        let endpoint = provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !endpoint.isEmpty,
              let cached = try ManagedProvisioningCacheStore.load(endpoint: endpoint) else { return nil }
        provisioningLastSync = cached.fetchedAt
        let plan = try applyManagedDocument(cached.document, preferredSelection: cached.selectedProfileIDs)
        return (cached, plan)
    }

#if UI_TEST
    func managedCacheLaunchSummaryForTests() throws -> (action: String, selectedCount: Int, fetchedAt: Date)? {
        guard let (cached, plan) = try managedLaunchPlanFromCache() else { return nil }
        return (plan.action, plan.selectedProfileIDs.count, cached.fetchedAt)
    }
#endif

    @discardableResult
    private func startManagedFromCache(automatic: Bool) -> Bool {
        do {
            guard let (_, plan) = try managedLaunchPlanFromCache() else { return false }
            provisioningUsingCache = true
            provisioningUpdatePending = false
            provisioningStatus = managedStatus(prefix: "使用本地缓存")
            problem = nil
            if automatic && !plan.resident {
                append("缓存配置已关闭后台常驻，本次自动恢复取消")
                return true
            }
            append("使用最近一次成功同步的远端配置立即启动")
            launch(plan.action, automatic: automatic, stdinData: plan.stdinData, extraArguments: plan.extraArguments)
            scheduleProvisioningBackgroundRefresh(after: 0.5, reason: "启动后台同步")
            return true
        } catch {
            append("远端配置本地缓存不可用，将重新同步 API")
            return false
        }
    }

    func scheduleProvisioningBackgroundRefresh(after delay: TimeInterval, reason: String) {
        guard remoteConfigurationSelected, !provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        provisioningRefreshWorkItem?.cancel()
        let item = DispatchWorkItem { [weak self] in self?.beginProvisioningBackgroundRefresh(reason: reason) }
        provisioningRefreshWorkItem = item
        DispatchQueue.main.asyncAfter(deadline: .now() + max(0.1, delay), execute: item)
    }

    private func scheduleProvisioningRefreshFromCache() {
        guard remoteConfigurationSelected, !provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        do {
            guard let cached = try ManagedProvisioningCacheStore.load(endpoint: provisioningURL) else { return }
            provisioningLastSync = cached.fetchedAt
            let delay = ManagedProvisioningCachePolicy.refreshDelay(fetchedAt: cached.fetchedAt)
            scheduleProvisioningBackgroundRefresh(after: delay, reason: "48 小时自动同步")
        } catch {
            // A broken cache must never block the current runtime. First-use/manual sync can replace it.
        }
    }

    private func beginProvisioningBackgroundRefresh(reason: String) {
        guard remoteConfigurationSelected else { return }
        if provisioningBackgroundRefreshing || provisioningSyncing {
            scheduleProvisioningBackgroundRefresh(after: 60, reason: reason)
            return
        }
        let endpoint = provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !endpoint.isEmpty else { return }
        provisioningBackgroundRefreshing = true
        Task { @MainActor [weak self] in
            guard let self else { return }
            do {
                let result = try await RelayProvisioningClient.fetchWithResponse(endpoint: endpoint)
                let selection = try self.selectedIDsForFetchedDocument(result.document)
                let now = Date()
                try ManagedProvisioningCacheStore.save(
                    responseData: result.responseData,
                    endpoint: endpoint,
                    selectedProfileIDs: selection,
                    fetchedAt: now
                )
                self.provisioningLastSync = now
                self.provisioningRefreshRetryAttempt = 0
                self.provisioningBackgroundRefreshing = false

                if !ManagedProvisioningCachePolicy.shouldApplyRefreshImmediately(runtimeActive: self.running || self.busy) {
                    let previousRevision = self.provisioningRevision
                    let revision: String
                    let name: String
                    switch result.document {
                    case .profile(let payload):
                        revision = payload.revision ?? ""
                        name = payload.display_name ?? ""
                    case .bundle(let bundle):
                        revision = bundle.revision
                        name = bundle.display_name
                    }
                    self.provisioningUpdatePending = revision != previousRevision
                    if self.provisioningUpdatePending {
                        let title = name.isEmpty ? "远端配置" : name
                        self.provisioningStatus = revision.isEmpty
                            ? "\(title) · 已后台更新 · 下次重连生效"
                            : "\(title) · 已后台更新 · \(revision) · 下次重连生效"
                        self.append("Provisioning 后台同步到新配置；当前连接保持不变，下次重连生效")
                    } else {
                        self.provisioningUsingCache = false
                        self.provisioningStatus = self.managedStatus(prefix: "后台同步正常")
                    }
                } else {
                    _ = try self.applyManagedDocument(result.document, preferredSelection: selection)
                    self.provisioningUsingCache = false
                    self.provisioningUpdatePending = false
                    self.provisioningStatus = self.managedStatus(prefix: "已同步")
                    self.problem = nil
                }
                self.scheduleProvisioningBackgroundRefresh(
                    after: ManagedProvisioningCachePolicy.refreshInterval,
                    reason: "48 小时自动同步"
                )
            } catch {
                self.provisioningBackgroundRefreshing = false
                self.provisioningUsingCache = true
                self.provisioningStatus = self.managedStatus(prefix: "使用本地缓存 · API 暂不可用")
                self.append("Provisioning 后台同步失败；现有缓存与当前连接保持有效")
                let delay = ManagedProvisioningCachePolicy.retryDelay(attempt: self.provisioningRefreshRetryAttempt)
                self.provisioningRefreshRetryAttempt = min(
                    self.provisioningRefreshRetryAttempt + 1,
                    ManagedProvisioningCachePolicy.retryDelays.count - 1
                )
                self.scheduleProvisioningBackgroundRefresh(after: delay, reason: "API 后台重试")
            }
        }
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
            try? ManagedProvisioningCacheStore.updateSelection(endpoint: provisioningURL, selectedProfileIDs: next)
            try applyBundleSelection(bundle, ids: next)
            provisioningRuntimeStatus = [:]
            provisioningRuntimeError = [:]
            provisioningTCPPaths = [:]
            provisioningUDPPaths = [:]
            provisioningDiagnostics = [:]
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
        provisioningStatus = source == "local" ? "本地配置" : (provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "请填写 API 地址" : "远端配置 · 本地缓存优先")
    }

    func saveProvisioningURL() {
        guard !configurationLocked else { return }
        do {
            let cleaned = provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines)
            if cleaned.isEmpty {
                try ProvisioningURLStore.delete()
                try? ManagedProvisioningCacheStore.delete()
                provisioningStatus = "请填写 API 地址"
                provisioningRevision = ""
                provisioningDisplayName = ""
                provisioningLastSync = nil
                provisioningUsingCache = false
                provisioningUpdatePending = false
                resetProvisioningBundleState()
                append("远端配置 API 已清除")
                return
            }
            _ = try RelayProvisioningClient.endpointURL(cleaned)
            let previous = (try? ProvisioningURLStore.load()) ?? ""
            if !previous.isEmpty && previous != cleaned { try? ManagedProvisioningCacheStore.delete() }
            try ProvisioningURLStore.save(cleaned)
            provisioningURL = cleaned
            configurationSource = "remote"
            UserDefaults.standard.set("remote", forKey: Self.configurationSourceKey)
            resetProvisioningBundleState()
            provisioningLastSync = nil
            provisioningUsingCache = false
            provisioningUpdatePending = false
            if let cached = try ManagedProvisioningCacheStore.load(endpoint: cleaned) {
                provisioningLastSync = cached.fetchedAt
                _ = try applyManagedDocument(cached.document, preferredSelection: cached.selectedProfileIDs)
                provisioningUsingCache = true
                provisioningStatus = managedStatus(prefix: "本地缓存可用")
                scheduleProvisioningRefreshFromCache()
            } else {
                provisioningStatus = "远端配置 · 首次同步后生成本地缓存"
            }
            append("远端配置 API 已保存")
            problem = nil
        } catch { problem = error.localizedDescription }
    }

    func clearProvisioningURL() {
        guard !configurationLocked else { return }
        do {
            try ProvisioningURLStore.delete()
            try? ManagedProvisioningCacheStore.delete()
            provisioningRefreshWorkItem?.cancel()
            provisioningRefreshWorkItem = nil
            provisioningURL = ""
            provisioningStatus = "请填写 API 地址"
            provisioningRevision = ""
            provisioningDisplayName = ""
            provisioningLastSync = nil
            provisioningUsingCache = false
            provisioningUpdatePending = false
            resetProvisioningBundleState()
            problem = nil
            append("远端配置 API 与本地缓存已清除")
        } catch { problem = error.localizedDescription }
    }

    func syncProvisioning(startAfterSync: Bool = false, automatic: Bool = false) {
        guard !provisioningSyncing else { return }
        if busy || running {
            scheduleProvisioningBackgroundRefresh(after: 0.1, reason: "手动后台同步")
            return
        }
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
                let result = try await RelayProvisioningClient.fetchWithResponse(endpoint: endpoint)
                let selection = try self.selectedIDsForFetchedDocument(result.document)
                let now = Date()
                try ManagedProvisioningCacheStore.save(
                    responseData: result.responseData,
                    endpoint: endpoint,
                    selectedProfileIDs: selection,
                    fetchedAt: now
                )
                let plan = try self.applyManagedDocument(result.document, preferredSelection: selection)
                self.provisioningLastSync = now
                self.provisioningUsingCache = false
                self.provisioningUpdatePending = false
                self.provisioningRefreshRetryAttempt = 0
                self.provisioningSyncing = false
                self.provisioningStatus = self.managedStatus(prefix: "已同步并缓存")
                self.append("Provisioning 同步成功；最新配置已写入本地缓存")
                self.problem = nil
                self.scheduleProvisioningBackgroundRefresh(
                    after: ManagedProvisioningCachePolicy.refreshInterval,
                    reason: "48 小时自动同步"
                )
                if startAfterSync {
                    if automatic && !plan.resident {
                        self.append("Provisioning 已关闭后台常驻，本次自动恢复取消")
                    } else {
                        self.launch(plan.action, automatic: automatic, stdinData: plan.stdinData, extraArguments: plan.extraArguments)
                    }
                }
            } catch {
                self.provisioningSyncing = false
                if (try? ManagedProvisioningCacheStore.load(endpoint: endpoint)) != nil {
                    self.provisioningUsingCache = true
                    self.provisioningStatus = self.managedStatus(prefix: "同步失败 · 本地缓存可用")
                    self.problem = "Provisioning API 同步失败；本地缓存仍可用于启动"
                    let delay = ManagedProvisioningCachePolicy.retryDelay(attempt: self.provisioningRefreshRetryAttempt)
                    self.provisioningRefreshRetryAttempt = min(
                        self.provisioningRefreshRetryAttempt + 1,
                        ManagedProvisioningCachePolicy.retryDelays.count - 1
                    )
                    self.scheduleProvisioningBackgroundRefresh(after: delay, reason: "API 后台重试")
                } else {
                    self.provisioningStatus = "同步失败 · 尚无本地缓存"
                    self.problem = "Provisioning API 同步失败：\(error.localizedDescription)"
                }
                self.append("Provisioning API 同步失败；未覆盖最近一次成功缓存")
            }
        }
    }

    func applyRemoteAssignedProvisioningURL(_ endpoint: String) throws {
        guard remoteManagementEnabled else { throw Message("远程管理未在本机启用") }
        let cleaned = endpoint.trimmingCharacters(in: .whitespacesAndNewlines)
        _ = try RelayProvisioningClient.endpointURL(cleaned)
        let previous = provisioningURL
        try ProvisioningURLStore.save(cleaned)
        provisioningURL = cleaned
        configurationSource = "remote"
        UserDefaults.standard.set("remote", forKey: Self.configurationSourceKey)
        if previous != cleaned {
            resetProvisioningBundleState()
            provisioningRevision = ""
            provisioningDisplayName = ""
            provisioningUpdatePending = false
            provisioningUsingCache = false
            provisioningStatus = "远程管理分配 · 正在同步"
        }
        if running || busy {
            scheduleProvisioningBackgroundRefresh(after: 0.1, reason: "远程管理配置分配")
        } else if !provisioningSyncing {
            syncProvisioning()
        }
    }

    func startForwarding(automatic: Bool = false) {
        guard !busy && !running && !provisioningSyncing else { return }
        if remoteConfigurationSelected {
            guard !provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { problem = "请先填写远端配置 API 地址"; return }
            if startManagedFromCache(automatic: automatic) { return }
            syncProvisioning(startAfterSync: true, automatic: automatic)
        } else {
            launch("run", automatic: automatic)
        }
    }

    func profile() throws -> Profile {
        guard let port = Int(listenPort) else {throw Message("请输入本地入口端口")}
        let cleaned = relays.map {RelayRow(host:$0.host.trimmingCharacters(in:.whitespacesAndNewlines),port:$0.port,download_mbps:$0.download_mbps,upload_mbps:$0.upload_mbps)}
        let p = Profile(schema_version:3,mode:"userspace_multipath",listen_port:port,relays:cleaned,udp_enabled:udpEnabled,tcp_enabled:tcpEnabled,transport_key:transportKey.trimmingCharacters(in:.whitespacesAndNewlines),scheduler_mode:schedulerMode,uot_enabled:uotEnabled)
        try p.validate()
        return p
    }
    struct Message: LocalizedError { var text: String; init(_ text: String) {self.text = text}; var errorDescription: String? {text} }
    func save() {
        guard !configurationLocked else { return }
        do {
            let p = try profile()
            try TransportKeyStore.save(p.transport_key ?? "")
            UserDefaults.standard.set(try p.preferenceData(), forKey: "multipath-profile-v2")
            problem = nil
            append("Userspace 配置已保存；传输密钥保存在本机钥匙串")
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
        if (line.contains("MPX/4 Draft") || line.contains("MPX/4 Protocol Version")) && !line.contains("错误") { return nil }
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
        let action = requestedAction == "doctor" ? "doctor-userspace" : requestedAction
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
            if action == "run-bundle" {
                provisioningRuntimeStatus = [:]
                provisioningRuntimeError = [:]
                provisioningTCPPaths = [:]
                provisioningUDPPaths = [:]
                provisioningDiagnostics = [:]
            }
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
    @discardableResult
    func consumeBundleProfileEvent(_ event: EngineEvent) -> Bool {
        guard let profileID = event.profile_id, event.bundle_id != nil else { return false }
        let title = event.profile_name ?? profileID
        switch event.kind {
        case "connecting", "reconnect_attempt":
            provisioningRuntimeStatus[profileID] = "连接中"
            provisioningRuntimeError.removeValue(forKey: profileID)
        case "reconnecting":
            if let seconds = event.retry_after_seconds, seconds > 0 {
                provisioningRuntimeStatus[profileID] = "重连中 · \(seconds)s"
            } else {
                provisioningRuntimeStatus[profileID] = "重连中"
            }
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

        var diagnostic = provisioningDiagnostics[profileID] ?? ProfileDiagnosticState()
        if let value = event.configured_scheduler_mode { diagnostic.configuredSchedulerMode = value }
        if let value = event.effective_scheduler_mode { diagnostic.effectiveSchedulerMode = value }
        if let value = event.mode_switches { diagnostic.schedulerModeSwitches = value }
        if let value = event.last_mode_reason { diagnostic.lastSchedulerModeReason = value }
        if event.kind == "stats" || event.kind == "listening" {
            diagnostic.paths = event.paths ?? diagnostic.paths
            diagnostic.connections = event.connections ?? diagnostic.connections
            diagnostic.sent = event.sent ?? diagnostic.sent
            diagnostic.received = event.received ?? diagnostic.received
            diagnostic.reorderBytes = event.reorder_bytes ?? diagnostic.reorderBytes
            diagnostic.reorderPeak = event.reorder_peak ?? diagnostic.reorderPeak
            diagnostic.pendingBytes = event.pending_bytes ?? diagnostic.pendingBytes
            diagnostic.retransmits = event.retransmits ?? diagnostic.retransmits
            if let current = event.path_stats { diagnostic.tcpPaths = current; provisioningTCPPaths[profileID] = current }
        }
        if event.kind == "udp_stats" {
            diagnostic.udpConnections = event.connections ?? diagnostic.udpConnections
            diagnostic.udpSent = event.sent ?? diagnostic.udpSent
            diagnostic.udpReceived = event.received ?? diagnostic.udpReceived
            diagnostic.udpDropped = event.dropped ?? diagnostic.udpDropped
            if let current = event.path_stats { diagnostic.udpPaths = current; provisioningUDPPaths[profileID] = current }
        }
        if let current = event.resources { diagnostic.resources = current }
        if let current = event.lifecycle { diagnostic.lifecycle = current }
        provisioningDiagnostics[profileID] = diagnostic

        if event.kind == "error" { append("[\(title)] \(customerFacingRemoteError(event.message))") }
        else if event.kind == "reconnecting" {
            let seconds = event.retry_after_seconds ?? 0
            append(seconds > 0 ? "[\(title)] 断线，\(seconds) 秒后自动重连" : "[\(title)] 断线，正在自动重连")
        } else if ["connecting","reconnect_attempt","ready","listening","transport_closed"].contains(event.kind) {
            let label: String
            switch event.kind {
            case "connecting", "reconnect_attempt": label = "正在连接"
            case "ready": label = "认证通过"
            case "listening": label = "已启动"
            default: label = "已停止"
            }
            append("[\(title)] \(label)")
        }
        return true
    }

    private func consume(_ bytes: Data, process child: Process) {
        guard process === child else {return}
        pending.append(bytes)
        if pending.count > 131072 {pending = Data();return}
        while let end = pending.firstIndex(of: 10) {
            let line = pending.prefix(upTo: end); pending.removeSubrange(...end)
            guard let event = try? JSONDecoder().decode(EngineEvent.self, from: line) else {continue}

            if consumeBundleProfileEvent(event) { continue }

            receiveSchedulerEvent(event)
            switch event.kind {
            case "bundle_listening":
                running = true; busy = false
                recoveryAttempt = 0; needsRecovery = false
                let reconnecting = event.reconnecting_profiles ?? 0
                if reconnecting > 0 {
                    status = "部分配置运行中"
                    problem = nil
                } else {
                    problem = nil
                    status = (event.connecting_profiles ?? 0) > 0 ? "部分配置已启动" : "多配置入口已启动"
                }
            case "bundle_degraded":
                running = true; busy = false
                recoveryAttempt = 0; needsRecovery = false
                status = "部分配置运行中"
                problem = nil
            case "bundle_reconnecting":
                running = true; busy = false
                recoveryAttempt = 0; needsRecovery = false
                status = "全部配置重连中"
                problem = nil
            case "bundle_connecting":
                status = event.message ?? "部分配置连接中"
            case "bundle_failed":
                running = false; busy = false
                status = "全部配置不可用"
                problem = event.message ?? "所有选中 Profile 均不可用"
            case "listening":
                running = true; busy = false
                recoveryAttempt = 0; needsRecovery = false; problem = nil
                status = listeningStatus
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
        remoteControlClient?.stop()
        remoteControlClient = nil
        recoveryWorkItem?.cancel(); recoveryWorkItem = nil
        if !backgroundResident { setWantsForwarding(false) }
        defer { endForwardingActivity() }
        guard let child = process, child.isRunning else {return}
        child.terminate()
        for _ in 0..<20 {if !child.isRunning {return};usleep(50000)}
        kill(child.processIdentifier, SIGKILL)
    }

}

private struct DeskCard<Content: View>: View {
    let content: Content
    init(@ViewBuilder content: () -> Content) { self.content = content() }
    var body: some View {
        VStack(alignment: .leading, spacing: 12) { content }
            .padding(16)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color(nsColor: .controlBackgroundColor))
            .overlay(RoundedRectangle(cornerRadius: 16).stroke(Color.black.opacity(0.08), lineWidth: 1))
            .clipShape(RoundedRectangle(cornerRadius: 16))
    }
}

private struct DeskMetricTile: View {
    let label: String
    let value: String
    let detail: String
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(label).font(.system(size: 11, weight: .medium)).foregroundColor(.secondary)
            Text(value).font(.system(size: 19, weight: .semibold, design: .rounded)).lineLimit(1)
            Text(detail).font(.system(size: 10)).foregroundColor(.secondary).lineLimit(1)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .background(Color(nsColor: .controlBackgroundColor).opacity(0.8))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(Color.black.opacity(0.06), lineWidth: 1))
        .clipShape(RoundedRectangle(cornerRadius: 12))
    }
}

private struct DeskStatusPill: View {
    let running: Bool
    let text: String
    var body: some View {
        HStack(spacing: 7) {
            Circle().fill(running ? Color.green : Color.gray.opacity(0.65)).frame(width: 8, height: 8)
            Text(text).font(.system(size: 11, weight: .semibold))
        }
        .padding(.horizontal, 10).padding(.vertical, 7)
        .background((running ? Color.green : Color.gray).opacity(0.12))
        .foregroundColor(running ? .green : .secondary)
        .clipShape(Capsule())
    }
}

private struct DeskSidebarItem: View {
    let title: String
    let icon: String
    let selected: Bool
    let action: () -> Void
    var body: some View {
        Button(action: action) {
            HStack(spacing: 10) {
                Image(systemName: icon).frame(width: 17)
                Text(title)
                Spacer()
            }
            .font(.system(size: 12, weight: selected ? .semibold : .regular))
            .foregroundColor(selected ? .primary : .secondary)
            .padding(.horizontal, 10).padding(.vertical, 9)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(selected ? Color.accentColor.opacity(0.12) : Color.clear)
            .clipShape(RoundedRectangle(cornerRadius: 10))
            .contentShape(RoundedRectangle(cornerRadius: 10))
        }
        .buttonStyle(.plain)
    }
}

struct DesktopView: View {
    @ObservedObject var model = Model.shared
    @ObservedObject var updater = AppUpdater.shared
    var locked: Bool { model.configurationLocked }

    var body: some View {
        ZStack {
            Color(nsColor: .windowBackgroundColor).ignoresSafeArea()
            HStack(spacing: 0) {
                sidebar
                Divider()
                VStack(spacing: 0) {
                    topbar
                    Divider()
                    ScrollView {
                        pageContent
                            .padding(.horizontal, 22)
                            .padding(.vertical, 20)
                    }
                }
            }
        }
        .frame(minWidth: 700, minHeight: 620)
    }

    private var sidebar: some View {
        VStack(alignment: .leading, spacing: 17) {
            HStack(spacing: 10) {
                ZStack {
                    RoundedRectangle(cornerRadius: 10).fill(LinearGradient(colors: [.indigo, .blue], startPoint: .topLeading, endPoint: .bottomTrailing))
                    Image(systemName: "point.3.connected.trianglepath.dotted").foregroundColor(.white).font(.system(size: 17, weight: .semibold))
                }.frame(width: 34, height: 34)
                VStack(alignment: .leading, spacing: 2) {
                    Text("MPTCP Desk").font(.system(size: 14, weight: .semibold))
                    Text("MPX/4 Client").font(.system(size: 10)).foregroundColor(.secondary)
                }
            }
            Divider()
            Text("WORKSPACE").font(.system(size: 10, weight: .bold)).foregroundColor(.secondary)
            DeskSidebarItem(title: "连接概览", icon: "bolt.horizontal.circle", selected: model.tab == 0) { model.tab = 0 }
            DeskSidebarItem(title: "路径诊断", icon: "waveform.path.ecg", selected: model.tab == 2) { model.tab = 2 }
            DeskSidebarItem(title: "运行日志", icon: "doc.text.magnifyingglass", selected: model.tab == 1) { model.tab = 1 }
            DeskSidebarItem(title: "设置", icon: "slider.horizontal.3", selected: model.tab == 3) { model.tab = 3 }
            Spacer(minLength: 12)
            VStack(alignment: .leading, spacing: 8) {
                Text("CURRENT STATE").font(.system(size: 10, weight: .bold)).foregroundColor(.secondary)
                DeskStatusPill(running: model.running, text: model.status)
                Text(model.remoteConfigurationSelected ? "远端配置" : "本地配置")
                    .font(.system(size: 10)).foregroundColor(.secondary)
                if model.remoteManagementEnabled {
                    Label(model.remoteControlConnected ? "设备已连接" : model.remoteControlStatus, systemImage: "lock.shield")
                        .font(.system(size: 10)).foregroundColor(.secondary).lineLimit(1)
                }
            }
        }
        .padding(16)
        .frame(width: 168)
        .frame(maxHeight: .infinity, alignment: .topLeading)
        .background(Color(nsColor: .controlBackgroundColor).opacity(0.72))
    }

    private var topbar: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 3) {
                Text(pageTitle).font(.system(size: 19, weight: .semibold))
                Text(pageSubtitle).font(.system(size: 11)).foregroundColor(.secondary)
            }
            Spacer()
            DeskStatusPill(running: model.running, text: model.status)
            if model.running || model.busy {
                Button { model.stop() } label: { Label("停止", systemImage: "stop.fill") }
                    .buttonStyle(.bordered)
                    .tint(.red)
                    .disabled(model.status == "停止中")
            } else {
                Button { model.startForwarding() } label: { Label("启动转发", systemImage: "play.fill") }
                    .buttonStyle(.borderedProminent)
                    .tint(.indigo)
                    .disabled(locked)
            }
        }
        .padding(.horizontal, 22).padding(.vertical, 14)
    }

    private var pageTitle: String {
        switch model.tab { case 0: return "连接概览"; case 1: return "运行日志"; case 2: return "路径诊断"; default: return "设置" }
    }
    private var pageSubtitle: String {
        switch model.tab { case 0: return "配置入口、数据源与当前传输状态"; case 1: return "引擎事件和恢复记录"; case 2: return "Carrier、Scheduler 与资源窗口"; default: return "远程管理、后台常驻和更新" }
    }
    @ViewBuilder private var pageContent: some View {
        switch model.tab {
        case 0: overview
        case 1: logPage
        case 2: diagnosticsPage
        default: settingsPage
        }
    }

    private var overview: some View {
        VStack(alignment: .leading, spacing: 14) {
            DeskCard {
                HStack(alignment: .top, spacing: 12) {
                    VStack(alignment: .leading, spacing: 7) {
                        Text(model.remoteConfigurationSelected ? "远端配置源" : "本地配置源").font(.system(size: 15, weight: .semibold))
                        Text(model.remoteConfigurationSelected ? "从 Provisioning API 获取 Profile 或 Bundle" : "直接在此编辑当前 Profile，并保存到本机")
                            .font(.system(size: 11)).foregroundColor(.secondary)
                    }
                    Spacer()
                    Picker("配置来源", selection: Binding(get: { model.configurationSource }, set: { model.setConfigurationSource($0) })) {
                        Text("本地").tag("local")
                        Text("远端").tag("remote")
                    }
                    .pickerStyle(.segmented).labelsHidden().frame(width: 150).disabled(locked)
                }
                if model.remoteConfigurationSelected { remoteSourceEditor.padding(.top, 13) }
            }
            if model.remoteConfigurationSelected && model.provisioningManaged {
                remoteProfileCard
            } else if !model.remoteConfigurationSelected {
                localEditor
            }
            connectionMetrics
            if let problem = model.problem, !problem.isEmpty {
                Label(problem, systemImage: "exclamationmark.triangle.fill")
                    .font(.system(size: 11)).foregroundColor(.red)
                    .padding(12).frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color.red.opacity(0.08)).clipShape(RoundedRectangle(cornerRadius: 12))
            }
        }
    }

    private var remoteSourceEditor: some View {
        VStack(alignment: .leading, spacing: 9) {
            SecureField("Provisioning API URL", text: $model.provisioningURL).textFieldStyle(.roundedBorder).disabled(locked)
            HStack(spacing: 8) {
                Button("保存地址") { model.saveProvisioningURL() }.disabled(model.provisioningSyncing || locked)
                Button { model.syncProvisioning() } label: {
                    if model.provisioningSyncing { ProgressView().controlSize(.small) } else { Label("同步", systemImage: "arrow.clockwise") }
                }.disabled(model.provisioningSyncing || model.provisioningURL.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || locked)
                if !model.provisioningURL.isEmpty { Button("清除") { model.clearProvisioningURL() }.disabled(model.provisioningSyncing || locked) }
                Spacer()
                Text(model.provisioningStatus).font(.system(size: 10)).foregroundColor(.secondary)
            }
            if model.provisioningUsingCache { Label("使用最近一次成功缓存，API 在后台刷新", systemImage: "clock.arrow.circlepath").font(.system(size: 10)).foregroundColor(.secondary) }
        }
    }

    private var localEditor: some View {
        VStack(alignment: .leading, spacing: 14) {
            DeskCard {
                VStack(alignment: .leading, spacing: 13) {
                    HStack {
                        VStack(alignment: .leading, spacing: 3) {
                            Text("运行配置").font(.system(size: 15, weight: .semibold))
                            Text("编辑本机 Profile；敏感 Transport Key 只保存在钥匙串").font(.system(size: 10)).foregroundColor(.secondary)
                        }
                        Spacer()
                        Button { model.importProfile() } label: { Label("导入", systemImage: "square.and.arrow.down") }.buttonStyle(.borderless).disabled(locked)
                        Button { model.save() } label: { Label("保存", systemImage: "square.and.arrow.down.on.square") }.buttonStyle(.bordered).disabled(locked)
                    }
                    Label("MPX/4 Userspace", systemImage: "point.3.connected.trianglepath.dotted").font(.system(size: 12, weight: .medium)).foregroundColor(.secondary)
                    HStack(spacing: 12) {
                        VStack(alignment: .leading, spacing: 5) { Text("本地入口").font(.system(size: 10)).foregroundColor(.secondary); HStack { Text("127.0.0.1").foregroundColor(.secondary); TextField("端口", text: $model.listenPort).frame(width: 76); Button { copy("127.0.0.1:\(model.listenPort)") } label: { Image(systemName: "doc.on.doc") }.buttonStyle(.borderless) } }
                        if model.userspace { VStack(alignment: .leading, spacing: 5) { Text("Scheduler").font(.system(size: 10)).foregroundColor(.secondary); Picker("Scheduler", selection: $model.schedulerMode) { ForEach(SchedulerPolicy.allCases) { Text($0.title).tag($0.rawValue) } }.labelsHidden().frame(width: 140).accessibilityIdentifier("scheduler-policy") } }
                        Spacer()
                    }
                    HStack(spacing: 18) {
                        Toggle("TCP", isOn: $model.tcpEnabled).toggleStyle(.switch).disabled(locked)
                        Toggle("原生 UDP", isOn: Binding(get: { model.udpEnabled }, set: { model.setNativeUDPEnabled($0) })).toggleStyle(.switch).disabled(locked)
                        Toggle("UoT", isOn: Binding(get: { model.uotEnabled }, set: { model.setUOTEnabled($0) })).toggleStyle(.switch).disabled(locked)
                    }
                    if model.userspace {
                        Text(model.uotEnabled ? "UoT 经 TCP 多路径转发 UDP；需 Landing 支持，与原生 UDP 互斥。" : "原生 UDP 与 UoT 二选一；TCP 可独立开启。")
                            .font(.system(size: 10)).foregroundColor(.secondary)
                        Text(model.schedulerExplanation).font(.system(size: 10)).foregroundColor(.secondary)
                    }
                    if model.userspace {
                        SecureField("Transport Key（64 位十六进制）", text: $model.transportKey).textFieldStyle(.roundedBorder).disabled(locked)
                    }
                }
            }
            relayEditor
        }
        .disabled(locked)
    }

    private var relayEditor: some View {
        DeskCard {
            VStack(alignment: .leading, spacing: 12) {
                HStack { VStack(alignment: .leading, spacing: 3) { Text("Relay 路径").font(.system(size: 15, weight: .semibold)); Text("每条路径都会建立独立 Carrier").font(.system(size: 10)).foregroundColor(.secondary) }; Spacer(); Button { model.relays.append(RelayRow(host: "", port: 21001)) } label: { Label("添加路径", systemImage: "plus") }.buttonStyle(.bordered).disabled(model.relays.count >= 8 || locked) }
                if model.userspace && model.schedulerMode == SchedulerPolicy.weighted.rawValue { Text("Weighted 模式需要每条 Relay 的下行容量。上行可留空自动估算。").font(.system(size: 10)).foregroundColor(.secondary) }
                ForEach(Array(model.relays.enumerated()), id: \.element.id) { index, relay in
                    HStack(spacing: 8) {
                        Text("\(index + 1)").font(.system(size: 11, weight: .semibold, design: .rounded)).frame(width: 22, height: 22).background(Color.indigo.opacity(0.1)).clipShape(RoundedRectangle(cornerRadius: 6))
                        TextField("Relay IPv4", text: Binding(get: { model.relays[index].host }, set: { model.relays[index].host = $0 })).textFieldStyle(.roundedBorder)
                        TextField("端口", value: Binding(get: { model.relays[index].port }, set: { model.relays[index].port = $0 }), formatter: Self.portFormatter).frame(width: 72).textFieldStyle(.roundedBorder)
                        if model.userspace && model.schedulerMode == SchedulerPolicy.weighted.rawValue {
                            TextField("↓ Mbps", value: Binding(get: { model.relays[index].download_mbps }, set: { model.relays[index].download_mbps = $0 }), formatter: Self.bandwidthFormatter).frame(width: 82).textFieldStyle(.roundedBorder)
                            TextField("↑ Mbps", value: Binding(get: { model.relays[index].upload_mbps }, set: { model.relays[index].upload_mbps = $0 }), formatter: Self.bandwidthFormatter).frame(width: 82).textFieldStyle(.roundedBorder)
                        }
                        Button { model.relays.removeAll { $0.id == relay.id } } label: { Image(systemName: "trash") }.buttonStyle(.borderless).foregroundColor(.red).disabled(model.relays.count <= 2 || locked)
                    }
                }
            }
        }
    }

    private var remoteProfileCard: some View {
        DeskCard {
            VStack(alignment: .leading, spacing: 12) {
                HStack { VStack(alignment: .leading, spacing: 4) { Text(model.provisioningIsBundle ? "远端 Bundle" : "远端 Profile").font(.system(size: 15, weight: .semibold)); Text(model.provisioningDisplayName.isEmpty ? "已同步配置" : model.provisioningDisplayName).font(.system(size: 11)).foregroundColor(.secondary) }; Spacer(); Text(model.provisioningRevision).font(.system(size: 10, design: .monospaced)).foregroundColor(.secondary) }
                if model.provisioningIsBundle {
                    Text(model.provisioningBundleMode == "parallel" ? "并行模式 · 可同时运行多个入口" : "单选模式 · 每次运行一个入口").font(.system(size: 11)).foregroundColor(.secondary)
                    ForEach(model.provisioningProfiles) { choice in remoteProfileRow(choice) }
                    if model.provisioningBundleMode == "parallel" { Text("各 Profile 独立重连；本地端口冲突会在启动前阻止整组。\n").font(.system(size: 10)).foregroundColor(.secondary) }
                } else {
                    Text("MPX/4 Userspace · 127.0.0.1:\(model.listenPort) · \(model.relays.count) 条 Relay").font(.system(size: 11, design: .monospaced)).foregroundColor(.secondary)
                }
            }
        }
    }

    private func remoteProfileRow(_ choice: ProvisioningProfileChoice) -> some View {
        let selected = model.provisioningSelectedProfileIDs.contains(choice.id)
        let state = model.provisioningRuntimeStatus[choice.id] ?? "等待启动"
        let bad = state == "错误" || model.provisioningRuntimeError[choice.id] != nil
        return Button { model.setProvisioningProfileSelected(choice.id, selected: !selected) } label: {
            HStack(spacing: 10) {
                Image(systemName: model.provisioningBundleMode == "parallel" ? (selected ? "checkmark.square.fill" : "square") : (selected ? "circle.inset.filled" : "circle"))
                    .foregroundColor(selected ? .indigo : .secondary)
                VStack(alignment: .leading, spacing: 3) { Text(choice.name).font(.system(size: 12, weight: .semibold)).foregroundColor(.primary); Text("127.0.0.1:\(String(choice.listenPort)) · \(choice.relayCount) Relays · MPX/4").font(.system(size: 10, design: .monospaced)).foregroundColor(.secondary); if let error = model.provisioningRuntimeError[choice.id] { Text(error).font(.system(size: 10)).foregroundColor(.red).lineLimit(1) } }
                Spacer(); Text(state).font(.system(size: 10, weight: .medium)).foregroundColor(bad ? .red : .secondary)
            }.padding(10).background(selected ? Color.indigo.opacity(0.07) : Color.black.opacity(0.025)).clipShape(RoundedRectangle(cornerRadius: 10))
        }.buttonStyle(.plain).disabled(model.running || model.busy || model.provisioningSyncing)
    }

    private var connectionMetrics: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("实时状态").font(.system(size: 13, weight: .semibold))
            HStack(spacing: 9) {
                DeskMetricTile(label: "TCP Carrier", value: model.paths < 0 ? "—" : String(model.paths), detail: "可用路径")
                DeskMetricTile(label: "连接", value: String(model.connections), detail: "逻辑连接")
                DeskMetricTile(label: "上传", value: Self.bytes(model.sent), detail: "累计发送")
                DeskMetricTile(label: "下载", value: Self.bytes(model.received), detail: "累计接收")
            }
            if model.datagramEnabled {
                HStack(spacing: 9) {
                    DeskMetricTile(label: "\(model.datagramLabel) 映射", value: String(model.udpConnections), detail: "活跃映射")
                    DeskMetricTile(label: "\(model.datagramLabel) 上传", value: Self.bytes(model.udpSent), detail: "UDP 载荷")
                    DeskMetricTile(label: "\(model.datagramLabel) 下载", value: Self.bytes(model.udpReceived), detail: "UDP 载荷")
                    Spacer()
                }
                if model.hasUOT { Text("UoT 共用 TCP Carrier；UoT 载荷已计入上方上传、下载流量。").font(.system(size: 10)).foregroundColor(.secondary) }
                else if model.userspace { Text("原生 UDP 健康路径：\(model.udpHealthyPaths)").font(.system(size: 10)).foregroundColor(.secondary) }
            }
        }
    }

    private var diagnosticsPage: some View {
        VStack(alignment: .leading, spacing: 14) {
            if model.remoteConfigurationSelected && model.provisioningIsBundle {
                HStack(spacing: 9) { DeskMetricTile(label: "活跃 Profile", value: String(model.provisioningSelectedProfileIDs.count), detail: "Bundle 选择"); DeskMetricTile(label: "TCP 路径", value: String(model.paths), detail: "当前汇总"); DeskMetricTile(label: "连接", value: String(model.connections), detail: "当前汇总"); Spacer() }
                ForEach(model.provisioningProfiles.filter { model.provisioningSelectedProfileIDs.contains($0.id) }) { choice in
                    profileDiagnostic(choice: choice, diagnostic: model.provisioningDiagnostics[choice.id] ?? ProfileDiagnosticState())
                }
            } else if model.userspace {
                schedulerSummary(configured: model.configuredSchedulerMode, effective: model.effectiveSchedulerMode, switches: model.schedulerModeSwitches, reason: model.lastSchedulerModeReason)
                pathOverview(model.tcpPaths)
                HStack(spacing: 9) { DeskMetricTile(label: "当前重排", value: Self.bytes(model.reorderBytes), detail: "正在等待有序数据"); DeskMetricTile(label: "重排峰值", value: Self.bytes(model.reorderPeak), detail: "会话峰值"); DeskMetricTile(label: "等待确认", value: Self.bytes(model.pendingBytes), detail: "可靠数据"); Spacer() }
                resourcePanels(model.resources, streamExpanded: $model.localStreamResourceExpanded, windowExpanded: $model.localWindowResourceExpanded)
                pathSection(model.uotEnabled ? "TCP / UoT 共用路径" : "TCP 路径", model.tcpPaths, hideEndpoint: model.remoteConfigurationSelected)
                if model.udpEnabled { pathSection("UDP 路径", model.udpPaths, hideEndpoint: model.remoteConfigurationSelected) }
            }
        }
    }

    @ViewBuilder private func profileDiagnostic(choice: ProvisioningProfileChoice, diagnostic: ProfileDiagnosticState) -> some View {
        DeskCard {
            VStack(alignment: .leading, spacing: 12) {
                HStack { VStack(alignment: .leading, spacing: 3) { Text(choice.name).font(.system(size: 14, weight: .semibold)); Text("127.0.0.1:\(String(choice.listenPort))").font(.system(size: 10, design: .monospaced)).foregroundColor(.secondary) }; Spacer(); Text(model.provisioningRuntimeStatus[choice.id] ?? "等待启动").font(.system(size: 10, weight: .semibold)).foregroundColor(model.provisioningRuntimeError[choice.id] == nil ? .secondary : .red) }
                if let error = model.provisioningRuntimeError[choice.id] { Text(error).font(.system(size: 11)).foregroundColor(.red) }
                schedulerSummary(configured: diagnostic.configuredSchedulerMode, effective: diagnostic.effectiveSchedulerMode, switches: diagnostic.schedulerModeSwitches, reason: diagnostic.lastSchedulerModeReason)
                pathOverview(diagnostic.tcpPaths)
                HStack(spacing: 9) { DeskMetricTile(label: "Carrier", value: String(diagnostic.paths), detail: "在线路径"); DeskMetricTile(label: "连接", value: String(diagnostic.connections), detail: "逻辑连接"); DeskMetricTile(label: "上传", value: Self.bytes(diagnostic.sent), detail: "累计"); DeskMetricTile(label: "下载", value: Self.bytes(diagnostic.received), detail: "累计") }
                HStack(spacing: 9) { DeskMetricTile(label: "重排", value: Self.bytes(diagnostic.reorderBytes), detail: "当前"); DeskMetricTile(label: "峰值", value: Self.bytes(diagnostic.reorderPeak), detail: "历史峰值"); DeskMetricTile(label: "重传", value: String(diagnostic.retransmits), detail: "TCP"); Spacer() }
                resourcePanels(diagnostic.resources, streamExpanded: profileStreamBinding(choice.id), windowExpanded: profileWindowBinding(choice.id))
                if choice.udpEnabled || choice.uotEnabled {
                    HStack(spacing: 9) {
                        DeskMetricTile(label: choice.uotEnabled ? "UoT 映射" : "UDP 映射", value: String(diagnostic.udpConnections), detail: "活跃映射")
                        DeskMetricTile(label: "UDP 上传", value: Self.bytes(diagnostic.udpSent), detail: "UDP 载荷")
                        DeskMetricTile(label: "UDP 下载", value: Self.bytes(diagnostic.udpReceived), detail: "UDP 载荷")
                    }
                }
                if choice.uotEnabled { Text("UoT 共用 TCP Carrier；载荷已计入上方上传、下载流量。").font(.system(size: 10)).foregroundColor(.secondary) }
                pathSection(choice.uotEnabled ? "TCP / UoT 共用路径" : "TCP 路径", diagnostic.tcpPaths, hideEndpoint: true)
                if !choice.uotEnabled && !diagnostic.udpPaths.isEmpty { pathSection("UDP 路径", diagnostic.udpPaths, hideEndpoint: true) }
            }
        }
    }

    private func schedulerSummary(configured: String, effective: String, switches: UInt64, reason: String) -> some View {
        HStack(spacing: 8) {
            Text("配置 \(Model.schedulerTitle(configured))").font(.system(size: 11, weight: .semibold))
            Image(systemName: "arrow.right").font(.system(size: 9)).foregroundColor(.secondary)
            Text("当前 \(Model.schedulerTitle(effective))").font(.system(size: 11, weight: .semibold)).foregroundColor(.indigo)
            Text("· 切换 \(switches) 次").font(.system(size: 10)).foregroundColor(.secondary)
            Spacer()
            if !reason.isEmpty { Text(reason).font(.system(size: 10)).foregroundColor(.secondary).lineLimit(1) }
        }
        .padding(10).background(Color.indigo.opacity(0.06)).clipShape(RoundedRectangle(cornerRadius: 10))
        .accessibilityIdentifier("scheduler-status")
    }

    private func pathOverview(_ paths: [PathMetric]) -> some View {
        let online = paths.filter(\.connected).count
        let learning = paths.filter { ($0.role ?? "").lowercased() == "learning" }.count
        let degraded = paths.filter { ["probe", "backup"].contains(($0.role ?? "").lowercased()) }.count
        let rtts = paths.filter { $0.connected && $0.rtt_ms > 0 }.map(\.rtt_ms)
        let averageRTT = rtts.isEmpty ? "—" : String(format: "%.1f ms", rtts.reduce(0, +) / Double(rtts.count))
        return HStack(spacing: 9) {
            DeskMetricTile(label: "在线路径", value: "\(online) / \(paths.count)", detail: "Carrier 状态")
            DeskMetricTile(label: "Learning", value: String(learning), detail: "正在收集样本")
            DeskMetricTile(label: "保护 / 探测", value: String(degraded), detail: "Probe + Backup")
            DeskMetricTile(label: "平均 RTT", value: averageRTT, detail: "在线路径")
        }
    }

    @ViewBuilder private func resourcePanels(_ resource: ResourceMetric?, streamExpanded: Binding<Bool>, windowExpanded: Binding<Bool>) -> some View {
        VStack(spacing: 8) {
            DisclosureGroup(isExpanded: streamExpanded) {
                if let resource { VStack(alignment: .leading, spacing: 5) { Text("活跃 Stream \(resource.active_streams) · Closing \(resource.closing_streams ?? 0) · 本地连接 \(resource.local_connections ?? 0)"); Text("Opening \(resource.lifecycle_opening ?? 0) · 双向开放 \(resource.lifecycle_open_bidirectional ?? 0) · 半关闭 \(resource.lifecycle_half_closed ?? 0)").foregroundColor(.secondary); Text("空闲 DATA >30s / >1m / >5m：\(resource.data_idle_over_30s ?? 0) / \(resource.data_idle_over_1m ?? 0) / \(resource.data_idle_over_5m ?? 0)").foregroundColor(.secondary) }.font(.system(size: 10, design: .monospaced)).padding(.top, 8) } else { Text("暂无 Stream / 生命周期资源数据").font(.system(size: 10)).foregroundColor(.secondary).padding(.top, 7) }
            } label: { HStack { Label("Stream / 生命周期", systemImage: "arrow.triangle.branch"); Spacer(); Text(resource.map { "活跃 \($0.active_streams)" } ?? "等待数据").font(.system(size: 10)).foregroundColor(.secondary) } }.padding(11).background(Color.black.opacity(0.035)).clipShape(RoundedRectangle(cornerRadius: 10)).accessibilityIdentifier("stream-resource-disclosure")
            DisclosureGroup(isExpanded: windowExpanded) {
                if let resource { VStack(alignment: .leading, spacing: 5) { Text("待确认帧 \(resource.pending_frames)/\(resource.pending_frame_limit)"); Text("接收未消费 \(Self.bytes(resource.receive_credit_bytes))/\(Self.bytes(resource.receive_credit_limit_bytes)) · 实际分页 \(Self.bytes(resource.receive_allocated_bytes))/\(Self.bytes(resource.receive_allocated_limit_bytes))").foregroundColor(.secondary); Text("DATA 队列 \(resource.data_pending_frames ?? 0) · 控制队列 \(resource.control_pending_frames ?? 0) · 窗口等待 \(resource.window_blocked_writers ?? 0)").foregroundColor(.secondary); Text("待调度 \(Self.bytes(resource.ready_data_bytes ?? 0)) · 软目标 \(Self.bytes(resource.queue_admission_limit_bytes ?? 0)) · 排队等待 \(resource.queue_admission_waiters ?? 0)").foregroundColor(.secondary) }.font(.system(size: 10, design: .monospaced)).padding(.top, 8) } else { Text("暂无 Window / Credit 资源数据").font(.system(size: 10)).foregroundColor(.secondary).padding(.top, 7) }
            } label: { HStack { Label("Window / Credit", systemImage: "rectangle.split.3x1"); Spacer(); Text(resource.map { "待确认 \($0.pending_frames)" } ?? "等待数据").font(.system(size: 10)).foregroundColor(.secondary) } }.padding(11).background(Color.black.opacity(0.035)).clipShape(RoundedRectangle(cornerRadius: 10)).accessibilityIdentifier("window-resource-disclosure")
        }
    }

    private var logPage: some View {
        DeskCard {
            VStack(alignment: .leading, spacing: 12) {
                HStack { VStack(alignment: .leading, spacing: 3) { Text("引擎事件").font(.system(size: 15, weight: .semibold)); Text("保留最近的连接、认证、重连和资源事件").font(.system(size: 10)).foregroundColor(.secondary) }; Spacer(); Button { copy(model.logs.joined(separator: "\n")) } label: { Label("复制日志", systemImage: "doc.on.doc") }.buttonStyle(.bordered) }
                ScrollView { Text(model.logs.isEmpty ? "暂无日志。启动转发后，状态事件会显示在这里。" : model.logs.joined(separator: "\n")).font(.system(size: 11, design: .monospaced)).foregroundColor(model.logs.isEmpty ? .secondary : .primary).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .topLeading).padding(10) }.frame(minHeight: 360, maxHeight: .infinity).background(Color.black.opacity(0.035)).clipShape(RoundedRectangle(cornerRadius: 10))
            }
        }
    }

    private var settingsPage: some View {
        VStack(alignment: .leading, spacing: 14) {
            DeskCard {
                VStack(alignment: .leading, spacing: 12) {
                    HStack { VStack(alignment: .leading, spacing: 3) { Text("远程管理").font(.system(size: 15, weight: .semibold)); Text("设备只通过 HTTPS long polling 主动连接，不提供远程 Shell").font(.system(size: 10)).foregroundColor(.secondary) }; Spacer(); Toggle("", isOn: Binding(get: { model.remoteManagementEnabled }, set: { model.setRemoteManagementEnabled($0) })).toggleStyle(.switch) }
                    Text("默认关闭；控制服务器地址和配对必须在这台 Mac 本地完成。").font(.system(size: 10)).foregroundColor(.secondary)
                    TextField("控制服务器 HTTPS 根地址", text: $model.remoteControlServer).textFieldStyle(.roundedBorder).disabled(!model.remoteManagementEnabled)
                    HStack { Button("保存服务器") { model.saveRemoteControlServer() }.disabled(!model.remoteManagementEnabled); Text(model.remoteControlStatus).font(.system(size: 10)).foregroundColor(model.remoteControlConnected ? .green : .secondary) }
                    if model.remoteDeviceID.isEmpty { HStack { TextField("一次性配对码", text: $model.remotePairingCode).textFieldStyle(.roundedBorder); Button("配对并启用") { model.pairRemoteManagement() }.buttonStyle(.borderedProminent).disabled(!model.remoteManagementEnabled) } } else { HStack { Label("设备 ID \(model.remoteDeviceID.prefix(12))…", systemImage: "checkmark.shield").font(.system(size: 10)).foregroundColor(.secondary); Spacer(); Button("解除配对…") { model.unpairRemoteManagement() }.buttonStyle(.bordered).tint(.red) } }
                }
            }
            DeskCard {
                HStack { VStack(alignment: .leading, spacing: 4) { Text("后台常驻").font(.system(size: 15, weight: .semibold)); Text("登录、唤醒、网络恢复或引擎异常后自动重建转发").font(.system(size: 10)).foregroundColor(.secondary) }; Spacer(); Toggle("", isOn: Binding(get: { model.backgroundResident }, set: { model.setBackgroundResident($0) })).toggleStyle(.switch).disabled(model.remoteConfigurationSelected) }
                Text(model.backgroundResidentStatus).font(.system(size: 10)).foregroundColor(.secondary).padding(.top, 8)
            }
            DeskCard {
                HStack { VStack(alignment: .leading, spacing: 4) { Text("应用更新").font(.system(size: 15, weight: .semibold)); Text("仅使用内置 EdDSA 签名更新通道").font(.system(size: 10)).foregroundColor(.secondary) }; Spacer(); Button("检查更新") { updater.checkForUpdates() }.buttonStyle(.bordered) }
                HStack { Toggle("自动检查", isOn: Binding(get: { updater.automaticChecks }, set: { updater.setAutomaticChecks($0) })).toggleStyle(.switch); Spacer(); Text(updater.status).font(.system(size: 10)).foregroundColor(.secondary) }
                Divider()
                HStack { VStack(alignment: .leading, spacing: 3) { Text("运行环境诊断").font(.system(size: 13, weight: .semibold)); Text("检查引擎、文件描述符和本地运行条件").font(.system(size: 10)).foregroundColor(.secondary) }; Spacer(); Button("检查环境") { model.launch("doctor") }.buttonStyle(.bordered).disabled(locked) }
            }
        }
    }

    private func pathSection(_ title: String, _ paths: [PathMetric], hideEndpoint: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title).font(.system(size: 13, weight: .semibold))
            if paths.isEmpty { Text("尚无路径数据；启动后自动更新").font(.system(size: 11)).foregroundColor(.secondary) }
            ForEach(paths) { path in
                VStack(alignment: .leading, spacing: 6) {
                    HStack {
                        Circle().fill(path.connected ? Color.green : Color.gray).frame(width: 7, height: 7)
                        Text(hideEndpoint ? "路径 \(path.id + 1)" : "\(path.id) · \(path.address)").font(.system(size: 11, design: .monospaced))
                        if let role = path.role, !role.isEmpty {
                            Text(Self.pathRoleTitle(role)).font(.system(size: 9, weight: .bold)).foregroundColor(Self.pathRoleColor(role))
                                .padding(.horizontal, 6).padding(.vertical, 3).background(Self.pathRoleColor(role).opacity(0.11)).clipShape(Capsule())
                        }
                        Spacer()
                        Text(path.connected ? "在线" : "重连中").font(.system(size: 10)).foregroundColor(.secondary)
                    }
                    Text(String(format: "RTT %.1f ms · Goodput %.2f MiB/s · 队列 %@ · 在途 %@ · 错误 %llu", path.rtt_ms, path.goodput_bps / 1048576, Self.bytes(path.queue_bytes), Self.bytes(path.outstanding_bytes), path.errors)).font(.system(size: 10, design: .monospaced)).foregroundColor(.secondary)
                    HStack(spacing: 12) {
                        if let delivery = path.measured_delivery_bps, delivery > 0 { Label(String(format: "测得 %.2f Mbps", delivery * 8 / 1_000_000), systemImage: "speedometer") }
                        if let samples = path.delivery_samples { Label("样本 \(samples)", systemImage: "chart.bar") }
                        if let attempts = path.dial_attempts { Label("拨号 \(attempts)", systemImage: "arrow.triangle.2.circlepath") }
                        if let budget = path.budget_bytes, budget > 0 { Label("预算 \(Self.bytes(budget))", systemImage: "gauge.with.dots.needle.33percent") }
                    }.font(.system(size: 9)).foregroundColor(.secondary)
                    if let reason = path.role_reason, !reason.isEmpty { Text(reason).font(.system(size: 10)).foregroundColor(.secondary).lineLimit(1) }
                    if let error = path.last_error, !error.isEmpty { Text(hideEndpoint ? "路径连接异常" : error).font(.system(size: 10)).foregroundColor(.secondary).lineLimit(1) }
                }.padding(10).background(Color.black.opacity(0.035)).clipShape(RoundedRectangle(cornerRadius: 10))
            }
        }
    }

    private static func pathRoleTitle(_ role: String) -> String {
        switch role.lowercased() { case "learning": return "LEARNING"; case "active": return "ACTIVE"; case "probe": return "PROBE"; case "backup": return "BACKUP"; default: return role.uppercased() }
    }

    private static func pathRoleColor(_ role: String) -> Color {
        switch role.lowercased() { case "learning": return .indigo; case "active": return .green; case "probe": return .orange; case "backup": return .secondary; default: return .secondary }
    }

    private func profileStreamBinding(_ id: String) -> Binding<Bool> { Binding(get: { model.profileStreamResourceExpanded.contains(id) }, set: { var next = model.profileStreamResourceExpanded; if $0 { next.insert(id) } else { next.remove(id) }; model.profileStreamResourceExpanded = next }) }
    private func profileWindowBinding(_ id: String) -> Binding<Bool> { Binding(get: { model.profileWindowResourceExpanded.contains(id) }, set: { var next = model.profileWindowResourceExpanded; if $0 { next.insert(id) } else { next.remove(id) }; model.profileWindowResourceExpanded = next }) }
    private func copy(_ value: String) { let pasteboard = NSPasteboard.general; pasteboard.clearContents(); pasteboard.setString(value, forType: .string) }
    private static func bytes(_ count: Int64) -> String { ByteCountFormatter.string(fromByteCount: count, countStyle: .binary) }
    private static func bytes(_ count: Int) -> String { ByteCountFormatter.string(fromByteCount: Int64(count), countStyle: .binary) }
    private static let portFormatter: NumberFormatter = { let f = NumberFormatter(); f.numberStyle = .none; f.minimum = 1; f.maximum = 65535; f.allowsFloats = false; return f }()
    private static let bandwidthFormatter: NumberFormatter = { let f = NumberFormatter(); f.numberStyle = .decimal; f.minimum = 0.1; f.maximum = 6553.5; f.minimumFractionDigits = 0; f.maximumFractionDigits = 1; f.allowsFloats = true; return f }()
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
            Text("逻辑连接：\(model.connections)")
            if model.datagramEnabled { Text("\(model.datagramLabel) 映射：\(model.udpConnections)") }
        }
        Text("后台常驻：\(model.backgroundResident ? model.backgroundResidentStatus : "关闭")")
        Text("远程管理：\(model.remoteManagementEnabled ? model.remoteControlStatus : "关闭")")
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
        Button("检查更新…") { AppUpdater.shared.checkForUpdates() }
        Divider()
        Button("退出 MPTCP Desk") { NSApplication.shared.terminate(nil) }.keyboardShortcut("q")
    }
}

#if !UI_TEST
@main struct MPTCPDesktop: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var delegate
    var body: some Scene {
        Window("MPTCP Desk", id: "main") {DesktopView()}.windowResizability(.contentMinSize)
            .commands {
                CommandGroup(replacing:.newItem) {}
                CommandGroup(replacing:.appInfo) {
                    Button("关于 MPTCP Desk"){NSApplication.shared.orderFrontStandardAboutPanel()}
                    Button("检查更新…"){AppUpdater.shared.checkForUpdates()}
                }
            }
        MenuBarExtra("MPTCP Desk", systemImage: "network") { StatusMenu() }
    }
}
#endif
