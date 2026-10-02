# 0.9.8 / MPX/4 Draft 04 + Provisioning

0.9.8 发布候选至少必须通过：

- engine / Landing 全量 `go test ./...` 与 `go vet ./...`；
- MPX/4 VarInt / Frame / key-schedule / Secure-Record 字节级向量；
- Draft 04 `carrier-generation.json` 与 `error-scope.json` 官方语义向量；
- Carrier Generation：first=0、stale/equal reject、failed candidate no-commit、higher commit、SUPERSEDED、simultaneous equal、maximum no-wrap；
- Error Scope：STREAM_OPEN_REJECT、Carrier-scoped FRAME_ENCODING/AUTH、Session-scoped FLOW_CONTROL/FINAL_SIZE/TRANSMISSION_ID/STREAM_STATE；
- CARRIER_CLOSE / SESSION_CLOSE body 与 UTF-8/256-byte reason 限制；
- Transmission ACK future/stale/Stream-ID mismatch；
- 多 Carrier、loss/rejoin、retransmission/reinjection、scheduler negotiation；
- Provisioning 服务 `go test` / `go vet`；
- Provisioning URL HTTPS/localhost policy、redirect rejection、64 KiB response bound、完整 Profile 校验与 key-free preferences；
- arm64 / x86_64 Swift 编译与 managed-mode UI harness；
- Universal DMG、Linux amd64 Landing、Linux amd64 `mpx-provision`；
- 所有发布组件使用同一个 Source-ID。

macOS DMG 为 ad-hoc 签名、未 notarize。
