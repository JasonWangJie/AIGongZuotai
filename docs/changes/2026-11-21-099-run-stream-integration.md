# 099 · 持久聊天流升级、故障与联备集成

## 目的

验证聊天客户端已看到的文本在进程强杀和提交回执丢失后仍可按原游标读取，并检验 020 升级、私有目录联备与原键回放。

## 范围

V16-B1，H01/H02/H03/H04/H07/H09。结合 096 的存储、097 的执行与审核、098 的接口与浏览器回放，覆盖单控制写者下的普通和多轮聊天。B2 的租约过期与跨执行者竞争另验。

## 实际文件

- `internal/studio/recovery.go` 使用新格式的恢复事务。`internal/studio/storage_maintenance.go`、`ledger_admin.go` 和 `operations_admin.go` 将离线格式推进至 021。
- `internal/studio/run_stream_recovery_test.go` 覆盖真正的 020 升级、原请求、进程强杀、数据库提交回执丢失和备份恢复。`persistence_test.go` 构造旧格式的易失游标，`recovery_integration_test.go` 对新格式的已授权 Run 验收连续游标和原权益。
- 同目录下的 `media_review_dispatch_upgrade_test.go`、`media_scheduler_upgrade_test.go`、`storage_maintenance_delivery_test.go`、`storage_maintenance_review_test.go` 更新当前格式检查，保留旧版迁移与私有对象保护断言。`admission_test.go` 用实际授权但未关闭的执行验证恢复后的占用；`task_leases_test.go` 从原始空输出出发验证终态保存背压。
- `docs/api/openapi.yaml`、Wiki 的 `backend.md` 与 `persistence-auth.md` 描述 021 游标和旧格式差异。README、AGENTS 当前阶段、Wiki、决策 0021、数据库迁移与维护指引、V16 与 B 契约、任务源、生成的 TODO、推进记录与台账索引同步。

## 接口与数据影响

新增 021 增量迁移。020 的现存 Run 保留原正文、旧游标与恢复间隔。新 Run 的日志、水位、快照和执行身份随 PG 备份恢复。沿用原 Run/SSE 地址、事件名、权限、原幂等键和显式继续语义。

## 验证结果

- 本机隔离 PostgreSQL 16.14，Go 1.26.11，由集成人串行运行。首组 `go test -count=1 -v ./internal/studio` 定向选择 RunStreamUpgrade020、RunStreamProcessCommitBoundary、RunStreamBackupRestore、DeliveryOfflineToolsRequireExact021，15.386 秒通过。真实升级 1.21 秒，提交前强杀和提交后丢回执合计 5.09 秒，真实 pg_dump/pg_restore、两个私有目录及测试交付密钥联备 4.02 秒，三种错误格式拒绝 4.85 秒。
- 强杀时先由 HTTP 客户端收到第一块并用独立 PG 连接核对其已提交。第二块分别在数据库行锁上强杀进程、或只丢弃该写连接的 COMMIT 回执。两个进程重启场景均保留正确前缀，流与原键回放只产生一次模型请求。
- 首次执行、审核、慢订阅与既有回归组合 22.883 秒退出 1。四项新增专项通过。两个既有用例暴露了停顿时会话主文档检查点、无游标冷终态快照的兼容差异，保留原断言复验。
- 修正上述两处兼容问题后，12 项真实 PG 组合 29.033 秒通过。含原多轮进程强杀 3.04 秒、旧 SSE 慢读缓存 1.68 秒、终态/审核事务丢回执 2.76 秒、运营快照 2.22 秒，以及取消回滚、执行门禁、审批四种结果、窗口过期及跨页交接。后续 5 项组合 11.958 秒通过，覆盖同序内容冲突、过期写入、字节界限、同序篡改、020 旧游标以及恢复页边界。
- 隔离外部配置运行 `npm run check`，54.96 秒通过。Go vet、测试与构建通过，前端 30 文件 305 项测试、10 项治理测试通过。同一二进制串行运行基础、runtime、catalog、references、library、conversations 六套本地冒烟，依次为 9.16、1.11、1.63、7.72、7.59、1.37 秒，均退出 0。二进制 SHA-256 为 `561288065826422B2FB3F1205DBF0DA14D93D005B7136287C427324FD8746ECB`。
- 首轮 `npm run test:db` 的 studio 包在 582.409 秒退出 1。旧准入夹具在正常关闭后只把 Run 的 JSON 状态改回 RUNNING，旧终态背压夹具给尚未执行的 Run 预填了输出。两组数据均与 021 流状态冲突。分别改用实际未关闭的执行和真实新 Run 的空前缀，保留原业务断言，并增加背压恢复后只有一条增量和一个终态的持久断言。两项以 `-count=2` 连续复验，6.735 秒通过。
- 夹具修正后再跑 `npm run check`，48.73 秒通过。服务二进制与六套冒烟使用的 SHA-256 相同。另用 PyYAML 解析 OpenAPI，71 路径、83 操作、411 个内部引用均可解析，已核对省略游标与显式零的说明。
- 最终 `npm run test:db` 串行退出 0，studio 包 571.526 秒。启用本机版本化 S3、实际 pg_dump/pg_restore、`RECOVERY_CAPACITY_TEST=1` 和 `RECOVERY_LARGE_TEST=1`。20 万条记录完整启动 5.102 秒，Run/Task 峰值各 512；另一场景 25.729 秒投影 22,001 条通知，二次启动 0.302 秒，两组均无外发。A3.3 旧调度计划与 019 大规模升级两个可选基准未重跑；Windows 权限不足，两处符号链接子例未运行。

## 风险与限制

PG 每个公开块增加一次提交。项目通知、回放窗口字节与整机内存有独立口径。环回流服务用于控制故障点；真实供应商、混合负载与多实例容量按各自批次验收。

## 回滚方式

旧程序及离线工具会拒绝 021。若业务需要回到 020，停机并恢复升级前匹配的 PG、私有目录、owner、已启用的密钥及相应对象版本；升级后的业务应先纳入独立备份。禁止删除迁移登记或流表绕过栅栏。

## 文档同步

`npm run todo:sync` 后，`npm run check:todo`、`npm run check:changes`、`git diff --check` 通过。206 份文档、99 份台账和当前工作区 79 个变更路径通过治理；23 个纵向批次中完成 11 个，V16 仍在进行中。见[持久流决策](../wiki/decisions/0021-durable-run-stream.md)、[实施契约](../planning/v16b-contract.md)和[任务源](../planning/tasks.json)。
