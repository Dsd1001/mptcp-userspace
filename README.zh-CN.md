# MPTCP Tunnel 1.2.2

**一个共享 MPX/4 Session，多业务入口，最多八条普通 TCP Carrier。** macOS、Windows、Linux 客户端共用 Go 传输核心；隧道出口负责按路由白名单连接普通 TCP/UDP 业务后端。正式发行身份和验收结果以 GitHub Release 中的 Source-ID、TESTS.json 与 ACCEPTANCE.md 为准。

```text
本地入口 HKBN ─┐
本地入口 HKT  ─┼─ 一个客户端 Engine / 一个 Session ─ 六条 Relay Carrier ─ 共同隧道出口 ─ 普通 TCP/UDP ─ 业务 Landing
本地入口 iCable┘
```

每个业务 TCP 连接对应独立 MPX Stream；UDP 使用 UoT，每个本地 UDP 源地址对应有界关联。路由首部只在开流时发送一次。没有 MPX 内再套 MPX，也没有额外 smux。所有入口可使用全部健康 Carrier，保留单连接跨路径调度、带宽聚合、重组和重注入。

## 1.2.2 是不兼容升级

仅接受共享隧道，不再启动旧独立 Profile、多组 MPX Bundle、固定 backend 产品配置或 Native UDP Relay 数据面。底层 MPX/4 Stable wire 不变，但客户端与共同出口必须配套升级产品服务。不要把旧的三个 Landing 密钥自动合成一个。

- 客户端：`schema_version=1`、`kind=shared_mpx4_tunnel`。
- 共同出口：`schema_version=2`、`kind=mpx4_tunnel_egress`。
- 控制器下发：`schema_version=4`、`kind=tunnel_profile`。

GUI 保留旧偏好但不会自动运行。Windows 首次迁移单独备份旧设置；服务端旧配置会明确拒绝。生产升级前先备份二进制、配置和控制器数据，使用新端口验证；不要直接覆盖运行中旧服务。

## 开始使用

示例位于 `examples/shared-tunnel/client.example.json` 和 `egress.example.json`。示例中的密钥占位符必须替换，保留域名/IP 是不可直接使用的示意值。

1. 选择一个具有足够汇聚和后段带宽的共同出口。所有 Relay 的 TCP 转发最终到这个同一出口进程。
2. 出口配置 `routes`，每项 `id` 与客户端 `route_id` 一致。`backend` 是真实业务 TCP 端口，`udp_backend` 是真实业务 UDP 端口；不是旧 MPX 监听端口。
3. 两端使用同一新 Transport Key；Relay 不需要密钥。后段普通 TCP/UDP 不受 MPX 加密保护，业务自身的 TLS/代理加密可继续原样使用。
4. Mac/Windows GUI 配置一组 Relay 和多个入口；Linux 使用 `mptcp-client run < client.json`。本地入口仅绑定 127.0.0.1。
5. 检查实际 Session 数量、Carrier 数量、路由流量和失败原因，再做业务测试。

```sh
chmod 600 egress.json client.json
./mptcp-landing server --config egress.json --status-file egress-status.json
./mptcp-client validate < client.json
./mptcp-client run < client.json
# 只启用部分业务入口，仍只建立一个 Session：
./mptcp-client run hkbn hkt < client.json
```

服务端交互菜单、systemd 安装、状态、升级、回滚继续可用。管理命令不会自动修改防火墙、内核参数或其他代理服务。macOS/Windows 保留钥匙串/DPAPI、后台恢复、签名更新与本机主动启用的远程设备管理。

## 共享和隔离

发送按路由字节权重轮转，再按 Stream 轮转，最后由原有 Auto/Aggregate/Protect/Weighted 选择 Carrier。空闲路由不占用发送时隙；不是每入口固定分配几条 Relay。

本地排队背压不复制第二份 DATA，也不引入第二套重传账本。接收按实际未消费偏移（含空洞）与物理页面分别记账：业务路由默认上限 64 MiB，未选路组 4 MiB，并为其他路由保留有限余量。窗口已经授出的许可不会收回；极端资源不足对受影响 Stream 执行 RESET/STOP，不把正常授权的数据误判为 Session 协议错误。

整个 Session 仍共享 2048 个 Stream 上限、128 MiB 接收信用与 128 MiB 物理接收核算。预开、关闭中 Stream 与 UoT 关联也占用资源。UoT 排队另有每入口 2 MiB、Session 总计 8 MiB 限制；Session 总 UoT 客户端关联上限 1024。达到边界会背压、拒绝或丢弃，而不是无界缓存。

## 控制器

`mpx-provision` 同时提供加密配置下发与设备控制。管理界面只保留“隧道、设备、系统”。`/v1/tunnel/[alias/]token` 返回认证加密的隧道配置；旧 `/v1/config/` 和 `/v1/bundle/` 返回 410。设备只能分配一份隧道配置，控制启动/停止、同步、重启和更新，不提供任意远程 Shell。

见 [控制器说明](docs/userspace/PROVISIONING.md) 与 [1.2.2 设计和验收边界](docs/userspace/SHARED-TUNNEL-1.2.2.zh-CN.md)。

## 性能与故障边界

带宽叠加只发生在客户端与共同出口之间。共同出口 CPU、网口、共享瓶颈或出口到业务 Landing 的普通 TCP 链路可能限制总吞吐。减少十八条 Carrier 到六条，不代表有效带宽或 CPU 自动改善三倍。

单路 Relay 故障由 MPX 恢复；单业务后端失败只影响对应流/路由；共同出口或整个 Session 丢失影响所有业务入口。不会在新 Session 自动重放已建立业务连接，也不会重放离线期间旧 UDP 数据。

实验室、回环限速、原生系统测试与真实 WAN 证据分开记录。测试文件存在不等于该测试已在当前源码执行；只引用同一 Source-ID 的发行验收记录。历史 0.x/1.1.x 文档和验证记录仅用于考古，不是 1.2.2 部署说明。
