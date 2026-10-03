# MPTCP Userspace 0.10.0 / MPX/4 Draft 04 + Multi-Profile Provisioning

0.10.0 是整套发布版本：MPTCP Desk、Linux Client、Landing 与 Provisioning 统一升级到 0.10.0。MPX/4 Draft 04 数据面 wire format、Generation/Error Scope 语义、Scheduler ID 与 key schedule 不变；本次主要新增 Provisioning/Client 多 Profile 编排能力。

## 发布平台

- macOS Client：arm64 + x86_64 Universal DMG；
- Linux Client：amd64 + arm64 静态 ELF；
- Linux Landing：amd64 + arm64 静态 ELF；
- Linux Provisioning：amd64 + arm64 静态 ELF。

## Provisioning Bundle

Provisioning 现在有两个独立对象：

- **Profile**：完整运行配置，拥有自己的 `listen_port`、Relay、Scheduler、Transport Key、TCP/UDP 等；
- **Bundle**：选择多份 Profile，通过一条独立的高熵 secret API URL 下发。

Bundle 支持：

- `single_select`：Client 手工选择且只能启用一个 Profile；不同 Profile 可以复用同一个 listen port；
- `parallel`：Client 可以同时启用一个或多个 Profile；Bundle 内所有 Profile 的 listen port 必须唯一。

多 Profile 并行不是把 Relay 合并成一套。每份 Profile 都运行独立 MPX Session、Carrier 集合、Scheduler 和 Transport Key。

## Listen Port 规则

`listen_port` 继续由 Provisioning Profile 权威下发，不转为 Client 本地随机/自动配置。

Provisioning 在以下位置检查冲突：

1. 保存 parallel Bundle 时；
2. 修改被 parallel Bundle 引用的 Profile 时；
3. 删除被 Bundle 引用的 Profile 时会拒绝，必须先从 Bundle 移除。

Client 启动时再次检查选择结果，并在启动任何子 Profile 前预探测所有需要的 loopback TCP/UDP socket。任意端口不可用时整组启动失败，不保留半启动状态。

## Mac Client

Mac 仍只需要保存一条 secret Provisioning URL。0.10.0 自动识别：

- schema 1 单 Profile；
- schema 2 Bundle。

Bundle UI 会列出 Profile 名称、`127.0.0.1:<listen_port>`、Relay 数量和运行状态。single_select 使用单选；parallel 可以多选但至少保留一份。选择按非秘密 `bundle_id` 保存在本机，Provisioning URL 继续保存在 Keychain。

Bundle 的 Transport Key 不写入普通 UserDefaults；完整 Bundle 每次启动重新从权威 API 获取并仅在内存/engine stdin 中使用。API 获取失败不会用旧缓存偷偷启动。

并行启动由一个父 engine 编排多个独立子 runtime。父进程提供 `bundle_listening`、`bundle_stats`、`bundle_udp_stats` 聚合事件，同时保留每个 Profile 的状态标签。

## Linux Client

新增：

```text
validate-bundle [profile-id ...]
run-bundle [profile-id ...]
validate-managed
run-managed
```

`validate-bundle` / `run-bundle` 直接读取 Bundle JSON。`validate-managed` / `run-managed` 从 stdin 读取包含 secret URL 和可选 Profile IDs 的小型控制 JSON，再从 Provisioning 获取 schema 1/2 配置。这样 secret URL 不需要放进命令行参数或 `ps` 输出。

远程 URL 必须 HTTPS，loopback 开发允许 HTTP；不跟随 redirect。单 Profile 响应上限 64 KiB，Bundle 响应上限 512 KiB。

## Provisioning 管理后台

0.9.9 的 Profile 二级菜单、Relay Copy、自定义 API alias、管理员网页改密码全部保留。

0.10.0 另加 **Client Bundles**：

- 新建/编辑 Bundle；
- 选择包含哪些 Profile；
- single_select / parallel 模式；
- 实时显示 Profile 的 Listen Port 与冲突诊断；
- Bundle 自己的自动/自定义 alias API URL；
- 独立 Secret 轮换；
- Bundle 删除不删除 Profile。

Profile `/v1/config/...` URL 完全保留；Bundle 使用新的 `/v1/bundle/...` 路径。

## 兼容与迁移

0.10.0 Provisioning 可直接读取旧 0.9.8/0.9.9 `profiles.json`，无需迁移。原 Profile token/custom alias URL 继续有效。旧管理员 `admin-password` 文件继续优先于 bootstrap 环境变量。Bundle 数据第一次保存后写入独立 `bundles.json`。

MPX/4 Draft 04 数据面语义与 0.9.8 保持一致；正式发布/验证组合按 **0.10.0 Client + 0.10.0 Landing + 0.10.0 Provisioning**。0.9.5 及更早仍是 MPX/3，不兼容。

## 安全边界

- Profile URL 和 Bundle URL 都是 bearer credential；
- nginx/Caddy access log 应同时隐藏 `/v1/config/` 与 `/v1/bundle/`；
- Bundle alias 只是可读标识，高熵 Secret 始终保留；
- 修改 alias/mode 会自动轮换 Secret；手工轮换立即吊销旧 URL；
- Profile/Bundle 数据文件 `0600`，数据目录 `0700`；
- 管理员密码文件 `0600`，服务无需 root；
- Mac secret URL 在 Keychain；Bundle 选择只保存非秘密 Profile ID；
- Linux `run-managed` 的 URL 通过 stdin 输入而非 argv。

## 发布验证边界

0.10.0 是多 Profile 编排 feature release。发布必须有当前 Source-ID 的 Go test/vet/race、Provisioning test/vet、Swift 双架构 typecheck、Bundle API/engine tests、Linux amd64 原生运行验证以及冻结源码逐字节重建/DMG 验证。

由于本次不改变 MPX/4 Scheduler/传输语义，不把历史性能证据重新贴到新的 Source-ID 上，也不宣称新的容量/吞吐/WAN performance promotion。arm64 Linux 若没有物理机器，仅能声明交叉构建与冻结源码可复现，不能宣称物理 ARM runtime。

macOS DMG 仍为 ad-hoc 签名、未 Developer ID notarize；Intel 执行验证如在 Apple Silicon 上完成，则属于 Rosetta 而非物理 Intel。
