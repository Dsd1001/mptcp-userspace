# 0.9.4 / MPX/3 Rev5 scheduler compatibility

Auto / Aggregate / Protect behavior is retained and Weighted is added as the fourth configured policy. Rev5 adds authenticated scheduler byte 0x44 plus per-carrier directional capacity fields; 0x41/0x42/0x43 and their zero capacity bytes remain unchanged for compatibility with 0.9.3. Actual-data credit safety and the 2048-stream scale apply independently of all four policies. Use the source-matched SCHEDULER-MODES.json for release evidence.

---

# 0.8.0 / MPX/3 调度策略

## 用户选择

Userspace 配置区提供 Auto、Aggregate、Protect、Weighted 四个选项，默认 Auto。Weighted 用于已知每条 Relay 带宽能力的固定线路组。运行或启动/停止处理中不能手动改配置；停止转发后可修改并保存。选择写入普通 Profile 的可选 `scheduler_mode` 字段，合法小写值为 `auto`、`aggregate`、`protect`、`weighted`。缺失字段的旧 Profile 默认 Auto；Userspace 拒绝显式空串、大小写变体和未知字符串。Native 模式忽略该字段，界面隐藏 Userspace 调度选项。旧 Native 配置不会自动变成 Userspace。

Auto 是策略选择器：同质路径使用 Aggregate，稳定异质或交付故障时使用 Protect，并按滞回条件恢复。它不是第三套 DATA 发送算法。Aggregate 强制保留原高吞吐选择方法，适合质量接近的线路。Protect 对异常路径分级、限制慢路积压，健康 ACTIVE 集合仍使用同一个 Aggregate 选择方法。这是 Userspace TCP DATA 策略，不是 Native 的 macOS 系统聚合开关；不会修改系统 socket buffer、内核参数、Relay 或代理账号。

原 UDP 数据报调度/格式独立，Weighted 也不改变 UDP。任何 TCP 模式都保留 MPX/3 显式 WINDOW；Rev5 上限仍为 2048 流、每流 16 KiB bootstrap、128 MiB 会话信用、128 MiB sender DATA pending、128 MiB 物理接收页以及独立 control 硬上限。

## 双向认证与兼容

用户指定的 configured mode 通过每条 carrier 的认证 hello 传给 Landing，创建时绑定整个会话，重连时必须一致。Mac 与 Landing 的两个发送方向采用同一个 configured mode。Weighted 额外认证每条 Relay 的下行/上行容量：Landing→Mac 使用必填下行，Mac→Landing 使用选填上行；上行留空时只有该方向回退原在线估速。Auto 的 effective mode 和 path role 仍根据各自方向独立确定。

48 字节 MPX/3 hello 的第 7 字节（从零计数）在 Rev5 使用：`0x41` Auto、`0x42` Aggregate、`0x43` Protect、`0x44` Weighted。Weighted 时字节 40..41 / 42..43 分别是下行/上行容量，单位 0.1 Mbps；前三种模式这四字节继续为零。整个 hello 属于 client-proof/server-proof 的 HMAC transcript。非法能力或非法 Weighted 容量在会话分配前拒绝。

**Weighted 必须 Mac 与 Landing 都是 0.9.4。** 0.9.4 的 Auto/Aggregate/Protect 保留 0.9.3 的 0x41/0x42/0x43 hello，因此这三种模式可与 0.9.3 对接；0x44 Weighted 对旧端会 fail closed。0.9.0 Rev2、Rev3 候选、MPX/1、MPX/2 仍不兼容。

## 路径角色与滞回

LEARNING 表示尚无足够实测证据。effective=Protect 时，新路径最多持有四个最大 DATA 的账目成本，即 131328 字节。Auto 仍处于 Aggregate 时与强制 Aggregate 一样保留原 startup/flight 上限，不提前套用 Protect 的 learning cap，避免人为限制供给后又把低利用率误判成低容量。带宽质量分类要求至少三个真实 receiver-clock delivery epoch，不将 PING、同一 epoch 的重复读取或尚未被真实样本替换的 4 MiB/s 启动先验当作已测容量。原 Aggregate 的容量滤波器和速率测量不改变，分类仅读取其中已实际测量的有效项。

相对于当前有足够样本、连接正常且未受 penalty 的参考路径：速率不低于最佳的 50%，且 base RTT 不超过最低的两倍，可保持 ACTIVE。速率低于最佳的 15% 或 base RTT 超过最低的三倍，属于 BACKUP 候选；其余不满足 ACTIVE 的属于 PROBE 候选。LEARNING 的首组三个有效交付 epoch 满足分类条件时立即完成首次分类；已经 ACTIVE 的路径后续普通降级仍需要连续三个新的有效交付 epoch 不满足 ACTIVE，不能靠时间轮询伪造三个样本。若一条高时延 LEARNING 路径因少被选择而带宽 epoch 不足，至少三个本路径首次发送 DATA 的有效 ACK 可提供独立 RTT 证据：只允许按原 2×/3× RTT 阈值保守降为 PROBE/BACKUP，不能用这些稀疏 ACK 推断带宽或晋级 ACTIVE。

对已经 ACTIVE 的路径，仅凭低速率降级前，还需确认当前 flight 预算足以在实际 ACK 反馈 RTT 内提供最佳路径 50% 速率所需的在途量；不足时保留 ACTIVE、清除连续坏样本计数，不把自限速后的利用率当成物理容量。此规则只是不足证据时暂缓速率降级，不提高发送预算、不捏造更高速率，也不放宽首次 LEARNING 分类或 PROBE/BACKUP 晋级。RTT 超过 2×/3×、stale 和 delivery timeout 的独立保护保持有效。它可能延后同 RTT 慢路径的速率降级，不能等同于已证明该路径健康或容量充足。

PROBE 每秒最多一个不超过 32768 字节 payload 的 DATA frame；对应 MPX 账目成本最多 32832 字节。旧未确认 DATA 债务不归零，不能再追加。BACKUP 默认不分配业务 DATA，继续原 PING/PONG 和连接检查；进入角色至少五秒后，才允许每五秒一次同样有界的资格探测。socket Write 成功不视为交付；角色切换之前已经分配的债务不可撤回，通过原 ACK、reset 或 reinject 自然处理。

已有已发送 DATA 债务且超过 `max(500 ms, 4 × base RTT)` 没有交付推进，或原 delivery timeout，可立即进入 BACKUP。这个无进展计时从当前 flight 的首个 DATA 交给 writer 时开始，或从该路径最近有效 DATA ACK 更新，以较新者为准；上一轮没有待确认 DATA 的空闲时间不计入下一轮超时。继续向同一未确认 flight 追加 DATA 不延后超时；socket Write 成功也不是 ACK。属于当前拥有路径的重传 DATA ACK 可以证明存活，但不参与容量/RTT 学习或探测晋级。晋级只接受本路径、匹配探测 ID、首次发送的真实 DATA ACK，晚到的跨路径重传 ACK不能用作晋级证明。BACKUP 恢复先进入 PROBE；连续三个有效探测满足 ACTIVE 条件且持续健康至少两秒才晋级 ACTIVE。只有 PING 恢复或缺少可靠速率证据时仍保持保守角色，不将稀疏探测的利用率冒充链路容量。

Auto 返回 Aggregate 需所有当前稳定路径均 ACTIVE，所有路径共同推进五个新的有效 epoch，并连续健康至少两秒、最近两秒没有 stale/timeout。强制 Aggregate/Protect 的 effective mode 不自动改变。切换不关闭已有逻辑流或重建 carrier；配置更改仍需用户停止后重新启动会话。

## 未绑定载路的控制帧

WINDOW、OPEN_OK 等未绑定载路的控制消息，不按 map 的随机遍历顺序分配。优先选择健康、控制队列有空位的路径；effective=Protect 时优先 ACTIVE 集合，再按基础 RTT、待确认债务及交付速率估计的控制到达成本选路。同成本按路径编号确定顺序。已有 DATA flight 达到上限不禁止发送控制消息，控制队列本身仍有硬上限。

明确绑定路径的 ACK、PING、PONG 在原路径可用时保持原路径，避免污染交付速率归属和健康探测。如果所有优先控制队列已满，允许在其他连接正常的路径上有界回退；全部队列都满时维持原丢弃计数和重生成机制。此选择不改变 DATA 的 Aggregate 算法、接收信用、协议帧或跨路重发语义。

## 诊断

两端 TCP stats 保留旧字段，并增加 `configured_scheduler_mode`、`effective_scheduler_mode`、`mode_switches`、`last_mode_reason`。路径增加 `role`、`role_reason`、`measured_delivery_bps`、`valid_delivery_samples` 和探测次数/字节/债务峰值。`goodput_bps` 仍是原 Aggregate 容量估计，可能含启动先验；不要与仅由实测项构成的 `measured_delivery_bps` 混淆。

Mac 路径诊断页显示配置策略、本端当前策略、切换原因和各路径角色。事件进入现有最多 64 项 lifecycle 环形记录，不包含传输密钥或后端凭据。普通偏好只保存模式等非秘密配置，transport key 仍只存 Keychain，进入引擎时通过 stdin 传递。

## 验收记录

功能测试、实验室性能、容量、构建来源和实际 App/Surge 现场是独立证据。0.9.4 `SCHEDULER-MODES.json` 必须绑定当前 Source-ID，至少记录四模式配置/协议/UI、Weighted 方向容量认证、上行留空回退、timeout/penalty 保护、旧三模式回归和 Weighted 高 BDP 回归。完整 2048/容量矩阵和实际 App+Surge 仍是独立、更高层级的验收。

300 Mbps 档 30/50 ms 门槛仍为 240 Mbps；500 Mbps 档仍为 300 Mbps。100 ms 实测值必须保留，不把无固定门槛解释成可以忽略退化。原始随机 64 MiB、16 MiB 暖机、完整 SHA/FIN 检查和计时范围不变，不挑最好样本、不用扩大资源或改系统设置制造通过。

只有全部自动验收通过才生成新的发行候选。实际 App+Surge 180 秒混合和独立原站/受保护服务审计未完成时，`RUNTIME.json` 必须仍是 pending，不把离屏 UI、测试子进程或实验室流量当作生产实机验收。精确结果以 Source-ID 匹配的回执和 HANDOFF 为准，本设计文档本身不证明任何性能门已经通过。
