# Go 后端：持久工作台与真实 Provider 边界

当前实现已从单纯内存演示推进到可选 PostgreSQL、账户会话、项目隔离、重启恢复及已文档化的异步媒体接入。V06 增加服务端模型目录、版本化能力、精确规格预设及受理时执行快照。默认仍是无密钥 memory demo；配置与能力必须以运行时 bootstrap 和 `/api/v1/models` 为准。总体方案中的多实例 HA、支付账本和容量目标尚未完成，不能把本轮当成公众收费上线验收。

## 运行与模块说明

使用 Go 1.26.11，在根目录执行 `go run ./cmd/server`；根开发脚本见[开发说明](development.md)。默认 `APP_ADDR=127.0.0.1:8080`，非回环监听被拒绝。构建前端目录为 `APP_WEB_DIR=apps/workbench/dist`，不存在时 API 正常、根页面提示前端未构建。标准库 net/http 为 HTTP 核心；依赖版本由 go.mod/go.sum 锁定。

- [PostgreSQL、认证、工作区与恢复](persistence-auth.md)：DATABASE_URL、AUTH_MODE、事务、会话、项目、检查点、分页和真实 DB 验收。
- [账户安全与自助恢复](account-security.md)：V11一次性验证/找回、并发改密隔离、会话撤销及账户安全记录；真实邮件渠道待单独验收。
- [异步媒体协议与安全资产](media-gateway.md)：真实图片/文生视频配置、精确请求、Retry-After、任务异常与结果导入。
- [当前 OpenAPI](../api/openapi.yaml)：请求、响应、认证、模型目录和 SSE 契约；前端仍为手写 TS 类型，尚未 codegen。
- [V06 模型目录与规格](models.md)：有限配置、公开版本与 ETag、完整参数预设、历史身份及兼容规则。
- [V07 私有参考素材与编辑](references.md)：真实图片校验、缩略图、上传幂等、项目引用及不可变编辑文件。
- [V08 资产目录](assets.md)：独立搜索、名称/标签、不可变元数据修订与回收站；[私有对象存储](object-storage.md)提供内容后端与迁移。
- [V04 事务业务审计](audit.md)：已授权业务动作、同事务记录、脱敏字段、项目/工作区只读活动分页；不是消费账单。
- [V03 项目云草稿](drafts.md)：PostgreSQL 草稿、CAS 冲突、不可变修订、幂等及保存审计。
- [004 持久化认证台账](../changes/2026-11-13-004-persistence-auth.md)：本轮变更与验证证据。原 [002 台账](../changes/2026-11-13-002-go-core.md) 保留第一阶段事实。

- [V10 持久会话](conversation.md)：不可变消息树、编辑分支、继续、CAS受理、冻结上下文与有界分页；[结构化Provider](chat-provider.md)说明真实messages、输出预算与配置。

## 配置概览

| 配置 | 实际行为 |
| --- | --- |
| `PROVIDER_MODE=demo` | 默认；LLM 是明确标注的本地文本，图片为固定 SVG，视频为模拟海报，不向外请求。 |
| `PROVIDER_MODE=live` | 明确启用真实请求，至少配置一个有效聊天或媒体网关；不自动回落为 demo。未配置某类网关时，该类模型不开放新请求。 |
| `NEWAPI_BASE_URL` | live LLM API 根，例如 `https://gateway.example/v1`；仅追加 `/chat/completions`。 |
| `NEWAPI_API_KEY / NEWAPI_CHAT_MODEL` | 前者是聊天网关服务端凭据；后者用于未指定目录文件时的默认 studio-chat 绑定，自定义目录使用逐模型 upstream_model。 |
| `MODEL_CATALOG_FILE` | 可选；完整替换默认目录，启动时读取 1—32 个模型，每个媒体模型 1—32 组精确参数预设。配置变更须重启，无热更新。 |
| `DATABASE_URL` | 配置即连接 PG 并按版本增量迁移；否则为 memory。错误不回退 memory。 |
| `AUTH_MODE` | disabled 默认本地工作区；required 要求 PG 并启用真实会话。 |
| `ASYNC_IMAGE_BASE_URL` | 异步媒体服务根，不能含 /v1；不设置时 live 媒体不可用。 |
| `ASYNC_IMAGE_API_KEY / ASYNC_IMAGE_CREDENTIAL_ID` | 服务端 key 与稳定凭据标签；任务仅保存不可逆身份指纹。 |
| `ASYNC_IMAGE_MODEL / ASYNC_VIDEO_MODEL` | 未指定目录文件时分别绑定默认图片/视频项；默认 live 视频必须显式 ASYNC_VIDEO_SECONDS，分辨率用 ASYNC_VIDEO_RESOLUTION。自定义目录使用其私有模型绑定及完整预设。 |
| `ASSET_DIR / ASSET_ALLOWED_HOSTS` | 私有媒体目录与允许下载的 CDN 域名；目录不得与前端构建目录重叠，也不得放入 Vite public。 |
| `REFERENCE_DIR` | 默认.runtime/references，私有原图、缩略图及编辑正文；与ASSET_DIR、前端目录和Vite public分别独立，禁止相等或父子嵌套。 |

进程不会自动读取 `.env`；根 `npm run dev` 会读取。不要把密钥放入 VITE_*、任务 JSON、日志或仓库。

## 运行与媒体 API

所有业务 API 在认证开启后要求有效 cookie，并检查工作区/项目归属。`/healthz`、`/readyz` 和身份发现保留匿名入口；匿名 bootstrap 为401。`/metrics` 默认404，配置后须独立运维Bearer令牌，cookie不能替代。

| 方法/路径 | 行为 |
| --- | --- |
| `GET /healthz` | 进程存活，200 `{status:alive}`，不表明数据库或供应商正常。 |
| `GET /readyz` | 最近观测到的就绪状态 `{ready,state}`，200/503；不在请求中Ping数据库或供应商。 |
| `GET /metrics` | 独立运维令牌保护的Prometheus进程指标，默认关闭；固定有界标签。 |
| `GET /api/v1/runtime` | 当前身份所属工作区全部项目的运行/任务/事件连接占用、限额及排队快照。 |
| `GET /api/v1/bootstrap` | storage、身份、项目、模型目录与真实capabilities；业务鉴权。 |
| `GET /api/v1/models` | `{version,models}`；ETag/If-None-Match 支持304，`Cache-Control: private, no-cache`，认证在条件请求判断前执行。 |
| `GET /api/v1/runs`、`GET /api/v1/tasks` | 所选项目的分页历史，limit/offset；返回 items/limit/offset/has_more。 |
| `GET/POST /api/v1/conversations` | 项目私有会话keyset分页与幂等创建。 |
| `GET /api/v1/conversations/{id}`、`/messages`、`/branches` | 会话摘要、选中叶子的祖先分页及叶子分支；完整响应有界。 |
| `POST /api/v1/conversations/{id}/runs` | CAS、原键优先、同会话单活跃；冻结结构化上下文后原子受理reply/continue。 |
| `POST /api/v1/runs` | project_id/model_id/input 和可选 model_version，201 Run；必需 Idempotency-Key。 |
| `GET /api/v1/runs/{id}/snapshot` | 完整输出和状态，历史可按需从 PG 恢复。 |
| `GET /api/v1/runs/{id}/events?after=N` | SSE 回放/实时订阅，Last-Event-ID 优先 after。 |
| `POST /api/v1/runs/{id}/cancel` | 显式取消，终态不可复活。取消不证明供应商已停止计费。 |
| `POST /api/v1/tasks` | project_id/kind/model_id、可选 model_version、inputs.prompt 与完整预设 parameters，202 Task；幂等受理并冻结有效执行参数。 |
| `GET /api/v1/tasks/{id}` | 状态、版本、产物与 can_reconcile。 |
| `POST /api/v1/tasks/{id}/cancel` | demo 支持；真实网关无取消协议时 409，保留原任务。 |
| `POST /api/v1/tasks/{id}/reconcile` | 仅持久的原 handle 续 Query，202；不接受上游 ID，不重新生成；15 秒冷却。 |
| `GET /api/v1/workspaces/{workspace_id}/events` | 所选项目 task.updated；初始仅活动/近期缓存，须配合分页列表。 |
| `GET /api/v1/assets/{id}` | 所属任务 ACL 后读私有资产，支持 Range；停用供应商也可读历史文件。 |
| `GET /api/v1/library`、`GET /api/v1/library/{id}` | 当前项目独立目录搜索/keyset分页与详情；trashed详情保留，内容URL为null。 |
| `PATCH /api/v1/library/{id}` | 名称/标签完整更新，expected_revision与Idempotency-Key，CAS/幂等/修订/审计同事务。 |
| `POST /api/v1/library/{id}/trash`、`/restore` | 版本校验后的软删除/恢复，原内容与任务执行快照不替换。 |
| `GET /api/v1/library/{id}/revisions` | 不可变元数据历史，before_revision分页，不提供旧内容访问绕过。 |
| `GET /api/v1/library/{id}/content`、`/thumbnail` | 当前项目active条目访问；旧assets/references入口同样核对active。 |
| `GET/POST /api/v1/references` | 当前项目参考图列表或单文件multipart上传；201，按实际内容和规范化文件名幂等，返回私有原图/缩略图URL。 |
| `GET /api/v1/references/{id}/content` 及 `/thumbnail` | 所属项目ACL、摘要验证后的原图/缩略图；支持HEAD、Range。 |
| `GET /api/v1/audit` | 当前项目或工作区的只读活动，复合游标分页；memory 仅最近512条。 |
| `GET/PUT /api/v1/projects/{project}/draft` | PostgreSQL 草稿读取与CAS保存，原键重放不重复创建修订/审计。 |
| `GET /api/v1/projects/{project}/draft/revisions` 及 `GET /api/v1/projects/{project}/draft/revisions/{revision}` | 历史修订元数据及单个不可变修订。 |
| `GET /demo-assets/*.svg` | 明确的本地公共示意图，不是真实模型产物。 |

认证与项目 API 详见[持久化认证页](persistence-auth.md)。统一错误为 `{error:{code,message,request_id}}`，响应头带服务端生成的 `X-Request-ID`；429/503带 `Retry-After: 3`，客户端冷却后手动操作；未知 API 返回 JSON 404，不能被 SPA HTML 回退吞掉。

## 模型目录、版本与历史身份

未指定目录文件时仍提供 studio-chat、studio-image、studio-video 三项兼容目录；live 模式中缺少默认模型或网关的项禁用。`MODEL_CATALOG_FILE` 可以替换为有限的多模型目录，公开模型提供 ID、名称、类型、说明、启用状态/原因、版本及能力。上游模型名、服务地址和凭据不进入公开响应。当前目录是部署级配置，并非每工作区不同的套餐授权，也不是从供应商实时发现能力。

媒体参数从完整 `parameter_presets` 选择，每组固定比例及可选 size/resolution/seconds；不会交叉拼成目录未声明的组合。图片最多4张且不含 seconds，视频每次1个结果、时长1—600秒；实际目录可以收窄。新前端携带 model_version，服务端兼容旧客户端省略版本；非空版本不匹配返回 `409 MODEL_VERSION_CHANGED`。禁用模型返回 `422 MODEL_DISABLED`，错误参数返回 `422 INVALID_MODEL_PARAMETERS`，均不新建任务。同键重放优先于当前目录启停、版本和准入检查，不能通过换目录把已有操作变成第二次生成。

新 Run/Task 保存受理时的 model_name、model_version、model_capabilities；Task 另保存实际 parameters。目录更名、移除、改映射后，历史仍显示原快照。旧记录缺少快照时只能使用旧 ID 等已存在信息，不能用今天的目录冒充过去配置。LLM 的私有上游绑定用于当前进程执行，重启后流中断而不续发；媒体的私有绑定直接固定在持久提交正文中。

## LLM 流式执行

POST 受理与 GET 订阅分离。默认 4 个执行 worker、32 项排队、每工作区 32 个和全进程 128 个 SSE 订阅，每订阅缓冲 64 项。流总时长最多 10 分钟，输出最多 128 KiB。021 的慢读者可以从 PG 补读再接实时通知，窗口已过期时取快照。浏览器断开后生成继续，重连只发 GET。

内存模式使用有界循环缓冲。PG 模式下，新 Run 的公开文字增量先提交再广播，Run.seq 与项目通知及媒体版本分别管理。新流窗口按记录数和载荷字节裁剪，终态保存公开前缀、权益和审核结果。[021 决策](decisions/0021-durable-run-stream.md)

- `message.delta` data 为 `{run_id,seq,delta}`。
- `run.completed / run.cancelled / run.failed / run.interrupted` data 为 `{run_id,seq,status}`。
- `run.snapshot` 的 data 为完整 Run。超出窗口、旧记录或没有回放起点时可以收到该快照。

021 重启时从已提交的流前缀恢复，未完成的 Run 追加一个 INTERRUPTED 序号。020 旧记录继续用安全序号间隔兼容易失游标。每秒业务检查点为会话历史和项目元数据提供进展，终态强制保存；重启和原键回放均不再调用模型。memory 重启后会丢失数据。

每个 SSE 单写协程，15 秒心跳；写入 deadline 5 秒并在写后清除，避免短全局 WriteTimeout 截断长流。会话注销/过期在心跳验证后关闭已有流。新 GET/POST 每次立即验证会话。

## 上游连接与解析

OpenAI 兼容文本 adapter 的多个模型共用一个 client/Transport；StreamModel 按受理时绑定在局部正文指定模型，不改共享默认值。连接/TLS 各 10 秒、响应头 45 秒、空闲 90 秒、单 host 最多 32 连接。Client 不设置覆盖整条流的短超时。远程要求 HTTPS，HTTP 仅回环；禁止重定向与环境代理，不向网关转发本平台幂等键，不主动重试生成提交。

SSE 解析支持 TCP 分片、UTF-8、CRLF、多行 data、usage 空 choices、显式 error 事件；每上游事件最多 256 KiB。必须收到 [DONE] 才完成，提前 EOF 失败且保留部分输出；不返回上游敏感错误正文。目前仅单条 user prompt，不含聊天历史、工具、附件；usage 尚未进入账本。

## 媒体与未知结果

memory模式单个scheduler每500ms检查到期任务，4worker/32队列；PG模式固定worker直接领取数据库到期任务；live 按至少 3 秒和 Retry-After 调度，媒体网络与导入单次操作总限 2 分钟。没有每任务 sleep goroutine，浏览器不直接轮询供应商。

媒体在平台受理阶段调用无 I/O 的 PrepareModel，把私有上游模型和已解析参数编码为不可变 MediaSubmission。PG 将它与 Task、平台幂等记录及受理审计同事务保存，随后 worker 持久 SUBMITTING，再用 SubmitPrepared 发送原始字节。不会在排队执行或重启后用新目录重建旧请求；memory 模式仅进程内保存这些记录。已有 handle 继续查询原任务，目录停用/移除不阻断该查询；服务根或凭据身份变化则进入核实。旧 live 任务若同时没有 handle 和提交快照，恢复为 RECONCILE_REQUIRED，禁止按新默认值补齐后提交。

状态包含 QUEUED/SUBMITTING/RUNNING/INGESTING/MODERATING/SUCCEEDED/PARTIAL/FAILED/CANCELLED，以及 SUBMIT_UNKNOWN、PROVIDER_STATE_UNKNOWN、RECONCILE_REQUIRED。未知提交不能自动再提交；已知handle、尚无私有审核候选且满足当前核对条件的待核实任务，可由有权限用户“重新核对”，只继续Query原任务。已经形成私有候选或审核决定的任务不借此重新审核或放行；完全无handle的未知提交仍需外部核实，不伪造恢复。

真实素材先完成受控下载、大小/格式检查和私有磁盘落盘，才发布对应产物。静态目录与 ASSET_DIR 的真实路径/祖先符号链接经过配置检查，防止绕过资产 ACL。

## 当前限制与验收

PG 模式有数据库级单写锁，业务SQL使用持锁控制连接，任务领取/续约使用有界独立连接；独立每秒调度的健康探测也覆盖无输出/无HTTP活动时的失锁，失败取消执行，业务API和readyz返回503，healthz仍返回200。就绪使用缓存状态；实际SQL和探测失败触发停止，连接锁等待和探测执行窗口意味着检测不是绝对1秒。它仍有串行 SQL/全局状态锁，不是多副本高吞吐架构。已实现数据库分页与终态缓存淘汰，长期历史不再永久占满 512 条缓存；当前 offset 分页需要前端去重。

Go 单元/HTTP/媒体 fixture、真实 PG 认证隔离和恢复测试、go vet 已执行通过，具体命令与环境见台账。未配置 TEST_DATABASE_URL 的测试会跳过 PG，不能当成数据库验收。Windows race 仍受缺少 CGO/C 编译器限制；未执行真实收费请求、生产备份恢复、多实例或容量验收。

V11已实现邮箱验证/找回与账户安全审计的本地链路，真实邮件仍待配置；V12已交付权益账本。V14-A新增按工作区授权的运营异常核实。MFA、成员协作、完整内容审核与风控、真实支付、生产迁移运维、多实例执行与故障压测仍需后续批次，再按总体方案开放公网。当前不宣称整个长期方案全部完成。

## V12 权益报价与账本

新增`POST /api/v1/billing/quotes`及`GET /api/v1/billing/wallet`、`/entries`、`/reservations`；全部要求账户和权益模式，按当前工作区/项目授权，有界分页。报价绑定完整原请求、模型/价格及模式，不调用上游；受理时与业务/幂等/审计/Outbox同事务冻结。原键重放先于当前报价、价格和容量门禁。

Go/PG以micro整数计算，HTTP以十进制整数字符串传值；只有完整可信usage或实际登记交付产物才能自动结算，未知保留REVIEW。没有公开赠额/退款写入口；停机管理工具默认预演并校验计划摘要。012迁移不可被旧写者绕过。完整规则见[权益账本](ledger.md)、[操作说明](billing-operations.md)、[用量证据](chat-usage.md)及[046验收](../changes/2026-11-13-046-billing-integration.md)。

## V14-A 运营异常核实

新增运营访问范围、异常队列、工单与时间线、预演和执行，共7个路径、8项HTTP操作。`OPERATIONS_ENABLED`默认关闭，启用须使用PG及账户认证；账户或工作区所有者不会自动获得运营权限。每次访问在事务内重核会话与指定工作区的角色授权，敏感执行还需当前密码确认，不能凭创作端项目选择或旧页面权限快照执行。

预演使用真实事务SAVEPOINT验证并回滚业务效果；正式执行在同一事务提交计划消费、工单回执、运营审计及适用的业务、账本与Outbox，成功提交后才换缓存。受理不明时只读工单核对，不自动再次执行。原媒体核对只查询已知请求，`no_action`只关闭工单。范围和限制见[运营权限](operations-access.md)、[异常工单](operations-cases.md)及[050验收](../changes/2026-11-14-050-operations-integration.md)；完整V14的内容审核、封禁、申诉与风控尚未完成。

V05的单写者资源准入、未知占用和幂等优先见[准入说明](admission.md)；指标鉴权、缓存计数口径及SSE观测见[可观测说明](observability.md)。

V06 的目录、原始请求兼容、共享连接池、真实 PG 恢复和前端联合验证见 [021 集成台账](../changes/2026-11-13-021-model-integration.md)。数据库005版本是数据格式栅栏，旧v0.4程序不能直接写回新快照，具体升级/回退限制见[持久化说明](persistence-auth.md)。

## V07 参考素材与编辑

- `GET/POST /api/v1/references`：当前项目分页列表／单文件multipart上传；上传必须携带幂等键。列表精确多取一条计算has_more，区别于旧任务列表的保守分页。
- `GET/HEAD /api/v1/references/{id}/content` 和 `/thumbnail`：项目鉴权、私有缓存策略、Range；路径中的ID不是公开访问令牌。
- TaskInput新增可选`operation`和`inputs.reference_asset_ids`；默认generate，edit仅图片、1—4个有序不同引用，数量仍受模型上限约束。生成请求禁止混入参考图。
- 模型能力新增`image_edit_supported`与`max_reference_images`；bootstrap给出`reference_limits`。缺省live目录不开编辑，必须明确配置实际支持的模型。

格式、大小、工作区配额及鉴权负例见[参考素材](references.md)。已有上传/任务的幂等重放优先于新配额/目录检查；编辑准备在全局锁外完成，重入后再次检查，失败不发送上游请求。服务端原件、缩略图和不可变正文的校验、磁盘容量与权限边界见[ADR0004](decisions/0004-private-reference-bodies.md)。API变更详见[OpenAPI](../api/openapi.yaml)，验证证据见[025](../changes/2026-11-13-025-reference-integration.md)。

## V09 持久通知与任务领取

工作区SSE在PG模式使用范围绑定opaque游标，发送元数据`resource.changed`及`stream.reset`，保留兼容task快照；run专用流仍用数字seq。详情见[事件协议](events.md)。业务/审计/幂等/Outbox同事务，满队列503 `EVENT_BACKLOG_FULL`回滚本次变更；LLM已完成后的背压只重试保存，不重新调用模型。

媒体采用数据库到期领取、周期续约和owner/epoch/generation/版本条件更新；实际网络期间有界worker维持续约，未知提交不自动POST。`TASK_LEASE_SECONDS`默认30、范围5—300，使用数据库时钟；详见[调度说明](scheduler.md)。控制写锁和本地私有目录归属继续保留。迁移008/009、真实进程Kill和升级证据见[034](../changes/2026-11-13-034-durable-execution-integration.md)。

## V10 多轮对话

新增010迁移，消息节点、Run、上下文、幂等、审计及Outbox同事务。assistant节点引用Run，避免每个流检查点重写消息树；祖先Run批量读取，分页在释放全局状态锁后写入HTTP响应。输入ID严格校验，NUL和非法UTF-8流片段在进入数据库前拒绝，保留有效前缀。事件与指标不包含正文。

进程重启保留最后持久输出并标记INTERRUPTED，刷新不会重新生成；继续为用户明确发起的新Run。旧单轮API与历史仍保留。上下文估算只用于预算，不能据此结算；真实供应商与生产容量分别由V15/V17验收。证据见[035](../changes/2026-11-13-035-conversation-core.md)、[036](../changes/2026-11-13-036-chat-provider.md)及[038](../changes/2026-11-13-038-conversation-integration.md)。

## V14-B1文本审核与提交后交付

`MODERATION_MODE`默认`disabled`；启用须PG和`AUTH_MODE=required`。既有`POST /api/v1/runs`与会话runs接口共用审核受理链路，没有公开绕过或人工放行接口。bootstrap新增`moderation`能力，包含`enabled/mode/policy_id?/policy_version?/text_delivery/media_supported`；文本关闭时为`stream`，启用时为`after_review`。B1阶段拒绝新媒体；当前仅启用文本审核而没有配套媒体审核时仍拒绝，B2能力见下节。Run新增有界`moderation`摘要：政策ID/版本、固定`output`阶段、PENDING/ALLOWED/BLOCKED/REVIEW、修订号及可选固定原因码。

输入先按原幂等键查找已受理请求，再准备实际完整`ChatRequest.Messages`；审核调用在全局状态锁和数据库事务之外执行。返回后重新检查原键、身份与项目作用域、会话版本及重建上下文摘要，才在同一事务创建Run、会话、冻结政策、适用的权益冻结和Outbox。不能把锁外检查前的上下文当成再次受理时仍然有效；输入未通过不调用生成。

启用审核的新Run不发送`message.delta`。最多128KiB的候选正文只在执行内存，输出按受理时冻结政策在锁外全文审核；候选不进入检查点、历史、消息、Outbox或日志。只有审核ALLOWED与Run COMPLETED同时成立，并确认业务、审核决定、账本及Outbox事务提交后，才替换公开缓存并以`run.snapshot`或GET结果交付正文。快照、分页历史、消息分支和后续上下文共同执行正文可见性检查；上文普通流式路径的“保留部分输出”不适用于这些受审核Run。

取消、中断、进程恢复或提交确认不明不能放行候选，也不自动重新生成。重启丢弃文本候选并将未完成审核置REVIEW；输出隔离不证明供应商未计费，财务核实不改变内容决定。015持久化冻结政策和不可变决定，不保存审核密钥、服务地址或文本候选原文；B1交付时离线CLI栅栏为精确001—015，当前016要求见下节。

`rules`仅供demo的有限字面规则；`http`是自有私有text-v1契约，真实审核商尚未选型，本地HTTP fixture不等于真实语义审核验收。旧文本、旧任务和资产不追溯；人工放行、申诉和封禁尚未实施，完整V14仍为`in_progress`。配置、限额、错误与恢复细节见[文本审核](moderation.md)，B1历史验证保留在[054集成记录](../changes/2026-11-14-054-text-review-integration.md)。

## V14-B2媒体字节审核与016格式

新增独立`MEDIA_MODERATION_MODE=disabled|fixtures|http`，启用须PG、账户认证及已启用的文本审核。任务提示词按冻结文本政策检查；新参考上传、编辑实际引用及生成图片/视频按完整私有字节检查。fixtures仅供demo，以SHA256、长度、类型和input/output阶段精确匹配，未知文件REVIEW；http使用私有media-v1流式上传，不根据prompt或海报推断输出通过。协议与配置见[媒体审核](media-moderation.md)。

新上传只有同步批准后才注册reference、资产库、幂等和审计。生成完成先在租约/版本检查通过的事务中保存016私有候选并进入MODERATING，公开Artifacts为空；审核在锁外执行。只有全部返回候选ALLOWED，且任务为SUCCEEDED/PARTIAL，才同事务提交决定、原顺序产物、目录、账务和Outbox，确认后更新缓存。任一候选未通过则整批隔离，不把未公开结果注册成交付。所有新旧内容、缩略图、library及reference入口共同执行门禁。

与B1仅内存文本候选不同，B2媒体候选持久保存并纳入GC可达集合，取消、阻断、REVIEW及终态均保留。恢复或重领已持久候选会保持隔离，不自动重审或重生成；有原handle但尚无候选、仍满足核对条件的任务可继续Query原任务。财务charge/release不改变审核决定，也不授予内容下载权限。

B2交付时账本、角色及存储维护CLI均要求精确001—016。升级前联备PG、私有参考/产物目录、S3对象与收据、owner及必要密钥；关闭模式或删除格式登记不能替代配套回退。引擎、执行、界面和联合证据分别见[055](../changes/2026-11-14-055-media-moderation-engine.md)、[056](../changes/2026-11-14-056-media-moderation-execution.md)、[057](../changes/2026-11-14-057-media-moderation-workbench.md)、[058](../changes/2026-11-14-058-media-review-integration.md)。真实审核商未选，fixture不代表语义质量或生产渠道放行，完整V14仍在进行中。

## v0.15 / V14-C1工作区生成控制

新增10个HTTP入口，严格8KiB JSON及字段/编码检查，scope、申诉原幂等、限制轮次、预演CAS、当前密码、事务回执和审计贯通。017表及新moderator权限见[控制核心](generation-controls.md)，三类新受理/报价与锁外检查后复核见[准入](generation-admission.md)。读取和限制共享稳定workspace行锁，旧任务继续生命周期；关闭运营配置不绕过持久限制。本批采用有界拉取，没有新建项目Outbox广播。联合结果见[062](../changes/2026-11-14-062-generation-controls-integration.md)。

## V16-A1聊天执行

单轮/多轮统一[有界公平队列](execution-scheduler.md)，执行数量与持久受理数量分开；五项CHAT配置及独立chat HTTP池支持启动配置。原键、工作区限制/审核/报价/会话原子受理不变，执行未实际返回不提前释放预算。017格式未变，SQL现集中[db](../../db/migrations/)。完整V16的媒体lane和逐token持久回放未完成。

## V16-B2 聊天执行权

PostgreSQL 模式的每个已启动聊天持有独立租约，默认 30 秒。执行、审核及终态保存期间定期续租。数据库在领取、续租、增量、检查点和终态写入时检查授权；失权时只按已提交的公开前缀中断。原模型即使延迟响应，也不能重新交付或结算。服务继续以单控制写者运行。[决策 0022](decisions/0022-run-execution-leases.md)

## 项目工作流

PG 与账户模式下，项目成员通过 workflow-definitions 保存定义，按固定版本启动 workflow-runs，再对就绪节点逐步授权。每次授权都在子资源受理事务检查原模型、输入、权限及权益。原键可读收据，工作流详情只公开已批准的产物。确认选图用当前资产修订，并核对原项目、任务、产物和内容身份。改名后刷新可以选择原图；回收或替换内容后不能选择。[接口契约](../api/openapi.yaml) · [编排与联备](decisions/0023-project-workflows.md)

## 025 局部重跑与取消

重跑预览、创建新尝试、停止原流程和读取原键回执均要求当前项目身份。原键重放与新尝试独立计费相容，项目流按工作流版本只传状态元数据。实际退出和持久收据决定子执行结果。[025 编排](decisions/0025-workflow-attempts.md)
