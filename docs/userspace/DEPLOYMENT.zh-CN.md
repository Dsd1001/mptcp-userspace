# 0.9.5 配套部署、后台常驻与回滚

0.9.5 继续使用 MPX/3 Rev5，网络协议与 0.9.4 相同。0.9.5 Mac 可以直接连接 0.9.4 Landing 并使用 Weighted；只有 0.9.3 及更早 Landing 不理解 Weighted 0x44。Auto / Aggregate / Protect 仍保持与 0.9.3 的 hello 兼容。

## Mac：后台常驻

0.9.5 新增可选“后台常驻”。开启后：

- 使用 macOS ServiceManagement 注册当前 App 为登录项；
- 记住“转发应保持运行”的用户意图；
- 监听系统 sleep / wake；
- 唤醒后主动重建 engine/session，而不是尝试延续睡前 TCP socket；
- 用 Network framework 等待网络可用后再拨 Relay；
- engine 意外退出时使用 1 / 2 / 5 / 10 / 30 秒退避；连续 5 次仍未恢复到 listening 时停止本轮自动重试并提示需要处理；
- 用户手动点击“停止”会清除运行意图，后台逻辑不会再自动拉起；
- 关闭后台常驻会撤销登录项，但不会强制停止当前已运行的转发。

后台常驻要求 macOS 13+，与本 App 的最低系统版本一致。它不是 root LaunchDaemon，也不会修改系统代理、路由、DNS、防火墙或内核 MPTCP。

若系统设置把登录项状态标记为“需要批准”，App 会显示对应状态；需要用户在 macOS 登录项设置中允许后，下一次登录自启才会生效。睡眠/唤醒和当前 App 会话内的 engine 恢复不依赖 root 权限。

旧 schema 3 Profile 可直接载入。Weighted 每条 Relay 的下行 Mbps 仍必填、上行 Mbps 仍选填；上行留空表示 Mac→Landing 继续自动估算。

## Landing：0.9.4 可继续使用

本版本没有新的 wire revision，因此已经部署的 0.9.4 Landing **无需为了 0.9.5 Mac 再升级**。如希望版本号统一，也可以部署 0.9.5 Landing；其协议与资源边界不变。

Landing 管理命令仍为：

```sh
./mptcp-landing version
./mptcp-landing doctor
./mptcp-landing status
```

受控升级示例：

```sh
chmod 755 /root/mptcp-landing
/root/mptcp-landing upgrade --source /root/mptcp-landing --sha256 <完整SHA256>
/usr/local/bin/mptcp-landing doctor
/usr/local/bin/mptcp-landing status
```

管理器会保留上一份二进制和配置用于 rollback。不要把 transport key 输出到报告或聊天。

## 安装与回滚

替换 App 前保留上一版 DMG。0.9.5 使用新的 UserDefaults 标志记录后台常驻与“应保持运行”意图；关闭“后台常驻”即可撤销登录项并清除自动运行意图。回滚到 0.9.4 时，0.9.4 不读取这些新标志，因此不会实现自动唤醒恢复。

构建成功不等于物理 App+Surge 或公网性能验收。精确验证状态以随包 ACCEPTANCE.md、TESTS.json、SCHEDULER-MODES.json、CAPACITY.json、RUNTIME.json 为准。

本发行流程不会自动替换已安装 Mac App，也不会自动部署 HKT；部署是单独、明确的操作。不要顺手调整 Surge、Soga、Relay、Native、防火墙或 UDP 设置。
