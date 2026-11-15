# 2026-11-15-086：分页恢复与先行 generation 栅栏

## 目的

接续085实际批读，推进V16-A4.1整体分页/背压，拆除整个restore共用5秒的范围，避免未完成任务积压耗尽一次启动的Outbox容量。

## 范围

项目、Run、Task按稳定ID分页，128条/16 MiB基础与私有编码总额。基础JSON在SQL按前缀体积门禁返回；私有数据复用085只读一致快照，超限缩页不触发数据库熔断。业务恢复逐资源走原账务/审核/调度/Outbox事务，每次5秒；确认提交才替换缓存并推进ID。页间消费Outbox，满队列实际回滚后释放互斥锁、消费并重读未提交项。generation单独短事务先提交，旧lease随后128 ID一批撤销；恢复完成前不启动worker/HTTP。

## 实际文件

新增internal/studio/recovery.go、recovery_test.go；修改persistence.go（移除旧restore）、app.go（顺序与私有聚合统计）、task_leases.go（先切generation、分批撤销）。新增本台账和docs/wiki/paged-recovery.md，更新README、Wiki导航、台账索引、A4契约、任务源/双TODO、progress。087/088独立验证文件由各自台账记录。

## 接口与数据影响

无公共API、配置或新迁移，001—020不修改。原业务状态、幂等、冻结请求、handle、候选和冷却保持；不会自动重POST或重审STARTED。统计仅保留整数页数、编码峰值及缓存条数，不包含提示词/身份/URL。全量缓存仍保留，A4.2另验。

## 验证结果

恢复用户目标后确认暂停进程已退出；补跑隔离普通check（.runtime/v16a41-resume-check.log）退出0：10治理、Go测试（studio缓存复用）、301前端/29文件、构建1.80s、186文档通过。这是085后、086核心修改前的结果，不当作086回归通过。

086初稿编译及TestRecoveryMediaPageRejectsInvalidBoundsBeforeDatabase通过0.035s。核心旧恢复回归、基页字节边界、087完整New和088真实故障尚在验证；最终结果继续追加，A4.1保持pending。

## 风险与限制

单控制写者与私有目录排他锁保持。基础页和私有页不是同一事务，依赖先提交generation并在恢复期间无新worker/HTTP；私有页内部由085负责RR事务，不能嵌套进写事务。普通5秒/迁移60秒边界不扩大。完整万条New、ACK/Kill、S3及联合回归未验收，不能宣称完整启动容量。

## 回滚方式

不删迁移/owner/候选。停机后以新的逻辑变更恢复旧启动代码并验证；保留本次事实和已提交的恢复状态。已转INTERRUPTED/未知/隔离的业务不因程序回退再次生成或审核。

## 文档同步

README/Wiki说明当前开发与待验范围，tasks.json中A4.1仍pending、V16仍in_progress。根代理维护共享索引；087验证完整New矩阵和大数据启动，088验证进程/ACK/generation故障，所有PG仍由根串行执行。

## 2026-11-15 再次暂停

用户再次暂停：086分页协调器/先切generation/分批lease已编码。旧恢复13专项36.477s通过；故障专项15.392s失败，真实页边界Kill、generation先行和基础字节边界通过，恢复COMMIT ACK代理未触发失败，尚未诊断或修复。故障测试进程已经自然退出1，未遗留go/studio.test进程。087跨页/12002条完整New尚未PG验证。并行代理已中断，不继续开发。

恢复后先诊断ACK故障，再补完087/088及整批联合验收；A4.1仍pending，未修改已验收历史。
