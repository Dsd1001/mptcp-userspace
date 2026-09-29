import Foundation

struct BackgroundRecoveryPolicy {
    static let retryDelays: [TimeInterval] = [1, 2, 5, 10, 30]
    static let maxAutomaticAttempts = retryDelays.count

    static func shouldRecover(
        backgroundResident: Bool,
        wantsForwarding: Bool,
        sleeping: Bool,
        quitting: Bool,
        manualStopRequested: Bool
    ) -> Bool {
        backgroundResident && wantsForwarding && !sleeping && !quitting && !manualStopRequested
    }

    static func canRetry(attempt: Int) -> Bool { attempt < maxAutomaticAttempts }

    static func retryDelay(attempt: Int) -> TimeInterval {
        let index = min(max(attempt, 0), retryDelays.count - 1)
        return retryDelays[index]
    }
}
