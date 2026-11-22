# 089 · A4.1 恢复故障与完整启动联合验证

## 目的

完成 V16-A4.1 自 088 暂停后的故障验证，检查恢复时原请求、权益和事件在重启前后的一致性。

## 范围

H01/H02/H03/H04/H07/H09，基于已实现的 085 私有阶段批读与 086 分页协调器，覆盖 087 跨页和大积压完整启动及 088 的真实进程边界。

## 实际文件

启动顺序见 `internal/studio/app.go` 与 `internal/studio/mail_delivery.go`。故障代理见 `internal/studio/outbox_test.go` 和 `internal/studio/recovery_process_test.go`。启用邮件队列密钥的前置条件由 `internal/studio/recovery_mail_key_test.go` 验证。

恢复时满队列的消费进度检查见 `internal/studio/recovery.go`，专项见 `internal/studio/recovery_backpressure_test.go`。仅测试的下载通道显式领取与完成检查在 `internal/studio/media_schedule_delivery_test.go`。087 的空 handle 断言在 `internal/studio/recovery_integration_test.go`。文档包括本台账、README、Wiki 的分页/故障/联合验收说明与导航、变更索引、任务源、TODO 与进度。

## 接口与数据影响

测试代理从真实等待行锁的 PostgreSQL 连接识别目标提交，读取目标进程标识且不记录协议密钥。启用邮件队列时，在切换媒体执行代次和保存恢复业务之前检查待投递记录的密钥身份。恢复写入因队列满而回滚后，至少一条真实事件投影成功才能重试；若持久计数已满且实际无事件可投影，启动报错退出。数据库格式仍为 020。

## 验证结果

- 环境为 Windows、Go 1.26.11、回环隔离 PostgreSQL 16.14、随机 schema，集成人确认前一进程退出后串行运行 PG。依赖代理首次超时，按原 go.sum 从可用镜像下载。
- 088 历史日志的目标 COMMIT 未被丢弃。旧测试分流器启动时捕获了指向内层代理的 URL，返回外层地址前又改写同一个 Host，第一条连接会回到外层并直接转发至数据库。用旧夹具 overlay 在本次环境尝试复现时，子进程初始化先超时（26.66s），未到达注入点。
- 改用行锁对应的实际 backend PID 后独立运行 `go test -count=1 -v ./internal/studio -run '^TestPostgresRecoveryProcessCommitAckUnknown$'`，目标用例 7.08s，通过页边界回执丢失、拒绝开放 HTTP、重启读回、单次事件与原键断言。
- 首轮组合专项 73.016s 退出 1。Kill、旧 generation、基础页字节通过；空 handle 断言只接受旧 JSON 字面值，另一个模式夹具改写已冻结模式，引发 020 投影外键 23503。修正断言以完整的零值 handle 和旧空对象作为允许表示；模式夹具从受理时即冻结 demo，再以 live 启动。
- 复验跨页、密钥和无进展三项 7.743s 全通过。258 Run 与 258 Task 共三页各自恢复，单页最大128条、75,125编码字节，清理257个旧租约；二次启动检查权益、业务、事件和原幂等。

- 显式启用 `RECOVERY_LARGE_TEST=1`，执行 `go test -timeout 90m -count=1 -v ./internal/studio -run '^TestPostgresRecoveryNewLargeBacklog$'`，用例 60.65s 通过。单次完整启动 42.326s，处理 12,002 条、清理 6,001 个租约，将先前 9,999 条及新事件共 22,001 条全部投影。Run 和 Task 各 47 页，最大 128 条、74,601 编码字节。二次启动 0.688s，原业务快照一致，外发 0 次。装载 12.803s 与迁移单测量 0.093s 另列。[完整聚合测量](../wiki/assets/recovery-large-2026-11-21.json)
- 无 PG/渠道环境的 `npm run check` 通过（71.39s，10 项治理、301 项前端、Go vet/test/build）；同一构建的基础、runtime、catalog、references、library、conversations 六套 smoke 均退出 0（9.09、1.21、1.67、7.81、7.69、1.44s）。构建 SHA-256 为 D4A7DE7F9D45C09B5BEC635844A6A64BC258E5AD1CCBF83E69DABB527359D75E。

- 全量命令 `npm run test:db`（隔离库，Go 包串行，含真实 pg_dump/pg_restore 与签名版本化环回 S3）。首轮 studio 489.295s 退出 1，唯一失败为下载冷却恢复用例只记录到 7 次成功。旧测试只等待步进函数返回；它允许未领到任务时直接返回。改为有界等待指定 lane 领取、八项终态和实际成功下载，再断言渠道容量。定向重复 5 次均通过（26.999s），随后将测试等待改为同步执行。最终全量 studio 540.412s 通过。A3.3 的两个可选大规模计划/升级测量本次未重跑；A4.1 的可选大积压完整启动已独立执行。
- 本机 S3 使用校验过归档与二进制 SHA-256 的 SeaweedFS 4.53；独立版本桶验证两版本并保留对象。全量通过本地与 S3 保留对象的 PG 联备，不涉及远端服务灾备。任务、Wiki、联备和变更记录链接均已纳入治理检查。

## 风险与限制

A4.1 仍保留热缓存；跨 512 条历史资源受理与 100k 活跃、100k 历史完整启动由 A4.2 验收。供应商 HTTP 只使用环回服务。

## 回滚方式

按本条记录撤回应用与测试改动即可回到 088 的代码。已提交业务、恢复事件、私有目录与密钥按现行联备流程保留。回滚后需重新考虑旧版本的邮件密钥检查时点。

## 文档同步

README、Wiki 导航、变更索引、任务源、生成的两份 TODO 与推进记录同步更新。见[恢复验收](../wiki/recovery-validation.md)。
