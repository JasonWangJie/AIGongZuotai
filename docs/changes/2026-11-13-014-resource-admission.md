# 014 · V05 工作区与模型资源准入

日期：2026-11-13。关联批次：**V05**；最终状态由集成人依据[任务登记](../planning/tasks.json)统一验收。

## 目的

限制同一工作区跨项目持续创建未完成生成任务，保护当前单写服务。使幂等、取消、故障、缓存淘汰和重启不会破坏额度计数；它不实现计费权益或分布式配额。

## 范围

LLM/媒体的 workspace 与 workspace/model 双层占用上限、严格环境变量解析、工作区快照、PG非终态部分索引与验证。协作接入观测代理的SSE工作区上限、受保护指标及request_id；这些HTTP/观测细节由其独立台账说明。

## 实际文件

- `internal/studio/admission.go`：配置、校验、内存/PG占用聚合、快照与准入判断。
- `internal/studio/app.go`：幂等之后、事务受理之前执行额度检查；obs与metrics配置接线和慢订阅计数钩子。
- `internal/studio/migrations/004_admission_indexes.sql`：两类资源非终态workspace/model部分索引，不改历史迁移。
- `internal/studio/admission_test.go`：并发、模型/工作区隔离、重放、释放、parked恢复与真实PG故障验证。
- `cmd/server/main.go`、`cmd/server/main_test.go`：严格读取新环境变量，空值/零/负数/异常格式/超界拒绝启动。
- `.env.example`：准入默认值与独立METRICS_TOKEN说明。
- `docs/wiki/admission.md`、本台账：规则、实际边界、升级与验证；根负责人同步README/索引/TODO。

## 接口与数据影响

新生成超过额度返回429 ADMISSION_WORKSPACE_LIMIT 或 ADMISSION_MODEL_LIMIT，不新增资源、幂等或审计行。同键重放和409冲突早于限额；安全重新核对保持原任务一份占用。SSE采用ADMISSION_STREAM_LIMIT。三者由HTTP层提供Retry-After:3与请求标识，不承诺三秒内有空位。

默认LLM工作区64/模型32、媒体工作区128/模型64、SSE工作区32。所有未终态包含未知与待核实都占用；只有真实终态释放。数据库快照覆盖未载入/已淘汰记录，runtime只向当前工作区提供占用/排队/模型统计。新增迁移004索引，无新的配额业务表或费用字段。

## 验证结果

- `go test ./...`、`go vet ./...`：通过。命令行配置测试覆盖所有新环境变量的空值/零/负数/小数/非数字/溢出/范围上界。
- `go test ./internal/studio -run TestPostgresAdmission -count=1 -v`（真实隔离TEST_DATABASE_URL）：初轮两组通过，1.507秒。
- 增补13种资源状态与部分索引验收后，真实PG `go test ./... -count=1` 全量通过，studio包12.395秒；同时保留旧账户、草稿、审计、媒体与恢复回归。
- 内存测试验证并发12请求只受理1个、workspace/model双层、跨项目累计/跨工作区独立、重放/409先于quota、拒收不写幂等、取消/失败/完成释放、parked核对只查原任务。
- PostgreSQL验证parked缓存淘汰和重启仍计入、原键跨重启可重放、持久RUNNING检查点恢复INTERRUPTED释放、重新核对不重占、跨账户隔离、拒绝审计INSERT后整事务回滚且重启无虚假占用；004两条部分索引存在且13种状态聚合与Go终态定义一致。
- 未调用真实供应商或收费生成，未执行生产容量/多实例验收。本机race仍受CGO/C编译器缺失限制；无TEST_DATABASE_URL时PG测试跳过，不作为数据库通过证据。

## 风险与限制

仍依赖单实例全局状态锁、数据库单连接及session advisory lock，不是跨实例原子信号量。每次新受理/工作区快照增加聚合查询，部分索引减少历史扫描但不代表生产吞吐验收。额度以任务数而非图片数/token/费用计；仍受全局worker/队列/缓存上限影响。未知任务持续占名额，运营人工结案接口尚未实现。降额不强制取消已受理任务。

## 回滚方式

保留数据库与资产备份。修改环境变量可调整额度，但显式0不是关闭保护。004应用后旧二进制会拒绝未知迁移；若需回退程序，在独立数据库恢复匹配旧版本的升级前备份，不能直接删除迁移版本或用户记录。保留升级后的原库以免丢失新增数据。

## 文档同步

[准入Wiki](../wiki/admission.md)及本台账同步；观测代理更新runtime/OpenAPI，前端代理更新资源使用说明。根负责人维护README、Wiki/台账索引、V05任务状态及生成TODO，并执行联合检查。
