# MPTCP Userspace 0.10.7 / MPX/4 Draft 04

0.10.7 是一次前端界面更新版本：正式发布重新设计后的 MPTCP Desk macOS 界面与 Provisioning Web 管理后台。Client、Landing、Provisioning 套件版本统一为 0.10.7。

**MPX/4 继续使用 Draft 04：WireProtocol=4、CapabilityRevision=4。Carrier、Frame、Generation/Error Scope、Scheduler、flow-control 与 key schedule 均保持不变。**

## 前端更新

### MPTCP Desk

- 重新组织主界面的导航、状态层级和配置展示；
- 优化 Profile / Bundle 使用过程中的信息密度与状态可读性；
- 保留现有本地启动/停止、Provisioning、远程管理、路径状态与更新入口；
- 不改变配置 schema、runtime supervisor 或远程控制协议。

### Provisioning Web Console

- 重新设计 Profile、Bundle 与 Devices 管理界面；
- 简化页面结构与操作区域，统一状态展示；
- 底层 API、数据文件格式、配对凭据和 Device Control 语义保持兼容。

## 既有能力保持

- 0.10.6 Sparkle 2 EdDSA 签名更新通道保持不变；
- 0.10.6 可选远程设备管理保持默认关闭、必须本地开启和配对；
- Client 仍通过 outbound HTTPS long polling 工作，不要求公网 IP；
- 0.10.5 parallel Bundle 每 Profile 独立 supervisor 继续按 1s → 2s → 5s → 10s → 30s → 每 30s 自动重连；
- 0.10.4 Last Known Good 缓存与睡眠/重启快速恢复语义保持不变；
- Profile schema 1、Bundle schema 2、opaque envelope v1 不变。

## Validation

0.10.7 按 0.10.x feature/patch release 门执行：

- Go test / vet / race；
- Provisioning test / vet / race；
- Swift arm64 / x86_64 typecheck；
- Swift UI smoke；
- Bundle API / engine regression；
- Linux amd64 runtime smoke；
- Universal DMG、Sparkle appcast 与 EdDSA 签名校验；
- frozen-source / provenance 校验。

本版不重新宣称新的 MPX Scheduler、容量或 WAN 性能提升。
