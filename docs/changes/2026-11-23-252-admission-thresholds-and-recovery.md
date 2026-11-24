# 252 · 正常受理的配额核对与重开扫描

## 目的

为 V17 的在途媒体验收核对两个独立事实。新请求必须使用当前工作区和模型的剩余名额。服务重开后必须逐页扫描需要恢复的任务，即使这些任务随后能从原幂等收据单独读回。

## 范围

PostgreSQL 单控制写者上的聊天与媒体准入，以及通过正式媒体接口建立在途任务的本机测试。模块为 H03、H07、H08。

## 实际文件

- `internal/studio/admission.go` 在一次数据库查询中判断工作区和模型是否达到各自上限。
- `internal/studio/admission_test.go` 将业务终态、在途状态和两个阈值与原统计逐项对照。
- `internal/studio/media_admission_capacity_test.go` 在服务重开后、首次原键回放前采集启动恢复的行数、页数及缓存峰值。
- 根 README、Wiki 与台账索引、任务源、生成 TODO、推进记录和配套的分档数据。

## 接口与数据影响

平台继续先处理同作用域、同输入的原键重放。需要新占位时，对聊天扫描当前工作区的运行索引，对图片或视频扫描当前工作区的任务索引。在同一语句快照内查找第 N 个非终态记录和第 M 个同模型非终态记录，N、M 分别取当前工作区与模型上限。两者同时已满时先返回工作区的 429。运行概况仍读取精确数量、排队时长和种类。内存演示继续按内存中的资源计数。沿用 004 的两个局部索引和终态谓词，未新增数据库迁移。

重开验收要求完整扫描全部持有原句柄的 RUNNING 任务。无句柄的 SUBMIT_UNKNOWN 从原收据核对并继续计入配额。每页还核验记录数和编码字节上限。测试用一个单独哨兵验证重开后确实再次发出原 GET，其余任务遵守上游的延期查询时间。

## 验证结果

首次隔离副本缺少仓库自带的模型与演示价格文件，原键重放用例在准备阶段中止。补齐相同测试文件后，执行 `go test -count=1 -v ./internal/studio -run '^TestPostgres(Admission(ParkedEvictionRestartAndRecovery|TenantIsolationAndAtomicFailure|TerminalPredicateAndIndexes)|MediaAdmissionWithFullForeignCache|GenerationAdmission(OriginalReplayAndExistingWork|RechecksAfterModeration|RechecksAfterEditPreparation))$'`，七项 PostgreSQL 检查全部通过，核心包用时 28.124 秒。

设置 `STUDIO_MEDIA_ADMISSION_TASKS=4096`，先后在旧代码和候选代码上运行 `go test -count=1 -v ./internal/studio -run '^TestPostgresMediaNormalAdmissionCapacity$'`。每档在四个正常登录的工作区完成全部 4096 项受理，逐项重放两次，并核验四次模型配额、十二次工作区配额拒绝。原实现在首次受理及溢出核对用时 148.131 秒，候选为 93.783 秒。两次完整用例分别用时 181.24、154.65 秒。本机按固定顺序各运行一档。

服务重开时，启动恢复从 32 页读到全部 4095 个在途任务，单页最多 128 条、321646 字节，任务缓存峰值 512。另一个无句柄的受理不明任务在两个阶段都继续占位。重开后观察到原 GET 1 次，上游追加生成 POST 为 0。[两档完整记录](../wiki/assets/media-admission-thresholds-040.json)

## 风险与限制

每次检查仍访问数据库。查询规划器自行选择访问方法，大档的总时长还包括持久写、事件、登录、原键回放和原库重开。测试仅使用本机受控 HTTP 接收端。真实模型与固定容量环境另验。

## 回滚方式

恢复先前使用完整占用统计的准入查询即可。原任务、原收据、配额、索引和私有文件均使用相同格式。

## 文档同步

[媒体容量](../wiki/media-original-query-capacity.md) · [任务源](../planning/tasks.json) · [推进记录](../planning/progress.md)
