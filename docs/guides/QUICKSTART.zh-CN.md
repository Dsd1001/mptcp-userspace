# 快速开始

[English](QUICKSTART.md)

本文面向 **MPTCP Userspace v0.10.6 / MPX/4 Draft 04**。

## 1. 下载并校验

从 [v0.10.6 Release](https://github.com/Dsd1001/mptcp-userspace/releases/tag/v0.10.6) 下载需要的文件。

常用产物：

- MPTCP-Desk-0.10.6-universal.dmg
- mptcp-client-linux-amd64 / arm64
- mptcp-landing / mptcp-landing-linux-arm64
- 需要远端配置时下载 mpx-provision / arm64
- MPTCP-Userspace-0.10.6-SHA256SUMS

安装前先校验 SHA256。

## 2. 准备 Landing

Linux 上：

~~~sh
chmod 755 ./mptcp-landing
./mptcp-landing version
./mptcp-landing menu
~~~

已有部署升级时，先备份当前二进制、systemd unit 与配置，再替换二进制并重启服务。不要把 Transport Key 写进公开仓库、Issue 或普通日志。

推荐正式组合为 0.10.6 Client + 0.10.6 Landing。

## 3. 安装 Client

### macOS

打开 Universal DMG 安装 MPTCP Desk。当前发布为 ad-hoc 签名，未做 Developer ID notarization。

首页可以选择：

- **本地配置**：直接填写运行参数；
- **远端配置**：保存一条 secret Provisioning Profile/Bundle URL。

### Linux

~~~sh
chmod 755 ./mptcp-client-linux-amd64
./mptcp-client-linux-amd64 version
./mptcp-client-linux-amd64 doctor-userspace
~~~

Linux 只支持 Userspace MPX/4；Native MPTCP fallback 仍只在 macOS。

## 4. 本地配置

当前 Userspace Profile 需要：

- listen_port，例如 1081；
- TCP/UDP 开关；
- Auto / Aggregate / Protect / Weighted；
- 2–8 条 Relay IPv4/端口；
- 64 位十六进制 Transport Key；
- Weighted 模式下的路径容量。

127.0.0.1:<listen_port> 是透明 TCP 入口，不是 SOCKS5。

## 5. 远端 Provisioning

Provisioning 可以下发单个 Profile，也可以下发包含 1–32 个 Profile 的 Bundle。

macOS 在 **远端配置** 中粘贴 secret URL，保存并完成第一次同步。第一次成功同步会生成持久化 Last Known Good 缓存；之后正常启动、App/系统重启和睡眠唤醒恢复都直接从匹配缓存启动，API 在后台刷新，不再等待 API timeout。

Linux 建议通过 stdin 传 URL，避免 bearer credential 出现在 ps：

~~~json
{"url":"https://config.example.com/v1/bundle/<secret>","profile_ids":["profile-a","profile-b"]}
~~~

~~~sh
mptcp-client-linux-amd64 validate-managed < managed.json
mptcp-client-linux-amd64 run-managed < managed.json
~~~

远端 URL 必须使用 HTTPS。0.10.2+ Provisioning 的公网响应是加密 envelope，0.10.6 Client 会自动解密。

## 6. Parallel Bundle

parallel 启动前先原子检查所有已选择 Profile 的本地 listen_port 是否唯一且可用。

预检查通过后，各 Profile 独立运行：一份配置连接/认证失败，只标记该 Profile 错误，其他健康 Profile 继续运行。只有全部不可用或用户主动停止时才结束整组。

## 7. 验证运行

MPTCP Desk 打开 **路径诊断**，查看：

- Profile 状态；
- 配置/当前 Scheduler；
- 路径数量；
- RTT / Goodput / queue / outstanding；
- retransmit、reorder、pending；
- Stream/Lifecycle 与 Window/Credit 资源。

远端 Profile 的客户 UI 会隐藏 Relay IP/端口。

更多说明见 [故障排查](TROUBLESHOOTING.zh-CN.md) 与 [部署/回滚](../userspace/DEPLOYMENT.zh-CN.md)。
