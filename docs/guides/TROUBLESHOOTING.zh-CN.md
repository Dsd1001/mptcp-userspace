# 故障排查

本文面向 **v0.10.7 / MPX/4 Draft 04**。

## 1. 先确认版本与 Source-ID

Client、Landing、Provisioning 最好使用同一正式版本。当前 0.10.7 Source-ID 为发布包中的 SOURCE_ID；不要只看文件名判断二进制来源。

Landing：

~~~sh
/usr/local/bin/mptcp-landing version
systemctl status mptcp-userspace-landing.service
~~~

Linux Client：

~~~sh
mptcp-client-linux-amd64 version
mptcp-client-linux-amd64 doctor-userspace
~~~

## 2. 完全没有流量

依次确认：

1. 本地 listen_port 是否监听；
2. Client 是否能到达至少一条 Relay；
3. Relay 是否能把 TCP 转发到正确 Landing 端口；
4. Client/Landing Transport Key 是否一致；
5. Landing backend 是否可达；
6. 上层应用是否真的把 TCP 交给本地透明入口。

注意：本地入口不是 SOCKS5。

## 3. 认证失败

最常见原因是 Transport Key 不一致，或者 Relay 指向了错误的 Landing/端口。

远端客户 UI 会把原始 endpoint 错误收敛成“认证失败 / 连接超时 / 连接失败”等，不直接展示 Relay IP/端口。需要服务端排障时再看受控的 Landing/系统日志。

## 4. Parallel Bundle 一份配置失败

0.10.7 的正常行为是：

- 本地端口预检查失败：整组不启动；
- 远端连接、认证或运行失败：只标记对应 Profile；
- 还有健康 Profile 时：Bundle 继续运行；
- 全部 Profile 都暂时不可用：Bundle supervisor 仍保持运行并持续自动重连；
- 只有用户主动停止、App/engine 被终止或不可恢复的本地配置校验失败才结束本轮运行。

因此“某个 Profile 重连中、另一个仍运行”是正常的故障隔离/自愈行为，不是 Bundle 自身故障。

## 4.1 Parallel Profile 自动重连

0.10.7 中，parallel Bundle 的单个 Profile 断线会显示“重连中”，并按 1s → 2s → 5s → 10s → 30s → 每 30s 自动重试。其他健康 Profile 继续工作。

如果所有 Profile 都暂时不可用，主状态会显示“全部配置重连中”，run-bundle 不会退出。恢复后对应 Profile 会自动重新进入“已启动”，无需手动停止/启动。

如果一直无法恢复，再检查 Relay、Landing、Transport Key、出口网络和本地端口；永久故障不会 busy-loop，而是停留在 30 秒重试档。

## 5. 端口冲突

parallel Bundle 要求所有已选择 Profile 的 listen_port 唯一，而且启动时必须都可绑定。

如果出现端口冲突，先检查：

~~~sh
lsof -nP -iTCP:<port> -sTCP:LISTEN
~~~

或 Linux：

~~~sh
ss -lntp
~~~

single_select 可以让不同 Profile 复用同一个端口，因为一次只运行一份。

## 6. 浏览器打开 Provisioning URL 只看到 v/n/d

这是 0.10.2+ 的正常行为。公网 Profile/Bundle 响应外层是 AES-256-GCM envelope，因此不会直接显示 Relay、端口和 Transport Key。

完整 URL 本身仍然是 bearer credential；不要把它贴到公开日志、Issue 或截图中。

## 7. Weighted 某条路径流量很少

不一定是故障。Weighted 不是固定百分比分流。

实时 RTT、queue、outstanding、delivery、penalty、disconnect 与超时都会改变路径成本。路径也可能处于 LEARNING / PROBE / BACKUP。

应一起看：

- connected；
- role / role_reason；
- RTT；
- measured goodput；
- queue / outstanding；
- retransmits；
- last error。

## 8. RTT 满载显著升高

这通常说明路径存在排队或 bufferbloat。调度器会把 RTT/queue 纳入选择，但无法消除运营商、Relay 或出口设备自身的排队。

Weighted 容量高估也会放大这一现象。

## 9. 路径诊断看不到 Relay IP

远端配置下这是有意设计。客户 UI 只显示“路径 1 / 路径 2 …”及运行指标，不展示 Relay endpoint。

本地配置仍可用于更直接的工程排障。

## 10. 后台常驻没有自动恢复

确认：

- MPTCP Desk 的“后台常驻”已开启；
- macOS 登录项未处于 requires approval；
- 用户不是刚刚手动点击了“停止”；
- 网络已经恢复；
- 至少曾成功同步过一次远端配置，或者当前 API 可用以完成首次同步。

0.10.7 有匹配 LKG 缓存时，睡眠唤醒恢复不会等待 Provisioning API；它直接用缓存重建 Session/Carrier，同时在后台刷新 API。若 API 暂时不可用，现有缓存仍然有效。

## 11. 需要哪些日志

客户界面日志只保留同步、连接、认证、启动、错误、恢复与停止等必要事件。

协议/服务端排障使用：

~~~sh
journalctl -u mptcp-userspace-landing.service -n 100 --no-pager
journalctl -u mpx-provision.service -n 100 --no-pager
~~~

不要把 Transport Key、完整 Provisioning URL 或其他 bearer secret 放进公开日志。
