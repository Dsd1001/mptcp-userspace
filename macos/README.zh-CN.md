# MPTCP Desk 1.0.0 / MPX/4 Protocol Version 4 Stable

MPTCP Desk 是 MPTCP Userspace 的 macOS GUI Client。1.0.0 的协议源固定为 `protocol-v4.0.0` / `44f587fd279ed2238b070dd68114c76822353f4d`。

## 配置来源

首页区分本地配置与远端 Provisioning Profile/Bundle。secret URL、Transport Key 与 Remote Control credential 通过冻结 Keychain Broker 管理。

## 本地 Profile

一份 Userspace Profile 包含 listen_port、TCP/UDP 开关、2–8 条 Relay、Auto / Aggregate / Protect / Weighted、本地 Transport Key 以及可选后台常驻。Native MPTCP fallback 是独立的 macOS-only 模式。

Stable MPX/4 的 Carrier ID 使用完整非零 VarInt 空间；当前产品仍把同时配置/运行的 Relay 限为 2–8 条，并通过 MAX_CARRIERS 表达实现并发上限。

Auto / Aggregate / Protect / Weighted 是本地 scheduler policy，不是 Stable Core 协商值。Weighted 的 receive-side 容量可通过官方 RECEIVE_CAPACITY_HINT 扩展提供给对端。

## Provisioning / Bundle

Bundle 可以包含 1–32 个 Profile，支持 single_select 和 parallel。parallel 启动前原子检查本地端口；一个 Profile 失败不会终止健康 Profile。

1.0.0 保留 opaque v/n/d Provisioning 加密响应以及旧明文 schema-1/schema-2 迁移兼容。Profile schema 1、Bundle schema 2 和现有 URL/API 数据模型保持兼容。

## Last Known Good 与自动重连

首次远端同步成功后，Application Support 中保存持久化 LKG 缓存。启动、系统重启、睡眠唤醒恢复优先使用缓存，不等待 API；后台刷新拿到新 revision 后只影响下一次重连。

parallel Bundle 的每个 Profile 独立自愈：1s → 2s → 5s → 10s → 30s，随后每 30s 重试；恢复到 listening 后清零自己的退避。健康 Profile 不重启；全 down 时 supervisor 仍继续等待恢复。

## 内置更新与远程管理

Sparkle 2 更新通道、EdDSA appcast、每 24 小时自动检查以及手动检查入口保持不变。

远程管理默认关闭，只能在本机手动开启和设置控制服务器。Client 主动通过 HTTPS long poll 工作，不要求公网 IP。服务端仅能控制 Desired State、配置同步、启动/停止、重连和签名更新；不提供远程 Shell 或任意命令。

## 路径诊断

每个 Profile 可查看 configured/effective Scheduler、path/connection 数、流量、reorder/pending/retransmit、RTT/Goodput/queue/outstanding/error，以及 Stream/Lifecycle 和 Window/Credit 资源。远端 UI 隐藏 Relay IP/端口和原始 endpoint 错误。

## Keychain Broker

从 0.10.11 起，主 App 不直接访问三类 Keychain secret。1.0.0 继续复用 **完全相同的** `MPTCPKeychainBroker` v1 二进制，安装位置和 service/account 均不变。

Broker v1 解码后二进制 SHA-256 必须为：

`5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9`

构建脚本在 App 编译前即校验该值。不要重建、重签、覆盖或升级 Broker v1；真正需要改 Broker 时必须使用新 Broker 版本和明确迁移流程。

1.0.0 的目标更新路径是 **0.10.12 → 1.0.0 更换 App、本体签名/cdhash 变化，但 Broker bytes/cdhash 不变**，从而保持 Keychain 授权连续性。

## 签名边界

当前发行使用长期固定的本地自签名代码签名证书 `MPTCP Desk Stable Local Code Signing`，不是 Apple Developer ID notarization。Sparkle 更新真实性仍由独立 EdDSA feed/signature 验证。

当前协议、调度与发布限制见 `docs/userspace/PROTOCOL.md`、`SCHEDULER-MODES.md` 与 `VALIDATION.md`。
