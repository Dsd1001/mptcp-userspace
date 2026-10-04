import Foundation
import Sparkle

@MainActor
final class AppUpdater: NSObject, ObservableObject, SPUUpdaterDelegate {
    static let shared = AppUpdater()

    @Published private(set) var status = "自动检查已启用"
    @Published private(set) var automaticChecks = true
    @Published private(set) var sessionInProgress = false

    private var remoteInstallRequested = false
    private var previousAutomaticDownloads: Bool?
#if UI_TEST
    private static let startsUpdater = false
#else
    private static let startsUpdater = true
#endif
    private lazy var updaterController = SPUStandardUpdaterController(
        startingUpdater: Self.startsUpdater,
        updaterDelegate: self,
        userDriverDelegate: nil
    )

    override private init() {
        super.init()
        let updater = updaterController.updater
        automaticChecks = updater.automaticallyChecksForUpdates
        sessionInProgress = updater.sessionInProgress
    }

    var updater: SPUUpdater { updaterController.updater }

    func checkForUpdates() {
        status = "正在检查更新…"
        updaterController.checkForUpdates(nil)
    }

    func setAutomaticChecks(_ enabled: Bool) {
        updater.automaticallyChecksForUpdates = enabled
        automaticChecks = enabled
        status = enabled ? "自动检查已启用" : "自动检查已关闭"
    }

    func requestRemoteUpdate() {
        guard !updater.sessionInProgress else {
            status = "已有更新任务正在进行"
            return
        }
        remoteInstallRequested = true
        previousAutomaticDownloads = updater.automaticallyDownloadsUpdates
        updater.automaticallyDownloadsUpdates = true
        status = "远程请求：正在检查签名更新…"
        sessionInProgress = true
        updater.checkForUpdatesInBackground()
    }

    private func restoreAutomaticDownloadPreference() {
        if let previousAutomaticDownloads {
            updater.automaticallyDownloadsUpdates = previousAutomaticDownloads
        }
        previousAutomaticDownloads = nil
    }

    func updater(_ updater: SPUUpdater, didFindValidUpdate item: SUAppcastItem) {
        sessionInProgress = true
        status = "发现新版本 " + item.displayVersionString
    }

    func updaterDidNotFindUpdate(_ updater: SPUUpdater, error: Error) {
        remoteInstallRequested = false
        restoreAutomaticDownloadPreference()
        sessionInProgress = false
        status = "已是最新版本"
    }

    func updater(_ updater: SPUUpdater, didAbortWithError error: Error) {
        remoteInstallRequested = false
        restoreAutomaticDownloadPreference()
        sessionInProgress = false
        status = "更新失败：" + error.localizedDescription
    }

    func updater(
        _ updater: SPUUpdater,
        didFinishUpdateCycleFor updateCheck: SPUUpdateCheck,
        error: Error?
    ) {
        sessionInProgress = updater.sessionInProgress
        if let error {
            status = "更新检查结束：" + error.localizedDescription
        } else if !remoteInstallRequested && !updater.sessionInProgress {
            status = "更新检查完成"
        }
    }

    func updater(
        _ updater: SPUUpdater,
        willInstallUpdateOnQuit item: SUAppcastItem,
        immediateInstallationBlock immediateInstallHandler: @escaping () -> Void
    ) -> Bool {
        guard remoteInstallRequested else { return false }
        remoteInstallRequested = false
        restoreAutomaticDownloadPreference()
        status = "签名更新已下载，正在安装并重启…"
        Model.shared.prepareForRemoteAppUpdate {
            immediateInstallHandler()
        }
        return true
    }
}
