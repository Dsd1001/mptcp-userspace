# MPX/4 多目标共享隧道：实验实现

状态：开发分支原型，不是正式发布；不得直接替换生产客户端或 Landing。

基线：公开 main `70d6597141482b05d9737a429bb8bb26353f1044`（v1.1.2）。本次没有合并工作目录中 v1.1.6 / WO03 等历史实验分支，没有改写原有发布版本。

## 1. 本次实现的边界

一个客户端 Engine 进程维护一个 MPX/4 Session，通过 2–8 条普通 TCP Carrier 汇聚到同一个出口。多个 loopback TCP 入口共用该 Session；每条业务 TCP 对应一个独立 Stream。出口通过预配置 route_id 建立普通 TCP 连接，目标是真正的业务监听端口，不是另一个 MPX/4 端口。

```text
1081 / 1082 / 1083
        │ 进程内部直接接入，不增加 localhost 代理跳数
        ▼
一个 Session / 多个独立业务 Stream
        │ 六条 Carrier，均可承载任意路由的 DATA
   六台透明 TCP Relay
        │ 全部汇聚到同一出口
        ▼
MPX/4 隧道出口 + 目标白名单
        ├─ 普通 TCP → HKBN 业务端口
        ├─ 普通 TCP → HKT 业务端口
        └─ 普通 TCP → iCable 业务端口
```

不使用外层 smux，不嵌套 MPX/4，不把一个入口的所有业务塞进一个大 Stream，不将路由绑定到某条 Carrier。

本次增加的是产品服务及运行时，MPX/4 Core 握手、帧类型、Transmission ID、ACK、重注入、关闭和窗口语义不变。产品两端必须升级；原始固定 backend 模式保留原行为。

## 2. 服务格式与连接建立

在已认证的产品 Session 中，新增 `ROUTED_TCP=3`、`ROUTE_PROBE=4`。沿用 `MPTU` 产品 Carrier preface 与既有域分离密钥；业务连接没有额外 TLS、MUX 或独立 MPX Session。

`ROUTED_TCP` 请求是普通 Stream DATA：

```text
MPS1 (4) | kind=3 (1) | flags=0 (1) | route_id, uint32 big endian (4)
```

响应沿用六字节 `MPA1 | kind | status`。0=成功，2=不支持，3=目标连接失败，4=容量/拨号等待超时，5=目标不在白名单。其余响应不能当成功处理。

route_id 为非零 uint32，一次选择后不可修改。路由信息只发送一次，后续字节透明转发。服务首部不是业务内容，绝不交付给应用或后端。

客户端启动时用 ROUTE_PROBE 确认服务支持；旧服务拒绝后直接报错，不能静默降级到任意固定 backend。出口的 routed-only 模式拒绝原始固定后端 Carrier，也没有兜底目标。

沿用一个共享的 16 个预开 Stream 池。池只预开 MPX Stream，不预连目标 TCP。拿到业务连接后立即发路由首部，服务端先发数据的协议也可用。允许最多 64 KiB（含服务首部）的初始 DATA 提交；私有响应读取任务只消费六字节状态，成功后唤醒发送方。应用即使先 Write 大块数据、后 Read，也不依赖应用先读取状态才能解除首包限制。响应失败只终止当前 Stream；不自动向其他目标重放业务。

## 3. 调度和资源限制

发送采用“按路由字节加权 DRR → 路由内 Stream 轮转 → 原有 Carrier 调度”的顺序。两个端点分别实施自己的路由权重。权重默认 1，可设 1–16；它不是固定带宽预留，也不能覆盖协议窗口和 Carrier 可用性。

路由队列只保存既有 ready queue 的引用，不复制 DATA、不增加可靠性账本。只有一个路由有流量时可使用所有可用 Carrier，空闲路由不占发送份额。不同连接数的入口不会仅因连接更多而取得成倍的路由发送机会。

保留已有 32 MiB 未调度 DATA 软目标，并对有竞争的路由实施本地入队背压。既有数据不撤销，Carrier 调度释放空间后仍采用定向唤醒。`MPX_QUEUE_ADMISSION_MIB=0` 同时关闭依赖该软目标的本地路由队列限制，不能误认为关闭了协议窗口。

入口和出口均可设置 `max_connections`，0/省略表示 512。客户端另有 Session 范围的 2016 个业务连接槽，为预开/控制留出部分空间；最终仍由 Core 的 2048 个活动及未结算 Stream 上限约束。关闭态可能暂时占用 Stream 身份，2016 不构成无条件承载保证。

出口每个路由最多 8 个并发拨号任务。等待拨号也占该路由连接槽，并在 5 秒总拨号等待预算内结束；不会阻塞其他路由的拨号。路由表在 Session 生命周期内不可变。

**尚未实现严格的按路由接收信用隔离。** 本版本仍共享 128 MiB Session 接收信用及物理缓冲边界，单 Stream 延续现有最高 16 MiB 窗口。发送公平和连接限额不能证明一个完全停止消费的路由不会影响其他路由。生产发布前必须补齐考虑“已授权但未到达字节”的接收预算与慢后端压力验证；不能简单撤回 WINDOW，也不能恢复旧的第二套全局 txUsed 信用池。

## 4. 配置与命令

参考 `examples/shared-tunnel/client.example.json` 和 `egress.example.json`。这些是故意不可直接运行的模板：示例地址不是生产地址，密钥占位符必须替换，目标端口也必须按实际业务服务确认。

客户端全部入口强制绑定 `127.0.0.1`。每个入口 ID、端口、route_id 唯一。隧道配置集中保存一份 Key、Relay 列表和 Scheduler。第一版仅 TCP；UDP/UoT 不在新配置中，未知字段会拒绝。

从仓库的 `macos/engine` 目录编译开发用 CLI：

```sh
go build -o /tmp/mptcp-client-shared .
go build -o /tmp/mptcp-egress-shared ./cmd/mptcp-landing
```

核验开发构建身份；默认 Source-ID 是 `unfrozen-development-build`，不能当作官方 v1.1.2 制品校验通过：

```sh
/tmp/mptcp-client-shared version
/tmp/mptcp-client-shared validate-tunnel < /path/to/client-private.json
/tmp/mptcp-client-shared run-tunnel < /path/to/client-private.json
```

出口继续使用现有 Landing CLI 和权限检查。前台运行使用 `mptcp-egress-shared server --config /path/to/egress-private.json --status-file /path/to/status.json`；不会安装系统服务，不能让开发构建自动覆盖系统服务。含 Key 的出口配置必须为 0600/0400 普通文件；不要把实际密钥、配置 URL 或运行时私密配置提交到 Git。

不应把三台旧 Landing 的 Key 自动合并。新隧道有独立的 Client–Egress Key，三台业务服务自己的认证/加密保持不变。

## 5. 生命周期与安全

所有入口先绑定成功，之后才拨 Carrier；任一端口冲突会回滚已绑定端口。Session 不可用时新业务连接立即拒绝，不在内存里无限等待或缓存。

一条 Carrier 失败使用原有恢复/重注入。整个 Session 丢失时只在隧道级执行 1、2、5、10、30 秒退避，新 Session 承接新业务；旧 TCP 连接不隐式恢复或重放。不支持服务、认证失败不会隐藏在无限重试中。一个目标拒绝、拨号失败或达到限额只影响该连接，不重启共享 Session。

只有出口操作员能指定目标 host:port。允许目标 DNS 名称，建立新后端连接时交给系统解析器；已有 TCP 不随 DNS 改动迁移。本版本不支持热更路由表、动态增删入口或业务透明跨出口迁移。

MPX 加密在共同出口终止。后段是普通 TCP，是否加密取决于原业务协议；不能宣称后段仍受 MPX 加密保护。公网业务端口要另行检查访问控制、认证和加密。

共同出口是新瓶颈和共同故障域。六条路径须真正汇聚到同一个 Session 持有者，不能用六台独立出口只配置相同密钥冒充一个 Session。选址和出口到三台业务 Landing 的容量必须单独测量。

## 6. 诊断与验证

共享 CLI 每秒输出一份 Session 统计，包含六路径状态、路由 ready 字节与调度 payload 字节、入口连接计数。`dispatched_payload_bytes` 包括产品首部及重注入调度尝试，不是业务有效吞吐。入口 rejected 计数是本地资源/开流阶段拒绝，不涵盖所有异步业务失败。

新增测试覆盖：三路业务正确分发、一个 Session/六条物理 Carrier、单流使用六路径、服务首部透明剥离、服务端先发数据、先大块 Write 后 Read、半关闭、未知路由拒绝、慢/失败目标连接与容量隔离、Carrier 中断、旧服务拒绝、按路由字节公平、空闲份额复用、严格配置和端口绑定回滚。

```sh
go test ./...
go vet ./...
go test -race ./...
MPX_ROUTED_PERF=1 go test ./multipath -run '^TestRoutedControlledAggregation$' -v -count=1
```

限速实验对一、二、六条各自 2 MiB/s、5 ms 延迟的 loopback TCP 转发路径进行单 Stream echo 测量。它验证当前实现是否仍可跨路径叠加，不是实际 WAN 测速，也不是 CPU/内存下降比例证明。原始结果见本 Project 的 `.scratch/shared-tunnel-20261115/`。

## 7. 发布前剩余工作

- macOS/Windows GUI、Provisioning、远端 Bundle 尚未接入新 schema。旧界面继续运行原有模式，不能在截图中的旧 Bundle 上勾选一个选项就启用此功能。
- 接收信用的路由保护、停止消费的后端压力、长时间高并发和尾延迟需要专项实现/验收。
- 对照旧直连、同出口三 Session、同出口一 Session，区分拓扑变化与共享机制的效果；补 CPU、RSS、P95/P99 及原始测量。
- 暂未发布安装包、推送远端分支、签名或升级任何生产机器。Relay 新端口、共同出口和业务目标须在明确部署计划中单独配置。

这个原型用于验证传输核心和后续 GUI/控制面的接入，不是生产升级指令。
