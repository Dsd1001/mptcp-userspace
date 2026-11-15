# MPTCP Tunnel 1.2.2 发布说明

## 更新重点

此版在 1.2.1 共享隧道架构之上修复 Weighted 多连接场景的 Herd Guard 路径选择：
达到 64 个 Session Stream 时，旧代码可能在已有可发送 Carrier 的情况下因
25ms Cost Deferral 暂缓 DATA 分发。1.2.2 的 Routed Weighted 保持
work-conserving（有可用路径立即分发），非共享模式继续保留原有 Herd Guard 安全保护。

其他组件与 1.2.1 同步版本号：macOS Universal Desk、Windows 安装器/Portable、
Linux 客户端、共享隧道出口和独立远端控制器。配置格式、MPX/4 v4 协议与
1.2.1 相同，**无须重新生成或更换 Transport Key**；HKT 与 iCable 仍作为 HKBN
统一出口后的普通业务目标，不启动独立 MPX/4 共享隧道出口。

## 质量与部署

- 新增 64-Stream 工作保守回归，保留 1.1.x 非共享 Herd Guard、路由 DRR 与
  流控上限回归；跨平台原生 CI 和产物 Source-ID 验收必须通过后才可公开。
- 实验室流量和 macOS WAN 实测分开报告；94 Mbps × 6 仅为六条 Carrier 的
  调度容量提示，不保证物理 564 Mbps。
- 更新 macOS 与 HKBN 出口应分阶段执行：先保持旧配置的原始备份及可回滚
  二进制，再部署服务端，观察六路重连；最后更新 Mac 客户端并复测；
  其他 Landing、控制器仅同步可执行文件，不更改管理凭据、数据和 Relay 转发规则。
- 公共 Release 不自动执行生产变更。Windows 终端必须通过用户本机安装器、
  远程设备管理流程或受管更新进行升级，不能凭发布文件声称已升级所有在线设备。
- macOS 使用原 stable-local 自签代码签名，非 Apple Developer ID 公证。
