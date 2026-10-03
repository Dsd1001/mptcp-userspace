# MPTCP Userspace 0.10.3 / MPX/4 Draft 04

0.10.3 是整套发布版本：MPTCP Desk、Linux Client、Landing 与 Provisioning 统一为 0.10.3。MPX/4 Draft 04 数据面 wire format、Generation/Error Scope、Scheduler、key schedule 以及 0.10.2 Provisioning 加密封装均不变。

## 路径诊断

- 远端 Provisioning Bundle 不再只显示路径卡片；每个选中的 Profile 现在独立保存并展示完整 Session 诊断。
- 每个远端 Profile 可查看配置/当前 Scheduler、自动切换次数、发送方向原因、TCP 载路、连接、上传/下载、当前/峰值重排、等待确认、TCP 重传、UDP 丢弃以及完整路径统计。
- 远端路径仍只显示“路径 1 / 路径 2 …”，不会在客户 UI 暴露 Relay IP、端口或原始 dial endpoint 错误。
- 不同 Profile 的资源、路径与 Scheduler 数据严格分开，不做跨 Session 混合。

## 可折叠资源窗口

- 原来的深度资源信息拆成两组：`Stream / 生命周期资源` 与 `Window / Credit 资源`。
- 两组默认收起，减少路径诊断页纵向占用；标题行仍显示“活跃/Closing”或“分页/待确认”的简要摘要。
- 点击标题即可展开原有完整字段，包括 Stream 生命周期、DATA 静默、待结算、发送未消费、接收 Credit、实际分页、基础/增长占用、DATA/控制帧、窗口阻塞、idle/small/bulk 与 OPEN Credit 等待。
- 本地/单 Profile 使用一组折叠状态；远端 Bundle 中每个 Profile 各自独立。状态只保留在当前 App 会话，不写入 UserDefaults/Keychain。

## Validation

发布门槛包括 Go test/vet/race、Provisioning 加密兼容回归、Swift arm64/x86_64 typecheck、逐 Profile 遥测隔离断言、默认收起/展开 UI 离屏渲染、隐藏远端 endpoint 回归、Parallel Bundle 故障隔离，以及冻结源码的 Linux amd64/arm64 可复现构建与 Mac Universal DMG 验证。
