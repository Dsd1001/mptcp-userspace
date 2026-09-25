# 0.9.4 配套部署与回滚

0.9.4 新增 MPX/3 Rev5 Weighted。Weighted 必须 Mac 与 Landing 都升级到 0.9.4；Auto / Aggregate / Protect 继续使用 0.9.3 的 0x41/0x42/0x43 hello，因此只使用旧三模式时可以与 0.9.3 对接。0.9.0 Rev2、Rev3 候选及更早协议仍不能混连。

旧 schema 3 Profile 可以直接载入，默认仍为 Auto。只有选择 Weighted 时才要求为每条 Relay 填写下行 Mbps；上行 Mbps 可留空，表示 Mac→Landing 方向继续自动估算。Relay 仍只转发普通 TCP 字节，无需协议改造；backend、传输密钥和端口模型不变。

部署前核对交付物 SHA256 和 Source-ID，保留上一版 DMG、Landing 二进制及配置。构建成功不等于真实 App+Surge 或公网性能验收；精确状态以随包 ACCEPTANCE.md、TESTS.json、SCHEDULER-MODES.json、CAPACITY.json 和 RUNTIME.json 为准。

## Landing：Linux amd64

`mptcp-landing` 同时是服务和交互管理程序。可使用 `./mptcp-landing menu`、`version`、`doctor`、`status`、`config`。不要把传输密钥输出到报告或聊天。

已有托管安装可走受控升级：

```sh
chmod 755 /root/mptcp-landing
/root/mptcp-landing version
/root/mptcp-landing upgrade --source /root/mptcp-landing --sha256 <交付校验文件中的完整SHA256>
/usr/local/bin/mptcp-landing doctor
/usr/local/bin/mptcp-landing status
```

管理器保留上一份二进制和配置，支持 `mptcp-landing rollback`。若使用 Weighted，回滚 Landing 时 Mac 也必须退出 Weighted 或回滚到配套版本。

对于现有 HKT 部署，继续沿用现有 Landing 监听端口、backend、max_sessions 和 transport key，除非另有明确要求。不要顺手调整 Soga、Native、Relay 转发、防火墙或 UDP 开关，也不要重启无关服务。

## Mac

停止旧版转发后再用配套 Universal DMG 替换 App。该包为 ad-hoc 签名，未做公证；请先核对 Source-ID，再按 macOS 正常授权流程打开，不自动清除扩展属性或绕过系统安全策略。

默认 Auto；也可选择 Aggregate、Protect 或 Weighted。运行期间配置锁定。Weighted 每条 Relay 的“下行 Mbps”为必填，“上行 Mbps”为选填；上行留空时该方向继续原自动估速。显式 0 或超过 1 位小数会被配置校验拒绝。

双端升级后核对版本、Source-ID、capability revision、carrier 数量、Configured/Effective Scheduler、每条路径的 Weighted rate、重传和资源账目。路径历史 `last_error` 不代表当前仍处于 penalty，应结合当前连接、时间和业务流量判断。

本发行流程不会自动替换已安装 Mac App，也不会自动部署 HKT。实际部署必须是单独、明确的操作。
