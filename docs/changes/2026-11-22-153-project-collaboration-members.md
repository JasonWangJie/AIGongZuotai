# 2026-11-22-153 项目定向邀请、成员授权与原键收据

## 目的

允许已有账户明确接受一个项目的站内邀请，获准后只进入该项目的协作草稿。所有者可以查看成员、撤回未接受邀请、将编辑者降为查看者或撤权。客户端在操作响应未知时，可读取本人原操作的确定结果。

## 范围

V21-A1 的 H01 身份与权限和 H02 项目与创作数据。覆盖站内定向邀请、共享项目发现、当前成员授权及草稿作者收据的数据结构。共享草稿内容与版本接口、工作台、双账户浏览器和联合恢复由同批其他台账登记。真实邀请邮件属于单独验收。

## 实际文件

- `db/migrations/032_project_collaboration.sql`
- `internal/studio/collaboration_members.go`
- `internal/studio/collaboration_members_http.go`
- `internal/studio/collaboration_members_test.go`
- `internal/studio/collaboration_members_transactions_test.go`
- `docs/changes/2026-11-22-153-project-collaboration-members.md`

## 接口与数据影响

- 普通登录会话的工作区和项目列表维持个人范围。本人可通过 `GET /api/v1/collaboration/invitations` 与 `GET /api/v1/collaboration/shared-projects` 分别查尚未过期的待接受邀请及当前共享项目。只有宿主工作区实际所有者可读 `GET /api/v1/projects/{project}/collaboration/members`。这些入口仅显示项目名／ID、定向账户 ID、邀请人显示名、角色、状态、邀请身份与版本。接受不透露宿主邮箱、钱包、其他项目或成员会话。
- 所有者在项目的 `collaboration/invitations` 提交一个已知目标账户 ID、角色及预期版本（首次为 0）。收件人显式向 `/api/v1/collaboration/invitations/{invitation}/accept` 提交当前版本。所有者在 `collaboration/members/{user}/{withdraw|downgrade|revoke}` 提交邀请 ID 和预期版本。每次变更带稳定键，操作 JSON 限 4 KiB。状态为 PENDING、ACTIVE、WITHDRAWN、REVOKED；72 小时过期由数据库时钟计算为 EXPIRED，新轮次带新邀请 ID。过期或已关闭轮次可以重新邀请；升为编辑者需要结束旧授权并重新接受。editor 可降为 viewer，viewer 和 editor 都可撤权。
- 032 增加 `studio_collaboration_members`、`studio_collaboration_receipts`、`studio_collaboration_draft_keys`。每项目最多 64 条当前关系，每位受邀人跨项目最多 128 条。接受生效的成员和未过期的待接受邀请共享限额。首次邀请和重新邀请均在项目与受邀人锁内通过有界索引查询核对两侧名额。撤回、撤权或过期后可立即邀请其他账户、接受其他项目的邀请，历史轮次与收据保留。同一关系最多 128 轮、512 个状态版本，新轮次为之后的降权和撤权保留容量。
- 所有者成员列表先显示全部当前关系，再显示最近 16 条历史。按 `?target_user_id=<明确账户 ID>` 可只查该账户与本项目的关系及最新版本；从未建立关系时返回空列表。
- 每次操作在同一事务重新核验真实会话、账户状态及当前项目角色。邀请签发按固定顺序锁定相关账户，再锁稳定项目行；共享草稿授权在取得项目锁后另读当前成员版本，包含等待期间提交的降权或撤权。接受的权限绑定账户，对本人所有仍有效会话生效。普通资产、任务、账务、其他项目及原个人工作区继续使用原授权。
- 管理收据使用当前用户、操作类别与原键 SHA-256，保存完整请求摘要和已经提交的结果。`GET /api/v1/collaboration/receipt?operation=…` 只读本人原键；旧接受收据反映当时的 ACTIVE 版本，实际共享权限始终读取当前成员状态。成员草稿键另按项目及当前作者隔离，并引用原草稿修订。003 原所有者草稿键不迁移、不回填、不更名。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./internal/studio -run '^TestCollaborationMembershipBodyLimitsAndProtocol$' -count=1` | 通过，完整成员专项编译后 0.305 秒 | 操作字段、显式版本、非法 JSON、大小上限。 |
| `TestPostgresCollaborationMembershipLifecycleAndPrivateInbox` | 真实 PostgreSQL 通过，3.52 秒 | 三方真实账户及收件人第二会话、定向收件箱、接受／降权／撤权／重邀、旧键不恢复权限，宿主个人 API 拒绝。 |
| `TestPostgresCollaborationExpiredRoundAndCapacityExit`、`TestPostgresCollaborationFullProjectAndRecipientRetainRevocation` | 待集成人串行运行 | 数据库真实到期行、128 轮上限与退出、满额降权及撤权后立即换不同对象，重新邀请也受当前名额限制；到期释放名额后重邀仍须重新接受。核对当前关系排序、历史上限和精确查询。 |
| `TestPostgresCollaborationConcurrentAcceptOrWithdrawHasOneReceipt` | 真实 PostgreSQL 通过，2.60 秒 | 两个真实连接等待同一项目行，收件人接受与所有者撤回竞争，唯一胜方留下收据。 |
| `TestPostgresCollaborationLostAcceptAcknowledgement` | 真实 PostgreSQL 通过，3.05 秒 | 针对接受事务丢弃真实提交确认，重开工作台后用收件人的原键核对唯一接受结果，邀请人不能读取该收据。 |
| `TestPostgresCollaborationUpgrade031PreservesAuthorAndLegacyDraftKey` | 真实 PostgreSQL 通过，1.68 秒 | 从旧 031 数据库升级，比对 003 草稿及原键、031 分镜和作者确认，032 新表为空；旧 031 写者拒绝新格式。 |

## 风险与限制

收到邀请的一方必须已有平台账户，并自行向邀请方提供账户 ID。到期按查询和受理时的数据库时间投影。当前分享范围限项目草稿；角色与历史记录上限适合本机受控协作，完整工作区团队、外部访客及真实邮件仍待独立验收。

## 回滚方式

停写后保全 032 数据库、私有目录和身份、已启用的密钥及对象版本。回到 031 时，使用升级前配套联备恢复到独立环境，核对备份点后的新邀请、撤权及共享草稿修订，再决定业务重开。保留 032 收据供核查，禁止通过删除迁移登记启动旧写者。

## 文档同步

集成人统一更新 README、Wiki、台账索引、任务源、生成的 TODO 和推进记录，登记实际验证的项目范围、账户边界和外部条件。
