# 0.9.4 / MPX/3 Rev5

0.9.4 的发布重点是正式 Weighted：方向容量认证、下行必填/上行选填配置、故障保护不被固定容量覆盖，以及旧 Auto/Aggregate/Protect 回归。当前 Source-ID 必须通过 Go test/vet/race、Swift UI/Profile 检查和 Weighted 高 BDP 实验室回归后才能进入 weighted-release 包。

完整 30 秒容量矩阵和真实 App+Surge 180 秒现场仍是独立的更高层验收；未重跑时必须明确记录为 not-run-for-weighted-release，不能拿历史结果替代。资源上限仍是 2048 streams、128 MiB session credit、128 MiB DATA pending 和 128 MiB physical receive pages。

以下保留的是先前架构说明／历史资料；当前 0.9.4 Rev5 的精确验收以随包回执为准。

---

# MPTCP Desk 0.8.0 验证范围

> 本修订新增 Auto（默认）/Aggregate/Protect，并要求双端 MPX/3 调度能力 revision 1。旧的无模式协商 0.8.0 候选也不能混连。选择、保护阈值、方向性和验收边界见 `docs/userspace/SCHEDULER-MODES.md`（发行包内同名文档）；模式变更需先停止。正式候选需 SCHEDULER-MODES/CAPACITY/PROVENANCE 匹配新 Source-ID，实际 App/Surge 未验收时 RUNTIME 仍须 pending。

MPX/3显式WINDOW与零隐式OPEN信用，2048流、每流16KiB bootstrap、128MiB会话信用、128MiB sender DATA pending、128MiB物理接收页和独立control资源；原Native/Keychain/UI生命周期不变。

验收分层：全模块race/vet与协议/故障/资源回归；旧门槛高BDP20案短测；128/256/512/1024/2048个实际并发Stream各30秒两轮；实际App+Surge六Relay180秒分段混合。旧版30分钟少流下载、HTTP请求量或离屏SwiftUI渲染不能代替高并发及实机验收。

PROVENANCE验证冻结源码与二进制关系；CAPACITY和RUNTIME分别验证容量与实机，ACCEPTANCE.md记录当前交付状态。任何pending都意味着候选，不应声称完成。具体方法与限制见docs/userspace/VALIDATION.md。Rosetta不是实体Intel；没有公证、独立安全审计、前向保密或多日长稳承诺。原始MPX UDP能力与外部代理支持范围分开。
