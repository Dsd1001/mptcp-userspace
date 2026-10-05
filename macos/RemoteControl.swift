import Foundation

struct RemoteControlCredential: Codable {
    var deviceID: String
    var secret: String
}

enum RemoteControlCredentialStore {
    static func save(_ credential: RemoteControlCredential) throws {
        try KeychainBrokerClient.save(try JSONEncoder().encode(credential), slot: .remoteControl)
    }

    static func load() throws -> RemoteControlCredential? {
        guard let data = try KeychainBrokerClient.load(.remoteControl) else { return nil }
        return try JSONDecoder().decode(RemoteControlCredential.self, from: data)
    }

    static func delete() throws {
        try KeychainBrokerClient.delete(.remoteControl)
    }
}


struct RemotePairResponse: Decodable {
    var device_id: String
    var device_secret: String
    var control_revision: UInt64
}

struct RemoteDesiredState: Decodable {
    var revision: UInt64
    var desired_state: String
    var assignment_type: String?
    var assignment_id: String?
    var provisioning_url: String?
    var restart_generation: UInt64?
    var sync_generation: UInt64?
    var update_generation: UInt64?
    var desired_version: String?
}

struct RemoteDeviceReport: Encodable {
    var app_version: String
    var running: Bool
    var status: String
    var config_revision: String
    var bundle_id: String
    var profile_status: [String:String]
    var update_status: String
    var last_error: String
    var restart_generation: UInt64
    var sync_generation: UInt64
    var update_generation: UInt64
}

enum RemoteControlEndpoint {
    static func baseURL(_ raw: String) throws -> URL {
        let cleaned = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let url = URL(string: cleaned),
              let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              components.user == nil, components.password == nil,
              components.query == nil, components.fragment == nil,
              let host = components.host, !host.isEmpty,
              components.path.isEmpty || components.path == "/" else {
            throw ProfileError("远程管理服务器需填写 HTTPS 根地址，例如 https://control.example.com")
        }
        let loopback = host == "localhost" || host == "127.0.0.1" || host == "::1"
        guard components.scheme == "https" || (components.scheme == "http" && loopback) else {
            throw ProfileError("远程管理服务器必须使用 HTTPS")
        }
        return url
    }

    static func url(base: URL, path: String, query: [URLQueryItem] = []) throws -> URL {
        guard var c = URLComponents(url: base, resolvingAgainstBaseURL: false) else {
            throw ProfileError("远程管理服务器地址无效")
        }
        c.path = path
        c.queryItems = query.isEmpty ? nil : query
        guard let result = c.url else { throw ProfileError("无法构造远程管理请求") }
        return result
    }
}

final class RemoteControlClient {
    private weak var model: Model?
    private let base: URL
    private let credential: RemoteControlCredential
    private var task: Task<Void,Never>?
    private let session: URLSession

    init(model: Model, base: URL, credential: RemoteControlCredential) {
        self.model = model
        self.base = base
        self.credential = credential
        let config = URLSessionConfiguration.ephemeral
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        config.urlCache = nil
        config.httpCookieStorage = nil
        config.urlCredentialStorage = nil
        config.timeoutIntervalForRequest = 35
        config.timeoutIntervalForResource = 40
        self.session = URLSession(configuration: config)
    }

    func start() {
        stop()
        task = Task { [weak self] in
            await self?.run()
        }
    }

    func stop() {
        task?.cancel()
        task = nil
        session.getAllTasks { tasks in tasks.forEach { $0.cancel() } }
    }

    private func authenticatedRequest(_ url: URL, method: String = "GET", body: Data? = nil) -> URLRequest {
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 35)
        request.httpMethod = method
        request.setValue(credential.deviceID, forHTTPHeaderField: "X-MPX-Device-ID")
        request.setValue("Bearer " + credential.secret, forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let body {
            request.httpBody = body
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        return request
    }

    private func run() async {
        var retryIndex = 0
        let retryDelays: [UInt64] = [1, 2, 5, 10, 30]
        while !Task.isCancelled {
            guard let model else { return }
            let enabled = await MainActor.run { model.remoteManagementEnabled }
            guard enabled else { return }
            do {
                let since = await MainActor.run { model.remoteControlRevision }
                let url = try RemoteControlEndpoint.url(
                    base: base,
                    path: "/v1/device/poll",
                    query: [URLQueryItem(name: "since", value: String(since))]
                )
                let (data, response) = try await session.data(for: authenticatedRequest(url))
                guard let http = response as? HTTPURLResponse else { throw ProfileError("远程管理返回的不是 HTTP 响应") }
                if http.statusCode == 401 {
                    await MainActor.run {
                        model.remoteControlStatus = "需要重新配对"
                        model.remoteControlConnected = false
                    }
                    try await Task.sleep(nanoseconds: 30_000_000_000)
                    continue
                }
                guard http.statusCode == 200 else { throw ProfileError("远程管理 HTTP " + String(http.statusCode)) }
                let desired = try JSONDecoder().decode(RemoteDesiredState.self, from: data)
                await MainActor.run {
                    model.remoteControlConnected = true
                    model.remoteControlStatus = "已连接"
                    model.remoteControlLastSeen = Date()
                    model.applyRemoteDesiredState(desired)
                }
                try await report()
                retryIndex = 0
            } catch is CancellationError {
                return
            } catch {
                await MainActor.run {
                    model.remoteControlConnected = false
                    model.remoteControlStatus = "连接中断 · 自动重试"
                }
                let seconds = retryDelays[min(retryIndex, retryDelays.count - 1)]
                retryIndex = min(retryIndex + 1, retryDelays.count - 1)
                try? await Task.sleep(nanoseconds: seconds * 1_000_000_000)
            }
        }
    }

    func report() async throws {
        guard let model else { return }
        let snapshot = await MainActor.run { model.remoteDeviceReport() }
        let body = try JSONEncoder().encode(snapshot)
        let url = try RemoteControlEndpoint.url(base: base, path: "/v1/device/report")
        let (_, response) = try await session.data(for: authenticatedRequest(url, method: "POST", body: body))
        guard let http = response as? HTTPURLResponse, http.statusCode == 204 else {
            throw ProfileError("远程状态上报失败")
        }
    }

    func unpair() async {
        guard let url = try? RemoteControlEndpoint.url(base: base, path: "/v1/device/unpair") else { return }
        _ = try? await session.data(for: authenticatedRequest(url, method: "POST", body: Data("{}".utf8)))
    }

    static func pair(base: URL, code: String, name: String, appVersion: String) async throws -> RemotePairResponse {
        let url = try RemoteControlEndpoint.url(base: base, path: "/v1/device/pair")
        let payload = [
            "code": code.trimmingCharacters(in: .whitespacesAndNewlines),
            "device_name": name,
            "app_version": appVersion,
        ]
        let data = try JSONSerialization.data(withJSONObject: payload)
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 15)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.httpBody = data
        let config = URLSessionConfiguration.ephemeral
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        config.urlCache = nil
        config.httpCookieStorage = nil
        config.urlCredentialStorage = nil
        config.timeoutIntervalForRequest = 15
        config.timeoutIntervalForResource = 20
        let session = URLSession(configuration: config)
        defer { session.invalidateAndCancel() }
        let (responseData, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw ProfileError("配对返回的不是 HTTP 响应") }
        guard http.statusCode == 200 else {
            throw ProfileError(http.statusCode == 403 ? "配对码无效或已过期" : "远程管理配对失败（HTTP " + String(http.statusCode) + "）")
        }
        return try JSONDecoder().decode(RemotePairResponse.self, from: responseData)
    }
}

extension Model {
    static let remoteManagementEnabledKey = "remote-management-enabled-v1"
    static let remoteControlServerKey = "remote-control-server-v1"

    func initializeRemoteManagement() {
        remoteManagementEnabled = UserDefaults.standard.bool(forKey: Self.remoteManagementEnabledKey)
        remoteControlServer = UserDefaults.standard.string(forKey: Self.remoteControlServerKey) ?? ""
        if let credential = try? RemoteControlCredentialStore.load() {
            remoteDeviceID = credential.deviceID
        }
        if remoteManagementEnabled {
            registerLoginItem()
            configureRemoteControlClient()
        } else {
            remoteControlStatus = "关闭"
        }
    }

    func saveRemoteControlServer() {
        do {
            let base = try RemoteControlEndpoint.baseURL(remoteControlServer)
            remoteControlServer = base.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
            UserDefaults.standard.set(remoteControlServer, forKey: Self.remoteControlServerKey)
            problem = nil
            remoteControlStatus = remoteManagementEnabled ? "服务器已保存" : "关闭"
            if remoteManagementEnabled { configureRemoteControlClient() }
        } catch {
            problem = error.localizedDescription
        }
    }

    func setRemoteManagementEnabled(_ enabled: Bool) {
        remoteManagementEnabled = enabled
        UserDefaults.standard.set(enabled, forKey: Self.remoteManagementEnabledKey)
        if enabled {
            registerLoginItem()
            configureRemoteControlClient()
            append("远程管理已由本机用户开启")
        } else {
            remoteControlClient?.stop()
            remoteControlClient = nil
            remoteControlConnected = false
            remoteControlStatus = "关闭"
            if !backgroundResident {
                unregisterLoginItemIfUnused()
            }
            append("远程管理已由本机用户关闭")
        }
    }

    func pairRemoteManagement() {
        let code = remotePairingCode.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !code.isEmpty else { problem = "请输入后台生成的一次性配对码"; return }
        do {
            let base = try RemoteControlEndpoint.baseURL(remoteControlServer)
            remoteControlStatus = "正在配对…"
            let appVersion = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"
            let name = Host.current().localizedName ?? "MPTCP Desk"
            Task { @MainActor [weak self] in
                guard let self else { return }
                do {
                    let response = try await RemoteControlClient.pair(base: base, code: code, name: name, appVersion: appVersion)
                    let credential = RemoteControlCredential(deviceID: response.device_id, secret: response.device_secret)
                    try RemoteControlCredentialStore.save(credential)
                    self.remoteDeviceID = response.device_id
                    self.remotePairingCode = ""
                    self.remoteControlRevision = 0
                    self.remoteRestartGeneration = 0
                    self.remoteSyncGeneration = 0
                    self.remoteUpdateGeneration = 0
                    self.remoteManagementEnabled = true
                    UserDefaults.standard.set(true, forKey: Self.remoteManagementEnabledKey)
                    self.remoteControlServer = base.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
                    UserDefaults.standard.set(self.remoteControlServer, forKey: Self.remoteControlServerKey)
                    self.registerLoginItem()
                    self.remoteControlStatus = "已配对 · 正在连接"
                    self.problem = nil
                    self.configureRemoteControlClient()
                } catch {
                    self.remoteControlStatus = "配对失败"
                    self.problem = error.localizedDescription
                }
            }
        } catch {
            problem = error.localizedDescription
        }
    }

    func unpairRemoteManagement() {
        let client = remoteControlClient
        Task { await client?.unpair() }
        remoteControlClient?.stop()
        remoteControlClient = nil
        try? RemoteControlCredentialStore.delete()
        remoteDeviceID = ""
        remoteManagementEnabled = false
        remoteControlConnected = false
        remoteControlStatus = "未配对"
        remoteControlRevision = 0
        remoteRestartGeneration = 0
        remoteSyncGeneration = 0
        remoteUpdateGeneration = 0
        UserDefaults.standard.set(false, forKey: Self.remoteManagementEnabledKey)
        if !backgroundResident {
            unregisterLoginItemIfUnused()
        }
        append("远程管理设备已在本机解除配对")
    }

    func configureRemoteControlClient() {
        remoteControlClient?.stop()
        remoteControlClient = nil
        remoteControlConnected = false
        guard remoteManagementEnabled else { remoteControlStatus = "关闭"; return }
        guard !remoteControlServer.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            remoteControlStatus = "请填写控制服务器"
            return
        }
        do {
            let base = try RemoteControlEndpoint.baseURL(remoteControlServer)
            guard let credential = try RemoteControlCredentialStore.load() else {
                remoteControlStatus = "等待配对"
                return
            }
            remoteDeviceID = credential.deviceID
            remoteControlRevision = UInt64(max(0, UserDefaults.standard.integer(forKey: "remote-control-revision." + credential.deviceID)))
            remoteRestartGeneration = UInt64(max(0, UserDefaults.standard.integer(forKey: "remote-control-restart." + credential.deviceID)))
            remoteSyncGeneration = UInt64(max(0, UserDefaults.standard.integer(forKey: "remote-control-sync." + credential.deviceID)))
            remoteUpdateGeneration = UInt64(max(0, UserDefaults.standard.integer(forKey: "remote-control-update." + credential.deviceID)))
            let client = RemoteControlClient(model: self, base: base, credential: credential)
            remoteControlClient = client
            remoteControlStatus = "正在连接…"
            client.start()
        } catch {
            remoteControlStatus = "需要重新配对"
            problem = error.localizedDescription
        }
    }

    @MainActor
    func applyRemoteDesiredState(_ desired: RemoteDesiredState) {
        guard remoteManagementEnabled else { return }
        if desired.revision >= remoteControlRevision {
            remoteControlRevision = desired.revision
            if !remoteDeviceID.isEmpty {
                UserDefaults.standard.set(remoteControlRevision, forKey: "remote-control-revision." + remoteDeviceID)
            }
        }

        if let endpoint = desired.provisioning_url, !endpoint.isEmpty, endpoint != provisioningURL {
            do {
                try applyRemoteAssignedProvisioningURL(endpoint)
            } catch {
                problem = "远程配置分配失败：" + error.localizedDescription
            }
        }

        let restartGeneration = desired.restart_generation ?? 0
        if restartGeneration > remoteRestartGeneration {
            remoteRestartGeneration = restartGeneration
            if !remoteDeviceID.isEmpty {
                UserDefaults.standard.set(restartGeneration, forKey: "remote-control-restart." + remoteDeviceID)
            }
            remoteRestartForwarding(desiredRunning: desired.desired_state == "running")
        } else {
            if desired.desired_state == "running" {
                if !running && !busy && !provisioningSyncing {
                    startForwarding()
                }
            } else if desired.desired_state == "stopped", running || busy {
                stop()
            }
        }

        let syncGeneration = desired.sync_generation ?? 0
        if syncGeneration > remoteSyncGeneration {
            remoteSyncGeneration = syncGeneration
            if !remoteDeviceID.isEmpty {
                UserDefaults.standard.set(syncGeneration, forKey: "remote-control-sync." + remoteDeviceID)
            }
            if remoteConfigurationSelected && !provisioningURL.isEmpty {
                if running || busy {
                    scheduleProvisioningBackgroundRefresh(after: 0.1, reason: "远程管理请求配置同步")
                } else if !provisioningSyncing {
                    syncProvisioning()
                }
            }
        }

        let updateGeneration = desired.update_generation ?? 0
        if updateGeneration > remoteUpdateGeneration {
            remoteUpdateGeneration = updateGeneration
            if !remoteDeviceID.isEmpty {
                UserDefaults.standard.set(updateGeneration, forKey: "remote-control-update." + remoteDeviceID)
            }
            if desired.desired_version == nil || desired.desired_version == "latest" {
                AppUpdater.shared.requestRemoteUpdate()
            }
        }
    }

    func remoteRestartForwarding(desiredRunning: Bool) {
        if running || busy {
            stop()
            guard desiredRunning else { return }
            DispatchQueue.main.asyncAfter(deadline: .now() + 2.5) { [weak self] in
                guard let self, self.remoteManagementEnabled, !self.running, !self.busy else { return }
                self.startForwarding()
            }
        } else if desiredRunning {
            startForwarding()
        }
    }

    @MainActor
    func remoteDeviceReport() -> RemoteDeviceReport {
        let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"
        return RemoteDeviceReport(
            app_version: version,
            running: running,
            status: status,
            config_revision: provisioningRevision,
            bundle_id: provisioningBundleID,
            profile_status: provisioningRuntimeStatus,
            update_status: AppUpdater.shared.status,
            last_error: problem ?? "",
            restart_generation: remoteRestartGeneration,
            sync_generation: remoteSyncGeneration,
            update_generation: remoteUpdateGeneration
        )
    }

    @MainActor
    func prepareForRemoteAppUpdate(_ completion: @escaping () -> Void) {
        append("已收到签名客户端更新；将短暂停止当前转发并安装")
        if running || busy {
            stop()
            let work = DispatchWorkItem(block: completion)
            DispatchQueue.main.asyncAfter(deadline: .now() + 2.5, execute: work)
        } else {
            completion()
        }
    }
}
