# 故障排查

本页优先排查 Userspace / MPX/3 Rev5 常见问题。

## 1. 完全没有速度

先确认四件事：

1. Mac 与 Landing 版本是否匹配；
2. Landing 是否运行；
3. Relay 是否能连接到 Landing；
4. 上层代理是否把 TCP 交给了正确的本地透明入口。

Landing：

~~~sh
/usr/local/bin/mptcp-landing version
/usr/local/bin/mptcp-landing doctor
/usr/local/bin/mptcp-landing status
~~~

Mac 端确认 carrier 数量、连接状态和 configured/effective scheduler。

注意：127.0.0.1:1081 是透明 TCP 入口，不是 SOCKS5。把它当普通 SOCKS5 使用会得到错误结果。

## 2. Weighted 一开就连接失败

最常见原因是 Landing 不是 0.9.4。

Weighted 使用 0x44 hello 和 Rev5 方向容量字段，必须双端都是 0.9.4。

如果暂时不能升级 Landing，可先切回 Auto / Aggregate / Protect；0.9.4 在这三个模式下与 0.9.3 保持 hello 兼容。

## 3. 某一条 Relay 基本没有流量

不一定是故障。

可能原因：

- 当前业务量不足以同时利用全部路径；
- Auto/Protect 已把该路径降为 PROBE/BACKUP；
- 路径处于 penalty；
- RTT/queue 导致其预计到达成本明显更高；
- carrier 刚恢复，正在重新学习；
- Weighted 中虽然配置了容量，但实时交付保护暂时压低了它的使用率。

诊断时不要只看历史 last_error。历史错误存在不代表当前仍处于 penalty。

应同时看：

- connected；
- role / role_reason；
- penalty 状态；
- base RTT / 当前 RTT；
- writer queue；
- measured delivery；
- retransmission / reinjection；
- lifecycle 最近事件。

## 4. Weighted 比例与配置值不完全一致

这是预期行为。

Weighted 配置的是容量先验，不是硬性的 packet ratio。

例如六条路径都填 50 Mbps，也不意味着任意 1 秒窗口里每条都必须严格占 1/6 流量。

实时因素仍会改变选择：

- RTT；
- queue；
- penalty；
- timeout；
- disconnect；
- 当前 flight；
- retransmission / reinjection。

长时间、持续大流量且所有路径健康时，配置容量会对分配产生稳定影响；短流、突发流或异常路径下不应要求精确比例。

## 5. 空载 RTT 约 30 ms，满载升到 180 ms

这通常代表链路出现明显排队。

调度器会把实时 RTT 和 writer queue 纳入选择，因此某条路径排队严重时，即使 Weighted 配置容量较高，它的 ETA 成本也会上升。

但如果所有路径都被打满并同时产生 bufferbloat，调度器无法凭空消除物理链路排队。

建议同时检查：

- 配置容量是否高估；
- Relay / Landing 出口是否有限速；
- 运营商链路是否存在深队列；
- 是否只有单条路径 RTT 上升；
- retransmission 是否同步上升。

如果是 Weighted，容量配置应尽量接近**可持续净可用吞吐**，而不是运营商标称峰值。

## 6. 上行与下行行为不同

Weighted 是有方向的：

- download_mbps：Landing → Mac；
- upload_mbps：Mac → Landing。

如果 upload_mbps 留空，Mac → Landing 会继续使用 Aggregate 的在线估速。

因此“下行按固定容量、上行仍自动学习”是合法配置，不是异常。

## 7. 路径重启后恢复，但之前长期摸鱼

重点检查：

- carrier 是否曾断开；
- 是否出现 penalty / delivery timeout；
- role 是否停留在 PROBE/BACKUP；
- 恢复探测是否成功；
- retransmission / reinjection 是否持续增长；
- Relay 自身 CPU / 网络是否异常。

重启 Relay 转发后恢复，说明问题可能发生在 Relay 进程、TCP carrier 或该机网络状态，不等于 scheduler 本身已经证明有 bug。

## 8. status 里看到 last_error，但现在业务正常

last_error 是历史诊断信息。

判断当前是否仍异常，应结合：

- connected；
- 当前 role；
- 当前 penalty；
- 最近 lifecycle；
- 最新 RTT / delivery；
- 当前业务流量。

不要仅凭一条历史 last_error 判断路径仍然不可用。

## 9. Landing 升级后异常

先执行：

~~~sh
/usr/local/bin/mptcp-landing doctor
/usr/local/bin/mptcp-landing status
~~~

如果确定新版本不适合当前环境，可以回滚：

~~~sh
/usr/local/bin/mptcp-landing rollback
~~~

回滚 Landing 到 0.9.3 后，Mac 不应继续使用 Weighted。

## 10. macOS 无法正常打开 DMG 内 App

v0.9.4 DMG 是 ad-hoc 签名，未 notarize。

请使用正常 macOS 安全提示流程处理，不建议：

- 关闭 SIP；
- 全局关闭 Gatekeeper；
- 批量清除安全属性；
- 从不可信来源重新签名。

安装前先核对 SHA256。

## 11. CPU 突然升高

先区分是：

- Landing；
- Relay 转发进程；
- Mac Engine；
- 上层代理。

Landing 先看 status / logs / doctor；Relay 应单独检查其系统 CPU、连接数和异常进程。

高 CPU 与“某路径被少用”不是同一个问题，不应直接把两者归因于 scheduler。

## 12. 应该提供哪些信息用于排查

在不泄漏密钥的前提下，优先提供：

- Mac / Landing 版本；
- Source-ID；
- scheduler mode；
- Relay 数量；
- 每条路径配置容量；
- RTT / minRTT；
- role / penalty；
- connected 状态；
- retransmission / reinjection；
- doctor / status 中的非敏感部分；
- 问题发生时间与业务类型。

不要提供：

- transport key；
- backend 密码；
- SSH 私钥；
- API token；
- 未脱敏的凭据文件。

更深入的调度行为见 [SCHEDULER-MODES.md](../userspace/SCHEDULER-MODES.md)，协议见 [PROTOCOL.md](../userspace/PROTOCOL.md)。
