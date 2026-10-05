# 从源码构建

## 工具链

发布构建使用：

- Go：Engine、Linux Client、Landing、Provisioning；
- Swift / Xcode Command Line Tools：macOS UI；
- Sparkle 2.10.0，并固定 SHA-256；
- macOS 自带 codesign、hdiutil 等工具。

如果所需 Go 不是默认 `go`，使用 `MPTCP_GO` 指定。

## Linux Client

~~~sh
./scripts/build-linux-client.sh
~~~

## Linux Landing

~~~sh
./scripts/build-userspace-landing.sh
~~~

## Provisioning

~~~sh
./scripts/build-provisioning.sh
~~~

## 构建 macOS DMG

正式 App 内更新版本默认使用固定的长期本地签名身份：

~~~sh
./macos/build.sh
~~~

固定身份名称为 `MPTCP Desk Stable Local Code Signing`。其**公开证书**固定保存在
`macos/signing/MPTCP-Desk-Stable-Local-Code-Signing.crt`；**私钥只保存在发布机器的登录钥匙串中，
绝不能提交到仓库**。构建脚本会核对钥匙串中的证书指纹与仓库 pin 是否一致，并把最终
Designated Requirement 写入 `MPTCP-Desk.BUILDINFO`。

仅用于开发的 ad-hoc 构建要显式指定：

~~~sh
MPTCP_CODESIGN_IDENTITY=- ./macos/build.sh
~~~

如果以后使用 Developer ID，则显式切换：

~~~sh
MPTCP_CODESIGN_STYLE=developer-id \
MPTCP_CODESIGN_IDENTITY='Developer ID Application: ...' \
./macos/build.sh
~~~

Developer ID 模式会开启 hardened runtime 和 timestamp。只有该模式允许再配置
`MPTCP_NOTARY_PROFILE` 做 Apple notarization。

主要输出：

- MPTCP-Desk-<version>-universal.dmg
- MPTCP-Desk-<version>-SHA256SUMS
- MPTCP-Desk.BUILDINFO

### 为什么要使用稳定的自签名身份

MPTCP Desk 当前有三类独立的 Keychain 凭据：

- MPX Transport Key；
- Provisioning URL；
- Remote Management credential。

ad-hoc 签名的 Designated Requirement 绑定程序内容哈希；每次重新构建以后哈希变化，Keychain
可能把新版 App 视为新的访问者，于是三个条目分别要求授权，这正好对应更新后连续出现多次密码提示。

长期保存的自签名代码签名证书提供稳定的证书锚点、Bundle ID 与 Designated Requirement。
按照 Apple 的代码签名/Keychain ACL 规则，这一“身份连续性”并不要求 Developer ID。

从已有 ad-hoc 版本切换到稳定签名版本时属于一次性迁移：旧 Keychain 项目仍可能要求重新授权。
完成第一次授权后，必须连续构建两个不同版本、使用**同一张证书**签名并完成一次真实更新测试。
第二次更新才是关键回归：三个 Keychain 项目不应再次请求授权。

不能为了消除提示而给所有应用开放 Keychain、修改为明文保存或降低凭据保护。

稳定自签证书只解决本机代码身份连续性；它不等于 Apple notarization，也不会自动获得
`/Applications` 的写权限。Sparkle 的 EdDSA 更新签名仍然独立负责更新包真实性。

### 私钥保管

这张私钥就是后续版本的连续身份。必须导出一份带强密码的 PKCS#12 离线备份，不能随普通版本
轮换证书。私钥丢失或换证书都会造成下一次 Keychain 身份迁移。

## Source-ID

构建脚本会在构建前后计算 Source-ID，防止构建过程中源码漂移。固定的**公开**签名证书进入
source manifest；私钥不进入。

每个正式版本对应的 Git tag 是该 Release 的权威源码快照。顶层 README 与 docs/guides 下的
使用说明不进入 release source manifest。

## 测试与 Release Gate

“成功编译”不等于“完整验收通过”。正式发布还必须通过
[验证边界](../userspace/VALIDATION.md) 中的 gates，并发布与 Source-ID 匹配的证据。
### 冻结 Keychain Broker

0.10.11 起，稳定自签名 App 的 Designated Requirement 仍用于验证主 App，但三个 file-based Keychain 条目不再由主 App 直接访问。原因是现代 macOS 还会维护独立的 `partition_id`，其中包含访问进程的 cdhash；即使主 App 的 Designated Requirement 不变，每次重建仍会产生新的 cdhash。

因此发布包固定携带 `macos/keychain-broker/MPTCPKeychainBroker.v1.b64`。它是已经签名的 Universal Broker 二进制的 Base64 表示；构建脚本只把这些字节作为资源复制进 App，绝不能重新编译或重新签名后仍声称是 v1。主 App 会核对 SHA-256 `5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9`，解码安装一次后长期复用。

如果未来必须修改 Broker，必须升为新的 Broker protocol/version，并按一次新的 Keychain 身份迁移处理，不能覆盖 v1。
