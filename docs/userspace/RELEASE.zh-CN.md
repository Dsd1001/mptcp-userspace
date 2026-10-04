# MPTCP Userspace 0.10.5 / MPX/4 Draft 04

0.10.5 是 Parallel Bundle 单 Profile 自动重连与运行时自愈版本。MPTCP Desk、Linux Client、Landing 与 Provisioning 套件版本统一为 0.10.5。

**MPX/4 Draft 04 数据面与 0.10.4 完全相同。本版不修改 Carrier、Frame、Generation/Error Scope、Scheduler、flow-control、key schedule 或 wire format。**

## Parallel Profile 自动重连

Parallel Bundle 中，每一份已启用 Profile 现在都有独立 supervisor。

当某一份 Profile：

- child runtime 启动失败；
- Relay / Landing 暂时不可达；
- 认证/连接阶段失败并退出；
- 已经 listening 后 runtime 意外退出；

只停止并重建这一份 Profile。其他健康 Profile 不会被停止、重启或重新绑定端口。

## 重试节奏

同一 Profile 连续失败时按以下节奏重试：

~~~text
1s → 2s → 5s → 10s → 30s → 30s → 30s → ...
~~~

达到 30 秒后无限期保持每 30 秒一次，直到恢复或用户主动停止。

Profile 成功进入 listening 后会清零自己的失败计数。以后再次断线时重新从 1 秒开始。

## 全部 Profile 暂时不可用

0.10.4 及之前，如果 parallel Bundle 的所有 child 都退出，整个 run-bundle 会结束，需要用户或上层重新启动。

0.10.5 改为：

- Bundle supervisor 保持运行；
- 状态进入 bundle_reconnecting / “全部配置重连中”；
- 每份 Profile 继续自己的独立退避重试；
- 任意 Profile 恢复 listening 后立即重新成为可用配置；
- 不要求手动 Stop / Start。

single_select 保持原有一次只运行一个 Profile 的失败语义，本次自动重连只针对 parallel Bundle。

## UI / telemetry

新增非 MPX 的本地 runtime telemetry：

- reconnecting profile state；
- retry_after_seconds；
- retry_attempt；
- reconnecting_profiles；
- bundle_reconnecting。

MPTCP Desk 的 Profile 卡片会显示“重连中 · Ns”，Bundle 全部断开时显示“全部配置重连中”。这些字段只用于 Client/engine 本地状态展示，不进入 MPX/4 wire protocol。

## 0.10.4 Managed LKG 行为保持不变

- 远端配置首次成功同步后持久化 Last Known Good；
- 有匹配缓存时启动、App/系统重启、睡眠唤醒不等待 API timeout；
- API 在后台异步刷新；
- 缓存不设置 TTL；
- 成功同步后 48 小时再次检查；
- 后台更新不强制重启当前 Session。

## Compatibility

- MPX/4 Draft 04 与 0.10.4 完全一致；
- Provisioning Profile schema 1、Bundle schema 2、encrypted envelope v1 不变；
- 当前仍为每 Profile 2–8 Relay / 每 Session 最多 8 Carrier；
- 本版不包含 96 Carrier 扩展；
- 0.10.5 Client / Landing / Provisioning 作为统一发布套件。

## Validation

0.10.5 新增以下回归：

- 精确重试序列 1s / 2s / 5s / 10s / 30s / 30s；
- 一个 Profile 失败后恢复，健康 Profile 不重启；
- listening 后 runtime crash 自动重连；
- 所有 parallel Profile 同时失败后 supervisor 保持存活并可恢复；
- 永久失败存在退避，不产生 busy loop；
- 用户/上下文取消能终止 supervisor；
- single_select 原有失败语义保持。

同时继续执行 Go test/vet/race、Swift arm64/x86_64 typecheck、UI 离屏渲染、Provisioning 加密/LKG 回归、Linux 双架构构建与 frozen-source provenance 验证。
