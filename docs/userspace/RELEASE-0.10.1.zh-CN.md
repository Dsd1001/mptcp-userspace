> **Historical release note / 历史版本说明** — 本文只描述 0.10.1，不代表当前 v1.1.1 行为。当前发布说明见 [RELEASE.zh-CN.md](RELEASE.zh-CN.md)，当前部署网络基线为 **Landing=CUBIC、Relay=BBR**。
>

# MPTCP Userspace 0.10.1 / MPX/4 Draft 04 + Parallel Bundle Fault Isolation

这是 0.10.0 的 Client / Landing 补丁版本。Provisioning 继续使用 0.10.0，Bundle schema 2 与 Profile schema 1 均不变。

## 主要变化

- `parallel` Bundle 仍在启动前对所有所选 Profile 的 TCP/UDP Listen Port 做原子预检查；端口冲突时一个都不启动。
- 通过端口预检查后，各 Profile 改为独立故障域。
- 某个 Profile 无法连接或认证 Landing、子运行时启动失败或运行中退出时，不再取消其他 Profile。
- 第一个可用 Profile 建立本地入口后，Bundle 即进入可运行状态，不再等待全部 Profile 都成功。
- 失败 Profile 会输出独立 `error` 事件；macOS UI 在对应 Profile 行显示红色错误详情。
- macOS 全局状态显示“部分配置运行中”，同时保留错误提示；健康 Profile 的本地入口继续可用。
- Linux `run-bundle` / `run-managed` 使用相同容错语义。
- 只有全部所选 Profile 都失败/退出时，Bundle 才整体退出。
- `single_select` 语义不变：唯一选中的 Profile 失败即启动失败。
- MPX/4 Draft 04 数据面、Transmission ID、Carrier Generation、Scheduler 与 Provisioning API schema 均未改变。

## 版本范围

- MPTCP Desk: 0.10.1
- Linux Client: 0.10.1
- Landing: 0.10.1
- Provisioning: 0.10.0（无 API/数据模型变更）
