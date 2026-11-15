# 2026-11-15-085：恢复私有阶段有界批读

## 目的

推进 V16-A4.1，移除启动恢复逐任务查询交付、派发及媒体审核状态的读 N+1。084 为先前设计，本记录对应实际代码，不把局部实现标为 A4.1 完成。

## 范围

恢复入口按最多 128 个任务批读私有元数据；16 MiB 保守编码体积预检，超限缩小只读页，单条仍超限则拒绝启动。页内 REPEATABLE READ / READ ONLY 保证体积预检和后续四类读取使用同一快照。调用方对象只在所有校验和只读 COMMIT 确认后替换。原逐资源读取保留用于执行、取消及事务写入钩子；审核解码规则共用。

## 实际文件

新增 internal/studio/recovery_media_phases.go、recovery_media_phases_test.go；修改 internal/studio/persistence.go、media_moderation.go。新增本台账、docs/wiki/recovery-private-phases.md、docs/wiki/decisions/0019-recovery-private-snapshot.md；同步 README.md、Wiki/台账导航、A4 契约、tasks.json、progress.md 与生成双 TODO。

## 接口与数据影响

无公共 API、配置和 SQL 迁移变化；001—020 不修改。每非空页 5 次 SELECT（体积预检 + 交付头 + 逐件收据 + 派发 + 审核/候选），另有事务 BEGIN/COMMIT；不是 5 个网络包。只读取元数据，不拉取供应商或 S3 媒体，也不批准候选。原真正字节核验、密钥校验和租约门禁继续执行。

## 验证结果

- `go test ./internal/studio -run '^TestMediaDeliveryCipherBindsPrivateManifestAndStableIdentity$' -count=1`：通过，0.041s。
- 隔离 `node .runtime/v16a33-check.mjs target 'Test(PostgresRecoveryMediaPage|RecoveryMediaPage)'` 首轮 3.427s 通过；补充并发写入快照与完整 128 个有交付记录的 SQL 计数后再次 3.348s 通过。真实 PG 验证完整私有收据/候选、精确字节边界、后段失败无部分修改、只读 COMMIT ACK 丢失不替换对象、并发提交后本页快照一致且下一页可见。计数器不保存参数或正文。
- 原有恢复专项、普通构建/测试、文档治理正在执行，结果随后追加；尚未运行本批完整 New 万条恢复、页边界 Kill、完整全 PG/S3 或生产容量验收。

## 风险与限制

当前仍是旧 restore 总体 5 秒范围和全量缓存，未解决整个启动时限、Outbox 背压、generation 分批撤销及 A4.2 缓存误限。批量体积是数据库行 JSON 文本编码的保守总数，包含 bytea 十六进制及未使用字段，不是 Go 堆占用或原媒体体积。预检在数据库计算，不汇聚传回全部正文。已有事务写钩子的逐资源查询不属于本次移除范围。

三个代理均因工具返回使用额度限制停止，未写入其分工文件；集成人接管本逻辑变更，PG 仍串行运行。这不是产品或数据库阻塞，整体目标继续有效。

## 回滚方式

无数据格式变更。停止本地进程后可在新的变更中恢复先前恢复读路径并重新验证；不得清理迁移、私有候选、owner 或已登记收据。保留本次测试与历史验收记录。

## 文档同步

根 README 最近变更与实施状态、Wiki 专项与 ADR、台账索引、A4 契约、任务源及双 TODO、推进记录同步。A4.1/A4.2 继续 pending，V16 继续 in_progress，11/23 整批不变。

暂停记录：用户于本轮明确暂停目标，已停止普通check验证进程树。此前12项真实PG恢复专项42.967s全部通过，含READY/STARTED本地pg_dump/pg_restore；S3子项未配置而SKIP。todo生成与文档变更检查通过186文档/85台账/405路径，git diff --check通过（仅既有换行警告）。普通check未确认完整结束，不能标为通过；完整A4.1仍pending，保留本地日志.runtime/v16a41-private-check.log及恢复日志。
