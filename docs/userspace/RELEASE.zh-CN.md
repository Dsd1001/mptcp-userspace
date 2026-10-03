# MPTCP Userspace 0.9.8 / MPX/4 Draft 04 + Provisioning

## 多平台发布补充

0.9.8 Release 现在同时提供以下正式二进制：

- macOS Client：Universal arm64 + x86_64 DMG；
- Linux Headless Client：amd64 + arm64；
- Linux Landing：amd64 + arm64；
- Linux Provisioning：amd64 + arm64。

Linux Client 与 MPTCP Desk 复用同一套 MPX/4 Draft 04 Go transport core。Linux 版本只开放 `userspace_multipath`，不提供 macOS 专用的 Native MPTCP fallback；配置通过 schema-3 JSON stdin 输入。

Provisioning Dockerfile 也改为使用 BuildKit `TARGETOS/TARGETARCH`，可构建 amd64 或 arm64 镜像。

## 本次版本

0.9.8 同时完成两件事：

1. 对齐 `Dsd1001/MPX-4` 最新 **Draft 04**（规范基准提交 `5854899b63676eb8bb43048678ef99b4589170c3`）；
2. 把此前开发完成的 **Provisioning 网页/API 平台**正式纳入完整发布链。

Draft 04 没有改变 Draft 03 的 Core wire registry、Frame type、Handshake Parameter、Scheduler ID 或 key schedule 字节格式，但新增了必须严格执行的 Generation replacement 与 error-scope 语义。因此 0.9.8 不是单纯改文档或版号。

## Draft 04 Carrier Generation

0.9.8 新增独立的 Session Generation high-water state：

- 未使用 Carrier ID 的第一次成功 incarnation 必须是 Generation 0；
- 每个用过的 Carrier ID 在 Session 全生命周期保存 Highest Accepted Generation；
- 更低 Generation 一律 `CARRIER_CONFLICT`；
- 相同 Generation 即使原 TCP 已断开也不能复用；
- higher Generation 在完整认证建立前不会修改当前 Session；
- 候选握手失败不会推进 Highest Accepted Generation；
- higher Generation 提交后，低 Generation incarnation 原子进入 SUPERSEDED；
- superseded Carrier 不再接收新的 Transmission Attempt，不再产生新 path sample，也不能通过晚到 Secure Record 创建新协议状态；
- outstanding Transmission 在 replacement 后仍保留原 Transmission ID；
- Generation 到达 MPX VarInt 最大值后不能回绕。

## Draft 04 Error Scope

0.9.8 按规范 Error Code registry 和 failure scope 处理错误：

- `STREAM_LIMIT` / stream-specific `RESOURCE_LIMIT` / 明确定义的 pre-open `STREAM_STATE_ERROR` → `STREAM_OPEN_REJECT`；
- authenticated malformed Frame → `FRAME_ENCODING_ERROR` + Carrier scope；
- authentication/integrity failure → 只终止对应 Carrier；
- `FLOW_CONTROL_ERROR` → Session scope；
- `FINAL_SIZE_ERROR` → Session scope；
- `TRANSMISSION_ID_ERROR` → Session scope；
- established `STREAM_STATE_ERROR` / generic `PROTOCOL_VIOLATION` → Session scope；
- Session scope 在有可写 authenticated Carrier 时发送 `SESSION_CLOSE`；
- Carrier scope 在安全可报告时发送 `CARRIER_CLOSE`；
- JOIN candidate 被拒绝不会修改原 Session。

同时实现了 Draft 04 `CARRIER_CLOSE` / `SESSION_CLOSE` body：Error Code、Trigger Frame Type、Reason Length 与最多 256 UTF-8 字节的诊断 Reason。

## Transmission ID

TRANSMISSION_ACK 现在明确区分：

- outstanding ID：正常 settle；
- 已 settled 或已压缩的旧 ID：作为 stale duplicate 忽略；
- 从未分配过的 future ID：`TRANSMISSION_ID_ERROR`；
- ACK Stream ID 与 referenced Transmission 不一致：`TRANSMISSION_ID_ERROR`。

retransmission、reinjection 与 replacement 都不会生成新的逻辑 Transmission ID。

## Provisioning 网页/API

0.9.8 Release 同时发布 Linux `mpx-provision`。管理网页可维护多份客户端 Profile，并为每份 Profile 生成独立高熵 API URL。

远程 Profile 可以完整下发：

- transport mode；
- 本地 listen port；
- TCP / UDP；
- scheduler；
- 2–8 条 Relay；
- Weighted download/upload Mbps；
- transport key；
- background resident。

Mac Managed Mode 只需要保存 API URL。启动和后台恢复前先同步权威配置；API 获取失败时不会用 stale cache 偷偷启动。API URL 与 transport key 均使用 Keychain，普通偏好设置不会保存明文 key。

Provisioning 服务包含内嵌 Web UI、systemd/二进制部署能力和 Docker 示例。`/v1/config/<token>` 应在反向代理上禁止或脱敏 access log。

## Scheduler / UDP / 资源边界

Auto / Aggregate / Protect / Weighted 的本地路径选择算法继续保留。Draft 04 明确 scheduler semantic contract，但不标准化具体 score、阈值或分配比例。

UDP 继续使用独立 MPU/1 数据报平面，没有伪装成 MPX/4 Core Datagram。Weighted `PATH_CAPACITY` 仍只作用于 MPX/4 TCP Stream 调度。

资源上限保持：8 Carrier、2048 Stream、32 KiB DATA、16 MiB per-Stream credit、128 MiB Session credit、128 MiB physical receive accounting，以及有界 sender pending/control queue。

## 兼容性

Draft 04 与 Draft 03 的字节编码兼容，但 0.9.8 修正并强制执行新的 Generation / Error Code 语义，因此正式支持组合是：

**MPTCP Desk 0.9.8 + Landing 0.9.8。**

0.9.8 与 0.9.6/0.9.7-derived build 使用相同 Version 4 基础 wire encoding，但混合版本不作为正式支持组合。0.9.5 及更早仍是 MPX/3，不兼容。

## 发布验证

0.9.8 发布前执行：

- engine / Landing `go test ./...`；
- `go vet ./...`；
- MPX/4 VarInt / Frame / key schedule / Secure Record 官方字节向量；
- Draft 04 官方 `carrier-generation.json`；
- Draft 04 官方 `error-scope.json`；
- Generation 0 / stale / equal / higher / max-no-wrap / simultaneous candidate 状态测试；
- Transmission-ID future/stale/mismatch 测试；
- CARRIER_CLOSE / SESSION_CLOSE 编解码；
- 多 Carrier、path failure/rejoin、scheduler、credit、retransmission/reinjection；
- Provisioning server `go test` / `go vet`；
- Provisioning HTTPS policy、Managed UI 与完整 Profile decode；
- macOS arm64 + x86_64 编译；
- Universal DMG；
- Linux amd64 static Landing；
- Linux amd64 static `mpx-provision`；
- 三个发布组件绑定同一个 Source-ID。

macOS 包仍为 ad-hoc 签名，未做 Developer ID notarization。
