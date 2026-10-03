# MPTCP Userspace 0.10.2 / MPX/4 Draft 04

0.10.2 是整套发布版本：MPTCP Desk、Linux Client、Landing 与 Provisioning 统一为 0.10.2。MPX/4 Draft 04 数据面 wire format、Generation/Error Scope、Scheduler 与 key schedule 不变。

## Client UI

- 首页在“视图”下增加“本地配置 / 远端配置”选择；未选择远端时不再常驻显示 API URL。
- 远端 URL 使用简洁浅灰提示“请输入 Provisioning API 地址”。
- 首页删除工程解释型小字，只保留必要操作和业务错误提示。
- 右上角只显示 `0.10.2`。
- 日志页过滤测速免责声明、协议实现解释等说明性文字，只保留同步、连接、认证、启动、故障、恢复和运行状态。
- 远端 Bundle 路径诊断按 Profile 展示路径状态、RTT、Goodput、队列/在途与错误；Relay IP/端口和原始 endpoint 错误不会显示在客户路径诊断 UI。
- 0.10.1 的 Parallel Bundle 故障隔离行为保留：单个 Profile 失败时其他健康 Profile 继续运行。

## Provisioning API opacity

- Profile/Bundle URL、Alias、Secret 轮换和内部 schema 1/2 均保持不变。
- `/v1/config/...` 与 `/v1/bundle/...` 的公网响应外层改成紧凑 `{v,n,d}` 加密封装，不再直接显示 Relay IP、端口、Transport Key 或 Profile JSON。
- URL 中现有 256-bit随机 Secret 作为密钥材料：HMAC-SHA256 固定上下文派生 256-bit key，AES-256-GCM 加密，96-bit nonce 每次响应随机生成，并使用固定 AAD 做完整性认证。
- 不增加设备注册、Public Key、授权列表或第二个密码。拥有完整 API URL 的人仍然拥有解密材料；此功能目标是避免配置直接可读，不替代 HTTPS 或 bearer URL 保密。
- 0.10.2 Mac/Linux Client 能读取新加密封装，也继续接受旧的明文 schema 1/2 API 以便迁移。

## Validation

发布门槛包括 Go test/vet/race、Provisioning 加密/错误 Secret/随机 nonce 测试、managed plaintext compatibility、Swift arm64/x86_64 typecheck、真实 Provisioning→Swift/Go 解密、UI 离屏渲染、Parallel Bundle 故障隔离回归，以及冻结源码的 Linux amd64/arm64 可复现构建与 Mac Universal DMG 验证。
