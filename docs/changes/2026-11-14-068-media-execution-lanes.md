# 2026-11-14-068：V16-A2 媒体提交与查询交付预算隔离

## 目的

原媒体 worker、到期领取与连接容量混用，慢提交可能占满已受理任务的查询执行位置。新增独立 submit 与 query+交付通道，确保一侧慢调用不会用尽另一侧执行预算，同时保留未知提交、租约、内容审核和权益不变量。

## 范围

关联 V16-A2、H03/H07。覆盖内存与 PostgreSQL 的工作线程、执行预算、租约连接、领取 SQL 和既有单步辅助入口；不实现独立下载/审核持久阶段、多租户媒体公平、token 持久回放或多实例控制写。

HTTP 适配池由 069 独立实施，实际 App/PG 验收由 070 独立代理负责，根配置/指标和联合收口由 071 登记；本台账不把这些代理的计划当成通过证据。

## 实际文件

- `internal/studio/media_execution_lanes.go`：有界配置、预算、统计、内存到期选择及两组 worker。
- `internal/studio/media_execution_lanes_test.go`：配置、预算、内存慢调用隔离、取消、核对迟结果、未知提交及 PG lane/连接/领取门禁。
- `internal/studio/app.go`：配置归一、HTTP 并发传参、两组启动、去单队列派发及内存版本/attempt 门禁。
- `internal/studio/task_leases.go`：独立连接池、同一路径配置归一、冻结 handle 的 lane SQL 过滤；不改迁移。
- `internal/studio/task_leases_worker.go`：预算先于领取、兼容步骤入口受限、完整执行返回后释放。
- `internal/studio/task_leases_test.go`：原 uncached PG Query 证明保留；删除的内存队列结构断言改为无内存 attempt、两个 durable lane 各执行一次。
- `docs/wiki/media-execution-lanes.md` 与本台账。

## 接口与数据影响

新增 `Config.MediaScheduling{SubmitWorkers,QueryWorkers}`，公开环境为 `MEDIA_SUBMIT_WORKERS`、`MEDIA_QUERY_WORKERS`，默认 2/2、各 1—32。旧内部 `TaskWorkers` 按向上/向下取半且各至少 1 拆分；旧值 1 在双通道下为 1+1。未设置与非法显式配置分开处理。

`MediaExecutionSnapshot` 只提供 Submit/Query 的 InFlight、Limit、Steps；没有动态身份字段，不产生用户事件。InFlight 是保留预算，Steps 是进入执行的已领取步骤，不等于网络请求数。两个阶段用已持久的 handle.ID 划分；原公开任务状态、版本、API、受理幂等、账务与格式 017 不变。

内存缓存就是有界到期队列，不另建无界等待；PG 继续使用持久到期队列。领取前预算不足或连接暂缺时只跳过本轮，不修改租约或 SUBMITTING。全部网络、下载和审核在业务锁外，预算覆盖终态背压和实际清理退出。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./internal/studio -run '^TestNonexistentA2Compile$'` | 通过，0.042s | 接口接线首次编译通过；该选择不执行测试。 |
| `go test ./internal/studio -run '^(TestMediaExecution|TestMediaLaneMetrics)' -count=1 -v` | 通过，0.339s | 四项核心普通测试加根实际双通道指标测试；验证取消不提前释放、内部辅助入口不绕限、核对后迟结果不覆盖、未知提交不重发。 |
| `go test ./internal/studio -run '^(TestRun|TestTask|TestConversation|TestBounded|TestChat|TestModeration|TestMedia|TestAdmission|TestCancel|TestBilling|TestCatalog|TestReference|TestAsset)' -count=1` | 通过，7.529s | 普通相关回归，不选择 TestPostgres，不作为 PG 验收。 |
| `node .runtime/v16-check.mjs target '^(TestPostgresTaskLease\|TestPostgresMediaModeration)'` 首轮 | 失败，26.782s | 与独立代理随机 schema 测试同时争数据库级控制写锁，早期 B2 用例报 writer lock unavailable；租约等后续通过不能代替整组通过。根协调为完全串行，不修改锁。 |
| 同组加新 lane PG 测试的第二轮 | 中止并修正 | B2 ACK fixture 直接构造 App，未经过 New 归一，零值 MediaScheduling 形成零连接池等待。已中止自有进程；initTaskLeases 也使用相同归一与预算初始化，保留所有故障注入断言。 |
| `node .runtime/v16-check.mjs target '^(TestPostgresTaskLease\|TestPostgresMediaModeration\|TestPostgresMediaExecutionLaneFilters)'` 修后串行专项 | 通过，37.077s | 20 函数：新 lane 1、B2 8、租约 11；包括真实丢 COMMIT ACK 三阶段、七个图/视频/PARTIAL分支、心跳/epoch、取消/核对、候选重领/恢复、无缓存实际 worker、终态背压、连接隔离及满预算零领取。使用隔离 55433 测试实例，无真实外发。 |
| 所属文件 `git diff --check` | 通过 | 没有空白错误；行尾 CRLF 提示保留，不作失败处理。 |
| `go vet ./internal/studio` | 通过 | 核心及当前并行集成 Go 静态检查完成，无诊断。 |
| 全量 PG、构建/vet、文档和任务治理 | 待集成 | 由根在全部文件冻结后统一运行，本子项不冒用旧批次证据。 |

## 风险与限制

查询通道仍含私有下载导入与审核；慢下载影响该通道，尚不是三段隔离。现有 009 混合到期索引仍可能扫描另一通道记录；没有大样本 EXPLAIN 或扫描成本隔离证明，A3 再以实际分布评估两个部分索引和通用预备计划。预算是当前单写者进程内限制，不是跨实例共享配额、QPS 或生产吞吐。没有新迁移，不能凭本批标记完整 V16 完成。

## 回滚方式

受控停机，保留 PG、私有目录和已启用队列密钥后回退至支持现有 017 的上一匹配二进制及配置，移除两个新增环境变量。本批没有数据格式降级；不得删迁移、owner、候选或账务记录。无 handle 的 SUBMITTING 继续未知隔离，原 handle 继续按恢复规则查询，不因回滚而重新生成。

## 文档同步

- README、Wiki 索引、台账索引、任务源/TODO和进度由根统一同步，避免并行覆盖。
- 本子项新增[媒体执行通道](../wiki/media-execution-lanes.md)，说明默认/旧调用兼容、领取/释放、统计、索引和三段拆分限制。
- 与媒体 HTTP 池 069、独立执行验收 070、根联合收口 071 交叉登记；V16 仍进行中。
