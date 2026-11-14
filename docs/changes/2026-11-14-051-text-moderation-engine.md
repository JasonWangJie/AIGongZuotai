# 051 · V14-B1文本审核引擎与私有适配契约

## 目的

为新受理的文本生成提供完整输入检查和输出审核判定，使无法确认通过的候选正文保持隔离；审核政策在受理时冻结，不能因配置变化或异常响应而默认为通过。

## 范围

V14-B1文本子批次，覆盖H01/H03/H06/H07/H09中的审核引擎、私有HTTP适配、冻结政策与不可变决定。默认关闭；rules只用于demo，HTTP为自有text-v1契约。真实渠道待条件，媒体完整审核为B2，本批启用时新媒体生成明确拒绝，历史资源不追溯。

## 实际文件

- `internal/studio/moderation.go`：配置/政策快照、固定summary/decision/capability、输入与输出校验、冻结政策/决定事务helper。
- `internal/studio/moderation_http.go`：有界私有HTTP客户端、严格text-v1绑定、无重试/redirect、错误归一化。
- `internal/studio/moderation_test.go`：规则/政策/上下文/大小边界，以及PG冻结政策/决定事务测试。
- `internal/studio/moderation_http_test.go`：协议绑定、隐私、畸形响应、超时/跳转/并发及端点校验。
- `internal/studio/migrations/015_text_moderation.sql`：私有冻结政策、状态约束及不可变决定历史。
- `config/moderation.example.json`：明确命名的demo fixture markers。
- `docs/wiki/moderation.md`、本台账：配置、协议、政策版本、隐私和实际边界。
- `docs/wiki/operations-access.md`、`docs/wiki/billing-operations.md`、`docs/wiki/storage-maintenance.md`：本批交付前同步当前CLI精确001—015栅栏，保留V14-A的014历史事实，补齐账务/权限/私有存储与审核隔离边界。
- `docs/wiki/backend.md`、`docs/wiki/development.md`：补齐公开能力与摘要、锁外审核及提交后正文门禁、环境配置和六套smoke/perf测试隔离。
- 主链路接入方独占App/Run/会话/持久化/恢复及其测试，根集成负责env/启动、CLI格式、README/索引/任务源和联合结果；本记录不冒充这些文件的独立实现台账。

## 接口与数据影响

新增`ModerationConfig{Mode,PolicyFile,Endpoint,APIKey,CredentialID}`和`moderationEngine`。`CheckInput`针对真实完整ChatRequest，仅ALLOW返回冻结政策；拒绝在生成受理前。`CheckOutput`使用原政策，固定返回ALLOWED/BLOCKED/REVIEW及闭集reason。policy最大64KiB、完整输入256KiB、候选输出128KiB，UTF-8/NUL和大小校验不通过时拒绝放行。

能力说明为enabled/mode/policy_id/policy_version/text_delivery/media_supported；公开结果summary仅含政策身份、output阶段、固定状态/原因及安全整数版本。原候选正文不保存于审核表。

HTTP需要显式服务端HTTPS端点与独立密钥；demo fixture只允许loopback IP的HTTP。固定10秒、4并发、8KiB响应，拒绝redirect、重试、Cookie/环境代理、未知或重复JSON字段。响应绑定request ID、原政策身份/摘要、phase与内容摘要。URL/密钥/原文不会写入政策、审计、日志或公开错误。

015表的policy不可更新/删除；PENDING→最终结果仅一次，版本+1，精确重复只读。`insertRunModerationSQL`/`saveRunModerationSQL`供主链路放入Run、会话、账务、审计及Outbox的同一事务。旧nil审核记录不追溯；适配身份变化和模式关闭不放行既有隔离内容。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `gofmt -w`，仅本批4个Go文件 | 通过 | 没有格式化他人正在修改的共享文件 |
| `go test ./internal/studio -run '^TestModeration' -count=1 -v` | 通过 | 9函数，含18项畸形响应子例，studio 0.259s；只访问本机HTTP fixture |
| 同一普通专项再次运行 | 通过 | 最终引擎代码0.222s；期间曾因并行core测试文件未完成而编译失败，接入方修复后通过，本批未改其文件 |
| 根统一PostgreSQL专项（`TestPostgresModerat…`） | 通过 | 根集成人执行，studio 8.853s；包含`TestPostgresModerationFrozenPolicyAndDecisionTransaction`的冻结policy、事务回滚、精确重放、终态不可覆盖和不可变历史，本子代理未占用PG窗口 |
| `node scripts/check-docs.mjs` | 通过 | 113份文档、54份台账，本地链接与必需章节有效 |
| 真实外部审核服务 | 未运行 | 用户尚未选型，HTTP fixture不替代真实渠道、语义效果或生产合规验收 |

普通专项覆盖完整历史messages、旧政策规则冻结、policy被篡改拒绝、关闭/凭据身份改变不放行、UTF-8/NUL/大小、全响应绑定、固定allow/block/review、未知/重复/别名/尾随/超大JSON、超时、无redirect和最多4并发。只读交叉复核主链路的局部候选缓冲、公开读取门禁、同事务决定、取消和重启路径未发现阻断；这不是新的执行测试。全量PG、主库升级和完整联合证据由根集成记录，不把普通专项冒充全量验收。

交付前文档同步按当前CLI实现、启动环境解析及`scripts/lib/process.mjs`逐项核对。根集成人已报告`npm run check`通过（前端237项测试）、真实PG全量170.241s、六套smoke与perf通过；这些是根执行的联合证据，本次文档同步未重新运行代码或PG测试，最终详细记录见[054](2026-11-14-054-text-review-integration.md)。

五份关联Wiki补齐后再次执行`node scripts/check-docs.mjs`，113份文档、54份台账通过；本次只改上述文档和本台账，未修改代码、任务状态或根导航。

## 风险与限制

有限字面规则只能演示，不是语义审核。HTTP text-v1需要真实服务单独实现和验证；服务原样回传身份只能验证响应绑定，不能证明其语义判断质量。没有媒体审核、人工放行、申诉或封禁。候选只存在内存，进程中断丢弃并进入REVIEW，不为找回候选重新生成；未公开不等于未产生供应商费用。旧文本与资产不追溯。

## 回滚方式

升级015前停止旧写者并联备数据库、私有目录与必要密钥；不删除政策、决定历史、迁移登记或owner绕过格式保护。关闭模式只阻止后续启用，不释放既有隔离结果。数据库退版需对应升级前联备，并先核对升级后产生的新业务与权益变化。

## 文档同步

本记录、`docs/wiki/moderation.md`及交付前补齐的运营权限、账务操作、离线维护、后端、开发五份Wiki由本模块同步；当前015要求与历史014验证分别标明。根README能力/最近变更、Wiki导航、台账索引、任务源/TODO及最终联合记录由根集成人合并。V14-B1验收不等于完整V14放行，V14继续in_progress，真实审核商尚未选型、真实渠道继续单独pending，B2尚未实施。
