# 123 · V14-C2b 账户限制、本人申诉和指定账户处置

## 目的

使受限用户可以在重新登录后获知具体原因和提交个人陈述，并保证解除仅作用于经授权的原账户及原轮次。

## 范围

2026-11-22，关联 V14、H01 身份与权限和 H06 审核与运营。本批在 PostgreSQL 上按账户保存限制和救济。每次运营处置必须同时具备指定目标的有效授权、当前会话、已核验密码和对应的短期计划。

## 实际文件

- [027 增量](../../db/migrations/027_account_governance.sql) 建立 `operator_user_id + target_user_id` 唯一授权、离线原键收据、账户限制轮次和状态、本人申诉、计划/执行收据及账户专属审计。在线审计保存实际主体，离线授权事件通过授权 ID 关联原操作者与目标。既有工作区 moderator、owner 与账户授权分开。
- [状态与申诉](../../internal/studio/account_governance_store.go) 在稳定用户行上读取当前状态和处理本人原键。缺控制行时返回 `OPEN@1`。受限者在本轮有且仅有一次申请，原因与最多 1024 字节陈述独立保存。列表隐藏陈述与公开答复，已授权详情提供两者。本人原键绑定目标、轮次与完整输入，保存键的 SHA-256。分页最多 50 条，游标绑定操作者、目标、读者身份及状态。
- [运营预演与确认](../../internal/studio/account_governance_action.go) 支持 `suspend`、`uphold`、`lift`。先检查指定账户授权，再按 ID 顺序锁住操作者和目标两行，重核会话、操作者账户状态及授权。计划冻结数据库、两方凭据版本、原授权修订、会话、动作、限制修订及该轮完整开放申请集。预演在 SAVEPOINT 内实际运行并回滚。确认需当前密码 proof 和精确摘要，业务、目标旧会话撤销、审计、计划消费和收据同事务。
- [存储测试](../../internal/studio/account_governance_store_test.go) 覆盖同键异参、跨目标查询、维持答复、闭环后的旧轮次、权限撤销又恢复、旧会话、持锁跨过期。认证门禁、离线授撤与前端由本批相邻记录说明。

## 接口与数据影响

首次限制建立独立轮次并将版本递增为 2。维持决定写入本人申请，令其成为 `RESOLVED/UPHELD`。解除要求原轮次及完整开放申请集合，已有开放申请可得到 `LIFTED` 答复。设限与解除均撤销目标的旧会话，新的登录读取当下权限。既有项目、任务、输出审核、财务冻结及已执行计费走原处理。

每目标最多 1024 轮、全库最多 100000 轮，申请和执行收据依附于实际轮次。为解除保留状态版本；每目标每种操作只保留最近的未执行预演。已消费计划和正式回执保留。公开状态只显示本轮通知、原因和版本，私有证据引用不会进入状态、申请页或项目通知。

## 验证结果

- `go test ./internal/studio -run '^TestAccountGovernance' -count=1 -v` 通过，包耗时 0.233 秒。验证输入上限、非法动作、开放申请集摘要及公开计划字段。
- 集成人串行执行真实 PG。首轮 `TestPostgresAccountGovernanceRoundAppealsAndScopedReceipts` 用时 4.00 秒，`TestPostgresAccountGovernanceWaitPastDeadlineRollsBackSessionRevocation` 用时 5.15 秒，两项均通过；同组升级和浏览器/恢复未通过。随后修正复验，两项存储专项分别用时 3.67 与 3.72 秒通过，026 升级通过，浏览器单独排查。证据见 `.runtime/v14c2b-first-focused.log` 与 `.runtime/v14c2b-fixed-focused.log`。
- 集成人复验 `TestPostgresAccountGovernanceBrowserAndRestore` 通过，用时 18.76 秒，见 `.runtime/v14c2b-browser-diagnostic.log`。

## 风险与限制

账户治理依赖持久账户模式，运营入口还需显式启用与逐目标授权。未在生产环境处理账户。自动额度风控、真实审核渠道和外部收费独立验收。

## 回滚方式

停机并恢复同一 026 时点的数据库、私有目录和所需密钥、对象版本，切换前核对新增限制、申诉、正式收据与授权。

## 文档同步

集成人同步[根 README](../../README.md) 最近变更、[Wiki](../wiki/README.md) 账户与运营说明、[台账索引](README.md)、[任务源](../planning/tasks.json)、生成的 TODO 与[推进记录](../planning/progress.md)。
