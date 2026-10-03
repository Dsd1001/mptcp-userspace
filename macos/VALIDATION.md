# 0.10.3 / MPX/4 Draft 04

0.10.0 发布候选至少必须通过：

- engine / Landing 全量 `go test ./...`、`go vet ./...` 与 race suite；
- MPX/4 VarInt / Frame / key-schedule / Secure-Record、Draft 04 Carrier Generation / Error Scope 既有回归；
- Provisioning `go test` / `go vet`；
- Bundle CRUD、secret rotate、schema-2 public API、旧 schema-1 Profile URL 兼容；
- `single_select` 同端口允许；`parallel` 重复端口拒绝；
- Profile 后续修改不能破坏已存在的 parallel Bundle；被 Bundle 引用的 Profile 不能直接删除；
- Client Bundle strict JSON、Profile selection、duplicate/unknown ID、端口冲突与端口预探测；
- Linux `validate-managed` 的 HTTPS/loopback、redirect rejection、response size 和 Profile/Bundle schema 检查；
- arm64 / x86_64 Swift typecheck 与 Universal DMG 构建；
- Mac Bundle UI 可显示并选择多 Profile；
- Linux Client amd64/arm64 静态 ELF；
- Linux Landing amd64/arm64 静态 ELF；
- Linux Provisioning amd64/arm64 静态 ELF；
- Linux amd64 原生执行 `version`、Bundle validate/managed fetch，以及多 Profile runtime/端口冲突验证；
- 冻结源码重新构建 Linux Client/Landing/Provisioning 与 Mac engine/UI 后匹配发布产物；
- 所有整套发布组件绑定同一个 Source-ID。

0.10.0 不以新的 Source-ID 宣称重新完成 Scheduler/capacity/WAN performance promotion；本次的发布门槛是当前源码的功能正确性、运行验证和构建可复现性。

macOS DMG 为 ad-hoc 签名、未 notarize。若无物理 Linux arm64/Intel Mac，则必须明确保留对应验证限制。
