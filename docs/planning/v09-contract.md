# V09 持久通知与任务领取契约

V09已完成本地集成验收（2026-11-13），证据见[034](../changes/2026-11-13-034-durable-execution-integration.md)。Outbox 为008迁移，租约为009迁移；保持当前单控制写者、私有目录锁和归属约束，不宣称多实例服务已放行。

## 通知事务与顺序

任务状态、run 生命周期/已持久检查点、现有闭集业务审计分别产生 task.updated、run.updated、audit.created 元数据事件。LLM token delta 不写 Outbox；原 run SSE 的数字 Seq、内存回放和检查点恢复语义保持。

业务状态、审计、幂等与 Outbox 插入共享同一 PG 事务。事件只含资源标识、资源版本/状态、审计动作，不含提示词、输出、上传正文、名称标签、凭据或存储 locator。audit.created 不替代原始审计表。

每项目的事件序号在事务内锁定 head 行后递增；跨项目无业务顺序保证，不用 bigserial 当提交水位。Task.Version、Run.Seq 和持久通知 seq 含义不同。幂等重放不重新创建受理事件。恢复产生的 INTERRUPTED/SUBMIT_UNKNOWN 等状态也须与通知同事务。

Commit 返回错误不推断事务未提交，不发布成功、不自动换键重做。已有 store 故障熔断保留；重启先从持久 Outbox 恢复，浏览器用原幂等请求查询结果。消费者投影、唯一事件去重、项目 checkpoint 与 pending 计数释放同事务；checkpoint 代表已进入持久 feed，不代表浏览器收到。

## 消费与有界保留

单个进程消费者每批最多100条；固定250ms轮询兜底，进程通知仅加速。Outbox 全球最多10000条未消费，原子计数检查；满时返回503 EVENT_BACKLOG_FULL，业务与事件一起回滚，健康数据库不因此永久熔断，消费者可继续排空。不得删除未消费事件。

已消费通知每项目保留最新2048条，全局最多100000条；裁剪必须同步维护项目 floor。每次读取最多100条；查询有项目索引，无每次业务写全表COUNT。重新投影依赖事件唯一键与项目 checkpoint，不能把发进内存channel视为可靠交付。

## SSE

沿用 GET /api/v1/workspaces/{workspace}/events?project_id=...。PG 模式读取持久 feed，memory 模式沿用有界内存快照/广播并明确不跨重启持久。认证、workspace及项目校验先于游标和SSE响应。

PG事件 id 是有界opaque游标：绑定格式版本、数据库storage UUID、用户、workspace、project及seq；Last-Event-ID优先于查询after。游标不是权限令牌，所有SQL仍按会话范围过滤；超过1024字节、无效或跨范围返回400 INVALID_EVENT_CURSOR；超出当前head返回409 EVENT_CURSOR_AHEAD。

首次连接或游标已低于保留floor，发送stream.reset，data为{reason:"initial"|"cursor_expired",cursor:<新游标>}。客户端重新拉任务/运行/活动/素材等所需列表，保留本地未确认请求和编辑；不能因为reset重发生成。先发reset基于已读水位，后续仅发送更大seq，列表刷新和订阅之间不留下缺口。

每条持久记录发送resource.changed，data含event_id、seq、resource_type、resource_id、resource_version、kind、status（可空）、action（可空）。兼容task.updated额外发送当前授权Task快照{task:Task}，版本以快照自身Version为准，不能冒充历史事件时点内容。两条对应同一cursor，客户端按资源版本/事件ID去重。原run专用流仍负责实时文本。

慢客户端沿用有界连接/写超时；断线后从持久cursor恢复。后台ticker仍重新核对会话。浏览器没有服务端持久ACK；最多一次/恰好一次浏览器交付不在承诺范围。

## 任务租约协作

taskRecord附带私有Lease。saveTaskSQL委托writeTaskWithLeaseSQL：有Lease时同一UPDATE验证owner、epoch、expiry、generation与原document版本；失效返回TASK_LEASE_LOST并回滚关联目录、审计和事件。控制面的cancel/reconcile/restore无Lease写入撤销旧领取。租约具体领取与续约、未知提交恢复由009实现及验证。

## 验收要求

验证业务/审计/事件回滚、幂等不重复、同项目并发提交有序、投影重复/重启恢复、commit受理不明、满队列恢复、跨账户/项目cursor拒绝、retention reset和慢客户端重连；LLM delta不得进入表。加入008/009后旧迁移加载器与离线维护必须同步格式栅栏。V09完整验收另需旧租约写入拒绝、失锁/宕机/不明受理故障，不能仅Outbox通过就勾选整批。
