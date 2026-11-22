# 运营权限、敏感认证与离线角色管理

关联批次：V14-A及V14-C1，横切H01/H06/H09。角色基础扩展至工作区新生成限制与复核；完整V14仍在进行中，账户封禁、内容重审及自动风控尚未完成。

## 启用及默认边界

`OPERATIONS_ENABLED` 默认 `false`。启用必须同时有 PostgreSQL 和 `AUTH_MODE=required`。注册、邮箱验证、工作区所有者身份均不会自动获得运营角色。演示内存模式没有运营管理员。

`GET /api/v1/auth/session` 的 `operations.enabled` 只说明当前环境是否开放运营入口，不承诺角色。`GET /api/v1/operations/access` 每次重新读取当前账户的有效授权，返回 `enabled` 和 `access`；每项为 `workspace_id/role/permissions/version`，不返回其他用户资料、密码或会话令牌。列表最多128条，超限显式失败，数据库不可用时拒绝访问。当前界面需在进入入口、切换身份及请求拒绝后重新核对访问状态。

## 工作区角色

| 角色 | 权限 | 能力边界 |
| --- | --- | --- |
| `observer` | `read` | 查看获授工作区的异常、工单及有界审计 |
| `operator` | `read/case_open/investigate` | 建立工单、核对原任务、无操作结案；没有财务处理权限 |
| `finance` | `read/case_open/finance` | 建立工单、按证据结算或释放待核实冻结；没有任务核对权限 |

| `moderator` | `read/generation_restrict/appeal_resolve` | 查看获授工作区生成限制及申请、预演并限制/维持/解除；无隐含财务或内容放行权限 |

同一用户可在同一工作区分别获得多个角色，权限取并集；不存在隐含 `admin` 或全站角色。运营目标工作区必须来自明确 `workspace_id`，不能由当前创作项目或其他账户身份推导。每个HTTP请求校验目标工作区授权，写事务再校验一次。没有权限与不存在的范围使用同类拒绝响应，减少作用域探测。

权限快照绑定 `grant_id/version/role/workspace_id/user_id/session_id/credential_version`，供操作预演摘要使用。角色撤销和再次授予递增版本，旧计划不因重新授予同名角色自动恢复。

独立工作区角色 `risk_reviewer` 可查看、预演及审议原报价的单次额度风控复核。授权命令中的角色名为 `risk_reviewer`。跨工作区的账户限制另按操作人与目标用户各自授权。[额度风控](decisions/0028-credit-risk.md) · [账户限制](decisions/0027-account-governance.md)

## 敏感执行

敏感执行请求携带当前密码，浏览器不会获得可转交的认证令牌。密码只在当前请求内处理，不存入计划、审计、幂等正文、日志或数据库。密码长度限制为10—72 UTF-8字节并拒绝NUL；复用账户认证的IP及用户限速和最多4个并行bcrypt槽位。

密码散列读取和bcrypt计算分开，bcrypt不占数据库事务或全局状态锁。核验后的私有内存proof有5分钟有效上限；执行事务按“用户→会话→授权→业务对象”顺序加锁，重新核对原密码散列、凭据版本、会话身份/有效期和角色。密码变化、会话撤销、角色撤销、工作区不符、缺失或过期proof均拒绝。普通读取、建立工单和预演使用实时权限检查；真正敏感执行额外使用密码proof。

审计只接受固定动作和资源种类、有界标识/原因码/证据引用，不接收提示词或任意正文，也不接收URL。审计与对应业务变化同事务，记录和离线角色操作回执禁止更新/删除。CLI角色变更没有登录态操作者，`actor_user_id` 为空，通过稳定操作ID、目标授权、原因和证据引用追踪。

## 离线管理流程

工具为 `go run ./cmd/operations`；只读取环境中的 `DATABASE_URL`，不自动读取 `.env`，不迁移数据库、不启动worker、不调用供应商。使用受控环境设置连接串，不放入命令参数、台账或截图。必须先停止工作台，再通过PG单写者锁及精确001..017版本栅栏。即使inspect也遵循停机规则。

1. 在普通账户完成注册后，获取明确的用户ID和目标工作区ID；不能以邮箱作为授权参数。
2. 停止服务，检查当前角色。inspect输出限定用户/工作区的至多4条授权，包含失效状态，不返回邮箱、姓名、凭据或数据库连接信息。
3. 使用稳定操作键、原因码及证据引用预演grant或revoke。默认执行真实事务并回滚，输出精确 `plan_digest`、目标授权和变更结果。
4. 核对后以完全相同参数加 `--apply --plan-digest` 执行。状态、凭据版本、数据库身份或授权容量变化会使原计划失效，需重新预演。
5. 确认回执后恢复服务，通过目标账户重新读取运营权限。

命令中的 `usr_...`、`ws_...` 为占位符，实际必须是对应前缀加32位小写十六进制ID：

```powershell
go run ./cmd/operations --action inspect --user usr_... --workspace ws_...
go run ./cmd/operations --action grant --user usr_... --workspace ws_... --role finance --operation-key role-20261114-001 --reason staff_assignment --evidence ticket:001
go run ./cmd/operations --action grant --user usr_... --workspace ws_... --role finance --operation-key role-20261114-001 --reason staff_assignment --evidence ticket:001 --apply --plan-digest <预演摘要>
go run ./cmd/operations --action revoke --user usr_... --workspace ws_... --role finance --operation-key role-20261114-002 --reason staff_departure --evidence ticket:002
```

提交结果不明时保留原操作键、参数与摘要，原样重放读取已提交回执；不能换键猜测重试。旧grant回执在后续撤权后重放仍只返回原回执，不会再次授予。相同键换角色/范围/原因被拒绝。已有active grant再次grant不会扩大权限，但仍有独立操作回执；revoke不存在的授权失败。

每用户最多128条active授权，每工作区最多1000条；没有批量通配授予。版本上限使用JavaScript安全整数，并为active grant保留下一次撤权修订。达到最终失效版本不能再次启用。

## 升级、验证与限制

013新增 `studio_operations_grants`、`studio_operations_audit`、`studio_operations_role_operations`；014由工单模块使用这些权限和审计。上线升级需停止旧写者并联备数据库、私有文件和所需密钥。不能删迁移登记或owner绕过格式栅栏；回退使用升级前联备和相应旧程序，不直接删除权限/审计表。

V14-A交付时采用001..014格式，其验收事实保留在[050集成记录](../changes/2026-11-14-050-operations-integration.md)。V14-B1增加015文本冻结政策与审核决定，当时离线角色工具要求精确001..015；历史结果见[054](../changes/2026-11-14-054-text-review-integration.md)。B2交付时增加016媒体政策、私有候选与决定，角色工具同步要求精确001..016，没有新增人工内容放行权限。

finance核实账务、operator核对原任务或关闭工单，都不能把隔离文本、图片或视频变为可见，也不能删除私有候选。有原handle但尚无候选、仍满足核对条件的停驻任务可以Query原任务；已有审核候选或最终决定不借此重新审核或生成。016联备包含数据库、私有目录、S3对象/收据、owner与必要密钥，关闭模式不会撤销证据或批准内容。完整边界见[文本审核](moderation.md)、[媒体审核](media-moderation.md)和[058](../changes/2026-11-14-058-media-review-integration.md)。

专项覆盖：默认无权限、跨工作区与多角色并集、实时撤权、非法scope不熔断数据库、密码散列/凭据/会话/时限重新核对、bcrypt并发上限、审计回滚、离线writer fence、预演、精确摘要、回执重放、撤权后重放不重授以及不可变审计。具体命令与实际结果见[047台账](../changes/2026-11-14-047-operations-access.md)，联合验收以集成台账为准。

当前仍为单控制写者、本地私有存储；没有在线角色授予、多因素认证、双人审批、超级管理员、组织继承或生产合规认证。本批HTTP不开放赠额、权益冲正或支付退款，既有全额权益冲正仍走停机ledger CLI。运营权限只开放本批工单业务，不授予普通用户的提示词、参考原件或私有下载接口权限。浏览器入口可见性不能替代服务端鉴权。

## V14-C1新增角色及当前格式

017扩展显式moderator角色与固定审计动作；角色、账本和存储维护CLI现要求精确001—017。既有角色不自动扩权，新角色也没有在线角色授予、资金退款或隔离内容放行能力。工作区生成限制是持久事实，关闭OPERATIONS_ENABLED不解除限制，普通用户仍可读取自己的状态和复核。详见[生成限制](generation-controls.md)、[准入边界](generation-admission.md)及[062集成记录](../changes/2026-11-14-062-generation-controls-integration.md)。

## 026 内容申诉

显式 moderator 才能看到私有申诉详情并预演、确认独立意见。finance 仅查看去敏财务队列，需要单独核实原工单。申请与复核不会改变账务，读取真实收据后才向本人展示释放金额。[权限与处置](decisions/0026-content-appeal.md)
