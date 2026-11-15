# 1.2.0 发布说明

此版为共享隧道产品升级，不兼容旧的独立 Profile/多组 MPX Bundle。代码包含已发布 1.1.6 的传输修复，继续使用 MPX/4 Stable Core，新增/统一产品层 routed TCP 与 routed UoT。

一个 Engine、一个 Session、一组 Carrier 承载多个业务入口；路由只在开流选择一次。客户端与出口提供路由公平、队列背压、实际接收内存保护、目标白名单、连接/拨号限额、断线恢复与明确错误范围。macOS、Windows、Linux CLI、隧道出口、配置下发和远程控制器均使用同一套新配置语义。

旧配置保留或备份但不会自动启动，控制器旧 API 返回 410。服务端不再公开固定 backend 产品模式和 Native UDP listener；UDP 使用共享 UoT。升级必须先建立共同出口和路由白名单，再升级匹配客户端与控制器。不得将普通业务流发往旧 MPX 端口。

正式发布必须附同一 Source-ID 的源码、二进制哈希、原生平台验证记录、TESTS.json、PROVENANCE.json 和 ACCEPTANCE.md。实验室聚合数据不是 WAN 提速承诺。发布操作不自动更新现网 Relay、Landing 或控制器；生产部署与公共出口选址是独立运维步骤。
