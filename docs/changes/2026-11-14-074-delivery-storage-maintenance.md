# 2026-11-14-074：018交付可达性与离线维护保护

## 目的

为V16-A3.1私有交付增加维护与迁移保护：已计划但尚未下载的身份不会被误报为恢复损坏，已落盘的计划文件和完整收据不会被GC或存储迁移改变。

## 范围

关联V16-A3.1、H03/H07/H09。本记录负责可达性、完整收据验证、离线精确版本与S3迁移门禁；018迁移及运行时交付由核心实现负责，配置/README/任务源和实际联备恢复由根集成。未实现A3.2独立审核、A3.3公平退避或跨实例能力。

## 实际文件

- `internal/studio/storage_maintenance.go`、`storage_maintenance_delivery.go`：018详细可达集合、计划/完成分离、收据字节核验和摘要。
- `internal/studio/ledger_admin.go`、`operations_admin.go`：精确001—018及报告格式版本。
- `internal/studio/asset_storage_migrate.go`、`cmd/storage/main.go`：数据库感知的迁移入口、双锁/UUID/版本门禁、交付ID与完成标记保护。
- `internal/studio/storage_maintenance_delivery_test.go`：5项PG函数，含3个格式子例及真实HTTP S3 fixture。
- `docs/wiki/storage-maintenance.md`及本台账。保留`maintenanceReachable`兼容包装，`storage_ownership.go`与旧B2测试无需修改；未改`postgres-dev.ps1`、既有测试断言或数据库迁移文件。

## 接口与数据影响

新增Go维护内部详细可达视图及`MigrateOwnedAssets`入口，无浏览器API变化。旧`MigrateAssets`发现PG owner时拒绝，只保留无owner本地fixture行为；CLI对PG目录需要DATABASE_URL，不能猜测归属。所有离线操作仍不自动迁移、不调用生成/审核服务、不删除远端对象。

018所有交付主表状态和逐件计划均参与可达性；NULL receipt允许缺文件，但同ID现存文件全部保护，已有旧任务/候选引用的完整文件要求优先。非NULL receipt必须有对应Delivery完成标记，与本地manifest完整相同，并检查原件及缩略图实际字节。摘要包含交付状态、版本、顺序和收据；终态不释放引用。

S3迁移对PG目录获取OS资产锁和控制写者锁、验证稳定UUID/角色及精确018；整个选择批次在远端写前拒绝任何delivery登记ID或Delivery完成标记。普通旧资产可继续迁移并保留本地副本。此限制保护冻结的完整存储定位，后续应通过追加定位证据解决，不能默改收据。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| 首两轮`go test ./internal/studio -run '^$'` | 并行接线未完成 | 首轮缺Delivery字段/HTTP辅助方法和观测字段，第二轮缺taskRecord.Delivery及观测字段；均为其他归属文件尚未接线，没有修改它们或削弱断言。 |
| 接线后`go test ./internal/studio -run '^$'` | 通过 | studio编译0.046s；不冒充执行测试。 |
| 存储/迁移/输入4项普通专项 | 通过 | 原S3失败恢复、原迁移损坏与分页、运营输入拒绝、维护精确扫描，总0.218s；符号链接子例因Windows权限未执行。 |
| `go vet ./internal/studio ./cmd/storage` | 通过 | 维护、迁移入口及测试静态检查通过。 |
| `node .runtime/v16-check.mjs target '^TestPostgresDelivery(Maintenance\|Migration\|Offline)'` | 通过 | 先清空MEDIA_DELIVERY_KEY并隔离其他真实渠道；独占55433本地测试PG，5函数及格式3子例全部通过，总9.614s。 |

PG细项：计划/完整收据/状态摘要1.47s，缺件/篡改/合法但不同收据1.64s，迁移登记保护与双锁1.11s，017/缺017/019四入口拒绝3.29s，普通旧资产实际HTTP S3复制/复验/保留副本/错库拒绝2.06s。测试只在随机schema、临时目录和loopback模拟器操作；不存在真实供应商、真实对象存储或用户目录apply。

普通专项的实际命令为：

```text
go test ./internal/studio -run '^(TestMaintenanceScanAndExactPlanFiles|TestS3MigrationRejectsLocalCorruptionAndPagesWithoutIO|TestS3FailureRecoveryPreservesReceiptAndSource|TestOperationsAdminRejectsInputsBeforeConnection)$' -count=1 -v
```

新增与修改Go文件`gofmt -l`无输出；本批路径`git diff --check`通过，仅有仓库既有CRLF转换提示。

精确版本扫描覆盖`internal/studio`、`cmd`及`scripts`：三个已有离线版本检查及报告已更新，新增迁移共用同一018检查；没有剩余生产代码的精确17断言。历史文档的017事实保留。全仓测试、联合联备恢复和文档治理由根收口，不借用以往批次结果。

## 风险与限制

当前只验证本地保留文件及收据，不远程验证所有S3对象是否仍存在。S3 fixture仅证明签名HTTP和存储边界，不是生产云灾备。加密来源不参与维护解密；备份恢复仍需独立保管媒体交付密钥及已启用邮件密钥。018交付资产暂不允许切换存储位置，即使任务已经完成。

共享PG测试必须按数据库串行，随机schema不能绕过控制写锁。仍是单控制写者/本机私有目录，未增加在线GC、远端删除或公开收费放行。

## 回滚方式

018生效后不能恢复只认识017的旧维护或业务写者；需要对应版本的独立数据库、私有目录/owner、对象版本和必要密钥联备。保留升级库及新增计划、收据、资产，不删除迁移/收据/owner强行回退。维护默认dry-run，apply仍要求精确摘要、重新全量扫描和24小时最低宽限。

## 文档同步

[私有存储维护Wiki](../wiki/storage-maintenance.md)同步018、计划缺件与完整收据、迁移限制、恢复要求和验证证据。根负责人依据联合结果更新README、Wiki索引/对象存储/数据库迁移说明、台账索引、任务源及生成TODO；本代理不覆盖这些共享入口。开发PG脚本没有备份入口，因此未向它加入隐式备份行为，实际018联备由根另行演练留证。

联合收口补记：根最终全PG301.345s覆盖本模块维护/离线门禁；local与保留S3对象的PG/私有目录/密钥恢复、018升级和最终治理通过。S3远端服务本身未做灾备，详见075。
