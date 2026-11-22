# PostgreSQL、账户、项目隔离与恢复

本轮实现可选 PostgreSQL 持久化与真实账户会话，保持默认 memory demo 可用。它是**单写实例、回环地址的持久工作台**，还不是多副本高可用或公众收费上线版本。媒体协议与资产安全详见[异步媒体](media-gateway.md)，接口见 [OpenAPI](../api/openapi.yaml)。

## 配置与启动

| 配置 | 实际行为 |
| --- | --- |
| `DATABASE_URL` | 非空时连接 PostgreSQL；连接/迁移/单写锁失败拒绝启动。留空保留 memory。 |
| `AUTH_MODE` | 默认 `disabled`。`required` 必须同时提供 DATABASE_URL。没有“内存假登录”替代。 |
| `TEST_DATABASE_URL` | 仅集成测试使用；每项测试创建随机 schema 并清理该 schema，不清空已有数据库。 |
| `APP_ADDR` | 默认 `127.0.0.1:8080`，仍拒绝非回环字面量地址。 |

根目录开发脚本读取 `.env`；直接 `go run ./cmd/server` 从进程环境读取。不要提交连接串、网关 key 或 cookie。连接串不回显到日志。PostgreSQL 应使用专属应用数据库/角色；当前启动按版本应用内嵌 [001基础](../../db/migrations/001_core.sql)、[002审计](../../db/migrations/002_audit.sql)、[003草稿](../../db/migrations/003_drafts.sql)、[004准入索引](../../db/migrations/004_admission_indexes.sql)、[005模型快照格式栅栏](../../db/migrations/005_model_snapshots.sql)，需要建表/触发器/索引权限。`studio_schema_versions` 登记已应用版本，已有001数据可以增量升级；迁移与版本登记同事务提交。发现数据库有当前二进制不认识的版本时拒绝启动，不能通过直接换旧二进制回退。

005 不新增表或重写旧记录，其 SQL 为 `SELECT 1`，作用是登记新数据格式版本。V06 在现有 JSON 列中保存模型身份/能力/参数快照，并改变新请求的平台幂等摘要口径；旧v0.4结构反序列化后写回会丢弃新字段。因此旧v0.4迁移加载器看到数据库版本5会拒绝启动，避免无声破坏已受理记录。不能删除版本号来强行降级，也不能把该栅栏当作自动数据回滚。

升级前备份数据库；如需回退，在独立库恢复升级前备份并用匹配应用验证，保留升级后原库中的新数据。尚未提供自动向下迁移或完整生产迁移/灾备工具，后续见[TODO V18](../../TODO.md)。项目私有文本草稿及原子审计的专项契约见[草稿与版本](drafts.md)。

默认关闭认证时，仅访问固定 `ws_demo` 的资源，不能借此读取数据库中其他账户的工作区。认证开启时，匿名 bootstrap 返回 401；`GET /api/v1/auth/session` 仍返回 200，形如 `{auth_enabled:true,user:null}`，供前端展示登录入口。

## 账户与会话

| 接口 | 请求 / 返回 |
| --- | --- |
| `POST /api/v1/auth/register` | `{email,password,name}`；201，原子创建账户、所属工作区、第一个项目及会话。 |
| `POST /api/v1/auth/login` | `{email,password}`；200，签发新会话。失败为通用 401。 |
| `POST /api/v1/auth/logout` | 撤销当前会话并清除 cookie，200。 |
| `GET /api/v1/auth/session` | 当前身份，或匿名 user:null；不返回 token/hash。 |
| `GET /api/v1/projects` | 当前工作区项目 `{items:[...]}`。 |
| `POST /api/v1/projects` | `{name}`，201 Project；每工作区最多 100 个项目。 |

登录、注册与已登录 session 返回 `auth_enabled,user,workspace,project,projects`。bootstrap 保留 models/mode/storage 和上述身份字段，V06 增加 catalog_version 以及逐模型版本/能力。认证后的 `GET /api/v1/models` 返回有限动态目录，支持 ETag/304；条件请求不能绕过登录或项目归属检查。项目对象含 id、workspace_id、name、created_at。

邮箱去首尾空白并转小写，最长 254 字节；名称去空白后 1–80 Unicode 字符。密码为 **10–72 UTF-8 字节**，bcrypt cost=12；数据库仅存密码哈希。会话采用加密随机 32 字节 token，cookie 保存 base64url 原值，数据库只保存 SHA-256 token hash。会话有效期 7 天；每个账户保留最多 10 个有效会话，登录时清理过期会话。注销与过期在下一次 API 鉴权立即生效，已有 SSE 每次心跳重新验证会话，最长约 15 秒后断开。

cookie 为 HttpOnly、SameSite=Strict、Path=/，TLS 请求加 Secure。当前仅回环 HTTP 联调，不把本地 HTTP cookie 配置宣称为公网部署配置。服务已检查同源 Origin/Host，保留明确的 Vite 本地白名单，未开启宽松 CORS。

密码验证最多并发 4 项；本地进程内按 IP 和邮箱散列限制登录/注册尝试，15 分钟窗口，邮箱每操作最多 10 次、IP 登录最多 30 次/注册最多 10 次；限流条目最多 2048。限流重启后清空，尚未替换为共享 Redis 限流。

V11新增[账户安全与自助恢复](account-security.md)：邮箱验证、一次性找回、修改密码、会话列表及撤销、独立安全记录。011增加credential_version及session公开随机ID；登录事务重查bcrypt快照，改密/重置原子递增版本并撤销全部会话，重置不自动登录。匿名session和bootstrap返回account_security，邮件未配明确禁用。验证不扩大权限；没有MFA、邀请成员或管理员角色，不应直接开放注册到公网。真实邮件验收与本地fixture分开记录。

## 工作区和项目边界

认证账户对应一个自有工作区。每次 API 请求从服务端会话确定 user/workspace，忽略客户端伪造用户身份。创建任务的 body.project_id 必须属于当前工作区；读、取消、SSE 和真实媒体资产下载都验证资源所属工作区，跨账户资源返回 404，不能通过已知 ID 绕过隔离。

项目选择使用 `X-Project-ID`，EventSource 使用 `?project_id=`；二者同时提供但不同返回 400。不提供时选最早创建项目。列表只列所选项目；提供明确项目上下文时，按 ID 获取也必须匹配该项目。未提供项目上下文的按 ID 查询仍允许访问当前工作区内的其他项目，便于直接资源链接。

`/api/v1/workspaces/{workspace_id}/events` 验证真实工作区 ID，并只广播所选项目任务。初始 SSE 是当前内存活动/近期缓存快照，不是完整历史；客户端必须同时拉取任务列表并按 version 去重。资产 API `/api/v1/assets/{id}` 先查任务所有权，再打开本地资产；缓存淘汰后也会查 PostgreSQL，不能绕过 ACL 直读媒体文件。资产读取独立于当前 Provider：停用真实网关或切换 demo 后，保留相同 ASSET_DIR 仍能下载已导入作品，不需要网关 key；只读 reader 不创建目录、不能执行导入。

## 持久原子性、检查点与恢复

PostgreSQL 保存 users、workspaces、projects、sessions、runs、tasks、idempotency。运行/任务的创建与幂等记录、受理审计在一个事务内提交，提交失败不先返回成功；幂等作用域为工作区 + 操作 + key，跨重启仍成立。

V06 新请求先对原始结构化输入生成摘要，再解析目录默认值形成执行副本；同键同输入返回原资源，同键异参409。平台摘要不是上游原始 JSON 字节幂等，两者分别保存。幂等重放先于目录版本、启停和准入检查，因此目录变更不能使同一个已受理操作重新生成。旧 Task 没有 model_version 时按旧 count=1、aspect_ratio=16:9 的省略值规则比较；旧 Run/Task 的字段顺序和 omitempty 兼容已通过固定历史 JSON 字面量摘要测试，不用当前目录重算旧参数。

新 Run/Task 的 JSON 文档保存受理时的 model_name、model_version、model_capabilities，Task 另存有效 parameters；当前目录可更名、停用或移除模型，而历史展示继续读取原快照。私有 LLM 上游模型只保留于当前进程的执行记录，重启中断所有未完成流，不按目录重新续发。旧记录不回填推测性的历史快照，详情保留已有 ID 等信息。

PG 021 的新 LLM Run 将每个非空公开文本块及其序号持久化，确认事务提交后才向浏览器发送。每秒另存有变化的业务检查点用于会话历史，完成、取消或正常停机保存终态。重启时根据已提交前缀追加一个 `INTERRUPTED` 序号。原游标在窗口内可逐段读取，窗口过期则取完整快照。未审候选继续只在私有内存，批准正文随已完成终态同事务交付。升级自 020 的旧 Run 只保留原业务快照和安全序号间隔，无历史增量可回填。两种记录的原键重放均不重提模型。[回放与权限](decisions/0021-durable-run-stream.md)

媒体保存 Task、有效输入、provider handle、next_poll，以及不可变 MediaSubmission：原始请求字节、上游幂等键、路径、服务根、凭据标签/指纹。V06 在平台受理阶段验证模型版本/精确预设并调用无 I/O 的 PrepareModel，提前固定实际模型与参数；Task、MediaSubmission、平台幂等和受理审计同事务提交。worker 再持久 `SUBMITTING`，调用 SubmitPrepared 发送原始字节；供应商受理成功后强制保存 handle。排队、目录更新或进程恢复不会重新序列化已有正文。重启后：

- 有 handle 的未完成任务只继续 Query，不能重新 Submit。
- `SUBMITTING` 且无 handle 转 `SUBMIT_UNKNOWN`，停止自动调度；不能把“没拿到 task_id”等同于“供应商没受理”。
- 旧 live 任务既无 handle 又无 MediaSubmission 时转 `RECONCILE_REQUIRED / EXECUTION_SNAPSHOT_MISSING`；不使用新目录或默认参数猜测旧请求并提交。
- `SUBMIT_UNKNOWN / RECONCILE_REQUIRED` 保留状态，等待人工核实，无 handle 的提交不明仍需外部核实；有原 handle 时支持下述安全续查。
- 任务记录的 provider_mode 与当前配置不一致时转待核实；不能将历史 live 任务用 demo Provider 伪造为完成。
- 服务根/凭据身份变化由媒体适配器拒绝盲查；原始密钥不持久在任务文档内。
- 同一网关身份保持时，目录停用、移除或改上游模型映射不阻断旧 handle 查询，也不改写已保存的提交正文。保留网关配置可在没有默认生成模型时继续处理历史。

失败查询可依据适配器 Retryable/Retry-After 延迟重查；live 最少间隔 3 秒，收到 Retry-After 取更长值。单次媒体 worker 操作最多 2 分钟，包含结果安全导入。ASSET_DIR 在配置时与 APP_WEB_DIR 及已知 Vite public 路径隔离，连同可解析的祖先符号链接一起检查。真实媒体取消能力以已确认协议为准；没有上游取消接口时返回 409，保留任务继续查询，不伪造已取消。

## 安全重新核对

任务响应增加 `can_reconcile`。仅当前供应模式一致、已持久保存原 handle，且处于 RECONCILE_REQUIRED/PROVIDER_STATE_UNKNOWN/SUBMIT_UNKNOWN 的任务可调用 `POST /api/v1/tasks/{id}/reconcile`。它返回 202、递增版本并保存 next_poll，只恢复原 handle 的 Query。请求不能传入上游 ID；没有 handle 返回 409。`reconcile_after` 持久记录 15 秒冷却，频繁操作返回 429。状态改变先落库再广播，前端提示“仅查询原任务，不重新生成”。

## 单写锁、失联处理与容量范围

服务用 `pg_try_advisory_lock` 持有数据库级固定 advisory lock，第二个写实例明确启动失败。**所有数据库语句均使用持锁的同一 pgx 连接**，连接丢失后不会偷偷从连接池取新连接继续写。固定每秒调度的数据库健康探测独立于 HTTP/新 delta，覆盖等待上游时的锁连接损坏；探测执行最多5秒，同连接锁等待可能进一步延迟发现，不能承诺1秒内发现。HTTP `/healthz` 仅表示进程存活；`/readyz` 与API前置检查使用最后观测到的状态，不逐请求额外Ping。实际SQL失败与探测失败均触发停止，详见[可观测](observability.md)。数据库错误会停止执行上下文并返回 503，需要恢复数据库后重启；没有静默回退 memory。API、会话读取、任务受理、检查点和状态写入都遵循此行为。

这是有意选择的单进程正确性基线：事务和状态临界区仍有串行部分，尚未达到总体方案的极致吞吐目标。后续应以带 fencing 的租约、事务 outbox、独立执行服务和连接池替代，并通过故障/容量测试后声称支持多实例；只把连接池加上去会破坏当前锁连接失联语义。

PostgreSQL 启动只加载可执行的未完成资源，历史终态按需读取。终态/待核实缓存达到上限后可淘汰，因此累计超过 512 条历史不会永久阻断新建。活动资源、worker、队列仍有界；memory 模式仍保留原 512 条总记录限制。项目元数据当前启动加载，列表使用数据库 limit/offset，而不是生产规模的 keyset 分页。

`GET /runs`、`GET /tasks` 支持 `limit=1..100`（默认 100）、`offset=0..100000`，返回 `{items,limit,offset,has_more}`。has_more 采用页是否满的保守判定；并发新增可能让 offset 页面重复，前端必须按 ID 去重。历史详情、幂等重放和下载支持按需从 PG 恢复。没有无限一次性导出全部历史的承诺。

## 实测与后续

2026-11-13，使用真实本地 PostgreSQL 18.7，`TEST_DATABASE_URL` 指向隔离测试 schema：

- `go test ./... -count=1` 通过，包含 PostgreSQL 集成测试；普通无变量运行会明确 Skip DB 用例。
- 实测注册/登录、HttpOnly/SameSite、密码与 token 哈希、注销和过期、跨用户资源/SSE/取消拒绝、项目归属、第二写实例拒绝。
- 实测历史超缓存上限仍可新建、分页与按需回读、幂等跨重启、LLM 中断快照、有 handle 媒体只查询、提交不明不重发、数据库连接损坏后503。reconcile冷却跨重启保留；停用网关后的历史资产仍可下载，另一账户请求返回404。
- 实测固定周期保存停顿流的最后输出、取消时立即保存最后 delta、无HTTP/无新delta时失锁取消；以128KiB逐字节输出验证恢复序号严格覆盖事件上界，并验证重复重启不再改变INTERRUPTED序号。
- `go vet ./...` 通过。race 本机仍受 CGO/C 编译器缺失限制，Linux CI 待执行；没有真实收费生成或高容量压测。

业务事务审计及版本迁移治理已实现；V05另以004部分索引统计全部持久未终态资源，待核实资源即使缓存淘汰或重启仍计入[准入限额](admission.md)。

V06 增加固定旧请求摘要兼容、目录变更后的原键重放和执行快照、旧 live 缺失快照停核实，以及005版本栅栏拒绝旧迁移集合的测试；真实 PG 与全仓联合验收见 [021 台账](../changes/2026-11-13-021-model-integration.md)。这些测试只使用本地网关 fixture，不代表供应商付费接口已验收。

仍待完成邮箱验证/找回密码/MFA、工作区成员/RBAC、运营审计后台、备份恢复演练、生产迁移工具、账本支付、人工核实后台、HA 租约/outbox与容量测试。当前实现与后续生产要求应分别评审。[pgx 官方文档](https://pkg.go.dev/github.com/jackc/pgx/v5)、[bcrypt 文档](https://pkg.go.dev/golang.org/x/crypto/bcrypt)、[PostgreSQL advisory lock 语义](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS) 是相关实现边界的依据。

## V07 私有文件与006迁移

006新增参考元数据、上传幂等及有序任务引用表；复合外键同时约束task/reference/project/workspace，数据库也拒绝跨项目拼接。原图和缩略图先落盘，元数据、幂等与`reference.uploaded`审计同事务；审计失败回滚业务写入，孤立文件保留并计入总容量。

Task JSON保存operation和原参考ID列表；私有提交快照保存multipart正文文件ID、类型、大小和摘要，不存图片Base64。文件内容及边界在重启后保持原字节。006也作为格式栅栏：只有001—005迁移集合的旧程序拒绝启动，不能删除迁移登记强行回退。

升级/备份应停止写入后协调备份PG、REFERENCE_DIR和ASSET_DIR；恢复时使用匹配版本、匹配文件及原网关身份。单独恢复PG不保证编辑可重放。已受理正文缺失/篡改时停在待核实，不能重新编码后自动重发。当前本地升级前备份和006应用记录见[025](../changes/2026-11-13-025-reference-integration.md)；并未完成V18生产灾备演练。

## V09 数据格式与执行恢复

008新增事务Outbox/项目序号/持久feed；009新增任务领取租约及控制generation。旧程序拒绝新格式，当时停机维护工具要求001—009。升级必须联合备份数据库与reference/assets私有目录，保留007生成的storage UUID/owner绑定；禁止删迁移登记或owner强行降级。

恢复未完成run、无handle的SUBMITTING时也同事务生成持久通知。消费者checkpoint仅表示投影成功，浏览器仍需游标重放或窗口重置补读。控制写连接仍独占数据库，租约辅助连接不授权多实例控制面。见[事件协议](events.md)、[任务租约](scheduler.md)及[034验收](../changes/2026-11-13-034-durable-execution-integration.md)。

## V10 持久会话与010迁移

010新增项目私有会话、不可变消息节点、私有冻结上下文。受理新轮次的CAS、两个节点、Run、幂等、审计和Outbox同事务；提交确认不明时停止执行并由原键查询确认，不能推断失败后换键。消息树正文不复制到通知或指标，冻结执行上下文不通过公开API返回。不可变触发器禁止就地改写消息和执行上下文。

旧单轮记录保留，未完成流重启标记INTERRUPTED且不重发。新对话按分支祖先重建上下文，部分输出后明确继续才新增一轮。维护格式要求更新为001—010，升级前停机联合备份数据库、reference/assets和owner身份，不能删迁移号强行回退。实际杀进程、原键重放及继续证据见[038](../changes/2026-11-13-038-conversation-integration.md)，领域约束见[会话Wiki](conversation.md)。

## 聊天执行失权与换代

022 把每次首次领取的 owner、epoch、generation 与截止时间保存在原 Run 流头。重启时先提交新的控制代次，再重建未完成聊天的已提交前缀。持有旧授权的保存请求会被数据库拒绝。旧 021 的执行身份和回放水位可原样恢复，只有新请求建立租约。[决策 0022](decisions/0022-run-execution-leases.md)
