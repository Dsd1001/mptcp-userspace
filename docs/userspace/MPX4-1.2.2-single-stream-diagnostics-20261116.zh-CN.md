# MPX/4 v1.2.2 单 Stream 慢起速：遥测和受控优化试验（2026-11-16）

## 目标与基线

基于 2026-11-16 的 HKBN egress 抓包，用户做了两轮多线程、两轮单线程 Speedtest，确实存在显著单流慢起速。HKBN 看到的四轮 Carrier 发送阶段分别约 448/478/249/349 Mbps（含冷启动的时段均值），六条 Carrier 都在线；iCable → HKBN 的独立 TCP 单流 iperf3 测过 560 Mbps。

抓包中单线程连接的 HKBN 后端 TCP 广告接收窗口开始约 48 KiB，缓慢扩大到约 1.5 MiB；晚期有少量 zero-window。这能说明后端读取、MPX 写入背压和 Linux TCP 窗口增长可能形成耦合，但不能单独证明 1.2.2 的根因。

## 本分支代码改动

基线 tag：`v1.2.2`（commit `2a96cb3f98f135b7229935f36154e724c813d698`）。

1. `session.go`、`stream.go`、`stream_flow_telemetry.go`：
   - `Stats.active_send_streams` 最多记录 8 条近期有发送活动的 Stream，不记录业务载荷或目标 IP。
   - 记录 Stream ID、route ID、已提交/已消费字节、Stream/Session 剩余发送窗口、未消费量、已就绪 DATA 帧数、路由队列可用额度、当前写入等待原因与持续时间。
   - 为单 Stream 累积 `stream_window`、`session_window`、`queue_admission` 等等待次数与纳秒数。旧有 Session 级 `resources.write_wait_reasons` 保持原语义。
   - 不更改 MPX/4 线协议、16 MiB Stream 窗口或 128 MiB Session Credit 上限。
2. `queue_admission.go`：修复共享路由下唤醒队列仅考虑全局队列额度、忽略本 Route 配额，导致写入者被无效唤醒的问题；只在两个额度都满足时唤醒。新增回归测试。
3. `routed_service.go`、`routed_backend_rcvbuf.go`：**可选**的 HKBN 后端 TCP 接收缓冲区实验，默认关闭，不改变生产行为。
   - 环境变量 `MPX_ROUTED_BACKEND_RCVBUF_KIB`，`0` / 未设置代表关闭，可设整数 128–4096；例如 `512` 仅作用于新建立的 Routed TCP 后端连接。
   - 设置失败不导致路由断开；UoT 不使用该选项。
   - Linux 显式设置 SO_RCVBUF 可能关闭接收缓冲区自动增长，并增加高并发内存开销。**不要未经 A/B 直接设成全局默认值**。
4. `single_flow_telemetry_test.go`：遥测上界、按原因计数、Route 无效唤醒与缓冲区开关测试。
5. `single_flow_downlink_perf_test.go`：默认跳过的 6 Carrier（564 Mbps 容量、50 ms RTT）Routed 后端主动下行单流/8 流基准测试。

## 已完成的本机测试

运行环境：本项目的隔离 v1.2.2 源码副本，与分支修改文件 SHA 一致；不是生产公网。

- `go test ./... -count=1 -timeout=150s`：通过（engine、landing、multipath、tunnelconfig）。
- `go vet ./...`：通过。
- 对新测试执行 `go test -race`：通过。
- 交叉编译 Linux amd64 egress 候选二进制：通过；文件保存在项目 `.scratch/perf-20261116-ab/artifacts/mptcp-landing-linux-amd64-singleflow`，SHA-256 `cb773dc4aa7e2f52049860660563393e964528eeed607d464b482fe6ddfdae7e`；只是诊断候选，**未部署**。
- 新增的 4 项针对性测试：通过；其中实际触发 Stream/Session 写入超时，验收逐 Stream 等待计数及累计时间与 Session 级计数一致。
- 六路 564 Mbps/50ms 模拟，Auto/Weighted 的 Raw/Routed 单流与多流对照：全部通过。
- 对应后端主动发送的 Routed 下行（Auto，单轮，仅作相对观测）：
  | 接收缓冲选项 | 单流 64 MiB | 8 流合计 64 MiB |
  |---|---:|---:|
  | 默认（不设定） | 463.36 Mbps | 443.29 Mbps |
  | 512 KiB | 455.78 Mbps | 434.90 Mbps |
- Stream WINDOW 消费反馈阈值 A/B（只有隔离副本临时变动，并已恢复 128 KiB）：
  | WINDOW 反馈批量 | 单流 | 8 流 |
  |---|---:|---:|
  | 128 KiB（基线） | 463.36 Mbps | 443.29 Mbps |
  | 64 KiB | 462.10 Mbps | 445.90 Mbps |
  | 256 KiB | 458.98 Mbps | 442.30 Mbps |

- 单条 Routed Stream 下暂时跳过 Auto Cost Deferral 的调度实验：基线 3 轮单流平均 458.5 Mbps、8 流平均 442.0 Mbps；修改后 3 轮单流仍是 458.5 Mbps、8 流约 446.4 Mbps。单流没有净提升，Cost Deferral 次数从数千降为 0，而 Flight Budget 阻塞次数上升。**该实验修改已恢复，未收入候选分支**。

单次小差异受本机抖动影响；目前没有发现“调大后端接收缓冲区”“改变 WINDOW 反馈批量”或“跳过单流 Cost Deferral”能够稳定改善单流吞吐的证据，所以没有将它们设为新的默认策略。

**注意**：单流测试中服务端 `stream_window` 写等待较多，但多数发生在真正发送者已用满对端额度后，是正常反压机制；累积等待时间不能直接当作性能损失证明。

## 后续公网压测时如何使用

1. 用候选版本搭配原有配置在测试节点运行，不要直接覆盖正在服务的 egress。
2. egress 原本每 2 秒生成一次状态快照。新版 `tcp[].active_send_streams` 与 `tcp[].resources.write_wait_reasons`、`tcp[].resources.path_selection`、`tcp[].path_stats` 一起看，每轮单流开始前后差分，不需要业务明文抓包。
3. 若 `stream_send_room_bytes` 持续接近 0，关注 WINDOW 反馈与 TCP 后端数据供给；若 `queue_admission_room_bytes` 接近 0，关注跨 Route 排队；若各 Carrier `outstanding_bytes` 接近 `budget_bytes`，关注调度飞行预算；若可发送窗口都充足而后端入站仍缓慢，重点检查后端 TCP 发送端和接收窗口增长。
4. 用相同 Speedtest 节点、相同单流/多流模式、相同六条 Carrier 的端到端 A/B 做对照。只有先验证默认版本，再试 `MPX_ROUTED_BACKEND_RCVBUF_KIB=512`，且留意并发内存、P95 延迟与零窗口比例。
5. 退出候选服务并删除该环境变量即可回到原始套接字设置；该选项不改变 wire format。

本次不更新签名/版本号，不发布 Release，不修改生产 systemd 配置、不重启生产客户端或 HKBN egress；项目原始工作目录中的其他未提交变更保持不动。
