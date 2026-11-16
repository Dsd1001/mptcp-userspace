# MPTCP Tunnel v1.2.3 发布说明

## 更新重点

此版基于 v1.2.2 的 MPX/4 v4 共享隧道架构，重点修复共享 Session 多连接场景的调度和首包起速行为。按用户明确决定，**完全删除 Herd Guard**，而不是仅改变触发阈值：不再根据 Session 已建立连接数或近期活跃 Stream 数进入额外的保守 Flight/首 DATA 禁止分支。

- Weighted DATA 发送改为始终采用已有的自适应 Flight Budget，同时维持 Carrier 预算、Socket Writer 拥堵判断及 Session **48 MiB** 总在途上限。
- Routed 首 DATA 重新获得**有界优先**发送机会，但必须先经过 Route DRR 字节配额，不能跨 Route 抢占；额外 Carrier 快速槽位仍最多两帧，并受 Token/ACK 回补规则约束。
- 优化 Route 就绪队列与 Queue Admission 的定向唤醒，保留 Session/Stream WINDOW、路由公平与资源限制。
- 增加每条活跃 Stream 的非正文遥测，支持更精确地定位单流起速及流控等待；增加默认关闭的 Routed 后端 TCP 接收缓冲区调优入口与对照测试。
- 更新 macOS、Windows、Linux、共享出口与远程控制器的版本标记为 **1.2.3**，协议仍为 MPX/4 v4。不要求轮换 Transport Key。

## 验证与已知限制

- macOS arm64 Go 引擎已执行完整 `go test ./...`、`go vet ./...`、部分关键竞态测试；Linux amd64 出口交叉编译通过。
- 本地六条 Carrier、每条成形 94 Mbps、50ms RTT 的模拟下，112 条空闲 Routed 连接时，单流下载中位数约 **531.6 Mbps**，八流约 **463.0 Mbps**。这是实验室模拟结果，**不代表已恢复用户公网测速中的单流 550+ Mbps**。
- 150 条预建 Routed TCP 同步发流共 600 MiB 的三轮模拟吞吐约为 **203.1–353.5 Mbps**，数据完整，但波动明显。移除 Herd Guard 的高并发性能权衡未获得公网/P95/P99 验收，升级前建议在真实出口进行 A/B 灰度并保留旧版。
- GitHub Actions 的 Linux/Windows 原生 CI 应在创建正式发布之前通过。macOS GUI 的正式签名/公证与所有平台的 Release 安装包需要独立产物核验，不能用源码测试代替。

## 部署注意事项

1. 备份当前 HKBN 共享出口、桌面客户端可执行文件和配置；**发布操作本身不更改生产机器**。
2. 分阶段升级 HKBN 出口与客户端，确认六路 Carrier 与三个 Routed 入口；HKT/iCable 继续作为普通 TCP 后端，非独立 MPX 共享出口。
3. 在相同 Speedtest 端点分别测试冷起速、单流、八流及 150 流，观察每路发送速率、队列、Window 等待和重传。
4. 发现真实高并发长尾或吞吐退化，回滚上一版二进制。v1.2.3 不承诺一定优于 v1.1.6 的公网数据。

详细变更、实验数据及验收记录参见 [无 Herd Guard 变更报告](MPX4-remove-herd-guard-20261116.zh-CN.md)。此前关于保留 Herd Guard 的调优文档仅作为历史实验记录。
