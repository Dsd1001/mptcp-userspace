# MPTCP Userspace v1.1.4 — Flow-Control Correctness & Carrier Diagnostics

发布日期：2026-10-09。基于 [v1.1.3](https://github.com/Dsd1001/mptcp-userspace/releases/tag/v1.1.3) 新建 `release/v1.1.4-admission-diagnostics` 分支发布；暂不合并到 `main`。

## 主要变更

- **修复 Queue Admission 漏唤醒**：计算 Writer 唤醒条件时使用下一帧的实际可发送大小（对端 Stream/Session WINDOW、协商的 Frame/Record 限制），不再无条件要求 32 KiB。按虚拟队列空间给多个小帧 Writer 分配唤醒额度，单轮最多唤醒 32 个。
- **细分等待原因**：增加独立 `stream_open` 和 `stream_window` 两项 Write Wait 统计；旧 `stream_window_or_open` 作为两项之和保留向后兼容，聚合时请勿重复求和。
- **Carrier 选路诊断**：在 Session 资源状态中增加 `path_selection`，分别记录选择成功与 `flight_budget`、`carrier_queue`、`role_restricted`、`path_penalty`、`no_active_carrier`、`cost_deferral`、`other` 等失败原因。每次选路恰好归属一项结果；是累计次数，不能直接等同于实际网络瓶颈时间。
- **持续采样工具**：`scripts/capture-landing-telemetry.py` 每 2 秒记录 Session/Carrier Budget、Outstanding、RTT、Wait、Dispatcher 与锁统计，便于与 tcpdump 按时间关联。仅读取现有 JSON 状态文件，不存储密钥。
- **Landing Session 数量**：新配置默认 `max_sessions=8`（以前为 4；合法范围仍为 1–16）。已有部署配置不会被二进制升级自动改写，需明确将现有配置改为 8。

## 不变的内容

- MPX/4 Protocol Version 4 Stable 和固定协议 SHA `44f587fd279ed2238b070dd68114c76822353f4d` 不变；
- 原有 32 MiB Queue Admission 软门限、128 MiB Session Credit、16 MiB Stream WINDOW、可靠重传逻辑不改；
- Weighted / Auto / Aggregate / Protect 调度语义不变；推荐 Landing 使用 CUBIC，Relay 使用 BBR；
- macOS Userspace-only 客户端及其冻结的 MPTCPKeychainBroker v1 不变；
- Windows 客户端 GUI 与独立 Engine 仍完整提供。

## 用于后续 A/B 的监控

在 Landing 上运行：

```sh
python3 capture-landing-telemetry.py --status-file /run/mptcp-userspace-landing/status.json --output /root/landing-v1.1.4-telemetry.jsonl --duration 360 --interval 2
```

同时比较单、多 Stream 的实际带宽，以及 `path_selection` 各类拒绝增量、每 Carrier 的 Flight Budget/Outstanding、`stream_open`/`stream_window` 与 Queue Admission Wait。高 Wait 计数不自动代表性能损失。本版本不宣称修复了所有真实网络 375–400 Mbps 平台期。

## 兼容与回退

仅修复实现与增加本地可观测性，没有改变 MPX/4 wire protocol。现有 Session 不支持在二进制重启中无缝迁移。可使用原有 v1.1.3 二进制和配置备份回退；若 HKBN `max_sessions` 已调成 8，回退时也可保留 8（v1.1.3 允许最多 16）。
