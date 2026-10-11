# WO-01 Weighted A/B：独立工作区阶段性审查记录（2026-10-11）

## 状态与限制

**工单未完成：NO-GO。** 目前仅 A0 基线隔离、A1 最小代码改动及 A1 工程回归已执行。A1 性能闸门尚未通过独立正交 A/B；A2 Flight Budget、B1 有界首 DATA 和 AB 多场景交替五轮均未开发/执行，不得声称整体实现或验收完成。不会将研究原型直接复制到正式候选。

## 基线和隔离

- 2026-10-11 核验 GitHub Latest Release 为 v1.1.5，tag peeled SHA `562d60c3068aaa1ecf2089f62418e88af35af2b1`。
- 独立本地 Git worktree `.scratch/workorders/wo01-ab-implementation-20261011`、branch `wo01/weighted-ab-20261011`。原 main 为 `bfc9f76734a73d0241ef0d7ea7727fd0dfddf6f2`，先前已存在12个已跟踪文件的修改及大量未追踪资料；均没有通过此工单覆盖/清理。
- 测试 Go `go1.23.12 darwin/arm64`，位置 `.scratch/go-ab-toolchain/go/bin/go`。本阶段未访问 Ali、HKBN/HKT，也没有生成公网数据面负载。
- 旧研究是技术参考，不作为最新源码基线。A1 上游重放基线必须取 v1.1.5，而非旧的 bfc9f76。

## A1 实施与工程验收

- 提交 `e9bd85f` (`Weighted: prefer eligible DATA paths without cost deferral`)：仅改 `macos/engine/multipath/session.go`。
- 在 `aggregatePathLocked` 中对 Weighted 取消由于更优路径暂时预算/队列满导致的 Cost Deferral；对非 Weighted 模式保留原硬等待；不修改 flightBudget() / weightedFlightBudget()、控制帧优先级、ACK Carrier 绑定、wire 格式和UI。
- 定向 `TestWeighted|TestScheduler|TestControl`: PASS。
- `go test -count=1 ./multipath`: PASS（25.237s）；`go test -count=1 ./...`: PASS；`go vet ./...`: PASS；macOS `go test -race -count=1 ./multipath`: PASS（26.751s）；Linux amd64 engine `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...`: PASS；Windows CLI `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...` (windows module): PASS。
- `GOOS=windows go build ./...` 在 macos/engine 目录下失败，原因是 Linux Landing 专用 syscall.Flock/Stat_t 在 Windows 不存在；这是错误目标组合，不能算 Windows CLI 失败，不掩饰该命令退出码 1。
- 此阶段无隔离吞吐原始轮次，故不能将 A1 宣称性能 PASS。研究 R6 原型提示解除硬等待有短流 P95 恶化的可能，需要 A2 独立对照。

## 未完成验收（均为 NOT RUN）

- A0 正确 Backend half-close/abortive 故障注入、所有 100ms 内核/MPX 指标与原始每轮校验。
- A1 独立 1/12/150 流、同质/异构/高 RTT 的正式 Legacy 同平台交替五轮 A/B。
- A2 自适应 Flight Budget 独立源码提交、独立 A/B 与高 BDP 性能验证。
- B1 首 DATA Session 级突发额度与公平退路、取消清理、控制帧 1500ms Stall 故障场景。
- AB 四配置至少五轮+第二批复验、150同步/错峰、后台生命周期、真实 WAN（需用户授权）。

## 下一轮安全接手

1. 继续在当前 worktree，不在原 main 上操作；用提交 `e9bd85f` 作为 A1 候选，`v1.1.5` 作为 Legacy 对照。
2. 先补 A0 半关闭夹具和结构化指标，再跑 A1-only 对照；不合格则暂不推进默认行为。
3. 另起 A2、B1 相互独立可回滚提交，强制保证控制帧优先、32MiB Admission、MPX/4 Stable和四模式不变；在真实数据齐全前不允许合并、发布和部署。
