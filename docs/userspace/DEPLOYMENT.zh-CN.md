# 0.10.7 部署、升级与回滚

本文面向 **MPTCP Userspace v0.10.7 / MPX/4 Draft 04**。正式环境建议 Client、Landing、Provisioning 使用同一版本。

## 1. 发布文件

从 v0.10.7 Release 下载并校验：

- MPTCP-Desk-0.10.7-universal.dmg
- mptcp-client-linux-amd64 / arm64
- mptcp-landing / mptcp-landing-linux-arm64
- mpx-provision / mpx-provision-linux-arm64
- MPTCP-Userspace-0.10.7-SHA256SUMS
- SOURCE_ID / PROVENANCE.json / TESTS.json

任何二进制替换前先确认 SHA256 与 Source-ID。

## 2. Landing

Landing 配置应保存在受限权限文件中，至少包含：

- listen 地址/端口；
- backend TCP/UDP；
- Transport Key；
- UDP 开关；
- max_sessions。

推荐由 systemd 管理，并把配置通过 LoadCredential 或同等受限方式传入。

升级步骤：

1. 记录当前 version / Source-ID / binary SHA256；
2. 备份旧二进制、systemd unit 与配置；
3. 校验新二进制；
4. 原子替换 /usr/local/bin/mptcp-landing；
5. restart；
6. 检查监听端口、systemd active 状态与日志；
7. 用同版本 Client 做一次真实认证/配置验证。

不要为了升级 Landing 修改无关的 Relay、backend、Native MPTCP、AB/network/tunnel 服务。

## 3. Provisioning

Provisioning 建议只监听 loopback，例如 127.0.0.1:8088，再由 nginx/Caddy 等成熟反向代理提供 HTTPS。

升级前备份：

- mpx-provision 二进制；
- systemd unit；
- env 文件；
- profiles.json；
- bundles.json；
- devices.json（0.10.6 新增，首次使用前可不存在）；
- admin-password；
- reverse proxy 配置。

0.10.7 保持现有 Profile/Bundle 数据模型与 URL；不需要迁移数据。

公网 /v1/config/ 与 /v1/bundle/ 响应是 v/n/d 加密 envelope。不要用“浏览器看不到明文”替代 HTTPS；完整 URL 本身仍是 bearer credential。

### 3.1 Device Control

0.10.7 的远程设备控制复用同一个 Provisioning HTTPS 站点，不需要额外开放 Client 端口。Mac 主动访问 /v1/device/*，因此 NAT / CGNAT 后的设备可以正常使用。

若 nginx/Caddy 对 upstream 设置了较短超时，请保证 Device long poll 的响应写超时至少大于 25 秒，建议 35–60 秒。不要记录 Authorization header，也建议对 /v1/device/ 路径关闭敏感 header/body 日志。

可通过 MPX_PROVISION_DEVICES 指定 devices.json 路径；默认与 profiles.json 同目录。该文件权限应为 0600。

## 4. macOS Client

替换 App 前先停止旧 runtime。

0.10.7 首页有：

- 本地配置；
- 远端配置。

远端模式下 secret URL 存入 Keychain。Bundle Profile 的 Transport Key 从权威响应进入内存/engine stdin，不写普通 preferences。

后台常驻开启时，登录、睡眠唤醒、网络恢复或 engine 异常后会重建 runtime。用户手动“停止”后不会自动拉起。

首次远端同步成功后会建立持久化 LKG 缓存。后续启动、App/系统重启、睡眠唤醒恢复都直接使用匹配缓存启动，不等待 Provisioning API timeout。API 在后台更新；拿到新配置只替换下一次重连使用的缓存，不强制中断当前 Session。

缓存不设置过期时间。成功同步 48 小时后自动再检查；失败后按 1 分钟、5 分钟、30 分钟、3 小时退避并持续重试。更换 API URL 后旧缓存因来源指纹不匹配而不会被使用。

### 4.1 内置更新与远程管理

MPTCP Desk 的“设置”页包含：

- 本地“启用远程管理”开关，默认关闭；
- 本地控制服务器 URL；
- 一次性配对码与解除配对；
- 手动检查更新与自动检查开关。

远程管理的本地开关与控制服务器地址不接受 API 下发。关闭开关后 Client 立即停止 long polling；解除配对还会删除本机 Keychain 设备凭据并请求服务端撤销。

App 更新使用 Sparkle EdDSA 签名 Feed。0.10.7 仍为 ad-hoc codesign / 未 notarize，更新真实性由独立 EdDSA 签名校验；后续若切换 Developer ID / notarization，不改变控制协议。

## 5. Linux Client

Linux Client 支持：

~~~text
validate
run
validate-bundle
run-bundle
validate-managed
run-managed
doctor-userspace
version
~~~

managed URL 建议只通过 stdin/0600 文件传入，避免出现在命令行参数。

## 6. Parallel Bundle 行为

parallel 模式分两层错误：

**本地配置错误**：重复/占用 listen_port 等在启动前原子预检查，失败时整组不启动。

**远端运行错误**：某个 Profile 无法连接/认证或运行中退出，只重建该 Profile；其他健康 Profile 继续运行。即使全部暂时不可用，parallel Bundle supervisor 也继续自动重连；用户主动停止才结束本轮 supervisor。

## 6.1 0.10.6 Parallel 自愈

parallel Bundle 的 child runtime 断开后会独立自动重建，退避为 1s / 2s / 5s / 10s / 30s，之后每 30s。全断时 run-bundle supervisor 仍保持运行。因此监控上不要把“0 个 active 但 supervisor 仍在 bundle_reconnecting”误判为主进程故障。

## 7. 回滚

每次升级都应保留一个可独立恢复的目录，例如：

~~~text
/var/backups/mpx-<version>-<timestamp>/
~~~

至少保存旧二进制、unit、配置和 Provisioning 数据。

回滚时：

1. 停止对应服务；
2. 恢复旧二进制；
3. 仅在必要时恢复与该旧版匹配的配置/数据；
4. daemon-reload（unit 有变化时）；
5. restart；
6. 验证 version、监听端口和真实 Client 连接。

不要在没有证据的情况下回滚或覆盖与本次升级无关的网络/代理服务。

## 8. 发布验证边界

发布包的 PROVENANCE/TESTS/CAPACITY/RUNTIME 等记录描述构建与验证证据。它们不能替代具体生产环境的连通性、带宽、Relay 或 backend 检查。

当前验证要求见 [VALIDATION.md](VALIDATION.md)。
