# MPTCP Userspace v1.1.6 — Weighted A+B 阶段成果（性能限制已知）

发布日期：2026-10-11。以 v1.1.5（562d60c）为基线，在独立工作树上开发并随完整组件一起发布。此版用户明确要求先发布阶段成果再继续研究；**不是 WO-01 全部性能闸门通过的宣称**，尤其不代表 150 同步流问题已经解决。

## 已落地功能

- A1：Weighted DATA 合格路径间按成本选择，避免一个更优却不可发送的 Carrier 长期阻塞其他路径；对持续阻塞的 Socket Writer 进行排除；非 Weighted 策略不改变 Cost Deferral 规则。
- A2：Weighted Flight 改为有界 baseRTT/交付反馈与冷却渐进调整；高并发（≥64 Streams）暂时退回原有的保守 Weighted Flight/Cost 策略，降低同步放大。保护 32 MiB Queue-aware Admission、不改变 Session/Stream WINDOW 硬上限。
- B1：Weighted 首 DATA 有界优先调度，在 Session 内共享额度、ACK 回补、维持普通 Bulk 公平退路；约束单 Carrier 优先队列；保留 v1.1.5 可靠控制帧避堵及 ACK/OPEN/WINDOW 原有规则。≥64 同步 Stream 保护模式暂不进行首 DATA 升级。
- 单独增加可复现的本机 127.0.0.1 回环 A/B 正确 Backend 半关闭/故障注入夹具及逐轮证据；MPX/4 Protocol Version 4 Stable wire 保持不变。

## 已有本机实验结果

以下来自 WO-01 各阶段冻结源码；**尚未全部独立重新在 v1.1.6 打包源码上完成正式性能闸门**。

- 六条各 92 Mbps Carrier、异构单向延迟 5/15/25/40/65/100ms、12 × 64 MiB：五轮 Legacy 中位约 390.7Mbps、短流 32KiB 完成 P95 约186.2ms；A+B 阶段性候选约542.6Mbps、P95约178.9ms，均 5/5 完整且无 MPX 重传。注意不同阶段提交必须单独标识 Source SHA，不可以当作当前发布 commit 的正式完整性能证明。
- 单 Stream、同质12流、单向100ms和300ms场景，各组本机五轮总体接近正式版吞吐，无明显系统性下降。
- **150 × 4 MiB 同步启动：性能 FAIL**。一批五轮 Legacy 完整性 4/5、吞吐中位约283Mbps，A+B 完整性 5/5、吞吐中位约225Mbps，后者约低 20%。两者皆存在多秒 Socket Write 阻塞、Kernel/MPX 重传与长尾；根因方向 C 未闭环，不应归功或归咎于单个尚未隔离的机制。
- 以上全部为本机成形 Relay 回环，不属于 Ali 公网/WAN 测试。短流每轮只有 12 个探针，P95/P99 受样本量限制。

## 风险与回退

- 不建议直接将 v1.1.6 用作极端 150 同步 Stream 的吞吐保证；如果关键业务高度依赖此场景，请保留 v1.1.5 安装包并先自行 A/B 验证。本版本 **不包含任何已证实的 150 Stream 长尾修复声明**。
- macOS 使用不变的 v1 Keychain Broker（原签名标识及 Broker bytes 不重构），App 内升级不应该重复安装 Broker；稳定本地签名不是 Apple Developer ID notarization。
- Windows GUI、Linux Client、Landing、Provisioning 与 Desk 统一 1.1.6；当前仅作 Release 分发，不自动部署到 HKBN/HKT/Landing/Relay/Provisioning。
- MPX/4 Stable 协议与原有密钥不变，允许分阶段升级；回退请使用 v1.1.5 发布资产（进程重启会重建原有 Session）。
- 后续独立研究针对 Socket Write 多秒阻塞、路径队列和 TCP 同步突发（R6–R9，WO-02）继续补证据；**不擅自修改生产 sysctl、线路或正式配置**。
