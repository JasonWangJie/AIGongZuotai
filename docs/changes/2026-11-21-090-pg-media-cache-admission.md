# 090 · PG 媒体受理与缓存容量分离

## 目的

避免一个工作区的旧媒体任务占满热缓存，导致有可用额度的其他工作区收到 503 LOCAL_STORE_FULL。

## 范围

V16-A4.2 首个子项。覆盖 PG 模式媒体生成、图片编辑重入、冷读、回放、取消和持久执行，保留内存演示的记录上限。关联 H02/H03/H04/H09。

## 实际文件

`internal/studio/app.go`、`internal/studio/repository_queries.go`、`internal/studio/media_admission_cache_test.go`。状态和说明同步至根 README、Wiki 导航与分页恢复说明、A4 契约、任务源、生成的两份 TODO、进度和台账索引。

## 接口与数据影响

PG 模式在工作区、模型与其他现有准入检查通过后，用原事务持久化媒体任务。锁外编辑准备后的原键重放与重新检查仍优先。新受理或冷读记录仅在有空位时加入缓存；PG 从持久调度表领取任务。数据库格式、公开 API 字段与幂等协议沿用 020。

## 验证结果

- 在 089 基线上通过 Go overlay 仅加回归用例：工作区 A 有 512 条冻结原 handle 的 RUNNING 任务，工作区 B 首次 POST 收到 503 LOCAL_STORE_FULL，专项退出 1（2.838s）。
- 修复后集成人串行执行 `go test -v -count=1 ./internal/studio -run '^TestPostgres(MediaAdmissionWithFullForeignCache|Admission.*|.*ImageEdit.*|RecoveryNewAcrossPagesPreservesBillingAndUnknown)$'`，5 个实际匹配的 PG 用例全通过（13.755s）。新专项 3.05s。两种操作均受理、原键读取同一任务、真实额度满 429、跨工作区读取 404、取消后额度归还；实际 PG submit 领取新任务后持久化 demo 供应端 handle。

- `npm run check` 70.23s 退出 0，覆盖 Go vet/test/build、内存存储上限和原键回放、301 个前端测试与 10 个治理测试。最终二进制 SHA-256 为 8531B9AE5522D21FCCD690FF30BE27CFAB6F189DE1BF1D43C7FEE0A2E74A47A7。基础、runtime、catalog、references、library、conversations 六套 smoke 依次为 9.99、1.57、2.30、8.07、7.59、1.69s，均退出 0。
- 在 089 的独立 PostgreSQL 16.14 和本机版本化 S3 上再次串行运行 `npm run test:db`，studio 567.911s 通过。覆盖跨工作区媒体缓存、页边界 Kill/ACK 与真实 pg_dump/pg_restore。A4.1 大积压沿用 [089](2026-11-21-089-recovery-acceptance.md) 的专项测量；本次未重测 A3.3 可选大积压预选与升级耗时。

## 风险与限制

PG 热缓存满时，新受理或多次读取同一未缓存资源可能需要更多数据库读。恢复预载、worker 写回、Run 执行与活动订阅、项目授权及 100k+100k 完整启动仍待后续子项。用户和资源均属本地测试库；未接入外部供应商。

## 回滚方式

撤回本增量在 app.go 和 repository_queries.go 的媒体缓存改动及对应测试即可，无 SQL 迁移。旧写者会重新按全局缓存长度拒绝；已有受理、幂等、权益与交付数据保留。

## 文档同步

见[分页恢复与缓存](../wiki/paged-recovery.md)、[A4 契约](../planning/v16a4-contract.md)及[任务源](../planning/tasks.json)。
