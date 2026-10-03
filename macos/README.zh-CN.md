# 0.10.3 / MPX/4 Draft 04

0.10.0 保持 MPX/4 Draft 04 数据面不变，并把 Provisioning 从“一个 secret URL 对应一个 Profile”扩展为“Profile + Bundle”。

Mac 可以继续手工配置，也可以保存一条 secret Profile/Bundle API URL。schema 1 Profile 按原逻辑运行；schema 2 Bundle 会列出多份完整 Profile，并支持：

- `single_select`：本机手工选择一个 Profile；
- `parallel`：本机选择一个或多个 Profile，同时运行独立 MPX Session。

每份 Profile 的 Listen Port、Relay、Scheduler、Transport Key、TCP/UDP 均由 Provisioning 下发。并行模式要求 Listen Port 唯一，服务端和 Client 启动前双重检查；Client 还会在创建任何子 runtime 前预探测全部本地 TCP/UDP socket。

Bundle 只做 Session 编排，不修改 MPX/4 wire protocol，也不会把不同 Profile 的 Relay 合并成一套。

secret Provisioning URL 保存在 Keychain。Bundle transport key 仅在权威 API 响应和 engine stdin 中使用，不写入普通 preferences；API 获取失败时不会用旧缓存启动。

完整 Draft 04 实现边界见 `docs/userspace/PROTOCOL.md`，Bundle/API 见 `docs/userspace/PROVISIONING.md`。

正式发布组合：MPTCP Desk 0.10.0 + Landing 0.10.0 + Provisioning 0.10.0。
