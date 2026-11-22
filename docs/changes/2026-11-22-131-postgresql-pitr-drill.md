# 131 · V18-A 本机 PostgreSQL 16 命名恢复点演练

## 目的

用两套临时数据目录验证 PostgreSQL 实体基础备份与连续 WAL 归档能恢复到指定事务边界。为 PG、私有文件、目录 owner 和工作台业务的联合恢复提供同一演练标识、插入时机和读回接口。联合运行证据见下方验证结果。

## 范围

覆盖 V18 的本机 PG 恢复子项，关联 H07、H09。在 Windows 上用 PostgreSQL 16 的 `pg_basebackup` 创建带 SHA-256 manifest 的物理备份，用 `pg_verifybackup` 校验，源库以 `basic_archive` 归档 WAL。恢复库设置命名目标并暂停，在该点查询事务可见性后恢复为可写库。演练独立于真实邮件投递。机制依据见 [PG16 基础备份](https://www.postgresql.org/docs/16/app-pgbasebackup.html)、[归档模块](https://www.postgresql.org/docs/16/basic-archive.html)和[连续归档与时间点恢复](https://www.postgresql.org/docs/16/continuous-archiving.html)。

## 实际文件

- `scripts/recovery/pg-pitr-drill.mjs` 提供分阶段驱动、归档复制、双向标记和只含非敏感字段的证据。
- `scripts/recovery/pg-pitr-drill.test.mjs` 校验 WAL 位置解析和恢复证据判定。
- `docs/changes/2026-11-22-131-postgresql-pitr-drill.md` 记录本次演练。三份文件准备于 `.runtime/v18a-preparation/` 下同名相对路径。

## 接口与数据影响

1. `const d = await createPitrDrill({binDir, stageParent})` 只接受本机 PG16 工具目录和已存在的本机 ASCII 父目录，默认使用 PG16 安装目录和系统临时目录。驱动只在新建的随机子目录内写入，用 Windows DACL 将数据与随机凭据限于当前用户和 SYSTEM。
2. `await d.startSource()` 分配临时环回端口并创建空库 `studio`。`await d.withSource(async ({databaseURL,drillId}) => { ... })` 把临时连接和随机身份交给调用者。每个回调先核验专属进程、端口和 `studio` 内 `public.pitr_drill_identity` 的唯一标识。集成程序迁移前也须比对该标识；报告仅保存 `drillId`。
3. `await d.takeBaseBackup()` 创建并校验物理备份。随后可用 `withSource` 提交工作台恢复点前的真实状态，并在 `d.privateRoot` 下配对备份已静止的私有目录、owner 和所需密钥。
4. `await d.createRestorePoint()` 在命名 WAL 记录前后分别提交标记，切换 WAL 并等待实际归档，再复制至独立目录逐段比对 SHA-256。可传 `{afterPoint: async ({databaseURL,drillId}) => { ... }}` 额外提交应被截断的业务状态。目标段必须缺席于基础备份自带的 WAL。报告保留源库时钟的命名点及后置提交观测时间、两位置间的 WAL 字节数，用于解释本轮选择的回退窗口。
5. `await d.stopSource()` 仅停止已核实的源进程，并将源 PGDATA 与原归档移至本轮私有目录内的离线位置。`await d.restore()` 从已校验的基础备份及独立归档复制件启动另一端口的实例。驱动核对相同物理集群标识、相同演练标识、命名目标、实际暂停状态、前后事务标记和 WAL 位置，再允许恢复库可写。
6. `await d.withRestored(async ({databaseURL,drillId}) => { ... })` 可读回工作台状态并验证业务恢复。`await d.stopRestored()` 后以 `d.evidence()` 取得阶段耗时、manifest 和 WAL 摘要、恢复目标与事务证据。异常路径使用 `finally { await d.close() }` 停止本轮专属进程并移除临时口令文件。

## 验证结果

- Node 22.22.2 执行 `node --check .runtime/v18a-preparation/scripts/recovery/pg-pitr-drill.mjs` 通过。内嵌 ACL 脚本通过 PowerShell 语法分析。
- 在准备区新建空目录，对其实际执行同一 DACL 设置并写入样本文件，逐个检查根目录与子文件的允许访问 SID 仅含当前用户和 SYSTEM。用文件产品版本信息核实本机 `pg_ctl.exe`、`postgres.exe`、`basic_archive.dll` 均为 16.14。
- `node --test .runtime/v18a-preparation/scripts/recovery/pg-pitr-drill.test.mjs` 的 5 项纯函数测试通过，耗时 123.375 ms。覆盖完整 64 位 LSN、越界及无效格式、命名目标与暂停、两侧事务可见性和严格排除后置提交的边界。
- 补充 Windows 原生命令复核后同一命令 6 项通过，187.7187 ms。真实 `cmd.exe` 将含 0x1a 和 NUL 的样本完整复制到含空格的路径，缺少请求的历史段时退出码 1，未产生目标文件。此项不启动数据库。PG16 的 Windows 启动器会让常驻 shell 继承标准句柄，驱动使用私有文件接收 `pg_ctl start` 输出并等待控制程序自身退出，随后以 PGDATA 内专属 PID 与数据库标识确认归属。[PG16 启动器源码](https://github.com/postgres/postgres/blob/REL_16_STABLE/src/bin/pg_ctl/pg_ctl.c)
- 准备阶段未启动 PostgreSQL。整合后可用 `node scripts/recovery/pg-pitr-drill.mjs --run` 单独复跑数据库演练。集成人两次执行完整联合恢复。最终一次目标段在基础备份中缺席，4 个外置归档段均按 SHA-256 核对，实际暂停 LSN 与目标 LSN 相同。恢复后目标前标记存在，目标后标记缺席。源库两次观测间相距 56696 个 WAL 字节。集群标识相同，恢复库完成提升并停止。[平台恢复](2026-11-22-133-recovery-platform-integration.md) · [脱敏报告](../wiki/assets/recovery-drill-2026-11-22.json)

## 风险与限制

本机演练使用独立随机角色、SCRAM、环回 TCP、校验和及一次性目录，不复用已有实例、用户指定端口或现存数据库。恢复依赖完整基础备份与所需 WAL，驱动拒绝基础备份内的符号链接和额外表空间。缺段、损坏、目标未达或后置提交可见均不能通过。单机本地归档测量无法证明托管环境的持久性、异地容灾及生产 RPO/RTO。私有对象、应用加密密钥、邮件和外部供应商需要各自的独立证据。

## 回滚方式

正常或故障退出只停止本轮匹配 PGDATA、端口和 PID 的实例。脚本不递归删除历史数据，不修改系统服务。复跑创建另一私有目录；本轮数据和诊断日志保留在原受限目录。仓库侧可撤回本批演练脚本、测试和同步说明，不涉及平台迁移或业务数据库降级。

## 文档同步

集成人在取得真实证据后同步根 README、Wiki、台账索引、进度和 `docs/planning/tasks.json`，以任务源生成 TODO。准备稿及纯函数测试不能勾选整批 V18。
