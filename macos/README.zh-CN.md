# 0.9.8 / MPX/4 Draft 04 + Provisioning

0.9.8 对齐 MPX/4 Draft 04，并把完整 Provisioning 托管模式正式纳入发布。

Draft 04 保持 Draft 03 的字节编码，但新增并强制执行 Highest Accepted Generation、equal-generation reuse rejection、authenticated replacement commit、SUPERSEDED Carrier、no-wrap、Transmission-ID validity 和 Error Scope / CARRIER_CLOSE / SESSION_CLOSE 等规范语义。

Mac 可以继续手工配置，也可以只保存一条 secret Provisioning API URL。托管模式会在启动与后台恢复前获取 transport mode、本地端口、TCP/UDP、Scheduler、Relay、Weighted 带宽、transport key 和后台常驻设置。API URL 与 transport key 均保存在 Keychain；权威 API 获取失败时不会使用旧缓存启动。

完整 Draft 04 实现边界见 `docs/userspace/PROTOCOL.md`，Provisioning 见 `docs/userspace/PROVISIONING.md`。

正式支持组合为 Mac 0.9.8 + Landing 0.9.8。Draft 04 与 Draft 03 wire encoding 相同，但混合旧实现不作为 0.9.8 的正式支持组合。
