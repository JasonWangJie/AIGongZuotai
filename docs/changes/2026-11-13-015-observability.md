# 015 · V05 就绪检查、私有指标与请求诊断

日期：2026-11-13。关联 **V05 可观测与资源准入**，覆盖 H01、H07、H09；批次完成状态由集成人按[任务源](../planning/tasks.json)验收。

## 目的

区分进程存活和服务可接单，提供当前工作区真实容量和受保护的全局缓存指标；使错误具备可反馈编号，并保持标签、日志与连接内存有界。消除每个 API 请求额外 Ping 数据库，以及默认逐请求日志 I/O 的成本。

## 范围

HTTP 中间件、healthz/readyz/runtime/metrics、独立运维令牌、固定标签统计、SSE 工作区连接准入与释放、慢订阅观测、文件流快速路径、OpenAPI 和验证。生成准入与配置接线由 [014](2026-11-13-014-resource-admission.md)记录，前端反馈与联合验收由各自本轮台账记录。

## 实际文件

- [internal/studio/observability.go](../../internal/studio/observability.go)：固定词表计数/直方图、安全日志、响应器包装、就绪/运行/指标端点与令牌校验。
- [internal/studio/observability_test.go](../../internal/studio/observability_test.go)：11 个观测测试函数及配置子例。
- [internal/studio/http.go](../../internal/studio/http.go)：端点注册、缓存就绪前置检查、SSE 工作区槽位和错误 request_id/Retry-After。
- [docs/api/openapi.yaml](../api/openapi.yaml)：v0.4 health/runtime/metrics、HTTPError 与准入错误契约。
- [docs/wiki/observability.md](../wiki/observability.md) 与本台账。
- `app.go` 的 obs 字段、慢订阅钩子、Config 与 `cmd/server` 环境接线由资源准入代理在 014 中登记；根 README、索引和 TODO 由集成人统一同步。

## 接口与数据影响

healthz 改为仅进程存活，数据库故障时仍 200；新增 readyz 读取最近已观测状态。API 前置检查不再逐请求 Ping，实际 SQL 与独立心跳仍失败关闭。新增 runtime 只返回当前工作区的完整占用，不能查询其他工作区；全局 metrics 默认 404，仅独立 `METRICS_TOKEN` Bearer 可读，账户 cookie 不授权。

所有经 HTTP handler 的响应有服务端生成 X-Request-ID，JSON 业务错误附 error.request_id。准入与订阅上限错误附 Retry-After:3。SSE 增加工作区上限且保留全局128连接上限，释放时清除对应工作区计数。指标与统计为内存态，重启清空；无新增数据库迁移，依赖 014 的准入查询与部分索引。

全局资源指标明确为 `cached` 缓存视图，不冒充完整 PostgreSQL 存量；工作区 runtime 的 PG 占用仍覆盖已淘汰待核实记录。原始身份、正文、URL 或凭据不进入日志/指标标签。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| 观测前 8 组非 PG 专项 `go test ./internal/studio -run 'TestObservability\|TestHealthAndReadiness\|TestMetricsAuthentication\|TestRuntimeWorkspace\|TestSSEObservation\|TestObservedWriter\|TestSlowSubscriber\|TestDefaultHTTPLog' -count=1` | 通过 | 配置、固定标签/脱敏、健康、隔离、流写期限、慢消费者与默认日志成本 |
| `go test ./internal/studio -run TestPostgresObservability -count=1 -v`，通过本地 env 加载器注入 TEST_DATABASE_URL | 通过 | 1.342 秒；两账户占用隔离，实际写者连接断开后独立检测与 health200/ready503，未输出凭据 |
| 代码审查后的 `go test ./internal/studio -run 'TestObservedWriter\|TestSSEAdmission\|TestSSEObservation' -count=1` | 通过 | 新增 ReaderFrom 直接转发/大内容保真/回退/超时，以及 TryLock 证明拒绝 writer 不持全局连接锁；共4组 |
| `go vet ./...` | 通过 | 最终响应器与 HTTP 修改后执行 |
| `node scripts/check-docs.mjs` | 通过 | 39份文档、17份台账；本地链接和必需字段有效，不替代语义审阅 |
| OpenAPI 严格 YAML 键与本地引用核对 | 通过 | 25路径、29操作、94本地引用；无重复键或断引用 |
| Prometheus 文本与 Go HTTP 契约核验 | 通过 | 一手规范核对累计桶/+Inf/TYPE/LF；测试验证 Flush/Unwrap/原始写期限及 ReaderFrom |
| 全仓库、真实 PG 与浏览器联合验收 | 集成人独立记录 | 见 [017](2026-11-13-017-runtime-integration.md)，专项结果不代替整批验收 |

## 风险与限制

就绪是最近已观测状态，不保证故障一秒内可见；心跳周期、数据库锁排队和5秒SQL超时可叠加。metrics 全局资源数只含有界缓存，不能用于完整持久任务对账。SSE handler 时长包括连接寿命，不能作为首字延迟。默认只有5xx Warn，未提供集中日志/采集/告警服务。本批没有真实供应商探测、生产万级压测或分布式准入。

## 回滚方式

关闭指标时清空 METRICS_TOKEN 并重启即可返回404，不影响数据库内容。回退本模块代码须同时回退 app Config/obs接线与前端 runtime 调用，重新构建并执行健康与 SSE/文件传输测试；无需删除数据库或用户文件。若恢复旧健康语义，还需同步部署探针和启动脚本，不能把旧 healthz 当作新 readyz。

## 文档同步

- README：由集成人更新 v0.4 当前能力、配置、验证和最近变更链接。
- Wiki：新增[运行状态与可观测](../wiki/observability.md)，与[资源准入](../wiki/admission.md)区分全量/缓存统计边界。
- OpenAPI：新增3个路径并更新healthz、错误及SSE/创建准入响应，保留资源内部错误与HTTP错误的字段区别。
- 索引、任务源与 TODO：由集成人按 V05 实际联合证据同步，不以本专项测试直接勾选整批。
