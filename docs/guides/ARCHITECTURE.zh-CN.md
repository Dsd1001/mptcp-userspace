# MPTCP Userspace 架构说明

本文描述 **v0.10.3 / MPX/4 Draft 04** 的当前架构。历史 MPX/2、MPX/3 文档仅用于实现考古，不是当前部署说明。

## 1. 组件

MPTCP Userspace 由五类角色组成：

1. **应用 / Surge**：产生实际 TCP 业务。
2. **MPTCP Desk / Headless Client**：接收本地连接并建立 MPX/4 Session。
3. **Relay**：只做普通 TCP 转发，不理解 MPX/4。
4. **Landing**：认证并终止 MPX/4，把逻辑 Stream 转成 backend TCP。
5. **Provisioning（可选）**：管理 Profile / Bundle，不经过业务数据路径。

~~~text
App / Surge
    |
    v
127.0.0.1:<listen_port>
    |
    v
Client
    |
    +-- Carrier 1 --> Relay A --+
    +-- Carrier 2 --> Relay B --+--> Landing --> backend
    '-- Carrier N --> Relay N --+
              MPX/4 Session
~~~

## 2. Session、Carrier 与 Stream

一份 Userspace Profile 对应一个独立 MPX Session。当前 v0.10.3 每个 Profile 配置 2–8 条 Relay，每个 Session 最多 8 条 Carrier。

Carrier 是普通 TCP 连接。每条 Carrier 都通过 MPX/4 CREATE/JOIN 完成认证，并有独立的 Generation、traffic key、IV、序号空间与路径统计。

应用 TCP 连接映射为 MPX Stream。Stream 字节身份与 Carrier 无关，因此 DATA 可以在不同 Carrier 上发送、重传或 reinject，而不改变逻辑字节位置。

## 3. Profile 与 Bundle

Profile 是一份完整运行配置，拥有自己的：

- listen_port；
- Relay / Carrier 集合；
- Scheduler；
- Transport Key；
- TCP / UDP 开关；
- 后台常驻设置。

Bundle 可以包含 1–32 个 Profile：

- single_select：一次只运行一份；
- parallel：同时运行一份或多份。

parallel 模式下不同 Profile 仍然是独立 Session，不会合并 Relay。启动前会原子检查本地端口；通过后，某个 Profile 的远端失败不会终止其他健康 Profile。

## 4. Scheduler

当前支持：

- **Auto**：根据稳定的路径状态决定是否进入保护行为；
- **Aggregate**：让多条 eligible Carrier 并行承担 DATA；
- **Protect**：限制异常路径，只保留有界探测/恢复；
- **Weighted**：把用户提供的方向容量与实时 RTT、queue、delivery、penalty 等信号一起用于路径选择。

Weighted 是容量先验，不是硬性流量比例，也不是带宽保证。

## 5. Flow control 与资源

MPX/4 同时使用 Stream 与 Session Credit。当前实现主要边界：

- 2048 active peer-initiated Streams；
- 32 KiB 最大 STREAM_DATA；
- 16 MiB 单 Stream 最大 receive-credit window；
- 128 MiB Session receive-credit；
- 128 MiB physical receive-page accounting；
- sender DATA/control queue 有硬上限。

Carrier 丢失不会自动销毁 Stream；未确认可靠数据可以重新调度到其他健康 Carrier。

## 6. 路径诊断

MPTCP Desk 展示：

- configured/effective Scheduler；
- Carrier/连接数量；
- RTT、Goodput、queue、outstanding；
- reorder、pending、retransmit；
- Stream/Lifecycle 资源；
- Window/Credit 资源。

远端 Bundle 按 Profile 独立显示完整诊断。客户 UI 不展示 Relay IP/端口或原始 endpoint 错误。

## 7. UDP

UDP 使用独立的认证 MPU/1 数据面，拥有自己的路径健康、receipt、分片/重组与调度逻辑。它不是 MPX/4 Core Datagram 扩展。

## 8. 安全边界

MPX/4 Draft 04 使用预共享 Transport Key、HKDF-SHA256/HMAC-SHA256 与 AES-256-GCM。Relay 不需要持有协议密钥。

Provisioning URL 也是 bearer credential。0.10.2+ 的公网配置响应外层使用不透明 AES-256-GCM envelope，但持有完整 URL 的人仍拥有解密材料，因此远程 Provisioning 必须使用 HTTPS。

更精确的 wire 行为见 [PROTOCOL.md](../userspace/PROTOCOL.md)，调度细节见 [SCHEDULER-MODES.md](../userspace/SCHEDULER-MODES.md)。
