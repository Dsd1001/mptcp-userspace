# 构建 MPTCP Userspace v1.1.1

本文说明当前 v1.1.1 整套组件的源码构建。**构建过程不会修改生产机的网络参数、systemd 服务、防火墙或拥塞控制。**

## 版本与协议身份

```text
产品版本：       1.1.1
Wire Protocol： MPX/4 Protocol Version 4 Stable
协议发布：       protocol-v4.0.0
协议源码：       44f587fd279ed2238b070dd68114c76822353f4d
Capability Rev：8
```

整套发布时 `macos/VERSION` 和 `provisioning/VERSION` 必须一致。二进制通过冻结 source manifest 写入 Source-ID。

## 工具链

- Go：Userspace Engine、Linux Client、Landing、Provisioning；
- Swift / Xcode Command Line Tools：macOS MPTCP Desk；
- Go 1.25 + Wails v2 + WebView2 + NSIS：Windows MPTCP Desk；
- Python 3：source manifest、打包与验证；
- macOS 原生工具：`codesign`、`hdiutil`、`lipo`、`plutil`。

Go 优先使用 `MPTCP_GO`；否则尝试 `macos/build/go-path`，最后才使用 PATH 中的 `go`。

## Linux Client

```sh
./scripts/build-linux-client.sh
```

生成静态 `CGO_ENABLED=0` 的 amd64/arm64 二进制：

```text
mptcp-client-linux-amd64
mptcp-client-linux-arm64
```

输出位于 `dist/userspace-1.1.1/`，并附带 SHA256 与 BUILDINFO。

## Landing

```sh
./scripts/build-userspace-landing.sh
```

生成：

```text
mptcp-landing
mptcp-landing-linux-arm64
```

这个脚本**只构建**，不会安装服务，也不会调整内核、proxy、防火墙或 congestion control。

## Provisioning

```sh
./scripts/build-provisioning.sh
```

整套发布模式下生成：

```text
mpx-provision
mpx-provision-linux-arm64
```

默认 suite scope 要求 Provisioning 版本与主套件版本一致。

## Windows MPTCP Desk

Windows 与 macOS 共用同一套 MPX/4 Userspace Engine，但明确不提供 Native/内核 MPTCP fallback。请在 Windows PowerShell 中构建：

```powershell
.\windows\build.ps1
```

脚本会在 `windows/build/bin/` 生成当前用户级 NSIS 安装器和 Portable ZIP。Windows 的代理串联方式与实现说明见 `windows/README.zh-CN.md`。

## MPTCP Desk

```sh
./macos/build.sh
```

默认使用长期固定的本地签名身份 `MPTCP Desk Stable Local Code Signing`，并在构建 App 前校验冻结的 `MPTCPKeychainBroker` v1。

Broker 固定 SHA256：

```text
5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9
```

默认输出 Universal arm64+x86_64 DMG 到 `dist/userspace-1.1.1/`。

只有显式提供 Developer ID 与 notarization profile 时才走 Developer-ID/notarized 构建。长期本地自签名与 Apple notarization 是两个不同概念。

## 打包与发布验证脚本

```text
scripts/package-userspace.py
scripts/build-appcast.sh
scripts/verify-userspace.py
scripts/release-gates.py
scripts/scheduler-gates.py
```

正式发布应从同一个 frozen Source-ID 生成整套产物，不要把不同工作树/不同提交构建出来的文件拼在一个 Release 里。

## 基础源码回归

Engine：

```sh
cd macos/engine
go test ./... -count=1
go vet ./...
go test -race ./multipath -count=1
```

Provisioning：

```sh
cd provisioning
go test ./... -count=1
go vet ./...
```

正式 Release 还需要 package/UI/provenance 等检查，见 [VALIDATION.md](../userspace/VALIDATION.md)。

## 拥塞控制属于部署，不属于构建

生产推荐：

- **Landing = CUBIC**；
- **Relay = BBR**，优先 `fq`。

构建脚本不会自动改这些 sysctl。部署时按 [网络与拥塞控制调优](NETWORK-TUNING.zh-CN.md) 单独设置、验证和回滚。
