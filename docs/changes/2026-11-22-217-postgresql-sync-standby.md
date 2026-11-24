# 2026-11-22-217：本机 PostgreSQL 同步副本与工作台计划切换

## 目的

验证单控制写者工作台在原主库停机、已获确认的 WAL 已在物理副本落盘并回放后，能否在新主库读回原账户、原操作收据及私有图片，并量出从开始停写到重新完成业务读取的耗时。对应原方案第 303、313、315 行。

## 范围

V18-F，关联 H01 账户权限、H02 项目数据和 H07 可靠性。演练仅使用 Windows 上隔离的两台回环 PostgreSQL 16 实例、同一份可校验的 baseline 工作台制品和本机两类原私有目录。准备阶段和正式双实例验收分别登记。

## 实际文件

- `scripts/recovery/pg-pitr-drill.mjs` 为专用同步分支复用 PG 工具、物理备份、受限目录、凭据、进程归属及清理。
- `scripts/recovery/pg-sync-standby.mjs` 配置同步复制，验证源发送端和备接收端的 WAL 位置，隔离原主并校验提升后的时间线。
- `scripts/recovery/workbench-sync-standby.mjs` 使用单份本地制品建立真实用户及业务数据，核对切换后的收据、私图与权限，输出去敏报告。
- `scripts/release/release-process.mjs` 复用原工作台进程持有者、身份校验及三锁观察，支持同一演练在统一退出后切换私有数据库端口。
- `scripts/recovery/pg-sync-standby.test.mjs` 覆盖 LSN、身份、切换前提和确认收据计数。216 提供既有 Go 三锁探针的 `RELEASE_CHECK_CLUSTER=restored` 选项。

## 接口与数据影响

`node scripts/recovery/workbench-sync-standby.mjs --artifact <本机已核验的037基线制品目录>`。制品可用仓库已有 `scripts/release/release-artifact.mjs build --role baseline --version <版本> --source <源码目录> --out <新目录>` 构建。演练自建 PG 源、备、账户、私有文件和两个不同环回端口，入口只接收制品目录。PG 连接口令由私有 ACL 目录内的临时口令文件提供。工作台使用已有业务 API 和精确格式。

源库使用 `remote_apply` 与一个具名同步副本。校验源端 `pg_stat_replication` 的状态、flush/replay LSN 及备端 `pg_stat_wal_receiver`、`pg_last_wal_replay_lsn()`；两侧 system identifier 必须一致。基于独立的 `pg_basebackup`，在写入 standby 配置前对基础备份及实际备目录运行 `pg_verifybackup`。授予提升之前，原工作台真实进程须正常退出，数据库 advisory 锁和两个目录锁可重新取得，原 postmaster 须已退出，原端口可独占且原主目录已隔离。重开原件使用同一私有目录和 owner。

业务对账分母固定为同步复制启用后从正常路由确认的 4 次草稿保存和 1 次工作流定义保存。切换后用原账户和每一把原键进行 GET 核对；按原收据计算缺失及内容不符数量。已确认原图另行比对完整字节摘要，原 Cookie、重新登录、未登录和非所属账户有独立检查。报告记录原主停止、提升、应用取得三锁并恢复就绪、实际业务核对完成四个累计耗时点。

## 验证结果

| 检查 | 结果 | 边界 |
| --- | --- | --- |
| `node --test scripts/recovery/pg-pitr-drill.test.mjs scripts/recovery/pg-sync-standby.test.mjs` | Windows 10 项通过，223.126 毫秒 | 含备用来源、同步状态、双方落盘与回放落后、错误账户/端口、任一旧锁未释放、三类收据差额及旧 PITR 标记/二进制复制。 |
| `node --test scripts/release/release-process.test.mjs` | 原 3 项通过，2369.640 毫秒 | Windows 真实受管测试进程确认所属监听器，正常退出后独立监听器仍工作，未启动数据库。 |
| 语法、无效命令及依赖 | 新脚本语法检查通过；未知参数 exit 1 | 已从文件版本核对本机 PostgreSQL、pg_ctl、psql、pg_basebackup、pg_verifybackup、basic_archive 为 16.14。Node 22.22.2、Python 3.12.10、Go 1.26.11。本机 PG16 的系统视图定义包含使用的接收端字段。 |
| `node scripts/recovery/workbench-sync-standby.mjs --artifact <已核验制品>` | 待集成人在其他 PG 进程退出后串行执行 | 需包含 216 的源/恢复锁探针、同格式 baseline 制品和可写本机私有暂存空间。真实 PG flush/replay 位置、切换结果、已确认缺失数和 RTO 以正式去敏报告为准。 |

## 风险与限制

测量范围为同一 Windows 主机的计划停写与物理复制切换。私图读回依赖交接的同一份私有文件，数据中心、机器或磁盘完全损坏时需要独立的对象副本与恢复证据。同步副本失联会影响新写入的可确认性，真实可用区、主机分割、异步跨区损失窗口与自动故障转移另验。

## 回滚方式

准备或核对失败时保留私有目录用于诊断，停止所持有的工作台和测试数据库。已提升副本的时间线须独立保全；演练不自动重新拉起旧主或把新写合并回旧记录。

## 文档同步

集成人运行正式演练后登记真实报告、RTO 和业务差额，更新 README、恢复或发布 Wiki、任务源、进度和台账索引。

## 集成人正式运行

同机 PG16 remote_apply 同步复制。原写进程退出、三锁释放后升主，时间线 1→2，5 份确认收据全部恢复；从停写至业务核对 3639 毫秒，原库已隔离。 原授权私图摘要一致，匿名与其他账户被拒绝。[实测汇总](../wiki/assets/recovery-sync-standby-037.json)
