# 054 · V14-B1 文本审核联合验收

## 目的

交付输入检查、冻结策略、生成后全文审核及安全展示的本地纵向链路。用户确认审核服务暂未选型，真实审核渠道后续单独验收。

## 范围

V14-B1 / H01/H03/H05/H06/H07/H08/H09；v0.13.0。B2媒体审核、真实文本渠道、V14-C与V11真实邮件仍为独立待办，不以本地规则冒充生产内容安全能力。

## 实际文件

根集成实际文件：

- `cmd/server/main.go`、`cmd/server/main_test.go`、`.env.example`：严格审核环境配置及验证。
- `internal/studio/ledger_admin.go`、`operations_admin.go`、`storage_maintenance.go`：离线管理精确001—015栅栏。
- `internal/studio/moderation_commit_test.go`：独立真实PG提交确认丢失专项，允许/阻断两种结果。
- `scripts/lib/process.mjs`、`smoke.mjs`、`runtime-smoke.mjs`、`catalog-smoke.mjs`、`reference-smoke.mjs`、`library-smoke.mjs`、`conversation-smoke.mjs`、`load.mjs`：隔离审核环境，不继承真实端点/密钥。
- `package.json`、`apps/workbench/package.json`、`apps/workbench/package-lock.json`：v0.13.0；`docs/api/openapi.yaml`：审核能力与摘要契约。
- `README.md`、`AGENTS.md`、`docs/wiki/README.md`、`docs/wiki/performance.md`、`docs/wiki/decisions/0011-text-review-gate.md`、`docs/planning/v14b1-contract.md`、`docs/planning/tasks.json`、`TODO.md`、`docs/changes/README.md`、本台账。
- `docs/wiki/assets/moderation-{browser,upgrade,validation}-v0.13-2026-11-14.json`、`performance-local-v0.13-2026-11-14.json`，桌面`moderation-phase13.jpg`/`moderation-input-phase13.jpg`/`moderation-blocked-phase13.jpg`，手机`mobile-moderation-phase13.jpg`/`mobile-moderation-blocked-phase13.jpg`。

核心引擎、生成交付、前端分别登记[051](2026-11-14-051-text-moderation-engine.md)、[052](2026-11-14-052-moderation-execution.md)、[053](2026-11-14-053-moderation-workbench.md)。本地联备、日志及一次性验证辅助脚本留在被忽略的`.runtime`，不提交凭据、数据库或构建产物。

## 接口与数据影响

默认关闭审核；启用须PG与账户。新增安全审核摘要、015冻结策略/决策记录。全文审核前不公开delta或历史正文，输入确定拒绝不发生成/冻结，生成后未放行仍按REVIEW处理。开启时新媒体请求明确不支持，旧请求幂等优先，旧资源无标记不追溯。

原幂等先于当前开关、目录和报价检查。输入外部检查在全局锁与事务外，重入核对实际上下文摘要；批准后的受理同事务。输出候选最多128KiB，只有ALLOWED且COMPLETED事务确认后写入公开正文与发布`run.snapshot`，不补发未审delta。审核version与资源seq分别单调；财务处置不会批准隔离正文。受限终态允许普通新问题，但不允许“从此继续”，服务端省略完整受限轮。

多轮冻结上下文沿用`studio_chat_contexts`；旧单轮保留prompt/模型快照及进程内实际ChatRequest，没有新增完整上游请求的持久化，不应泛称所有单轮上下文已冻结入库。

## 验证结果

2026-11-14根实际执行；详细[验证JSON](../wiki/assets/moderation-validation-v0.13-2026-11-14.json)。

| 验证 | 结果与范围 |
| --- | --- |
| `npm run check` | 通过：8治理、Go vet/普通测试/构建、237项前端/24文件（1.54秒）、严格TS及Vite（1.57秒）；JS gzip156.17kB、CSS23.18kB |
| `npm run test:db` | 通过：隔离真实PG全量，studio170.241秒、server0.036秒；覆盖身份/资产/权益/运营与新增审核 |
| `go test ./internal/studio -run '^TestPostgresModerat' -count=1 -v` | 隔离TEST_DATABASE_URL下五组通过，8.853秒；输入无副作用、ALLOW结算/BLOCK与REVIEW保留冻结、决策失败同事务回滚、恢复、不可变策略与决策 |
| `TestPostgresModerationUnknownCommitRecoversOnlyCommittedDecision` | 独立包2.418秒通过，ALLOW/BLOCK两子例；真实PG COMMIT确认被TCP代理丢弃时缓存/SSE不公开候选，App重启读取已提交结果，原键重放不再调用Provider；是App恢复与真实协议故障，非本批OS Kill |
| `npm run smoke`、`smoke:runtime`、`smoke:catalog`、`smoke:references`、`smoke:library`、`smoke:conversations` | 六套通过；父环境开启http审核且提供无效端点/策略，同时启用运营/权益/邮件，子进程明确隔离，不访问真实渠道 |
| `npm run perf:local` | 通过：memory/demo；bootstrap P95 22.12ms，任务创建+取消P95 11.98ms，demo首delta P95 56.26ms；不包含PG或审核负载 |
| OpenAPI YAML与本地引用/operationId核对 | 61路径、73操作、84 schema、344引用有效 |
| CUA桌面/手机与刷新 | 通过，下述实际链路；1440×1000与390×844无横向溢出，console warning/error为0 |

最终Go二进制与五份冒烟JSON、性能报告SHA256一致：`609e55e226b187081c4b1f369a373fcbb38c8670f07acb7ea533f3fa80405f9a`。`npm run todo:sync`、`check:todo`、`check:docs`、`check:changes`收尾通过：10模块/23批/11批整批已验收，113文档/54台账，变更集合472路径。当前仓库尚无首次提交，集合包含前批已有未跟踪文件，不能把472解释为本批新增。

浏览器使用本地Go+PG、demo Provider、rules与演示权益：输入命中规则明确拒绝、草稿保留、空会话仍0节点；输出命中规则后无助手正文/复制/继续，0.045056点保持待核实。随后同分支正常新提问成功，纳入1条/省略2条，整段批准后显示并结算0.000384点、释放0.044672点；旧受限回复保持隔离。刷新回默认图像页，再进入对话恢复4节点和正确审核摘要；新媒体报价禁用，旧示意资产继续按历史展示。浏览器未另测REVIEW变体，该分支由Go/PG/前端测试覆盖。[浏览器证据](../wiki/assets/moderation-browser-v0.13-2026-11-14.json)不包含账号或凭据。

014→015前停旧预览并联备到`.runtime/backups/pre-v0.13-20261114T044227603Z`：数据库dump189714字节，SHA256 `5e204d98c58c8b5c44e2cfd650dc8b2faae521e8e2da53eee34e9fd62619b429`，`pg_restore -l`可读；复制5个参考目录文件、2个资产目录文件。邮件关闭且无待发队列。升级后10资产/最高修订10、草稿v5、原会话2与12节点、10运行、10journal、4冻结、1钱包、3运营授权/1工单/1收据均未变化；新审核表0行，没有给旧资源补“批准”。详见[升级比对](../wiki/assets/moderation-upgrade-v0.13-2026-11-14.json)。这是备份可读与原地升级验收，没有宣称完整灾备恢复演练。

交付前修正已纳入同批：前端原先阻止受限终态后的普通新问题，现与后端整轮省略语义一致，新增测试后237项最终通过；没有修改已交付历史记录。

## 风险与限制

候选正文仅当前进程内存，崩溃丢弃，不自动重生成。本轮无人工重审/放行、实际审核商验收或多模态内容检查；依然单控制写者，无生产容量结论。

规则仅有限字符串匹配，不衡量语义准确率；text-v1为自有适配契约，不假设New API供应该接口。启用审核会延后首字交付至全文批准，HTTP审核有界并发4/10秒超时/8KiB响应，未做长稳态容量验收。开启时只限制新媒体生成，上传/旧资产不追溯。真实渠道需要审核商文档、服务端配置、受控内容及故障验收；用户已安排后续选型，本批不等待密钥。无真实AI、SMTP、支付或公网发布。

## 回滚方式

停写联备数据库和私有目录，保留已启用邮件密钥；旧程序不得绕过015格式栅栏。关闭审核影响新请求，不将旧隔离内容自动放行。回退需匹配版本完整联备，不删历史决策或迁移登记。

## 文档同步

README、Wiki、ADR0011、AGENTS、051—054台账、任务源和生成TODO同步。仅勾选V14-B1本地check；完整V14继续in_progress、completed_on为空，11/23整批已验收。真实B1渠道blocked、B2/C pending，V11真实邮件依赖未放行。下一批建议先B2媒体：冻结审核策略、私有候选产物、所有下载/缩略图/引用门禁、租约恢复与账务同事务，再做C申诉/封禁/风控；真实供应商验收独立保留。
