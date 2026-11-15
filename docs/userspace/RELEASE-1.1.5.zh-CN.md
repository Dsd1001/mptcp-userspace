# MPTCP Userspace v1.1.5 — Weighted 控制帧避堵

发布日期：2026-10-10。基于 v1.1.4 源码进行的独立实现修复；正式组件版本统一升级为 1.1.5。

## 本版仅新增一项 Weighted 控制帧优化

- 在 Carrier 原有加密 TCP writeFrames 调用内部记录实际写入开始时间。当持续等待超过 max(100ms, 2×该 Carrier minRTT)，Weighted 将该路径视为暂时不适合承载新的通用控制帧。
- 若其他有效 Carrier 的 Writer 没有阻塞，优先将 WINDOW、OPEN_OK 及待分派的可靠 OPEN/FIN/RESET 类控制帧发送给可及时写出的 Carrier，避免控制帧被停滞的数据 Socket 写入拖住。
- 直接绑定到特定 Carrier 的 ACK/PING/PONG 不改道。全部 Carrier 均阻塞时保持已有的控制发送保底逻辑，不丢弃控制帧。
- Auto、Aggregate、Protect 沿用 v1.1.4 行为；DATA 选路、Weighted Flight Budget、Cost Deferral、Queue Admission、Stream/Session WINDOW、MPX/4 Stable 协议和重传机制均不变。

## 验证

基于六条模拟 Relay、12 Stream 大流和短流混合、指定单条 Carrier 写入阻塞 1500ms 的受控故障注入，短流最慢 TTFB 从约 1322–1432ms 改善为约 254–382ms，Bulk 吞吐维持约 530–537Mbps。无故障注入的 12 Stream 同质/异构吞吐基本无回退；150 Stream 官方起速回归约539Mbps。控制路由的单元测试覆盖 ACK 原 Carrier 约束、全部 Carrier 阻塞时保底、其他调度模式隔离和加密 Socket Writer 时间戳；Go 全量测试、race 和 vet、Linux 及 Windows 客户端构建已通过。

以上为本地与模拟环境结果，不宣称修复 macOS 本地 150 Stream 同时大响应启动造成的六路 TCP 拥塞窗口坍缩，也不宣称已经完成新版本的物理 WAN 性能验收。

## 兼容、更新和回退

- 协议不变：MPX/4 Protocol Version 4 Stable；既有客户端/服务端 MPX/4 配置不需要迁移。
- 推荐 TCP 拥塞控制：Landing 使用 CUBIC，Relay 使用 BBR。
- macOS：保留 v1 冻结 Keychain Broker 二进制及原有更新机制，更新 App 主体时不重新替换 Broker。
- Windows：保留 Userspace-only GUI、独立 Engine 和原有更新元数据。
- Linux：Client、Landing 和 Provisioning 重新以统一 Source-ID 构建，并随版本更新。
- 回退：如需回退，使用 v1.1.4 已发布安装包及服务端对应二进制；服务端进程重启不会保持原 Session。

发布 v1.1.5 本身不等于在 HKBN 自动部署。物理网络高并发新一轮压力测试和更复杂的 Adaptive Weighted 算法仍属于后续独立研究范围。
