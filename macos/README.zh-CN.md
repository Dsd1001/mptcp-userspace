# MPTCP Desk 0.10.3 / MPX/4 Draft 04

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

0.10.3 能读取 0.10.2 引入的 v/n/d 不透明 Provisioning 加密响应，同时继续接受旧明文 schema-1/schema-2 响应用于迁移。

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
