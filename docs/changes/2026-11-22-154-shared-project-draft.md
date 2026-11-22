# 154 · 项目共享草稿、多人保存冲突与原键收据

## 目的

获邀的用户从自己的会话打开指定项目草稿，读历史或按角色编辑。两个人从同一修订保存时，先提交的一份生效，另一人收到冲突，可以保留本地修改并比较最新稿。

## 范围

V21-A1 的 H01、H02、H08、H10。153 提供实名会话、项目角色及 032 成员收据表。共享范围限于原 003 草稿的节点、修订及本人的保存收据。原个人工作台继续采用该用户自己的工作区与项目。

## 实际文件

- `internal/studio/drafts.go` 共用原 003 当前稿、历史查询及加锁保存，通过同一事务写草稿修订、作者审计和项目 Outbox。
- `internal/studio/collaboration_draft.go` 注册项目共享草稿接口，按当前会话和当前成员角色读取或保存，并区分所有者与各成员自己的原键收据。
- `internal/studio/collaboration_draft_test.go` 覆盖个人与共享接口、并发编辑、降权和撤权、真实提交应答丢失后的原键查询。

## 接口与数据影响

以 `/api/v1/collaboration/projects/{project}/draft` 为基址。GET 返回当前稿，PUT 以 `Idempotency-Key` 和 `{expected_revision,nodes}` 保存，节点含 `id,title,note`。GET `/revisions?before_revision=&limit=` 分页读修订摘要，GET `/revisions/{revision}` 读固定版本，GET `/receipt` 通过原请求头查本人的原修订。共享路由使用路径项目，拒绝携带个人工作台的 `X-Project-ID` 或 `project_id` 查询参数。

读写事务重核实名会话和开放账户，锁稳定项目行后核对当前成员资格。所有者与编辑可保存，查看者可读稿、历史和本人的已有收据。撤权提交后的新读取覆盖该账户的所有会话。草稿仍有最多 40 个节点、每项目最多 1000 次修订；历史每页最多 50 条。保存时原键重放先于版本比较，重复请求不追加审计或 Outbox。同键不同正文返回 `IDEMPOTENCY_CONFLICT`，过期修订返回 `DRAFT_CONFLICT`。所有者继续使用 003 原键表，成员收据按项目、操作者、原键摘要三者查找。

## 验证结果

`go test -count=1 ./internal/studio -run '^Test(CollaborationDraftMemoryRequiresAccountStorage|DraftMemoryRequiresPostgres|DraftValidationAndBodyBounds)$'` 本地通过，0.532 秒。三项覆盖新旧草稿的存储条件、输入字段和大小限制。

真实 PostgreSQL 复跑命令为 `go test -p 1 -count=1 -v ./internal/studio -run '^TestPostgresCollaborationDraft'`。本项编写期间未连接数据库，实测由集成人串行执行。三项分别检查双账户和双工作区、独立成员与所有者同键、并发保存与个人项目/账本/生成隔离，真实等待降权或撤权事务的旧会话，以及 PG 已提交但应答丢失后只读原收据。

集成人在真实 PostgreSQL 上复验。双账户同修订与个人项目隔离 2.72 秒，提交确认丢失后的原键读回 3.37 秒，降权与撤权栅栏 2.89 秒。含真实浏览器与新库联备的 032 专项共 58.729 秒。

## 风险与限制

每次操作以项目行锁确定读写和撤权的先后。在撤权之前已返回的草稿可能留在客户端。并行编辑使用修订比较，需由用户处理冲突；此范围未提供多人光标、字符合并或离线自动重发。

## 回滚方式

停写并联备数据库和工作台所需的私有目录。回到旧格式须恢复升级前的匹配备份与软件。继续用 032 时，所有者可通过原个人草稿接口读回当前稿和历史修订。

## 文档同步

由集成人将本条及协作角色、共享草稿边界同步至 README、Wiki、OpenAPI、台账索引和任务源，并生成 TODO。
