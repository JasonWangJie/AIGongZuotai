# 195 项目自由画布的版本与私有来源

## 目的

V17-G 为项目所有者提供可编辑、可保存和可重开的自由画布。连线记录节点之间的关系。图中只有绑定已有 Run 的节点才显示实际运行状态。

## 范围

每个持久项目可以保存一份画布，最多 64 个不可变修订。每版最多 2000 个节点、4000 条有向边和 128 组。节点以整数坐标定位，组保存折叠状态；保存前核对每条边的两端、节点和组的归属，拒绝悬空端点、自环和重复有向边。项目所有者可保存空图。003 节点草稿、032 协作草稿、执行工作流和视频时间线继续依各自入口使用。

## 实际文件

- `db/migrations/037_project_canvases.sql` 增加版本、来源、约束和不可变触发器。
- `internal/studio/canvas.go`、`internal/studio/canvas_store.go` 负责图结构、容量、事务授权、版本和收据。
- `internal/studio/canvas_sources.go`、`internal/studio/canvas_http.go` 负责已批准图片、真实 Run 快照和私有缩略图请求。注册方法为 `a.registerCanvasRoutes(mux)`。
- `internal/studio/canvas_test.go` 含图结构、升级、授权、并发、背压和提交响应丢失用例。

## 接口与数据影响

基址为 `/api/v1/projects/{project}/canvas`。GET 读当前稿，PUT 使用 `Idempotency-Key`、`expected_revision` 和 `document` 保存。GET `/receipt` 按同一个请求头查原修订。GET `/revisions` 按 `before_revision` 倒序分页，默认 20、最高 50；GET `/revisions/{revision}` 读固定版。新键 CAS 冲突返回 `CANVAS_CONFLICT`。64 版已满时新保存返回 `CANVAS_REVISION_LIMIT`，仍可读取或重放此前的保存。画布版本、冻结来源、审计和项目 Outbox 同事务提交。

每版最多绑定 16 个图节点和 16 个 Run 节点。图节点提交项目图库资产 ID 和当时的元数据修订。服务端另存原件 SHA-256、已登记来源及缩略图身份。仅允许本项目 active、明确审核通过、有已登记 PNG 缩略图的 PNG/JPEG 原件。改名后仍可查看相同内容，回收或收回批准后，原版本和原键收据仍可查，来源显示不可用。GET `/revisions/{revision}/sources` 单独读取来源；GET `/revisions/{revision}/nodes/{node}/thumbnail` 输出前复核当前会话、来源和已读缩略图摘要。Run 仅返回同项目实际状态和序号。客户端只提交已有业务 ID，不提供第三方 URL、文件路径、运行状态或执行命令。

## 验证结果

在 036 集成源上叠加 037 和单行路由，`go test -overlay <overlay.json> -count=1 ./internal/studio -run '^TestCanvas' -v` 通过，0.194 秒。合法数据分别为 500 节点/1000 边和 2000 节点/4000 边，另覆盖无效关系、越界坐标及禁止的输入字段。`go vet -overlay <overlay.json> ./internal/studio` 通过。

真实 PostgreSQL 验证由集成人串行运行。复跑命令为 `go test -p 1 -count=1 -timeout 8m ./internal/studio -run '^TestPostgresCanvas(OwnerHistoryFrozenSources|CompetingWritersCapacityAndOutboxRollback|LostSaveReceipt|Upgrade036PreservesExistingData)$' -v`。测试分别核对本人/已获协作权限的其他账户、冻结原图及改名/回收、独立连接竞争、背压事务回滚、修订额度耗尽后原键仍可读、提交后响应丢失和 036 升级前后原业务记录。

## 风险与限制

需要 PostgreSQL 和真实账户会话。每个请求及规范化图文档最多 2 MiB。缩略图仅占用四个并发读取位，业务检查覆盖原件登记身份和此次交付的缩略图字节。保存和查看画布不启动生成。画布平移、拖动、分组折叠、键盘操作和帧耗的浏览器证据分别由 196、197 提供。

## 回滚方式

037 写入后使用能识别 037 的服务和维护工具。需要回到 036 时，停写后恢复升级前的完整数据库、配套私有文件、目录 owner 和启用的密钥，并校验原修订和私图。保留迁移登记及原键回执。

## 文档同步

集成人统一登记 V17 的实际完成条件、README、Wiki、接口说明和变更索引。画布后端的结构和正确性验证与浏览器的 1 分钟实机测量分别记载。

## 037 画布实际平移验收

在本机 Intel 集显、Chromium 157、1920×1080 下，两档各实际拖动平移 60 秒，期间私有缩略图已解码、已受理 Run 持续活动。500 节点/1,000 边的 3,599 个帧间隔 P95 为 16.8 毫秒，最多 25 个详细节点；2,000 节点/4,000 边的 3,601 个间隔 P95 为 16.8 毫秒，最多 100 个详细节点。两个项目实际保存并重开 4 个修订，编辑与平移新增生成 POST 为零。 两组最大间隔分别为 67.2 和 33.3 毫秒。137.271 秒专项通过。报告使用前台浏览器 rAF 间隔，系统可用内存约 31.5 GiB；当次网络按 20 Mbps / 80 ms 配置，实际往返与吞吐校准单列。桌面与 390 CSS 像素截图已人工核对。 [全部帧间隔与逐秒样本](../wiki/assets/performance-canvas-037.json)。
