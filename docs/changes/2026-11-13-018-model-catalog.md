# 018 · V06 模型目录、能力与历史快照

日期：2026-11-13。关联批次：**V06**；最终状态由根负责人依据[任务源](../planning/tasks.json)验收。

## 目的

用有限服务端目录代替写死模型和独立参数枚举，使新请求经过版本/能力校验，并保持目录调整后历史幂等与执行请求不变。

## 范围

启动加载JSON目录、公开版本/ETag、精确预设、有效参数与能力快照、受理前冻结媒体原始请求、LLM私有绑定、旧hash兼容、恢复边界与格式版本栅栏。provider共享Transport/参数编码由019实现，前端模型选择由020实现，根维护OpenAPI和联合验收。

## 实际文件

- `internal/studio/catalog.go`、`catalog_test.go`：有限目录、严格JSON/重复键检查、版本、缓存JSON、HTTP目录、参数解析与单元/PG验证。
- `internal/studio/model.go`：model_version请求字段、历史model_name/version/capabilities、TaskParameters及深拷贝。
- `internal/studio/app.go`：目录初始化、幂等优先、解析能力/准入、受理时PrepareModel、LLM绑定与快照返回。
- `internal/studio/http.go`：GET models、bootstrap一致目录，移除提前执行的当前模型校验及目录默认归一化。
- `internal/studio/admission.go`：runtime由实际目录和占用历史生成模型列表，不再补不存在的studio模型。
- `internal/studio/persistence.go`、`internal/studio/migrations/005_model_snapshots.sql`：旧live无快照恢复保守处理、可测试迁移加载器与格式栅栏。
- `cmd/server/main.go`、`.env.example`、`config/models.example.json`：配置入口和完整占位示例。
- `docs/wiki/models.md`、本台账：规则、兼容、恢复和验收证据。根负责其他Wiki/README/索引/任务源。

## 接口与数据影响

新增GET /api/v1/models，Catalog{version,models}和强ETag；bootstrap新增catalog_version并使用同一models。新Input可填model_version；目录版本变更409、模型停用/参数不支持422，均在幂等重放之后判定。Task/Run保存公开身份与能力快照，Task保存有效parameters；私有上游名称仅进入服务端绑定与已有submission存储，不进入API/日志/指标。

新task原始请求hash与有效执行参数分离；旧记录按原固定默认规则兼容。旧live缺handle和submission不会猜新配置重发。005是JSON数据格式兼容栅栏，无表重写；阻止旧二进制保存时抹掉新字段。

## 验证结果

- `go test ./...`、`go vet ./...`：目录核心和provider接入后通过。
- 真实PG `go test ./internal/studio -run TestPostgresCatalog -count=1 -v`：首轮三组通过，1.167秒；覆盖停用/改能力后的原键重放、原body执行、旧hash、鉴权先于304、旧live缺快照待核实与已知handle只查询。
- 真实PG `go test ./... -count=1`：全量通过，studio包15.059秒。
- 后补005格式栅栏专项 `go test ./internal/studio -run TestPostgresCatalogFormatFence -count=1 -v`：通过，0.447秒；001—004增量升级后保留旧输出，模拟旧文件目录的相同迁移加载器明确拒绝005。
- 固定历史JSON字面量SHA256测试通过，覆盖run/task旧字段顺序和omitempty，不使用今天的结构生成期望hash。
- 根另执行目录smoke首轮通过；其最终构建/PG全量/浏览器/性能证据由021记录。未使用真实付费服务；无TEST_DATABASE_URL的跳过不当成数据库通过。本机race环境限制沿用既有说明。

## 风险与限制

目录来自有限本地配置，变更需重启，没有后台编辑/热更新或供应商自动发现。运营者仍须确认参数实际支持，示例不是厂商支持承诺。新版哈希以原typed请求为准，省略值和显式值即使执行结果相同也可视为不同请求；重试须保持原body/key。旧live未持久原始提交的历史任务无法恢复当时映射，保守待核实。仍为单写服务，无分布式版本发布或跨实例配额。

## 回滚方式

升级前保留数据库和资产备份。005已应用后旧二进制将拒绝启动，禁止删除迁移登记绕过。需回退时在独立库恢复对应升级前备份并使用匹配程序；保留当前原库的新任务和快照。目录文件本身可以改回旧内容后重启，但不能用此方式改变已经受理任务的执行快照或抹掉未核实记录。

## 文档同步

[模型Wiki](../wiki/models.md)、示例配置和本台账同步；根维护README、模型/OpenAPI契约、Wiki/台账索引、V06任务与TODO及021联合验收。既有历史台账保留当时事实，不回写掩盖兼容边界。
