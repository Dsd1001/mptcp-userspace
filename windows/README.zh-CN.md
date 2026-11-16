# MPTCP Tunnel for Windows 1.2.3

使用 Setup.exe 安装 GUI 与匹配的 mptcp-engine.exe，或解压 Portable.zip；不要混用不同 Source-ID 的 Engine。安装目录和更新身份保留 MPTCP Desk，以便沿用既有配置保护与升级路径。

只支持共享隧道：本地编辑一组 Relay 和多个业务入口，或同步控制器 schema 4 tunnel_profile 并选择入口。所有入口共用同一个 Engine/Session。UDP 经 UoT，无独立 Native UDP Relay 连接。

旧的 profile 配置首次迁移先保存为 settings.json.pre-1.2.backup；备份不可用时拒绝覆盖。旧配置不会自动运行、不会合并旧 Transport Key。新配置中的 Transport Key 与远端 URL 使用当前用户 DPAPI 加密；GUI 状态回传不包含明文密钥。

远程管理只在本机主动启用与配对后生效，只接受共享隧道分配；旧/过期控制不启动缓存配置。支持后台恢复、托盘和签名更新。安装包是否有 Authenticode 签名以发行清单为准；独立 Ed25519 更新签名不等同于 Authenticode。
