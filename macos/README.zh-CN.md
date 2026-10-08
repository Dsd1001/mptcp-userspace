# MPTCP Desk v1.1.1

MPTCP Desk 是 MPTCP Userspace 的 macOS GUI Client，当前使用 **MPX/4 Protocol Version 4 Stable**。

```text
App Version:       1.1.1
Protocol Release:  protocol-v4.0.0
Protocol Source:   44f587fd279ed2238b070dd68114c76822353f4d
Capability Rev:    8
```

## 配置来源

首页区分：

- 本地 Profile；
- Provisioning Profile / Bundle。

Transport Key、Provisioning URL 与 Remote Control credential 通过 Keychain/Broker 边界保护，不应写入普通偏好设置或公开日志。

## Userspace Profile

一份 Profile 包含：

- 本地 `listen_port`；
- TCP/UDP 开关；
- Transport Key；
- Relay 列表；
- Auto / Aggregate / Protect / Weighted；
- Weighted 可选方向容量；
- 后台常驻等产品设置。

当前实现最多 8 条 active Carrier。Carrier ID 的 wire namespace 本身使用更大的 MPX VarInt 空间。

## Scheduler

Auto / Aggregate / Protect / Weighted 是端点本地策略，不是 MPX/4 Stable Core 的 wire Scheduler ID。

Weighted 容量是 prior，不是固定百分比分流。实时 RTT、goodput、queue/outstanding、penalty 与路径健康仍会改变实际使用比例。

## v1.1.1 Flow Control

v1.1.1 以对端发布的 Stream/Session WINDOW 作为发送信用权威来源。历史 `txUsed` / `txGrowth` 只保留为兼容诊断账本，不再形成第二套 128 MiB send-admission。

本地 pending frame/byte、Carrier flight/budget 与 receiver memory 等资源保护仍然有效。

## Bundle 与自动恢复

Bundle 支持 `single_select` 与 `parallel`。

Parallel 模式下：

- 启动前原子检查本地端口；
- 某一 Profile 远端失败不会终止其它健康 Profile；
- 失败 Profile 按 1s → 2s → 5s → 10s → 30s → 每 30s 重试；
- 恢复 listening 后重置退避；
- 全部暂时 down 时 supervisor 仍等待恢复。

## Last Known Good

首次远端同步成功后保存受保护的 LKG。App 启动、系统重启、睡眠唤醒恢复可以先从匹配缓存启动，再后台刷新 Provisioning。

变更 Provisioning URL 后不会错误复用旧来源的 LKG。

## UoT 与 Native UDP

- Native UDP 使用独立认证 UDP 数据面；
- UoT 把 UDP payload 复用到认证后的 TCP Carrier Session。

两者不是同一数据面，延迟和底层拥塞控制行为也不同。

## 内置更新与远程管理

Sparkle 负责 App 更新通道和 EdDSA feed 验证。

远程管理默认关闭，只能本地开启并配置控制服务器。设备通过出站 HTTPS long poll 工作，因此不需要公网 IP。控制平面提供有限的产品操作，不提供远程 Shell。

## Keychain Broker

v1.1.1 继续复用冻结的 `MPTCPKeychainBroker` v1，SHA256：

```text
5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9
```

构建脚本在 App 构建前校验该值。不要在保持 v1 身份的情况下重建、重签或替换 Broker；未来如需改变 Broker，应通过新 Broker 版本和明确迁移流程完成。

## Linux 网络推荐与 Mac 的关系

当前生产拓扑推荐：

- **Landing 使用 CUBIC**；
- **Relay 使用 BBR**，优先 `fq`。

这是 Linux Landing/Relay 主机的部署建议。macOS Client 不适用 Linux `sysctl net.ipv4.tcp_congestion_control`，不要把 Relay 的 sysctl 机械复制到 Mac。

## 诊断

每个 Profile 的诊断重点包括：

- configured/effective Scheduler；
- Carrier connected/role；
- RTT / goodput；
- queue / outstanding；
- retransmit / reinjection；
- Stream/Session WINDOW；
- pending/resource 状态。

出现性能平台时，应同时检查 Relay CPU/BBR 与 Landing CPU/CUBIC，而不是只看 App 内 Scheduler。

更多信息见仓库根目录 `docs/`。
