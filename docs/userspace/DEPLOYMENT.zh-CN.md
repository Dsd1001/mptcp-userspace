# MPTCP Userspace v1.1.1 部署、升级与回滚

本文面向生产环境的 **v1.1.1 / MPX/4 Protocol Version 4 Stable**。

## 1. 生产基线

推荐首先固定以下基线，再做任何性能实验：

- Client / Landing / Provisioning 使用同一正式版本；
- **Landing 使用 CUBIC**；
- **Relay 使用 BBR，优先 `fq` qdisc**；
- Transport Key 只存在于可信 Client/Landing/受保护 Provisioning 数据中，不放在 Relay；
- 非 loopback Provisioning 只使用 HTTPS；
- 每次升级都保留可回滚的旧二进制和配置。

拥塞控制是部署建议，不改变 MPX/4 wire compatibility。

## 2. 发布文件与校验

v1.1.1 常用资产：

```text
MPTCP-Desk-1.1.1-universal.dmg
mptcp-client-linux-amd64
mptcp-client-linux-arm64
mptcp-landing
mptcp-landing-linux-arm64
mpx-provision
mpx-provision-linux-arm64
MPTCP-Userspace-1.1.1-SHA256SUMS
PROVENANCE.json
TESTS.json
RUNTIME.json
CAPACITY.json
SCHEDULER-MODES.json
ACCEPTANCE.md
```

替换前确认：版本、SHA256、Source-ID 与目标架构。

## 3. Landing

Landing schema 1 字段包括：

- `listen_tcp` / `listen_udp`；
- `backend_tcp` / `backend_udp`；
- `udp_enabled`；
- `uot_enabled`；
- `transport_key`；
- `max_sessions`（1–16）；
- `scheduler_mode`。

私有配置必须为 `0600`/`0400` 普通文件，或者使用受管理的 systemd credential 路径。

### 3.1 安装

```sh
sudo ./mptcp-landing install --config ./landing.json --yes --start
sudo /usr/local/bin/mptcp-landing status
sudo /usr/local/bin/mptcp-landing doctor
```

也可以直接无参数运行进入中文交互菜单。

### 3.2 **Landing 必须优先确认 CUBIC 基线**

```sh
sudo sysctl -w net.ipv4.tcp_congestion_control=cubic
sysctl net.ipv4.tcp_congestion_control
```

持久化示例：

```text
# /etc/sysctl.d/90-mptcp-userspace-landing.conf
net.ipv4.tcp_congestion_control = cubic
```

Landing 不要求为了本项目强行修改 qdisc；没有明确证据时保留发行版默认。

### 3.3 Landing 升级

建议流程：

1. 记录当前 `version`、Source-ID、二进制 SHA256；
2. 备份旧二进制、配置与 systemd unit；
3. 校验新二进制；
4. 使用内置 `upgrade --source ... --sha256 ...`，或按受控方式原子替换；
5. restart；
6. 检查监听端口、服务状态和日志；
7. 再次确认 **CUBIC 没有被其它 sysctl 覆盖**；
8. 用同版本 Client 做真实认证与 backend 验证。

不要因为 Landing 升级去修改无关的 Relay、SS、路由、Native MPTCP 或其它隧道服务。

## 4. Relay

Relay 只需要把普通 TCP Carrier 转发到 Landing listener。它不需要 Transport Key，也不应该解析 MPX Frame。

### 4.1 **Relay 推荐 BBR + fq**

```sh
sudo modprobe tcp_bbr
sudo sysctl -w net.core.default_qdisc=fq
sudo sysctl -w net.ipv4.tcp_congestion_control=bbr
```

确认：

```sh
sysctl net.ipv4.tcp_available_congestion_control
sysctl net.ipv4.tcp_congestion_control
sysctl net.core.default_qdisc
```

持久化示例：

```text
# /etc/sysctl.d/90-mptcp-userspace-relay.conf
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
```

如果内核不提供 BBR，先保持原算法，不要把一次失败的 sysctl 当成已配置完成。

## 5. Provisioning

Provisioning 建议只监听 loopback，再通过 nginx/Caddy 提供 HTTPS。

升级前备份：

- `mpx-provision`；
- systemd unit / env；
- `profiles.json`；
- `bundles.json`；
- `devices.json`；
- admin password；
- reverse proxy 配置。

公网 `/v1/config/` 与 `/v1/bundle/` 使用 opaque `v/n/d` envelope。它隐藏响应明文，但**完整 URL 仍是 bearer credential**，不能替代 HTTPS。

Device Control 使用 Client 主动 long poll，不需要 Client 有公网 IP。反向代理 upstream write/read timeout 应覆盖 long poll，建议留到 35–60 秒级别，不要记录 Authorization header 或敏感 body。

## 6. macOS Client

MPTCP Desk 支持：

- 本地 Profile；
- 远端 Profile/Bundle；
- parallel Bundle；
- LKG；
- Profile 自动重连；
- 后台常驻；
- Sparkle 更新；
- 可选远程设备管理。

后台常驻开启后，登录、睡眠唤醒、网络恢复或 engine 异常会触发 runtime 重建；用户主动停止后不会自动拉起。

远端首次成功同步后建立 LKG。后续 App/系统重启或睡眠恢复可以先使用匹配缓存启动，再后台刷新 API。

### 6.1 Keychain Broker

v1.1.1 继续复用冻结的 `MPTCPKeychainBroker` v1。Broker 与主 App 本体分离，更新主 App 不应重新生成或替换 Broker 身份。

## 7. Linux Client

常用命令：

```text
version
doctor-userspace
validate
run
validate-bundle
run-bundle
validate-managed
run-managed
```

managed URL 建议从 `0600` 文件/stdin 传入，避免出现在 shell history 或进程参数中。

## 8. Parallel Bundle

Parallel 模式分两层错误：

**启动前本地错误**：重复/占用 `listen_port` 等会使整组在启动前失败。

**运行中远端错误**：某个 Profile 连接/认证失败或运行中退出，只重建该 Profile；其它健康 Profile 继续工作。

退避：1s → 2s → 5s → 10s → 30s → 之后每 30s。成功进入 listening 后清零。即使全部 Profile 暂时 down，supervisor 仍保持运行。

## 9. Scheduler 部署建议

- Auto：默认起点；
- Aggregate：希望多路径更积极并发；
- Protect：希望快速隔离异常路径；
- Weighted：有可靠方向容量先验。

Scheduler 是端点本地策略。生产环境建议 Client/Landing 使用相同模式，除非明确要做非对称上下行策略。

## 10. 升级后的网络验收

每次升级至少记录：

```sh
# Landing
sysctl net.ipv4.tcp_congestion_control
ss -s
ss -ti

# Relay
sysctl net.ipv4.tcp_congestion_control
sysctl net.core.default_qdisc
ss -s
ss -ti
```

预期基线：

```text
Landing -> cubic
Relay   -> bbr
Relay qdisc -> fq (recommended)
```

然后在产品诊断中看 Carrier connected、RTT、goodput、queue、outstanding、retransmit/reinjection 与 WINDOW wait。

## 11. 回滚

升级前创建独立备份目录，例如：

```text
/var/backups/mpx-<version>-<timestamp>/
```

至少保存旧二进制、unit、私有配置和 Provisioning 数据。

回滚：

1. 停止对应服务；
2. 恢复旧二进制；
3. 必要时恢复匹配配置/数据；
4. unit 有变化时 `daemon-reload`；
5. restart；
6. 验证版本、监听端口和真实 Client；
7. 验证 Landing 仍为 CUBIC、Relay 仍为 BBR，除非回滚计划明确包含内核网络参数。

不要在没有证据的情况下同时回滚多组无关网络设置。

## 12. 性能变更原则

v1.1.1 发布时没有重新做物理 WAN throughput promotion。因此生产调优应采用单变量 A/B：

1. 先固定 Landing=CUBIC、Relay=BBR；
2. 再测业务；
3. 如需调整其它 sysctl，一次只改一类；
4. 同时看吞吐与 loaded RTT/queue/retransmit/CPU；
5. 保留可回滚配置。

完整网络说明见 [NETWORK-TUNING.zh-CN.md](../guides/NETWORK-TUNING.zh-CN.md)。
