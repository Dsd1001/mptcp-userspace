# MPTCP Desk 0.10.7 / MPX/4 Draft 04

MPTCP Desk 是 MPTCP Userspace 的 macOS GUI Client。

## 配置来源

首页明确区分：

- **本地配置**：直接编辑并运行本机 Profile；
- **远端配置**：保存并同步 secret Provisioning Profile/Bundle URL。

只有选择远端配置时才显示 URL 输入区。secret URL 保存在 Keychain。

## 本地 Profile

一份 Userspace Profile 包含：

- listen_port；
- TCP/UDP 开关；
- 2–8 条 Relay；
- Auto / Aggregate / Protect / Weighted；
- Transport Key；
- 可选后台常驻。

Native MPTCP fallback 是独立的 macOS-only 模式。

## Provisioning Bundle

Bundle 可以包含 1–32 个 Profile：

- single_select：一次选择一份；
- parallel：选择一份或多份独立 runtime。

parallel 启动前会原子检查本地端口。预检查通过后，一份 Profile 连接/认证失败不会终止其他健康 Profile。

Bundle 不会合并 Relay 集合。每个活跃 Profile 都拥有自己的 listener、MPX Session、Scheduler 与 Transport Key。

0.10.7 能读取 0.10.2 引入的 v/n/d 不透明 Provisioning 加密响应，同时继续接受旧明文 schema-1/schema-2 响应用于迁移。

## 远端配置缓存

首次远端同步成功后，会在 Application Support/MPTCPDesk 中保存持久化 Last Known Good 缓存。后续点击启动、App/系统重启或睡眠唤醒恢复都优先直接使用缓存，不等待 API。

API 在后台刷新；拿到新 revision 时只更新下一次重连配置，不强制断开当前 Session。成功同步 48 小时后自动再次检查，失败按 1m / 5m / 30m / 3h 退避。缓存不设过期时间，并通过 URL 指纹限制只能由同一 Provisioning URL 使用。

## Parallel Profile 自动重连

0.10.7 中 parallel Bundle 的每个 Profile 独立自愈。断线或 child runtime 退出后按 1s → 2s → 5s → 10s → 30s，随后每 30s 重试；恢复到 listening 后清零自己的退避。健康 Profile 不会被重启；所有 Profile 暂时不可用时 App 保持“全部配置重连中”。

## 内置更新

0.10.7 集成 Sparkle 2 更新通道。设置页和菜单栏可以手动检查更新，默认每 24 小时自动检查。更新 Feed 固定指向 GitHub Release 的 appcast.xml，DMG 与 appcast 使用 EdDSA 签名；远程 update 只能触发同一签名通道。

## 远程管理

远程管理默认关闭，只能在这台 Mac 上手动开启。控制服务器 URL 和一次性配对也必须在本机完成，API 无权打开开关或修改服务器。

启用后 Client 只主动建立 HTTPS long poll，不需要公网 IP。后台可控制 Desired State、分配 Profile/Bundle、请求同步配置、重启转发和签名客户端更新，并读取运行/逐 Profile 状态。设备 secret 存 Keychain，不提供远程 Shell 或任意命令。

## 路径诊断

远端 Bundle 现在也是完整的逐 Profile 诊断，不再只显示路径卡片。

每个 Profile 可以查看：

- configured/effective Scheduler；
- path / connection 数量；
- upload/download；
- reorder / pending / retransmit；
- RTT / Goodput / queue / outstanding / error；
- Stream / Lifecycle 资源；
- Window / Credit 资源。

两组深度资源面板默认收起，展开状态只在当前 App 会话中保存。

远端客户 UI 会隐藏 Relay IP/端口和原始 endpoint 错误。

## 后台常驻

开启后，MPTCP Desk 会注册 macOS 登录项，并可在登录、睡眠唤醒、网络恢复或 engine 异常退出后重建 runtime。

用户手动点击“停止”后不会自动拉起。

## 安全

Transport Key 和 Provisioning URL 都属于凭据。

Provisioning URL 保存在 Keychain；Bundle Transport Key 只从权威 API 响应进入内存/engine stdin，不复制到普通 preferences。

当前 DMG 使用 ad-hoc 签名，未做 Developer ID notarization。

当前协议与控制面说明见 docs/userspace/PROTOCOL.md 与 docs/userspace/PROVISIONING.md。
## Keychain Broker

从 0.10.11 起，MPTCP Desk 本体不直接访问 Keychain。固定签名的 `MPTCPKeychainBroker` v1 首次运行时安装到 `~/Library/Application Support/MPTCP Desk/KeychainBroker/v1/`，并长期复用同一份二进制。Broker 只接受满足固定 `org.mptcp.desktop` + 本地签名证书要求的父进程调用。

现有 `MPTCPDesk.UserspaceTransport`、`MPTCPDesk.Provisioning`、`MPTCPDesk.RemoteControl` 条目保持原 service/account，不复制为第二套 secret。第一次由 Broker 接管旧条目时可能各出现一次授权框；此后 Keychain partition 绑定的是冻结 Broker 的固定 cdhash，而不是每次更新都会变化的 App cdhash。不要删除或替换 Application Support 中的 Broker，除非执行明确的 Broker 升级/恢复流程。
