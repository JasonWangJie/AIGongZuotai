# 2026-11-14-070：媒体执行通道的独立集成验证

## 目的

通过真实 App、HTTP 受理、后台 worker 和 PostgreSQL 验证 V16-A2 的 submit/query 执行隔离，确认新的预算不会提前释放、重复提交或放行被取消的私有候选。

## 范围

关联 V16-A2、H03 模型与执行、H07 可靠性与性能、H09 工程与交付治理。仅新增独立测试和本台账；执行实现与核心回归由 068 负责，HTTP Transport 隔离由 069 负责，配置、指标与全仓联合验收由 [071](2026-11-14-071-media-lane-integration.md) 负责。A3 查询与导入/审核的持久移交仍未完成。

## 实际文件

- [internal/studio/media_lane_integration_test.go](../../internal/studio/media_lane_integration_test.go)：新增 5 个集成测试函数（内存 1、PostgreSQL 4）和本地受控 Provider。
- 本台账：保存本批独立验证、首次环境冲突及最终顺序重验事实。
- 不修改生产代码、原有测试、根 README、Wiki 索引、任务源或台账索引；根集成人负责同步。

## 接口与数据影响

无公共 API、产品行为或数据库格式变化。任务均从实际 HTTP 入口受理，PG 例先注册真实测试账户，使用后台固定 worker；只有模拟已提交点丢失的例子才修改该受理资源的持久状态。每例使用独立随机 schema 和临时私有目录，且不连接真实 AI、审核、邮件或支付渠道。

覆盖以下边界：

- 内存和 PG 各验证 submit=2/query=1：两条 Submit 同时占满时，另一个原 handle 可以进入 Query；Query 持续占满时，新 Submit 仍可进入。实际 Provider 峰值与总调用数分别断言，不把调度快照当网络并发。
- 槽位已满时，原键原输入通过 HTTP 重放仍返回原任务，不重复网络工作。慢 Submit 忽略取消直到受控返回；取消后预算仍占用，真正返回后下一任务才启动，迟到成功不能复活 CANCELLED。
- 真实 HTTP 受理后，故障注入无 handle 的过期 SUBMITTING；生产领取分支必须转入 SUBMIT_UNKNOWN，禁止再 POST，原键仍返回原身份。关闭 App 后用同一数据库/私有目录及 2/3 新预算重启，旧 cookie、原 body/key 仍可通过 HTTP 重放，后台轮询不再次提交或查询无 handle 任务。
- Submit 已实际返回但 Outbox 满时，保存事务等待期间仍占 submit 槽，下一任务不能偷用预算；清理注入背压后原 handle 成功保存、Query 只执行一次，不把背压误判为数据库宕机。
- 慢完整字节媒体审核占 query 槽期间，新任务的 Submit 与 handle 提交仍可完成。取消审核中及等待查询的任务后，迟到 ALLOW 不能公开资产、登记素材或开放私有下载；等待任务不自动进入审核。

## 验证结果

本批使用根已建立的独立本地 55433 测试 PG；忽略目录内的 wrapper 注入测试连接并隔离真实渠道配置，不覆盖用户开发库或原 `postgres.env`。

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./internal/studio -run '^TestMediaLaneIsolationMemory$' -count=1 -v` | 通过 | 首次内存实际双通道与迟到取消验证 0.098s；增加背压例后再次内存验证 0.092s。 |
| 首轮 `node .runtime/v16-check.mjs target '^TestMediaLane\|^TestPostgresMediaLane'` | 部分通过，环境争用 | 内存 0.06s、PG 双通道 1.85s、过期 SUBMITTING 1.69s 通过；B2 例启动时被数据库级控制写者锁拒绝，总 3.830s。另一代理同时使用同一 PG 数据库，不因 schema 不同而隔离该锁；没有修改锁或测试断言消除冲突。 |
| `go test ./internal/studio -run '^TestMediaLaneIsolationMemory$' -count=10` | 通过 | 10 次实际内存并发/取消重复验证，总 0.624s。 |
| 最终顺序 `node .runtime/v16-check.mjs target '^TestMediaLane\|^TestPostgresMediaLane'` | 通过 | 本文件 5 函数全部通过：内存 0.04s、PG 双通道 1.34s、过期 SUBMITTING 与改预算重启 1.73s、Outbox 背压 1.75s、B2 慢审核/取消 1.32s；同筛选额外包含根固定指标测试 0.03s，全命令总 6.255s。 |
| `go vet ./internal/studio`、`gofmt -l internal/studio/media_lane_integration_test.go` | 通过 | 新测试及当前 Go 模块静态检查成功，新增文件无格式偏差。 |

最终重验等待核心专项结束后独占测试数据库执行，没有并行 PG 写者。首次环境争用保留，未将其记为产品缺陷或完整通过；本文件没有为通过而削弱旧测试或修改生产代码。全仓 Go/PG、前端及文档治理由 071 集成后补充。

## 风险与限制

本地受控 Provider、HTTP 审核器和 Outbox 故障注入只证明这些状态边界，不代表真实供应商语义、生产吞吐、延迟 SLO 或长稳态容量。Query 预算本批仍覆盖查询、导入、审核及保存，不宣称 A3 已独立。PG 仍有数据库级单控制写者锁，随机 schema 不能让多个写者测试进程并行；不支持全站多实例。

## 回滚方式

本次仅新增测试和台账，无产品或数据库升级可回退。修正测试夹具时保留原失败事实，不删除取消、幂等、未知提交或私有候选断言来取得通过。生产回退遵循对应实现与集成记录，不能删除迁移、候选或 owner 标记。

## 文档同步

- README、Wiki 导航、台账索引、任务源和生成 TODO 由根依据实际联合证据同步。
- 契约：[V16 分解](../planning/v16-contract.md)、[V16-A2 执行隔离](../planning/v16a2-contract.md)。本台账提供独立证据，不自行勾选 A2 或完整 V16。
- Wiki 与实现边界由 068/071 同步，本记录保留测试范围及环境限制。
