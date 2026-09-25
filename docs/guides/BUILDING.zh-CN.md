# 从源码构建

[English](BUILDING.md)

如果要复现正式 v0.9.4，先切到发布 Tag：

~~~sh
git checkout v0.9.4
~~~

v0.9.4 冻结 Source-ID：

~~~text
d4b8f362a8179257f2889abfea587c480755470bac6a78d8c023c3496c66f2bf
~~~

可以用以下命令核对：

~~~sh
python3 scripts/source-manifest.py --id
~~~

## 工具链

Go module 声明：

~~~text
go 1.23.0
~~~

完整 macOS DMG 构建还需要 Apple 命令行工具，包括：

- xcrun / macOS SDK
- swiftc
- lipo
- iconutil
- codesign
- hdiutil

macOS 目标版本为 13+，最终 App 同时包含 arm64 和 x86_64。

## 构建 Linux Landing

~~~sh
./scripts/build-userspace-landing.sh
~~~

如果系统默认 Go 不是需要的版本，可以显式指定：

~~~sh
MPTCP_GO=/path/to/go ./scripts/build-userspace-landing.sh
~~~

输出位于：

~~~text
dist/userspace-<version>/
~~~

主要文件：

- mptcp-landing
- mptcp-landing.sha256
- mptcp-landing.BUILDINFO

Landing 使用：

- CGO_ENABLED=0
- GOOS=linux
- GOARCH=amd64

因此得到 Linux amd64 静态二进制。

## 构建 macOS DMG

在具备 Apple 工具链的 macOS 上：

~~~sh
./macos/build.sh
~~~

脚本会：

1. 分别构建 arm64 / x86_64 Swift UI；
2. 分别构建 arm64 / x86_64 Go Engine；
3. 使用 lipo 合并 Universal binary；
4. 生成 App Icon；
5. 写入 Source-ID；
6. ad-hoc codesign；
7. 校验签名和双架构；
8. 生成 DMG；
9. 生成 SHA256 与 BUILDINFO。

主要输出：

- MPTCP-Desk-<version>-universal.dmg
- MPTCP-Desk-<version>-SHA256SUMS
- MPTCP-Desk.BUILDINFO

当前 DMG 为 ad-hoc 签名，未 notarize。

## Source-ID 与文档提交

正式 Release 的 Source-ID 来自受控源码清单。

构建脚本会在构建前后重新计算 Source-ID，避免构建过程中源码漂移。

GitHub 上的 v0.9.4 Tag 是正式 Release 二进制对应的权威源码快照。

为了允许继续改进公开说明而不改变正式发布源码身份：

- 顶层 README；
- docs/README；
- docs/guides/

这些使用说明不进入 release source manifest。

因此 main 可以继续完善使用文档，而 v0.9.4 的 Source-ID 仍保持不变。

## 测试与 Release Gate

“成功编译”不等于“完整验收通过”。

仓库包含 release/scheduler gate 脚本，但公开 v0.9.4 Release 中的验收文件才是该版本的权威记录。

尤其不要因为本地 build 成功，就宣称已经完成：

- 完整 30 秒 capacity matrix；
- 真实 App+Surge 180 秒现场验收；
- 任意公网速度保证；
- 多日稳定性；
- 独立安全审计。

详见：

- [验证边界](../userspace/VALIDATION.md)
- [0.9.4 Release Notes](../userspace/RELEASE.zh-CN.md)
