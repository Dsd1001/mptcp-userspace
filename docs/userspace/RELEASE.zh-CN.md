# MPTCP Userspace 0.10.6 / MPX/4 Draft 04

0.10.6 是 MPTCP Desk 内置签名更新与 Provisioning 远程设备管理版本。Client、Landing、Provisioning 套件版本统一为 0.10.6。

**MPX/4 继续使用 Draft 04：WireProtocol=4、CapabilityRevision=4。Carrier、Frame、Generation/Error Scope、Scheduler、flow-control、key schedule 与 0.10.5 保持不变。**

## MPTCP Desk 内置更新

macOS App 集成 Sparkle 2.10.0：

- 菜单和设置页提供“检查更新”；
- 默认每天后台检查一次；
- 更新 Feed 固定为 GitHub Release 的 appcast.xml；
- DMG 使用独立 EdDSA 更新签名；
- App 内只保存更新公钥，签名私钥不进入仓库和发布包；
- 远程“更新客户端”也只能触发相同签名更新通道；
- 不支持远程指定任意 URL 或执行任意程序。

0.10.6 仍使用 ad-hoc codesign，尚未切换 Developer ID / notarization；更新真实性由 Sparkle EdDSA 独立校验。

## 远程设备管理

Provisioning 管理后台新增 Devices。

每台 Mac 的远程管理：

- 默认关闭；
- 只能在 Mac 本机手动打开；
- 控制服务器 HTTPS 根地址只能本地输入；
- 必须本地输入后台生成的一次性配对码；
- 设备 secret 单独存入 Keychain；
- API 不能打开本地远程管理开关，也不能改写控制服务器地址。

配对后的 Client 主动向服务器建立 HTTPS long poll，因此不需要公网 IP，也不要求 NAT 端口映射。

后台支持固定白名单动作：

- 分配 Profile 或 Bundle；
- Desired State：running / stopped；
- 请求立即同步 Provisioning 配置；
- 请求重启当前转发；
- 请求升级到签名更新通道的 latest；
- 查看 app version、running/status、配置 revision、Bundle 和逐 Profile 运行状态。

没有 Shell、脚本或任意 command 字段；未知控制字段会被严格 JSON 校验拒绝。

## 设备凭据与持久化

Provisioning 为每台设备维护独立 256-bit device secret，服务端只保存 SHA-256 hash。一次性配对码有效期 10 分钟，服务端同样只保存 hash。

Desired State 与 sync/restart/update generation 持久化到 devices.json，因此设备离线时发出的启动、停止、同步、重启或更新意图不会因为当时不在线而丢失。

devices.json 与其他 Provisioning 私有数据一样使用 0600 文件权限；设备可以从本机主动解除配对并使服务端凭据失效。

## 既有能力保持

- 0.10.4 LKG 缓存仍然无 TTL，启动/睡眠恢复不等待 API；
- 成功 Provisioning 同步后 48 小时后台检查，失败按既有退避重试；
- 0.10.5 parallel Bundle 每 Profile 独立 supervisor 保持 1s → 2s → 5s → 10s → 30s → 每 30s；
- 全部 parallel Profile 暂时断开时 Bundle supervisor 继续存活并恢复；
- single_select 既有语义不变；
- Profile schema 1、Bundle schema 2、opaque envelope v1 不变。

## Validation

0.10.6 发布门包括：

- Device create/pair/auth/report/revoke；
- 不同设备凭据隔离；
- 配对码和 device secret 不以明文落盘；
- Desired State / generation 离线持久化；
- 未知/任意 command 字段拒绝；
- Mac 远程管理默认关闭、关闭时 desired state 被忽略；
- 控制服务器强制 HTTPS（loopback 开发例外）；
- Sparkle 框架与更新公钥嵌入；
- appcast XML、版本/build、DMG 长度、EdDSA 签名一致；
- Swift arm64/x86_64 typecheck 与 UI smoke；
- Go test/vet/race、Provisioning test/vet；
- 0.10.5 reconnect 和 0.10.4 LKG 回归；
- frozen-source / provenance 校验。

本版不重新宣称新的 MPX Scheduler / WAN 性能提升。
