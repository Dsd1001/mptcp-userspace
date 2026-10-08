# MPTCP Userspace v1.1.1 故障排查

排障原则：先确认版本和网络基线，再从 Client → Relay → Landing → Backend 分层定位。

## 1. 先确认版本

Landing：

```sh
/usr/local/bin/mptcp-landing version
/usr/local/bin/mptcp-landing status
```

Linux Client：

```sh
./mptcp-client-linux-amd64 version
./mptcp-client-linux-amd64 doctor-userspace
```

正式环境尽量让 Client、Landing、Provisioning 使用同一版本。当前文档面向 v1.1.1 / MPX/4 Protocol Version 4 Stable。

## 2. **先确认 Landing=CUBIC / Relay=BBR**

很多“MPX 调度不稳定”实际上先受底层 TCP 排队影响。

Landing：

```sh
sysctl net.ipv4.tcp_congestion_control
```

推荐结果：

```text
net.ipv4.tcp_congestion_control = cubic
```

Relay：

```sh
sysctl net.ipv4.tcp_available_congestion_control
sysctl net.ipv4.tcp_congestion_control
sysctl net.core.default_qdisc
```

推荐：`bbr` + `fq`。

如果实际环境不是这个基线，先记录现状再决定是否修改，不要一边排障一边同时改十几个 sysctl。

## 3. 完全没有流量

按顺序确认：

1. 本地 `listen_port` 是否监听；
2. 上层应用是否真的连到 `127.0.0.1:<listen_port>`；
3. Client 是否能访问至少一个 Relay；
4. Relay 是否转发到正确 Landing TCP 端口；
5. Client/Landing Transport Key 是否一致；
6. Landing 是否能访问 backend；
7. Landing `max_sessions` 是否已耗尽。

注意本地入口不是 SOCKS5，除非上层产品明确做了额外协议适配。

## 4. 认证失败

最常见：

- Transport Key 不一致；
- Relay 指错 Landing/端口；
- 旧版本/旧协议 listener 与当前 Stable listener 混用；
- 把 backend 端口误当 Landing listener。

远端客户 UI 会隐藏 Relay endpoint 细节。服务端排障再看受控日志，不要把 secret URL 或 Key 放进公开 Issue。

## 5. Carrier 只有一条能连

逐条检查：

```sh
nc -vz <relay-host> <relay-port>
```

并在对应 Relay 上确认：

- 转发进程 active；
- 到 Landing 可达；
- 没有端口冲突；
- CPU / fd / conntrack 没耗尽；
- congestion control 是推荐 BBR；
- qdisc 与网卡队列没有异常积压。

MPX/4 允许只剩一条 Carrier 继续工作，因此“业务还能跑”不等于所有路径都健康。

## 6. 多线程/多 Stream 出现平台期

v1.1.1 已取消旧的本地 `txUsed/txGrowth` 二次 send-admission。排查时重点看：

- `stream_window_or_open` wait；
- `session_window` wait；
- `pending_frames`；
- `pending_bytes`；
- Carrier flight/budget；
- Session mutex / dispatcher telemetry；
- Landing/Relay CPU；
- 每条 Carrier RTT、goodput、queue、outstanding。

Legacy `bootstrap/growth/writer_turn` wait 在正常 v1.1.1 生产发送路径里应保持为 0 或不再成为限制来源。

如果 MPX WINDOW 没有卡住而吞吐仍平台，优先继续查底层 TCP、Relay CPU、Landing CPU 与 backend。

## 7. 单线程很好，多线程差

重点区分：

- 单 Stream 的 Carrier 能力是否正常；
- 多 Stream 时 Session/dispatcher 是否出现锁竞争；
- 多 Stream 是否共同打满某一 Relay/Landing CPU core；
- backend 是否对并发连接有限制；
- 所有 Carrier 是否真的并行承担 DATA；
- Relay 是否统一按 BBR 基线运行；
- Landing 是否仍为 CUBIC。

不要因为多线程差就直接扩大所有 WINDOW。v1.1.1 的 Session WINDOW hard limit 仍是 128 MiB，且这是协议流控资源边界，不等同于旧的本地二次发送 admission。

## 8. 满载 RTT 明显上升

先看哪里排队：

- Relay `ss -ti`；
- Landing `ss -ti`；
- MPTCP path queue / outstanding；
- 运营商链路；
- 本地出口设备。

推荐基线下 Relay 使用 BBR 的一个目的就是降低 Carrier 边缘持续队列。Landing 默认 CUBIC；如果 Landing 被改成 BBR 或其它算法，先恢复 CUBIC 做 A/B 基线。

Weighted 容量明显高估也可能造成更激进的路径使用，从而放大 loaded RTT。

## 9. Weighted 某条路径流量很少

这不一定是故障。Weighted 不是固定百分比分流。

一起看：

- connected；
- role / role_reason；
- RTT；
- measured goodput；
- queue / outstanding；
- retransmit；
- penalty；
- 用户填写的 download/upload capacity。

容量只提供 prior，实时坏路径可以被降权。

## 10. Parallel Bundle 某份配置断线

正常行为：

- 本地端口预检查错误：整组不启动；
- 远端连接/认证/运行错误：只影响对应 Profile；
- 其它健康 Profile 继续运行；
- 失败 Profile 按 1s → 2s → 5s → 10s → 30s → 每 30s 重试；
- 全部暂时 down 时 supervisor 仍保持运行；
- 恢复后自动回到 listening，不需要手动 Stop/Start。

## 11. 本地端口冲突

macOS：

```sh
lsof -nP -iTCP:<port> -sTCP:LISTEN
```

Linux：

```sh
ss -lntp
```

Parallel Bundle 要求已选择 Profile 的本地端口可以同时绑定。

## 12. Provisioning URL 打开只看到 v/n/d

这是正常的 opaque envelope。完整 URL 本身仍然是 bearer credential。

不要把完整 URL 放在：

- GitHub Issue；
- 公开截图；
- nginx access log；
- shell history；
- 聊天群。

## 13. Managed 配置无法刷新，但旧配置还能跑

这是 LKG 设计的一部分。首次成功同步后，匹配来源的缓存可以用于 App/系统重启与睡眠恢复，API 在后台刷新。

排查：

- HTTPS/证书；
- Provisioning 服务；
- reverse proxy timeout；
- URL 是否更换；
- Bundle/Profile 是否仍存在；
- 设备 long poll 是否被代理提前断开。

## 14. UoT 延迟高

UoT 复用 TCP Carrier，因此要同时看：

- Carrier RTT；
- TCP queue；
- Relay BBR 是否生效；
- Landing CUBIC 是否生效；
- Stream service setup 是否反复重建；
- backend UDP 本身的 RTT。

Native UDP 与 UoT 是不同数据面，不应期待两者延迟完全相同。

## 15. Landing 服务异常

```sh
sudo /usr/local/bin/mptcp-landing status
sudo /usr/local/bin/mptcp-landing doctor
sudo /usr/local/bin/mptcp-landing logs
```

或：

```sh
systemctl status mptcp-userspace-landing.service
journalctl -u mptcp-userspace-landing.service -n 200 --no-pager
```

检查私有配置权限是否仍为 0600/0400。

## 16. CPU 很高

分别看 Client、Relay、Landing、backend，不要只看一个进程。

Linux：

```sh
top
pidstat -p <pid> 1
ss -s
```

如果 Relay CPU 打满，底层 TCP 转发本身就会成为瓶颈；如果 Landing 打满，再看 Stream 数、Session 数、scheduler/dispatcher telemetry 与 backend。

## 17. 需要收集什么证据

建议最小证据集：

```text
Client/Landing version + Source-ID
Landing congestion control
Relay congestion control + qdisc
Carrier connected/RTT/goodput/queue/outstanding/retransmit
Stream/Session WINDOW waits
pending frames/bytes
Relay/Landing CPU
Landing logs
测试时间段与单线程/多线程条件
```

不要收集/公开：Transport Key、完整 Provisioning URL、Authorization header。

完整网络调优见 [NETWORK-TUNING.zh-CN.md](NETWORK-TUNING.zh-CN.md)。
