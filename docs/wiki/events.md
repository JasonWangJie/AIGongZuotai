# 持久通知与断线恢复

V09实现中的项目通知链路，当前契约见[v09-contract](../planning/v09-contract.md)，证据见[031台账](../changes/2026-11-13-031-durable-events.md)。仍运行在单控制写者及私有目录锁下；持久通知不等于完整分布式服务。

## 同事务写入

008新增项目head、Outbox、feed、消费者checkpoint及有界计数表。创建run/task、任务状态更新、run生命周期/每秒检查点，与对应Outbox记录在同一PG事务。既有审计插入同时追加audit.created，因此项目创建、草稿保存、参考上传及资产元数据/回收恢复也有通知；审计仍是独立业务记录，不变成消费账单。

Outbox是闭集元数据：资源类型/ID、版本、状态和审计动作。不保存提示词、LLM输出、文件正文、名称标签、凭据、上游handle或存储位置。LLM token delta明确排除，原run专用SSE仍用内存环与持久检查点，重启中断不能续接上游文本流。

事务内项目head行锁分配project_seq，回滚同时撤销递增；同项目提交有序，跨项目不承诺全局业务顺序。内部queue_order仅帮助选取待消费记录，不能作为已提交水位。[PostgreSQL sequence](https://www.postgresql.org/docs/current/functions-sequence.html)不具备事务回滚的无缝序号语义，因此公开续播不依赖bigserial。

Task.Version、Run.Seq、项目事件seq分别表示资源版本、run文本/终态位置和持久通知位置，不能互换。幂等重放优先返回原资源，不新增受理审计或Outbox；重复同资源状态/版本写入使用事件key去重。

## 消费、重复和提交不明

一个消费者每250ms取最多100条未投影记录，在同一事务内插入唯一event_id的feed、推进该项目checkpoint、标记已投影并减少pending。项目序号不连续时拒绝推进。崩溃后重新消费会由唯一键与checkpoint去重，不产生第二份持久效果。

checkpoint表示“已进入持久feed”，不是浏览器收到、展示或确认。内存唤醒channel只有一个槽，用来减少等待；丢失唤醒也能由SSE每秒查询兜底。进程在数据库提交后、广播前退出不会丢失已提交通知。

Commit失败时不能假设未提交。[pgx事务API](https://pkg.go.dev/github.com/jackc/pgx/v5#Tx)返回的连接错误不会证明服务端未完成commit。当前写连接异常沿既有failclosed路径停止执行；重启先投影旧Outbox，再恢复业务状态。客户端保留原请求/幂等键，不能自动换键重发模型。

## 有界行为

| 范围 | 当前策略 |
| --- | --- |
| 全球未消费 | 最多10000条，单计数行在生产/消费事务内更新，不逐请求全表COUNT |
| 容量耗尽 | 503 EVENT_BACKLOG_FULL，业务/审计/幂等/事件一起回滚；不将健康DB永久熔断，消费者可以继续排空 |
| 每批消费/读取 | 最多100条 |
| 项目保留 | 最新2048条已消费通知 |
| 全局保留 | 最多100000条已消费通知，裁剪同步推进相应项目floor |
| 未消费记录 | 不允许通过保留策略删除 |
| SSE | 沿用工作区/全局连接上限、5秒写deadline、会话心跳核验 |

裁剪后旧通知不能无限回放，必须重新拉取资源快照。单计数行和当前单连接事务仍有串行部分；这些是可验证的容量上界，不是生产吞吐成绩。当前不引入外部消息代理、跨主机fencing或浏览器持久ACK。

## HTTP与客户端规则

沿用`GET /api/v1/workspaces/{workspace}/events?project_id=...`，先验证会话、工作区、项目，再处理游标。memory模式保留原内存快照广播，重启后无持久通知。

PG模式的Last-Event-ID为opaque游标，包含版本、数据库storage UUID、账户、工作区、项目及项目seq；最长1024字节，Last-Event-ID优先查询after。无效或范围不同返回400 INVALID_EVENT_CURSOR，超过当前已投影水位返回409 EVENT_CURSOR_AHEAD。游标不是权限令牌，查询仍以会话范围过滤；同一账户重新登录可以恢复，同游标不能带到其他账户或项目。

首次连接发送`stream.reset`（reason=initial）；低于保留floor发送reason=cursor_expired。data包含当前cursor。水位使用**已投影checkpoint**，不使用producer head：在reset前已经提交、但尚未被消费者投影的事件仍会随后送达。客户端据reset刷新需要的任务/运行/素材/活动列表，保留编辑和未确认生成，不能触发新生成。

`resource.changed`含event_id、seq、kind、resource_type、resource_id、resource_version、status、action；kind为task.updated/run.updated/audit.created，resource_type为task/run/project/draft/reference/asset。数字序号限制为JavaScript可精确表示的安全整数范围。

任务通知另附兼容`task.updated`快照，内容是读取时当前Task，可能比通知本身更新，以Task.Version去重。两帧使用同cursor，可能在中间断线，因此客户端收到resource.changed也必须按需补拉，不能只等待兼容快照。run文本仍通过原run专用SSE处理，通知用于生命周期/列表同步。

## 验证与边界

真实PG专项覆盖producer/consumer事务失败、幂等事件去重、两连接并发序号和回滚、10000条实际pending背压与2048项目保留、重启补发、账户/项目cursor隔离。网络代理在PG已经回报COMMIT完成后丢弃确认，生产端和消费端各自验证实际受理不明恢复；确定性pending-before-reset证明初始游标不会越过未投影事件。首轮10.448秒、补充两项1.152秒；最终全量证据由根联合验收记录。

当前不是永久事件归档、历史状态完整重放或账务事件系统。通知保留窗口之外靠最新资源快照恢复；原run增量日志不在本批持久化。数据库、私有文件及008/009格式必须按[持久化](persistence-auth.md)和[存储维护](storage-maintenance.md)成套升级/回退，不删除迁移登记绕过旧程序拒绝。
