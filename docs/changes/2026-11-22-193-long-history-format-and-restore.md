# 193　长会话的配置、离线维护和恢复

## 目的

让现有部署继续按原节点数受理新请求，需要长历史的 PostgreSQL 工作台可以单独启用。升级与恢复必须能验证已经受理的原回复、分支、冻结上下文和幂等身份。

## 范围

V17-F2 的服务配置、格式栅栏及联备。192 负责 036 迁移、上下文和分页，191 负责前端正文缓存；本项验证它们依赖的启动和维护边界。

## 实际文件

- `cmd/server/main.go`、`cmd/server/main_test.go`、`internal/studio/app.go`、`.env.example`、`scripts/lib/process.mjs` 和 `internal/studio/conversation_history_config_test.go` 管理配置及隔离环境。
- `internal/studio/storage_maintenance.go`、`ledger_admin.go`、`operations_admin.go`、`order_reconciliation.go` 使离线维护、权益、授权及只读订单核对使用相同的精确格式。
- `internal/studio/storage_maintenance_delivery_test.go` 核对上一版、缺失 019、未来版本三类数据库，`internal/studio/conversation_history_restore_test.go` 核对恢复原授权会话、原页和原键。
- `docs/api/openapi.yaml` 的 Conversation.node_count 描述 036 完整返回范围，默认与显式上限分别说明。
- 决策、性能 Wiki、任务源、生成清单、导航和进展由集成时同步。

## 接口与数据影响

标准服务接受 `LONG_CONVERSATION_HISTORY_ENABLED=true|false`。缺省为 false，显式空串、大小写变体或数字报配置错误。内存演示拒绝开启。关闭时原默认写限为每会话 256 节点、每分支 128 节点；启用并重启后，上限为 32,768 和 16,384。相同 036 格式下关回开关，授权用户能继续翻页，原幂等键仍读回原受理。额度、模型上下文与单次输出沿用独立约束。

## 验证结果

隔离目录的 Go overlay 编译并运行严格配置与内存模式拒绝测试，cmd/server 0.600 秒、studio 0.153 秒。真实 036 格式、上一版本工具、完整联备恢复与深路径能力按集成人串行运行的结果补登。

## 风险与限制

开启扩大可持久化的历史，写入、索引、数据库备份会增加存储需求。已有历史纳入 036 的祖先索引；真实库升级耗时随数据量变化。不得从 035 全量成绩推断 036 迁移时间或生产容量。

## 回滚方式

恢复默认写限可关闭配置并在同一 036 版本重启。回到 035 格式须停写并联备恢复原数据库、两棵私有目录、原 owner、已启用的密钥和对象版本，同时使用匹配的旧服务和离线工具。保留新格式中已受理消息的原记录，按回滚点核对差异。禁止删除迁移编号或手工删消息以绕开栅栏。

## 文档同步

[配置与恢复决策](../wiki/decisions/0036-long-conversations.md) · [性能与验收](../wiki/performance.md) · [批次任务源](../planning/tasks.json)。

## 036 集成人实测

上一版、缺 019 和未来版本三个不兼容库均被离线工具拒绝，5.59 秒。首次联备跑到恢复后原键请求时，测试错误期待 200；实际原协议在重放时仍返回 201 并标明 Idempotency-Replayed。按原契约加验响应头及原 Run 后，通过 66 轮正常受理、停写 pg_dump/pg_restore、私有树和原密钥、关闭长写入后的七页逐字节数据、原键和 429 新写入限制，17.30 秒。

## 036 单分支正式验收

本机 PostgreSQL 通过正常受理入口完成同一分支 5,000 轮，逐轮核对上游上下文。Chromium 读取 500 个授权页、索引 10,000 条消息，实测最多挂载 10 行、缓存 120 条正文；首、中、末三处淘汰内容均通过原页重读。显式继续后原库有 10,002 个节点与 5,001 份冻结上下文，原键重放未增加上游调用。专项 219.573 秒。 [完整聚合数据](../wiki/assets/performance-conversation-history-036.json)。
