# MPTCP Userspace 0.9.4 / MPX/3 Rev5 Weighted 正式功能版

## 新增 Weighted

0.9.4 新增第四种 Userspace TCP 调度策略 `weighted`。它用于用户已经知道每条 Relay 实际带宽能力的固定线路组，避免在线 goodput 学习误差长期把业务偏到少数路径。

每条 Relay 在客户端可配置：

- `download_mbps`：Weighted 模式必填，表示该 Relay 从 Landing 向 Mac 的可用下行能力；
- `upload_mbps`：选填，表示 Mac 向 Landing 的可用上行能力。留空表示该方向继续使用原 Aggregate 在线估速；显式填写 0 无效；
- 合法范围 0.1–6553.5 Mbps，最多 1 位小数。

配置由 Mac 放进每条 carrier 的认证握手。Landing 的发送方向使用 `download_mbps`，Mac 的发送方向使用 `upload_mbps`；因此不需要在服务器再维护一份手工权重配置，也不会发生两端权重文件漂移。

Weighted 不是“无脑平均分流”。配置带宽替代的是正常 DATA 选择中的容量先验与 flight 预算；实时 RTT、writer queue、连接状态、penalty、delivery timeout、reinject 和重传仍然生效。某条路径真正出问题时会被临时避开，恢复后再按配置能力重新参与。

## 协议兼容

0.9.4 的 capability revision 为 5。认证 hello 的调度字节新增：

- `0x41` Auto
- `0x42` Aggregate
- `0x43` Protect
- `0x44` Weighted

Auto / Aggregate / Protect 继续保持 0.9.3 的 hello 字节和 40..43 保留字节为零，因此 0.9.4 使用这三种模式时可以与 0.9.3 对接。Weighted 的 `0x44` 以及方向容量字段只有 0.9.4 理解，所以 **Weighted 必须 Mac 与 Landing 都升级到 0.9.4**。

Weighted hello 中字节 40..41 为下行容量、42..43 为上行容量，均采用 big-endian uint16、0.1 Mbps 单位；上行值 0 只表示“字段省略/自动估速”。这些字节属于原 HMAC transcript，不能被中间人静默修改。

帧格式、AES-GCM、32 KiB DATA、2048 streams、128 MiB session credit、128 MiB sender DATA pending、128 MiB physical receive allocator、16 MiB 单流窗口上限均不扩大。Native 和 UDP 调度仍独立，Weighted 只作用于 Userspace TCP DATA。

## 验证边界

0.9.4 的正式交付要求当前 Source-ID 的 Go/Swift 正确性、协议认证方向、配置校验、旧三模式回归、Weighted 故障保护和实验室高 BDP 回归通过。具体结果以同包 `TESTS.json`、`SCHEDULER-MODES.json`、`PROVENANCE.json` 和 `ACCEPTANCE.md` 为准。

实验室吞吐与故障注入不是公网测速承诺，也不替代 30 秒全容量矩阵或真实 App+Surge 180 秒现场验收。未实际执行的项目必须在 `CAPACITY.json` / `RUNTIME.json` 中明确标记为未运行。

Mac 为 arm64/x86_64 Universal DMG，ad-hoc 签名，未做 Developer ID 公证。Landing 为 Linux amd64 静态二进制。本次构建不会自动替换已安装 App，也不会自动部署或重启 HKT、Surge、Soga、Relay、Native 或防火墙服务。
