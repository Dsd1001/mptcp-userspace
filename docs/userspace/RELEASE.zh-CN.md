# MPTCP Userspace 0.9.6 / MPX/4 Draft 03

## 核心变化

0.9.6 将 TCP Userspace 线协议从 MPX/3 Rev5 升级为 **MPX/4 Draft 03**。这次不是改协议名称，而是实际替换了 handshake、key schedule、Secure Record 和 Frame 编码层，同时继续复用 0.9.5 已经稳定的 Session / Stream / scheduler / credit / retransmission / reinjection 内核。

主要新增：

- MPX/4 `MPX\\0 + Version 4` connection preface；
- canonical 1/2/4/8-byte VarInt；
- CLIENT_INIT / SERVER_INIT / CLIENT_FINISHED / SERVER_FINISHED；
- 参数 TLV、严格排序、重复/非 canonical 拒绝；
- HKDF-SHA256 MPX-Expand-Label key schedule；
- HMAC-SHA256 Finished；
- AES-256-GCM Secure Record，sequence 从 0 开始，nonce 使用 traffic IV XOR seq96；
- 一个 Secure Record 可承载多个完整 Frame；
- Carrier Generation 与 replacement fresh keys；
- MPX/4 标准 Scheduler ID 与 PATH_CAPACITY；
- TRANSMISSION_ACK Receiver Timestamp 改为规范要求的微秒单位；
- CREATE/JOIN 在 Finished 认证完成前不会把 Carrier 挂到 live Session。

## 调度与资源模型

Auto / Aggregate / Protect / Weighted 的实际路径选择逻辑继续沿用并通过回归测试。Weighted 仍同时考虑配置容量与实时 RTT、delivery、queue、penalty、timeout 等状态。

既有资源边界保持不变：最大 8 Carrier、2048 Stream、32 KiB DATA、16 MiB 单 Stream 窗口、128 MiB Session credit、128 MiB physical receive accounting，以及有界 sender pending/control queue。

## 重连

同一个逻辑 Carrier 的第一次连接使用 Generation 0；后续 replacement 使用更大的 Generation。低 Generation 或冲突的相同 Generation 不会替换 live Carrier。每次 replacement 都重新执行 MPX/4 JOIN，使用 fresh nonce、fresh traffic keys 和新的 Record sequence space。

临时 Relay 断开在 JOIN 尚未完成时不会因为一个早期 EOF 就杀死仍由其他 Carrier 支撑的 Session。

## UDP

UDP 没有被硬塞进 MPX/4 Core。0.9.6 继续保留现有 MPU/1 独立数据报平面：独立认证/加密、path health、receipt、fragment/reassembly 与 per-datagram scheduling。Weighted PATH_CAPACITY 仍只用于 MPX/4 TCP Stream 调度。

## 兼容性

**0.9.6 TCP Userspace 与 MPX/3 不兼容。Mac 与 Landing 必须同时升级到 0.9.6。**

已有 Relay 地址、端口、scheduler 和 transport key profile 可以继续使用。

## 验证

0.9.6 在发布前执行：

- `go test ./...` 全量 engine / Landing / multipath 回归；
- MPX/4 Draft 03 官方 VarInt 向量；
- 官方 Frame encoding 向量；
- 官方完整 key schedule / Finished 向量；
- 官方连续 Secure Record / nonce / AAD 向量；
- TCP 多 Carrier、path failure + rejoin；
- scheduler negotiation/conflict；
- Stream multiplexing、credit、重传/reinjection；
- wrong-key 与 missing-session 负向测试；
- macOS arm64/x86_64 Universal 构建；
- Linux amd64 static Landing 构建。

0.9.5 的后台常驻、登录项和 sleep/wake 恢复能力保留。macOS 包仍是 ad-hoc 签名，未做 Developer ID notarization。
