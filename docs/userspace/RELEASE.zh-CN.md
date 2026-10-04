# MPTCP Userspace 0.10.4 / MPX/4 Draft 04

0.10.4 是远端配置持久化与睡眠/重启恢复版本。MPTCP Desk、Linux Client、Landing 与 Provisioning 套件版本统一为 0.10.4。

**MPX/4 Draft 04 数据面保持 0.10.3 完全相同的 wire format、Carrier Generation/Error Scope、Scheduler、flow-control 与 key-schedule 语义。本版不升级 MPX Draft。**

## Managed Last Known Good 缓存

- 第一次 Provisioning API 成功后，macOS Client 会持久化最近一次验证通过的完整 Profile/Bundle 响应。
- 缓存通过当前 Provisioning URL 的 SHA-256 指纹绑定来源；更换 URL 后旧缓存不会被使用。
- 缓存文件位于用户的 Application Support/MPTCPDesk 目录，目录权限 0700、文件权限 0600。
- 完整 secret API URL 继续保存在 Keychain；缓存文件不记录 URL。
- 当前 0.10.2+ Provisioning 的加密 v/n/d 公网响应会按原始不透明 envelope 缓存，因此 Relay 与 Transport Key 不会因为新增缓存而直接明文落盘。
- Bundle 缓存同时保存本机选择的 Profile ID；用户切换选择后会更新缓存元数据。
- 缓存不设置 TTL；除非用户清除/更换 API URL 或成功同步到新配置，否则一直有效。

## 启动、重启与睡眠恢复

有匹配缓存时，手动启动、App 重启、系统重启后的后台恢复以及 sleep/wake 恢复都会直接读取缓存并启动 engine，**不会先等待 Provisioning API 的 10–15 秒 timeout**。

缓存启动后 API 在后台异步刷新：

- 成功：写入新的 LKG 缓存；
- 当前 Session 正在运行：不重启、不替换当前 engine stdin，新配置在下一次自然重连/启动时生效；
- 失败：现有 Session 与旧缓存保持有效。

第一次使用，或者更换为一条没有匹配缓存的新 API URL 时，仍然必须先成功获取一次 API 配置。

## 48 小时自动同步

- 每次成功同步后记录 fetched_at，48 小时后自动再同步；
- App 重启时根据持久化 fetched_at 重新计算剩余周期，不依赖跨睡眠的固定 Timer；
- 后台同步失败后按 1 分钟、5 分钟、30 分钟、3 小时退避；
- 达到 3 小时档后持续按 3 小时重试；
- 用户在运行期间手动点同步时同样只更新后台缓存，不强制重启当前 Session。

## Compatibility

- MPX/4 Draft 04 与 0.10.3 完全一致，本版没有 Carrier、Frame、Scheduler 或 wire 语义更新；
- Provisioning Profile schema 1、Bundle schema 2 与 encrypted envelope v1 不变；
- 0.10.4 Client 继续接受旧明文 schema 1/2 响应用于迁移；
- 当前实现仍为每 Profile 2–8 Relay / 每 Session 最多 8 Carrier；
- 本版不包含此前讨论的 96 Carrier 扩展。

## Validation

发布门槛新增持久化缓存 round-trip、URL 指纹隔离、0600 权限、selected Profile ID 持久化、48 小时 due/retry policy、cache-first launch plan、运行中后台刷新不立即应用等回归；同时保留 Swift arm64/x86_64 typecheck、UI 离屏渲染、Go test/vet/race、Provisioning 加密回归、Parallel Bundle 故障隔离、Linux amd64/arm64 构建与 frozen-source provenance 验证。
