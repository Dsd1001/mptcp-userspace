# 0.9.6 / MPX/4 Draft 03

0.9.6 的 TCP Userspace 已升级到 MPX/4 Draft 03；Mac 与 Landing 必须双端 0.9.6。0.9.5 的后台常驻、登录自启、sleep/wake 与有界恢复逻辑全部保留。UDP 仍为独立 MPU/1 数据报平面。

以下保留历史架构资料。当前协议边界以 `docs/userspace/PROTOCOL.md` 与 0.9.6 release notes 为准。

# MPTCP Desk 0.8.0 / MPX/3

> 本修订新增 Auto（默认）/Aggregate/Protect，并要求双端 MPX/3 调度能力 revision 1。旧的无模式协商 0.8.0 候选也不能混连。选择、保护阈值、方向性和验收边界见 `docs/userspace/SCHEDULER-MODES.md`（发行包内同名文档）；模式变更需先停止。正式候选需 SCHEDULER-MODES/CAPACITY/PROVENANCE 匹配新 Source-ID，实际 App/Surge 未验收时 RUNTIME 仍须 pending。

Universal macOS 13+工程实验版，沿用窗口/菜单栏生命周期、Relay编辑、配置导入保存、钥匙串和Native模式。Userspace与0.7.x MPX/2不兼容，Mac和Landing必须配对升级/回退；Relay端口、原传输密钥及backend模型不改。

本地127.0.0.1:1081是透明入口，不是SOCKS5；使用原Surge/SS/AnyTLS等代理配置，原账号密码不改变。当前版本支持2048个轻量逻辑流，OPEN不等待receive credit，DATA以显式WINDOW背压。每流16KiB bootstrap，总会话信用128MiB，其中32MiB保护基础、96MiB供增长；sender DATA pending为128MiB，物理接收页独立限制128MiB。

正常退出旧App后从DMG替换Applications中的应用，处理正常系统及钥匙串提示后启动。不要关闭SIP/Gatekeeper或导出钥匙串。只有ad-hoc签名，未公证。关闭主窗口不停止转发，菜单栏可打开窗口/停止/退出；0.9.5 可选后台常驻/登录自启和唤醒恢复，不提供自动 Native 降级。

诊断页显示入口TCP、MPX占槽/2048、base/growth信用、DATA/control pending、窗口阻塞writer和生命周期；非敏感最新快照在用户Library/Logs/MPTCPDesk/latest-transport.json，owner-only，不含key/password/profile。Native仍使用原配置/内核路径，切换需同时选择对应旧Relay入口。

安装部署看docs/userspace/DEPLOYMENT.zh-CN.md，线协议看PROTOCOL.md，信用策略看MPX3-CREDIT.md。最终CAPACITY要求128/256/512/1024/2048流各30秒两轮，RUNTIME要求真实App/Surge三分钟分段混合；候选/构建通过不能代替它们。精确验收状态见同包ACCEPTANCE.md和两个回执，不宣称多日稳定、任意公网速度或实体Intel验证。
