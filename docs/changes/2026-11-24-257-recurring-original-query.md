# 257 · V17 真实受理任务的多轮原 GET 短档

## 目的

已有 040 三档曲线在 512 个原 handle 上各测量一次确定失败的终态 GET。方案按 100 个受理/秒、平均 600 秒在途、每 10 秒一次 GET 估算 6,000 GET/s。补测同一个真实受理任务多次重新到期的行为与迟滞。

## 范围

V17 本机重复轮询短档。采用独立 PostgreSQL 测试 schema、单写者、环回 HTTP 服务、64 个查询位和 4 条查询租约连接。两名实际注册用户在各自工作区经正常任务 API 获得共 25 份 202 回执，再以原键核对回执。

## 实际文件

- `internal/studio/media_query_recurring_test.go` 增加显式开关的真实受理与三轮轮询测量、按轮分位数、按连续三秒统计以及窗口边界回归。
- `docs/wiki/media-original-query-capacity.md` 增加试具口径和实库入口。

## 接口与数据影响

测试复用实际受理、冻结请求、上游 POST、公平选择、严格领取、原 GET、续租与结果提交路径。每个合法 GET 由同一环回服务至少处理 200 毫秒，正常 worker 自行查询、重新入队。测试旁路只读已提交状态。每份正常 API 受理和重复原键回执都核对所属工作区。生产配置、接口、写入行为和 001—040 迁移沿用原实现。

## 验证结果

- Go 1.26.11 清空 `TEST_DATABASE_URL` 后执行 `go test -v -count=1 -run '^(TestRecurringQueryWindow257|TestQueryCurveBacklog040|TestLargeQueryLimits040|TestMediaAdmissionCapacityFixture|TestMediaScheduler.*)$' ./internal/studio`。8 项通过，含跨窗归类、末尾不足一窗和提前写出拒绝。
- 同一隔离目录的 `go vet ./cmd/server ./internal/studio` 和 `go build ./cmd/...` 通过。
- 在测试环境指定本机回环 `TEST_DATABASE_URL`、设置 `STUDIO_MEDIA_QUERY_RECURRING=1` 后执行 `go test -p 1 -count=1 -v -timeout=3m ./internal/studio -run '^TestPostgresRecurringOriginalQuery257$'`，输出聚合 `MEDIA_QUERY_RECURRING_257`。实库结果由集成人串行复测后登记。
- 期望 25 次唯一原 POST，其中一次 202 无 handle 且为 SUBMIT_UNKNOWN。另 24 个 handle 各三轮共 72 次原 GET。三项必须以明确上游错误提交 FAILED，21 项须提交 RUNNING，原租约释放且下次到期在未来。最后再次保留 3.25 秒核对没有意外外发。
- 每轮核对数据库中真实任务版本、冻结渠道、租约代次、提交进度和完整原 handle。客户端使用 WroteRequest，网关独立记录到达和返回。报告区分受理回执耗时、真实到期至写出、当前全部门限至写出、写出至响应头、网关处理时长，每个真实时间窗另列到期/写出/响应。窗口终点为独立查询首次看见全部末轮已提交状态，包含最多一轮常规采样等待及查询时间。

## 风险与限制

末轮明确终态均为供应商回复失败，本档没有生成产物。末轮仍在运行的任务按 fixture 返回的 60 秒服务方等待期排入下一轮。前三轮采用生产实时调度和进度退避。每个原 GET 在环回服务内额外进行一次只读核验，会增加本机数据库负载。25 份 API 回执、三轮和 200 毫秒服务时间均为本次短档的实际边界。60,000 项、10 秒稳态间隔、600 秒平均在途及 6,000 GET/s 仍需固定容量环境验证。

## 回滚方式

撤回本次新增测试与专题文档。日常测试默认跳过此显式开关，生产服务没有新增开关。

## 文档同步

专题 Wiki 已列入口。集成人登记实库结果并同步顶层 README、台账索引、Wiki 导航、任务源、生成 TODO 和推进记录。

## 集成人实库结果

两个独立注册工作区从正常接口取得 25 份 202，复核同样 25 个原上游 POST。24 个原句柄各发 3 次 GET，另 1 个未知受理保持无句柄。三轮合计 72 次原 GET，3 个明确失败、21 个继续运行，重复 POST 与意外请求均为 0。真实查询观察窗口 7.728 秒，峰值同时在途 HTTP 为 22。三轮从到期到写出 P95 分别为 113.48、42.55、42.33 毫秒。 [分窗实测](../wiki/assets/recurring-media-query-040.json)

合入 256 和 257 后，主树在隔离外部渠道的环境中三次串行执行 7 个混合负载单测及 1 个查询窗口单测，共 24 项次通过。`go vet ./cmd/server ./internal/studio` 退出 0。
