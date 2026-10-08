# 快速开始 — MPTCP Userspace v1.1.1

本文用最短路径搭起一套 Client → Relay → Landing → Backend 的 v1.1.1 环境。

## 1. 先明确拓扑

推荐生产结构：

```text
MPTCP Desk / Linux Client
       | TCP Carrier 1 -> Relay A --+
       | TCP Carrier 2 -> Relay B --+--> Landing --> Backend
       | TCP Carrier 3 -> Relay C --+
```

Relay 只是字节转发节点；真正的 MPX/4 端点只有 Client 和 Landing。

## 2. 下载并校验 v1.1.1

常用 Release 文件：

```text
MPTCP-Desk-1.1.1-universal.dmg
mptcp-client-linux-amd64
mptcp-client-linux-arm64
mptcp-landing
mptcp-landing-linux-arm64
MPTCP-Userspace-1.1.1-SHA256SUMS
```

生产环境替换二进制前先核对 SHA256，不要只看文件名。

## 3. 安装 Landing

`mptcp-landing` 无参数运行会进入中文交互菜单，也可以非交互安装。

Landing 配置是 schema 1，例如：

```json
{
  "schema_version": 1,
  "listen_tcp": "0.0.0.0:24001",
  "listen_udp": "0.0.0.0:24001",
  "backend_tcp": "127.0.0.1:8388",
  "backend_udp": "127.0.0.1:8388",
  "udp_enabled": true,
  "uot_enabled": false,
  "transport_key": "替换为64位十六进制TransportKey",
  "max_sessions": 4,
  "scheduler_mode": "auto"
}
```

`max_sessions` 允许 1–16。配置文件包含 Transport Key，必须是普通 `0600`/`0400` 文件，或者由受管理的 systemd credential 路径提供。

安装并启动：

```sh
sudo ./mptcp-landing install --config ./landing.json --yes --start
sudo /usr/local/bin/mptcp-landing status
sudo /usr/local/bin/mptcp-landing doctor
```

Landing 的 MPX listener 和 backend 不要用同一个会形成回环的 endpoint。

## 4. **Landing 设置为 CUBIC**

这是当前项目的推荐生产基线：

```sh
sudo sysctl -w net.ipv4.tcp_congestion_control=cubic
sysctl net.ipv4.tcp_congestion_control
```

持久化示例：

```text
# /etc/sysctl.d/90-mptcp-userspace-landing.conf
net.ipv4.tcp_congestion_control = cubic
```

不要在这个阶段顺手修改一堆其它 TCP 参数。先把 Landing=CUBIC 固定下来。

## 5. 配置 Relay，并**启用 BBR**

每台 Relay 把对外 Carrier TCP 端口透明转发到 Landing 的 `listen_tcp`。

Relay 推荐 **BBR + `fq`**：

```sh
sudo modprobe tcp_bbr
sudo sysctl -w net.core.default_qdisc=fq
sudo sysctl -w net.ipv4.tcp_congestion_control=bbr
sysctl net.ipv4.tcp_available_congestion_control
sysctl net.ipv4.tcp_congestion_control
sysctl net.core.default_qdisc
```

如果 `tcp_available_congestion_control` 里没有 `bbr`，先解决内核支持问题，不要假装配置已经生效。

**Transport Key 不需要放到 Relay 上。**

## 6. 创建 Client Profile

本地 Userspace Profile 使用 schema 3：

```json
{
  "schema_version": 3,
  "mode": "userspace_multipath",
  "listen_port": 1081,
  "tcp_enabled": true,
  "udp_enabled": true,
  "transport_key": "与Landing完全相同的64位十六进制密钥",
  "relays": [
    {
      "host": "192.0.2.10",
      "port": 24001,
      "download_mbps": 50.0,
      "upload_mbps": 20.0
    },
    {
      "host": "198.51.100.20",
      "port": 24001,
      "download_mbps": 50.0
    }
  ],
  "scheduler_mode": "weighted"
}
```

当前实现同时 active Carrier 上限为 8。Weighted 中的上下行容量是调度先验，不是固定分流比例。

### macOS

安装 `MPTCP-Desk-1.1.1-universal.dmg`，添加本地 Profile 或 Provisioning URL，然后启动。

### Linux

先验证：

```sh
./mptcp-client-linux-amd64 version
./mptcp-client-linux-amd64 doctor-userspace
./mptcp-client-linux-amd64 validate < profile.json
```

再运行：

```sh
./mptcp-client-linux-amd64 run < profile.json
```

本地应用入口是 `127.0.0.1:<listen_port>`。

## 7. 测速前先做连通性验收

确认：

1. Landing 服务 active，监听端口正确；
2. **Landing 当前 congestion control 是 CUBIC**；
3. Relay 能访问 Landing；
4. **每台 Relay 当前 congestion control 是 BBR，qdisc 优先为 `fq`**；
5. Client 能访问每条 Relay；
6. Client/Landing Transport Key 一致；
7. 诊断页面能看到多条 Carrier connected；
8. Landing 能访问 backend。

Linux 主机可辅助看：

```sh
ss -s
ss -ti
```

Landing 日志：

```sh
sudo /usr/local/bin/mptcp-landing logs
```

## 8. Scheduler 怎么选

默认先用 **Auto**。

- Aggregate：明确希望多条健康路径积极并发；
- Protect：更重视把异常路径隔离；
- Weighted：知道每条路径大致上下行能力，希望把容量先验加入实时调度。

协议不要求 Client/Landing Scheduler 完全一致，但生产环境通常建议一致，便于理解上下行调度。

## 9. UDP 怎么选

- Native UDP：走独立 MPU/1 数据面；
- UoT：把 UDP payload 放进认证后的 TCP Carrier Session。

Landing=CUBIC / Relay=BBR 不会直接改变 Native UDP 的拥塞控制，因为它不是 TCP。

## 10. 下一步

- [网络与拥塞控制调优](NETWORK-TUNING.zh-CN.md)
- [系统架构](ARCHITECTURE.zh-CN.md)
- [部署、升级与回滚](../userspace/DEPLOYMENT.zh-CN.md)
- [故障排查](TROUBLESHOOTING.zh-CN.md)
- [Scheduler 模式](../userspace/SCHEDULER-MODES.md)
