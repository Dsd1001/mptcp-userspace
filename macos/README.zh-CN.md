# 0.9.7 / MPX/4 Draft 03 + Provisioning

0.9.7 在 0.9.6 MPX/4 Draft 03 基础上新增完整 Provisioning 托管模式：Mac 可以只保存一条 secret API URL，启动与后台恢复前自动获取传输模式、本地端口、TCP/UDP、Scheduler、Relay、Weighted 带宽、transport key 和后台常驻设置。API URL 与 transport key 均保存在 Keychain；托管 API 获取失败时不会使用旧缓存偷偷启动。

0.9.7 **没有修改 MPX/4 Draft 03 wire protocol**。因此 0.9.7 Mac 可以继续连接 0.9.6 或 0.9.7 Landing；Provisioning 服务只负责客户端配置发放，不进入 Relay/Landing 数据面。

0.9.5 引入的后台常驻、登录自启、sleep/wake 与有界恢复逻辑继续保留；当 Provisioning 托管后台常驻开关时，以最新成功拉取的远程配置为准。

完整 Provisioning 说明见 `docs/userspace/PROVISIONING.md`；协议边界见 `docs/userspace/PROTOCOL.md`。
