# 0.9.7 / MPX/4 Draft 03 + Provisioning

0.9.7 发布候选至少需要通过：

- Go engine / Landing 全量 `go test ./...` 与 `go vet ./...`；
- MPX/4 Draft 03 既有 VarInt / Frame / key-schedule / Secure-Record 回归；
- Provisioning 服务 `go test` / `go vet`；
- Provisioning URL HTTPS/localhost policy、redirect rejection、64 KiB response bound；
- 完整远程 Profile 的 Userspace/Native 校验与 key-free UserDefaults 持久化；
- 管理网页创建/修改/删除配置及 API URL rotation；
- 公共随机 API URL 到 Swift 客户端的真实 HTTP 联调；
- arm64 / x86_64 Swift 编译与 managed-mode UI harness；
- Universal DMG 与 Linux amd64 `mpx-provision` 静态二进制构建。

Provisioning 不改变 MPX/4 Draft 03 wire bytes，0.9.7 Mac 与 0.9.6 Landing 保持线协议兼容。macOS DMG 仍为 ad-hoc 签名、未 notarize。
