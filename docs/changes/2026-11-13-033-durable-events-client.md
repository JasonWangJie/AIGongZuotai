# 033 · 持久通知客户端与有界补全

日期：2026-11-13。关联批次：V09 持久事件与分布式任务领取。模块：H01、H07、H08、H09。前端版本：0.8.0。

## 目的

接入 PostgreSQL 持久项目通知，处理首次连接、保留期过期重置和资源元数据事件；补齐必要状态且不重发收费请求，不覆盖用户编辑或未确认操作，避免 LLM 检查点导致全量刷新风暴。

## 范围

工作区 SSE stream.reset／resource.changed、兼容 task.updated 去重、单资源快照补全、失效合并、读请求退避、项目／会话隔离、只读恢复反馈。任务／运行历史和当前活动／资产／参考／云草稿按需核对。原 run SSE 继续负责 token 流，memory 模式保持原有内存广播语义。

本模块不实现后端 Outbox／租约、浏览器持久 ACK、跨标签页共享连接或自动生成重试，不把前端测试当作 V09 整批或分布式高可用验收。

## 实际文件

- `apps/workbench/src/durableEvents.ts`、`durableEvents.test.ts`：公开事件校验、事件去重、资源合并、有界补读、可见性与退避／暂停。
- `apps/workbench/src/useInvalidation.ts`：可见组件的失效读取合并，不重挂编辑器。
- `apps/workbench/src/useStudio.ts`：工作区双事件接入、同 cursor 两帧缺口补全、用户／项目／代次和 abort、历史单飞与状态反馈。
- `apps/workbench/src/api.ts`：任务／运行列表及单资源只读请求支持 AbortSignal。
- `apps/workbench/src/App.tsx`：按区域分发失效版本和最小同步提示，0.8 版本显示。
- `apps/workbench/src/ActivityPage.tsx`、`LibraryPage.tsx`、`LibraryDetail.tsx`、`useReferences.ts`：当前页面列表／详情合并刷新，原本地修改及上传保留。
- `apps/workbench/src/CloudDraftCanvas.tsx`、`draftState.ts`、`draftState.test.ts`：只对干净草稿应用 observed 快照，未知／冲突／dirty 保持不变。
- `apps/workbench/package.json`、`package-lock.json`：0.8.0，无新增依赖。
- `docs/wiki/frontend.md`、本台账：实现、限制与验证；根 README／索引／TODO 由集成人维护。

## 接口与数据影响

沿用 `/api/v1/workspaces/{workspace}/events?project_id=...`。浏览器原生 EventSource 管理 opaque Last-Event-ID，客户端不解码、存盘或重造该 cursor，不把 run Seq 或服务端消费 checkpoint 当成通知游标／浏览器确认。事件只读元数据；follow-up GET 继续带当前项目 header 和身份 Cookie。

onopen／reset 在 250ms 窗口合并，刷新近期 tasks/runs，并分批核对已加载非终态资源及当前运行。任务 resource.changed 自身登记补全，不依赖同 cursor 兼容快照帧必达；兼容 Task 快照覆盖待读版本／状态时，可取消尚未执行的冗余 GET。快照必须覆盖通知版本，run 同 Seq 的终态变化仍须合并，不能把落后快照误作完成。

run.updated 只涉及对应 snapshot；audit.created 仅失效活动及其明确相关区域，任务终态才另外失效资产。事件去重最多 512 ID，普通事件待读队列和已知资源版本各最多 256，额外保留最多 8 项失败批；已加载资源使用独立惰性扫描游标，最多合并一个后续扫描，不灌入事件队列。单批最多 8 资源／4 并发，批次间隔至少 1.5 秒。溢出回到近期历史和当前已加载非终态资源核对。隐藏页面停止新补读，保留合并意图，返回后继续而不重建流。

失败按 3／6 秒退避，连续三批失败暂停；Retry-After 等待最多五分钟，重连／手动恢复也保留当前冷却。切 scope 时停调度、abort 补读并使旧响应失效。所有恢复仅调用 GET，原生成／上传／草稿／资产操作 body/key 不变。失效信号合并正在进行的组件读取；草稿／资产 observed 不覆盖 dirty、pending 或冲突状态，隐藏或不可安全读取的草稿延后。

## 验证结果

- `npm --prefix apps/workbench run typecheck`：通过。
- `npm --prefix apps/workbench test`：135 项／18 文件通过，保留原 118 项回归。
- `npm --prefix apps/workbench run build`：通过，68 个模块；JS 379.82 kB（gzip 120.11 kB）、CSS 76.96 kB（gzip 15.90 kB）。
- 覆盖：onopen/reset 合并、两帧中断补全、兼容快照取消冗余、run 检查点只读单资源、同 Seq 终态、事件去重／定向区域、单飞与批次／并发边界、三次失败暂停、原读意图恢复、服务端冷却、旧 scope stop、队列溢出、隐藏页面合并、无效／过大元数据与不安全数字、快照版本覆盖、草稿 reset 不覆盖编辑／未知键／跨项目状态。
- 已只读核对当前 `internal/studio/outbox.go`／`outbox_http.go`：kind、resource_type、string status/action、opaque cursor 与当前前端处理一致；未修改 Go 文件。
- `node scripts/check-docs.mjs`：通过：69 份文档、34 份台账，本地链接与必需字段有效。
- 本前端代理未启动服务、未操作前台浏览器，也未提交真实生成。实际持久流、数据库／租约故障和浏览器验收由集成人及后端台账另记。

## 风险与限制

EventSource 的 Last-Event-ID 是连接恢复机制，没有浏览器持久 ACK 或恰好一次交付保证；显式手动恢复会重建连接并通过 reset 核对。客户端去重和恢复仓仅页面内存。工作区流仍逐标签页建立连接。

补全是近期列表、已加载非终态与当前视图核对，不承诺把全部历史自动加载到浏览器。资源量超过当前页面有界队列时回退核对，未浏览的旧历史仍通过正常分页读取。列表／详情读取自身失败会显示对应错误，不能把 SSE transport 在线等同于所有数据已同步。性能预算只限定客户端调度，不是并发压测成绩。

## 回滚方式

恢复本台账前端文件到 0.7 并重新构建；不删除 Outbox、消费者进度、任务／草稿／资产或未确认记录。旧前端仍可接收兼容 task.updated，但缺少新通知的只读补全；后端迁移与租约回滚必须遵守对应格式栅栏，不以删除数据库登记降级。

## 文档同步

已同步 frontend Wiki 的 v0.8 通知语义、只读调度、编辑保护、文件组织及边界。根 README、台账索引和 V09 checks/evidence 由集成人依据全部后端、前端和联合验收更新；本模块完成不单独将 V09 整批标为完成。

## 本批溢出收敛修正

独立审阅复现原方案的恢复循环：已加载非终态超过 256 时，history 回调逐项 force 入事件队列，溢出再次触发 history，前部资源被反复重填、尾部不能到达。修复把已加载资源核对改为独立惰性迭代器；普通事件至多占每批前 4 项，其余扫描补至 8 项，正常队列为空时扫描可占满本批。扫描进度独立保留，不复制整套资源，也不通过 addResource 触发自身 overflow。

本地 resetId 只协调一次读取意图，和 SSE cursor／事件 seq 没有关系。同一次 reset 的读重试不会重建扫描；新的 reset 最多合并一个后续扫描。失败原批至多 8 项独立保存，恢复时先处理该批再从扫描游标继续。stop 清除当前／后续扫描和失败批，实际 GET 仍由原 scope AbortController 中止。

新增确定性测试直接复用 history 回调接线：300 个已加载非终态资源及 300 条通知触发溢出后，historyReads=1、uniqueFetched=300，最后 44 项全部可达，每个资源只进入扫描一次；完成后再推进两分钟无追加读取。400 项扫描中途一次读取失败后完整到达尾部，扫描不回头；同 reset 反复 begin 也不会重头。135 项测试、TS 与构建通过。async_scheduler 对修复源码和这些测试完成独立只读复核，未发现新的高风险问题；其未重跑服务或测试。
