# 250 · 原查询结果合批的实库对照

## 目的

V17 / H03、H07、H08。248 的分段数据显示，原查询取得结果后还需等待应用锁与控制写事务。实验比较逐项提交和最多八项共用事务，以实际完成率、到期滞后及数据库事务数判断是否采用。

## 范围

在 040 单控制写者和 249 的执行登记基础上，以已领取的原句柄执行本机 HTTP GET。仅处理上游明确回复的失败，或保持相同状态的进度更新。提交、媒体下载、首次审核、渠道探测及受理不明的任务均沿用各自的处理路径。功能专项还核对了工作流与权益。

## 实际文件

- [配对结果及重放文件散列](../wiki/assets/media-query-save-batch-040.json)记录各档计数、耗时和故障测试结果。
- [隔离试验源码补丁](../wiki/assets/media-query-save-batch-040.patch.gz)包含 12 个 `internal/studio` 源文件和测试文件的差异。Gzip 文件 SHA-256 为 `21e986a5dec0ba90253ed2ad896a4f044fa8bef4c2483f2f5e751536350266d2`。解压后在隔离基线副本执行 `git apply --check --whitespace=error` 和实际应用，逐文件核对了换行归一化后的内容与实测源码相同。
- 本台账、根 README、Wiki 导航、媒体容量 Wiki、台账索引、任务源、生成 TODO 与轮次进度记录。

## 接口与数据影响

正式服务采用逐项提交。`MEDIA_QUERY_SAVE_BATCH_SIZE` 是未采用的候选启动配置。重放用例只设置隔离测试进程内的批大小。数据库继续使用 040 格式。

实验在每个查询线程仍持有执行位、原工作区及渠道预算时，用 1 毫秒窗口收集结果。控制写者按原代次与任务行加锁，逐项复核原句柄、版本、租约和数据库时钟，在同一个事务写任务、适用的权益与父工作流、Outbox。只有确认提交后才发布。确认回滚后，线程用原结果单项入库，再次核对期限。提交确认不明即停写。

## 验证结果

8 项 PostgreSQL 正确性用例通过，核心包耗时 23.432 秒。覆盖共享物理提交、取消与过期混组、阻塞到租约过期、Outbox 背压、提交确认丢失、父工作流与权益同事务、继续 RUNNING 的进度以及回退单项再次过期。16 个继续 RUNNING 的原句柄各发生一次真实 GET，全部写入第二版进度，PG 中存在共用提交事务的任务。

128 个查询位与 4 条租约连接共用同一组配置。每秒安排 320 项到期，服务时间为 200 毫秒。关闭合批和上限 8 各执行一轮，各观察到 512 个唯一原 GET，512 项全部写入终态，生成 POST 和异常请求均为 0。该配对用例核心包耗时 21.404 秒。

| 合批上限 | 终态事务数 | 同事务写入多项的事务数 | 多项事务中的结果数 | 最大组 | 完成数/秒 | 到期至写出 P95 / P99（毫秒） |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 0 | 512 | 0 | 0 | 1 | 114.99 | 2279.94 / 2328.49 |
| 8 | 209 | 110 | 413 | 8 | 78.00 | 5570.72 / 5598.07 |

终态事务数由 PostgreSQL 任务行的 xmin 独立计数。单项控制保存事务 P95 为 6.63 毫秒，启用合批后的控制保存事务 P95 为 41.22 毫秒。两档已知结果等待应用锁的 P95 分别为 309.28 和 56.74 毫秒。本次合批减少了物理提交数，端到端完成率下降，到期迟滞增加。正式服务继续分别保存每个查询结果。该短档使用明确返回失败的原查询，6,000 次/秒及 24 小时稳态仍须独立验收。

在独立的 040 源码副本复跑时，从仓库根目录解压补丁并执行以下命令。先指定隔离回环 PostgreSQL 16.14 的 `TEST_DATABASE_URL`。两条 Go 命令均需等待上一进程实际退出。

```powershell
python -c "import gzip,pathlib; p=pathlib.Path('docs/wiki/assets/media-query-save-batch-040.patch.gz'); pathlib.Path('query-save-250-replay.patch').write_bytes(gzip.decompress(p.read_bytes()))"
git apply --check --whitespace=error query-save-250-replay.patch
git apply --whitespace=error query-save-250-replay.patch
go test -v -p 1 -count=1 -timeout=5m ./internal/studio -run '^TestPostgres(KnownQuerySaveGroup.*|QuerySaveRepeatedProgress|QuerySaveSingleFallbackExpiry)250$'
$env:STUDIO_MEDIA_QUERY_SAVE_COMPARE = '1'
go test -v -p 1 -count=1 -timeout=5m ./internal/studio -run '^TestPostgresQuerySaveBatchCurve250$'
```

## 风险与限制

每种设置测量一次，顺序为 0、8。吞吐窗口从首次预定到期至观察者确认各项终态，包含排队与排空。两档控制事务累计占用分别为 2635.57 和 3410.21 毫秒。多个阶段可能重叠，每个事务承载的任务数也有变化。单轮配对不能分离收集、逐项 SQL、锁等待及主机负载的各自因果贡献。正式接入真实供应商、跨区和稳定负载需要对应环境。

## 回滚方式

正式服务继续运行 040 单项保存路径。重放结束后停止并关闭该独立实验副本即可。归档补丁涉及 Go 代码和测试，使用现有 040 表结构。

## 文档同步

[媒体容量与实测口径](../wiki/media-original-query-capacity.md) · [任务源](../planning/tasks.json) · [本轮进度](../planning/progress.md)
