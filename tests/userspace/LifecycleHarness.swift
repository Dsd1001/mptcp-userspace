import Foundation

@main struct LifecycleHarness {
    static func main() {
        precondition(!BackgroundRecoveryPolicy.shouldRecover(backgroundResident: false, wantsForwarding: true, sleeping: false, quitting: false, manualStopRequested: false))
        precondition(!BackgroundRecoveryPolicy.shouldRecover(backgroundResident: true, wantsForwarding: false, sleeping: false, quitting: false, manualStopRequested: false))
        precondition(!BackgroundRecoveryPolicy.shouldRecover(backgroundResident: true, wantsForwarding: true, sleeping: true, quitting: false, manualStopRequested: false))
        precondition(!BackgroundRecoveryPolicy.shouldRecover(backgroundResident: true, wantsForwarding: true, sleeping: false, quitting: true, manualStopRequested: false))
        precondition(!BackgroundRecoveryPolicy.shouldRecover(backgroundResident: true, wantsForwarding: true, sleeping: false, quitting: false, manualStopRequested: true))
        precondition(BackgroundRecoveryPolicy.shouldRecover(backgroundResident: true, wantsForwarding: true, sleeping: false, quitting: false, manualStopRequested: false))

        precondition(BackgroundRecoveryPolicy.canRetry(attempt: 0))
        precondition(BackgroundRecoveryPolicy.canRetry(attempt: 4))
        precondition(!BackgroundRecoveryPolicy.canRetry(attempt: 5))
        precondition(!BackgroundRecoveryPolicy.canRetry(attempt: 500))

        let expected: [TimeInterval] = [1, 2, 5, 10, 30, 30, 30]
        for (attempt, value) in expected.enumerated() {
            precondition(BackgroundRecoveryPolicy.retryDelay(attempt: attempt) == value)
        }
        precondition(BackgroundRecoveryPolicy.retryDelay(attempt: -10) == 1)

        print("PASS: background resident recovery policy and bounded 1/2/5/10/30s retry schedule")
    }
}
