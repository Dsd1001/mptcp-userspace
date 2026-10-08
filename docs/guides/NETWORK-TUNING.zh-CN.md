# 网络与拥塞控制调优：Landing CUBIC / Relay BBR

本文给出 MPTCP Userspace v1.1.1 推荐的 Linux TCP 生产基线。

## 推荐基线

| 主机角色 | TCP 拥塞控制 | qdisc | 建议 |
| --- | --- | --- | --- |
| **Landing** | **CUBIC** | 默认保持发行版现状，除非实测需要改变 | **推荐默认** |
| **Relay** | **BBR** | **优先 `fq`** | **推荐默认** |
| Backend | 按业务决定 | 按业务决定 | 一般不要因为 MPX 随意修改 |
| macOS Client | 由系统管理 | 由系统管理 | 不适用 Linux sysctl |

再次强调：**默认不是所有机器都开 BBR，而是 Landing=CUBIC、Relay=BBR。** 这是部署层建议，不是 MPX/4 wire requirement。

## 为什么 Relay 推荐 BBR

Relay 位于 Carrier 的转发边缘，会在一个或两个转发方向上发送普通 TCP。面对不同 RTT、不同可用带宽和容易排队的公网链路时，BBR 的 pacing 以及带宽/RTT 模型通常更适合压低持续队列，减少单纯依赖丢包锯齿扩窗带来的 bufferbloat。

这与 MPX/4 的工作方式比较匹配：上层 Scheduler 本身已经在观察 RTT、queue、outstanding、delivery 和 penalty。Relay 如果因为传统 loss-based 扩窗在路径上制造很大的长期队列，Scheduler 看到的反馈会更滞后。

Relay 使用 BBR 时建议配合 `fq`，让内核 pacing 更稳定地落到实际发送队列。

## 为什么 Landing 推荐 CUBIC

Landing 是所有 active Carrier 的汇聚端。当前项目推荐这里保持 CUBIC，让汇聚点使用成熟、可预测的 loss-based TCP，而把多路径选择、路径 flight budget、retransmission/reinjection 留给 MPX/4 自己处理。

不要因为 Relay 使用 BBR 就把 Landing 也默认改成 BBR。Landing 叠加模型型拥塞控制后，会改变 MPX Scheduler 看到的 RTT/queue 反馈，可能让多 Carrier 行为更难解释。需要测试 Landing BBR 时，应当把它当成 A/B 实验，并始终与 CUBIC 基线比较，而不是直接作为生产默认。

## 修改前先检查内核

所有 Linux 主机先看：

```sh
sysctl net.ipv4.tcp_available_congestion_control
sysctl net.ipv4.tcp_congestion_control
sysctl net.core.default_qdisc
```

准备启用 BBR 的 Relay 必须在 `tcp_available_congestion_control` 中看到 `bbr`。

如果 BBR 是模块：

```sh
sudo modprobe tcp_bbr
sysctl net.ipv4.tcp_available_congestion_control
```

如果仍然没有 `bbr`，不要硬写 sysctl；先使用现有算法，或者升级到明确支持 BBR 的内核。

## Landing 设置为 CUBIC

临时生效：

```sh
sudo sysctl -w net.ipv4.tcp_congestion_control=cubic
```

验证：

```sh
sysctl net.ipv4.tcp_congestion_control
```

持久化建议单独创建文件：

```text
# /etc/sysctl.d/90-mptcp-userspace-landing.conf
net.ipv4.tcp_congestion_control = cubic
```

然后按发行版正常方式加载，例如：

```sh
sudo sysctl --system
```

Landing 只要求优先固定 CUBIC；`default_qdisc` 不必为了本项目强行修改。除非有明确排队证据，否则保留发行版默认即可。

## Relay 设置为 BBR

临时生效：

```sh
sudo modprobe tcp_bbr
sudo sysctl -w net.core.default_qdisc=fq
sudo sysctl -w net.ipv4.tcp_congestion_control=bbr
```

验证：

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

如果内核需要开机加载模块，再按发行版方式在 `/etc/modules-load.d/` 中加入 `tcp_bbr`。

## 这些设置实际影响什么

Linux TCP congestion control 只控制该主机作为发送方的 TCP socket。本项目里主要包括：

- Client / Relay / Landing 之间的 Carrier TCP；
- Landing 到 TCP backend 的普通 TCP socket；
- UoT 所复用的 TCP Carrier。

**Native MPU/1 UDP 不受 TCP congestion control 控制。**

## 修改后怎么判断有没有变好

一次只改一个角色，并保留修改前数据。最少检查：

```sh
sysctl net.ipv4.tcp_congestion_control
ss -s
ss -ti
```

同时观察 MPTCP Userspace 诊断：

- Carrier RTT；
- measured goodput；
- queue / outstanding；
- retransmit / reinjection；
- path role / penalty；
- Stream / Session WINDOW wait；
- Relay 与 Landing CPU。

不能只看峰值测速。如果峰值高了一点，但满载 RTT 长期暴涨、queue 越堆越大、重传明显增加或者 CPU 打满，这个修改就不应该视为成功。

## 不要一次改一堆 sysctl

推荐调优顺序：

1. **Landing=CUBIC**；
2. **Relay=BBR + `fq`**；
3. 保持 Linux socket buffer autotuning；
4. MTU 没有明确证据不要乱改；
5. 基线稳定后再单独测试 buffer、qdisc、offload、pacing 等其它参数。

每次只改一类变量，才能判断到底是什么造成了吞吐、RTT 或稳定性的变化。

## 回滚

运行时可以直接切回：

```sh
# Landing
sudo sysctl -w net.ipv4.tcp_congestion_control=cubic

# Relay 如需回退到 CUBIC
sudo sysctl -w net.ipv4.tcp_congestion_control=cubic
```

持久化回滚时删除/修改对应 `/etc/sysctl.d/90-mptcp-userspace-*.conf`，然后重新加载。修改 Relay qdisc 前应记录原值，以便需要时恢复。
