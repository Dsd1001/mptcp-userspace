# MPTCP Desk for Windows

Windows Client 与 macOS MPTCP Desk 共用同一套 MPX/4 Userspace Engine，但 **Windows 只支持 `userspace_multipath`**：没有 Native/内核 MPTCP fallback，也不会在 UI、Profile 或运行时暴露该模式。

## 用途

Windows Desk 的定位与 Mac 版一致：在本机提供一个应用层转发入口，供其它代理/转发软件串联使用：

```text
Windows 上的代理 App
        |
        v
127.0.0.1:<listen_port>
        |
        v
MPTCP Desk for Windows
        |
        +--> Relay A --+
        +--> Relay B --+--> Landing --> Backend
        +--> Relay C --+
```

本地入口不是系统 VPN/TUN，也不安装网络驱动。上游代理软件只需要把目标流量交给配置好的 `127.0.0.1:<listen_port>`。

## 与 macOS 版对齐的功能

- MPX/4 Protocol Version 4 Stable；
- Auto / Aggregate / Protect / Weighted；
- TCP、Native UDP、UoT；
- 2–8 Relay；
- 本地 Profile；
- Provisioning Profile/Bundle 与 opaque `v/n/d` 加密响应；
- Last Known Good；
- parallel Bundle Profile 独立重连；
- 路径 RTT/Goodput/queue/outstanding/role 诊断；
- Stream/Window/Credit 资源诊断；
- 后台常驻与异常恢复；
- 系统托盘；
- 远程设备管理；
- 签名更新清单与应用内更新接口。

## Windows 特有实现

- GUI：Wails v2 + WebView2，保持接近 macOS Desk 的卡片式布局；
- Engine：独立 `mptcp-engine.exe` 子进程，继续使用共享 Go MPX Engine；
- Secret：Transport Key、Provisioning URL、LKG、Device Credential 使用 Windows DPAPI 加密后再落盘；
- 后台常驻：当前用户 `HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Run`；
- 安装：per-user NSIS，默认安装到 `%LOCALAPPDATA%\\Programs\\MPTCP Desk`，不要求管理员权限；
- 更新：安装器 SHA256 + 与 Mac Sparkle 通道相同的 Ed25519 公钥双校验。

## 构建

Windows PowerShell：

```powershell
.\\windows\\build.ps1
```

输出位于：

```text
windows/build/bin/MPTCP-Desk-Windows.exe
windows/build/bin/mptcp-engine.exe
windows/build/bin/MPTCP-Desk-<version>-Windows-Portable.zip
windows/build/bin/MPTCP-Desk-<version>-Windows-Setup.exe
```

GitHub Actions 的 `Windows Client` workflow 也会生成相同的测试安装器和 Portable ZIP。

正式发布时，将 CI 产出的 Setup EXE 放到 macOS 发布机后运行：

```sh
./scripts/build-windows-update.sh /path/to/MPTCP-Desk-<version>-Windows-Setup.exe
```

脚本使用现有 Sparkle `sign_update` 私钥对 Windows 安装器的**原始字节**生成 Ed25519 签名，并输出 `windows-update.json`。因此 macOS 和 Windows 更新通道使用同一枚既有公钥，但两个客户端仍各自读取自己的更新元数据。

## 与其它代理 App 配合

最重要的边界是：Windows Desk 本身不抢系统代理，也不改路由。它只监听 loopback。

例如 Profile 配置 `listen_port=1081` 后，其它代理软件把对应上游/转发目标设置为：

```text
127.0.0.1:1081
```

如果使用 Parallel Bundle，则每个 Profile 继续拥有自己独立的 `listen_port`。

## Native MPTCP

Windows 版本明确不支持：

```text
mode = native_mptcp
```

共享 Provisioning Bundle 中如果同时存在 macOS-only Native Profile 和 Userspace Profile，Windows GUI 会自动隐藏 Native Profile，只保留 Windows 可运行的 Userspace Profile。如果 Bundle 中完全没有 Userspace Profile，则同步直接报错。
