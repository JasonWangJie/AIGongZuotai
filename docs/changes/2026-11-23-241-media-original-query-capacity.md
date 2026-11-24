# 241 · 媒体原任务的查询调度迟滞与容量对照

## 目的

V17（H03/H07/H08）需要分开测量冻结原 handle 的到期、实际 GET 发起、HTTP 响应和终态完成。原方案的 60,000 个在途、10 秒周期和 200 毫秒平均响应意味着 6,000 查询/秒及约 1,200 并发。

## 范围

在相同的 40 次/秒预定到期序列上，对照 2 和 16 个查询 worker，固定同工作区和渠道预算为 16。使用真实生产选择器、查询 worker 和 HTTP 适配器。内存与 PG 各有独立入口；PG 保留投影触发器、租约、续租、状态落库及最终独立 SELECT。

## 实际文件

- `internal/studio/media_query_capacity_test.go` 包含显式启用的测量、原 handle 夹具与分位数、漏重检验。
- `docs/wiki/media-original-query-capacity.md` 给出指标原文、现有连接与预选瓶颈、复现命令和解释。
- `docs/wiki/assets/media-original-query-capacity-2026-11-23.json` 保存两档内存实测聚合。
- 本台账与集成人维护的任务源、导航和进度同期登记。

## 接口与数据影响

仅测试在本机私有回环服务处理已登记身份的 GET，冻结计划先于所有执行。PG 夹具由测试连接在独立 schema 的单个事务预置已经受理的记录，不调用模型受理、额度或生成 POST。每个 GET 返回可确定的失败终态，以核对一项一次。生产接口、配置和迁移均沿用现状。

## 验证结果

- `STUDIO_MEDIA_QUERY_MEASURE=1 go test -v -count=1 ./internal/studio -run '^TestMediaOriginalQueryCapacity$'` 通过，18.707 秒，每档 128 个真实 GET 与成功领取，操作全程 0 POST、0 遗漏、0 重复。2 worker 实测峰值 2、9.83 次/秒、到期至写出 P95/P99 9206.32/9637.49 毫秒；16 worker 实测峰值 10、37.75 次/秒、P95/P99 31.77/38.97 毫秒。
- `go test -v -count=1 ./internal/studio -run '^(TestMediaExecutionConfigurationAndSeparateBudgets|TestAsyncMediaRejectsChangedSavedIdentity|TestMediaRetryAfterAndConfiguration|TestMediaOriginalQueryCapacity|TestPostgresMediaOriginalQueryCapacity)$'` 未启用测量时 3 个回归通过、2 个容量入口按设计跳过，0.558 秒；`go vet ./internal/studio` 通过。
- 隔离开发副本未连接 PG。PG 档可通过 Wiki 中的显式命令由集成人串行运行。

## 风险与限制

报告反映本机一个项目、单一渠道、128 项固定到期的一轮查询。真实供应商、工作区混合、公网网络抖动、真实业务受理、连续十秒轮询周期、6,000 QPS 和专用环境 24 小时稳态需要独立验收。测试默认跳过。生产查询 lane 当前上限为 32，同一宿主 200 毫秒 HTTP 均值对应理想约 160 查询/秒，实际还需预算、领取和保存时间。

## 回滚方式

移除本次新增的测试、报告、Wiki、台账入口与对应任务源记录。生产业务与数据库格式无需迁移。

## 文档同步

集成人同步根 README、Wiki 索引、台账索引、进度与任务源，生成两份 TODO。V17 全量容量与 24 小时稳态维持原验收条件。

## 集成人 PostgreSQL 实测

隔离 PG 16.14 开启 fsync 与同步提交，每档 128 个原 handle 均恰好一次实际 GET、零 POST、零异常。在每秒 40 个到期请求下，2 个 worker 完成 9.48 次/秒，P95/P99 迟滞 9672.21/10117.46 毫秒；16 个 worker 完成 37.77 次/秒，P95/P99 34.67/48.04 毫秒，峰值并发 10，平均 HTTP 响应约 200.43 毫秒。专项 23.14 秒。[完整数据](../wiki/assets/media-original-query-capacity-2026-11-23.json)
