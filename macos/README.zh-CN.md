# MPTCP Tunnel for macOS 1.2.2

安装发行 DMG 中的 MPTCP Desk.app；应用身份、钥匙串 Broker 与更新通道保持连续，界面名称为 MPTCP Tunnel。界面包括共享隧道、运行状态、远程管理、日志。

本地配置只设置一组 Relay、一个共同出口 Transport Key 和多个业务入口。每个入口可启用 TCP/UoT，路由编号与服务端白名单一致；入口监听 127.0.0.1。远端模式只接受控制器 API schema 4 的 tunnel_profile，可选择其中的业务入口，但不会为每入口建立 Session。

旧 Profile/Bundle 偏好保留但停止自动运行，不会猜测共同出口或合并旧密钥。先按 tunnel.example.json 建立新配置，再启用后台恢复。密钥与完整配置 URL 保存在钥匙串，缓存保留原始加密响应并在读取时重新验证。

关闭一个入口/改变路由配置需要停止并重连当前共享隧道；运行中同步只缓存新配置，不把旧业务流热切到另一个目标。远程管理须在本机主动启用和配对；支持同步、启动停止、重启与签名更新，无任意 Shell。

发行签名类型以 BUILDINFO 为准：稳定本地签名不等于 Apple Developer ID 公证；不应把本地签名描述为已公证。冻结 Keychain Broker v1 不重建或替换身份。
