# 238 · V20-C 原视频两种任务的 API、来源隔离与 039 联备验收

## 目的

核验原视频操作的项目权限、计价、受理丢回应和联备恢复。

## 范围

V20-C / H02、H04、H06。覆盖普通工作台任务接口从原视频报价、原键受理到审核交付和内部 CREDIT 结算，以及 038 升级与停机联备恢复。业务依赖 234 的文件化 HTTP 适配器、039 的来源绑定和独立价格。此项只增加验收代码。

## 实际文件

- `internal/studio/source_video_operations_restore_test.go` 新增本机 FFmpeg 编解码、038 升级、039 授权 HTTP 操作和联备恢复用例。
- 工作流集成试具允许在原有磁盘档之外显式选择私有版本化 S3。受理中等待会话到期的测试对锁观测单独设六秒上限。
- `docs/changes/2026-11-23-238-source-video-integration-restore.md` 记录本批验证条件及边界。

## 接口与数据影响

测试通过普通视频任务生成带音轨的红色原片，先保存为已交付、审核通过的项目资产，再显式配置编辑与延长的规格及不同价格。编辑和延长各单独报价、受理；本机 HTTP 接收方检查路由、完整请求字段、原片的每一字节、方向、服务端请求键以及凭据边界。蓝色编辑结果和绿色延长结果各经授权下载并比较完整字节。任务行的文件标识、摘要、长度和实际私有文件必须与收到的请求匹配，每个操作仅允许一条对应金额的结算记录。

延长操作在应用完成受理后关闭 TCP 连接，客户端按原项目和原键 GET 收据。同工作区别的项目、同项目不同登录人、其他租户、旧修订、回收原片、错误方向和交叉使用报价均有负例。回收和恢复原片后可读原收据与重放原操作，原片的旧修订不能用于新受理。停写后将 PG、自有资产目录、参考目录和交付密钥恢复至另一隔离库，重开应用并复核相同收据、MP4、私有请求、结算及 HTTP 次数。038 种子保留旧分镜任务、绑定、收据和冻结字段，升级不新造来源记录，旧写者拒绝 039。

## 验证结果

- Go 1.26.11，清空 `TEST_DATABASE_URL`，设置本机 `TEST_VIDEO_FFMPEG`，执行 `go test ./internal/studio -run '^(TestSourceVideoEncodedFixture039|TestSourceVideoFrozenWireAndOriginalHandle|TestPostgresSourceVideoOperationsAndRestore039|TestPostgresSourceVideoUpgrade038)$' -v -count=1`。独立编解码和编辑、延长两个适配器 wire 子用例通过。两个 PostgreSQL 用例编译通过并明确跳过。
- 实库交由集成人串行执行。设置 `SOURCE_VIDEO_INTEGRATION=1`、`TEST_DATABASE_URL`、`TEST_POSTGRES_BIN_DIR`、`TEST_VIDEO_FFMPEG`，运行 `go test ./internal/studio -run '^TestPostgresSourceVideo(OperationsAndRestore039|Upgrade038)$' -count=1 -v -timeout 10m`。联备使用真实 pg_dump/pg_restore，需要隔离库的建库权限。本行记录执行入口，实测结果待集成人登记。

## 风险与限制

测试用本机 HTTP 渠道及确定性审核响应，真实供应商能力、商户收费和正式渠道仍需独立验收。此处的权益结算单位为内部 CREDIT。

## 回滚方式

移除新增测试即可回滚本项，不修改迁移、运行数据或应用行为。

## 文档同步

集成人同步根 README、Wiki、任务源、进度和台账索引。

## 集成人真实 API 与联备结果

原视频编辑与延长各一次生成和权益结算，丢回应后只读收据，三份 MP4 与两个私有请求文件在停写联备恢复后逐字节匹配。初次业务 23.78 秒、038→039 升级 2.24 秒。补验相同工作区其他申请人的报价被拒，以及原会话在账务与业务写入锁等待期间自然到期时，提交前拒绝并回滚任务、冻结、收据及权益。含新增断言的业务用例 21.68 秒通过。

在 PostgreSQL 16.14 与本机 SeaweedFS 版本化桶上，设 `SOURCE_VIDEO_INTEGRATION=1` 和 `SOURCE_VIDEO_VERSIONED_S3=1` 后执行 `go test -p 1 -parallel 1 -count=1 -run '^TestPostgresSourceVideoOperationsAndRestore039$' -v ./internal/studio`。同一对象键追加蓝色 MP4 为最新版本后，两次生成和恢复校验均取回最初登记的红色 MP4。业务、唯一结算、凭据和恢复复读用例通过，耗时 28.45 秒。恢复时保留该本机对象服务及其原版本，异地对象库重建仍须另验。
