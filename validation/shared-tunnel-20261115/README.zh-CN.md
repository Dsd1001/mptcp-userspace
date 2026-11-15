# 共享隧道核心验证记录 — 2026-11-15

## 被验证对象

- 基线：`70d6597141482b05d9737a429bb8bb26353f1044`，实验分支 `feat/shared-mpx4-tunnel`。
- Engine 源文件清单哈希：`de04a730baae2b24586dc7fdebcc72e29f409e5146a9f2e31b9a64f8cbeec670`。具体文件哈希与编码方式见 `ENGINE-SOURCE.json`。
- 环境：`go version go1.27.2 darwin/arm64`；实际程序联调平台：`macOS-27.3-arm64-arm-64bit`。
- 开发制品 Source-ID：`shared-tunnel-experimental-20261115`。这不是旧发布的 Source-ID，也不是新的正式版本。

## 结果

| 检查 | 结果 | 原始记录 |
|---|---|---|
| 未修改基线的全量 Go 测试 | 通过 | baseline-tests.log |
| 修改后的全量 Go 测试 | 通过 | full-tests.log |
| go vet ./... | 通过，无诊断输出 | vet.log |
| go test -race ./... | 通过 | race-tests.log |
| 单 Stream 的 1/2/6 路限速对照 | 通过 | aggregation.log |
| 实际编译程序的三入口联调 | 通过 | process-smoke.json |
| macOS arm64/amd64、Linux amd64/arm64、Windows amd64 客户端核心编译 | 通过，仅 macOS arm64 运行 | build-matrix.txt |
| macOS arm64、Linux amd64/arm64 出口编译 | 通过，仅 macOS arm64 运行 | build-matrix.txt |

新增 14 个 Go 测试函数，其中一个受 `MPX_ROUTED_PERF=1` 控制；另外新增实际进程测试脚本 `tests/userspace/shared_tunnel_smoke.py`。全量测试默认跳过限速实验，本记录另行显式执行了该实验。

## 单流限速实验

每个 loopback Relay 使用独立 2 MiB/s 序列化限速和 5 ms 延迟。每组先做 1 MiB 预热，再测 8 MiB 业务字节 echo。以下是一次完整对照的全部样本，不是多次均值。

| Carrier 数 | 用时 | 有效吞吐 | 相对单路 |
|---|---:|---:|---:|
| 1 | 4.136166 s | 16.225 Mbps | 1.000x |
| 2 | 2.186212 s | 30.696 Mbps | 1.892x |
| 6 | 0.864493 s | 77.628 Mbps | 4.784x |

它证明本次实验中同一个 routed Stream 可以获得多路径吞吐收益；不代表公网性能、固定聚合效率或 CPU/内存节省幅度。未测试真实汇聚节点和三个生产 Landing 之间的后段链路。

## 实际程序联调

不是只调用 Go 内部函数：分别启动实际编译的客户端与出口进程，并建立六个 loopback TCP Relay 和三个不同目标。客户端运行 `run-tunnel`，出口运行前台 `server --config ... --status-file ... --root <临时目录>`，没有安装或控制系统服务。

验证了一个 Session、六条 Carrier；六个 Relay 的 accept 次数均为 1；三个入口各自传输并验证 1,048,576 字节随机数据，目标标识与半关闭正确。客户端和出口收到终止信号后退出码均为 0。测试用随机 Key 与临时配置已随临时目录清理，报告只保留业务校验和及二进制哈希。

## 尚未通过的发布门槛

GUI/Provisioning/远端 Bundle 接入尚未实现；按入口严格接收信用保护尚未实现；没有长时间压力、故障共同出口、Linux systemd 实机或真实 WAN A/B 验收。没有进行旧直连 / 同出口三 Session / 同出口单 Session 的拓扑控制对照。

本次没有推送远端代码、发布安装包、升级客户端、连接或改动生产 Relay/Landing。
