# MPTCP Userspace 0.9.5 / MPX/3 Rev5 后台常驻版

## 主要变化：macOS 后台常驻与睡眠恢复

0.9.5 在 0.9.4 Weighted 正式版基础上新增可选的“后台常驻”，不改变 MPX/3 Rev5 线协议、Weighted 算法、信用模型或资源上限。

开启“后台常驻”后，MPTCP Desk 会：

- 使用 macOS 13+ `SMAppService.mainApp` 注册登录项；
- 记住用户是否希望转发保持运行；
- 监听 macOS sleep / wake；
- 使用 `NWPathMonitor` 等待网络重新可用；
- 睡眠唤醒后主动放弃睡前 engine/session，重新建立本地入口和 Relay carrier；
- engine 意外退出时按 1 / 2 / 5 / 10 / 30 秒退避自动恢复；连续 5 次仍未恢复到 listening 时停止本轮重试并提示需要处理；
- 用户手动点击“停止”后清除运行意图，不会被后台逻辑重新拉起；
- 用户关闭“后台常驻”时撤销登录项；当前已经运行的转发不被强制停止。

用户主动退出 App 时，本次登录会话内不会被 KeepAlive 立即拉起；如果后台常驻仍开启且退出前仍希望保持转发，下次登录后会重新启动。

后台常驻是客户端生命周期能力，不是新的网络协议。没有 root LaunchDaemon，不修改系统代理、路由、防火墙或内核 MPTCP 设置。

## 0.9.4 Weighted 完整保留

Weighted 仍支持每条 Relay：

- `download_mbps`：必填；
- `upload_mbps`：选填，留空时 Mac→Landing 方向继续在线估速；
- 0.1–6553.5 Mbps，最多 1 位小数。

配置仍位于认证 hello 内；Landing→Mac 使用 download，Mac→Landing 使用 upload 或自动估速。实时 RTT、writer queue、连接状态、penalty、delivery timeout、reinject 与重传保护保持有效。

## 协议兼容

0.9.5 仍是 **MPX/3 capability revision 5**，没有新增 wire byte：

- `0x41` Auto
- `0x42` Aggregate
- `0x43` Protect
- `0x44` Weighted

因此：

- **0.9.5 Mac + 0.9.4 Landing：Weighted 可用**；
- 0.9.5 Auto/Aggregate/Protect 继续保持与 0.9.3 的 hello 兼容；
- 0.9.3 不理解 0x44，因此 Weighted 仍至少需要 Rev5（0.9.4+）双端；
- MPX/1、MPX/2、早期 Rev2/Rev3 候选仍不兼容。

帧格式、AES-GCM、32 KiB DATA、2048 streams、128 MiB session credit、128 MiB sender DATA pending、128 MiB physical receive allocator、16 MiB 单流窗口上限均不扩大。UDP 仍使用独立数据报调度，不使用 Weighted。

## 验证边界

0.9.5 正式交付要求当前 Source-ID：

- 后台恢复纯逻辑测试；
- macOS 13 ServiceManagement / Network / NSWorkspace API 编译；
- Swift Profile/UI/Scheduler harness；
- Go 全量 test / vet / race；
- 四种 scheduler 真实 stdin 回归；
- 0.9.4 Rev5/Weighted 方向容量、timeout/penalty 与高 BDP 回归；
- Universal DMG / Linux Landing 从冻结源码反向验证。

实验室回归不是公网测速承诺。本版本不会把历史容量或现场数据重新标记成 0.9.5 结果；若未重跑完整 30 秒容量矩阵与真实 App+Surge 180 秒现场，`CAPACITY.json` / `RUNTIME.json` 必须明确标记为未运行。

Mac 包仍为 arm64/x86_64 Universal DMG，ad-hoc 签名，未做 Developer ID 公证。构建/发布不会自动替换已安装 App，也不会自动部署 HKT、Surge、Soga、Relay、Native 或防火墙服务。
