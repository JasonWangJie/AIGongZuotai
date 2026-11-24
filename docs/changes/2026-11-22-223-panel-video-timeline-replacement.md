# 223 将指定分镜生成的视频替换到剪辑稿的一个镜头

## 目的

V20-B，H02/H03/H04。创作者在多次视频生成后，能明确选择其中一个已交付的结果和剪辑稿中的目标镜头，保留旧版供比较或导出。

## 范围

依赖 222 的 038 分镜视频受理关系、030 的版本化剪辑稿和私有导出。替换由当前项目所有者发起。普通已交付视频按实际发布状态显示；启用了输出审核的任务须有允许决定及与候选原件相符的内容身份。

## 实际文件

- `internal/studio/panel_timeline_replace.go` 验证项目、分镜版本、镜头、任务和所选 MP4，生成一版剪辑稿。
- `internal/studio/video_timeline_http.go` 在原项目认证中间件内注册替换入口。
- `internal/studio/video_timeline_store.go` 将既有保存的原键回读、版本检查、版本登记和收据写入用于两种保存操作。
- `internal/studio/video_timeline_store_assets.go` 将固定版本的镜头/来源读取和当前素材授权检查分开，以便按统一顺序一次锁住替换后的素材集合。
- `internal/studio/panel_timeline_replace_test.go` 覆盖输入边界、授权、媒体故障、确认丢失、权益、两版独立解码和恢复。

## 接口与数据影响

`POST /api/v1/projects/{project}/video-timelines/{timeline}/versions/{version}/clips/{clip}/replace-from-panel` 需要登录、当前项目与 `Idempotency-Key`。JSON 字段为 `comic_version`、`panel_id`、`task_id`、`asset_id`、`asset_revision`、`in_ms`、`out_ms`。成功返回 201 和 030 固定版本。相同原键和输入重放返回同版并带 `Idempotency-Replayed: true`。客户端丢失回执时使用 `GET /api/v1/projects/{project}/video-timelines/receipt` 携带原键只读核对。

版本检查、资产重新授权、来源绑定验证、新版本及原键在同一个事务内完成。替换保留原镜头 ID、标题和位置，其他镜头保留原元数据修订、时段及完整原件。选中视频须来自同一项目、分镜与指定任务，且属于当前可读取的已发布 MP4。存在 018 交付收据或冻结审核时，另外核对对应的原件内容身份。原键先于当前版本和素材资格，重读时仍投影当前可用性。实现复用 030 数据表和 038 绑定，无新增迁移。保存、导出和读回都不调用生成供应方或发起收费。

## 验证结果

- 设置 `TEST_VIDEO_FFMPEG`、`TEST_VIDEO_FFPROBE` 为本机 FFmpeg 9.1 和 ffprobe 后，运行 `go test ./internal/studio -run '^Test(PanelClipReplacement|VideoTimelineStoreBounds)' -count=1 -v`。隔离源码通过三项。FFmpeg 以每镜 250 毫秒分别导出红/红与红/蓝双镜头 MP4，再独立取两段的 RGB 和 PCM，判定画面及 440/880 Hz 音调。该次专项耗时 4.76 秒。
- `go test ./internal/studio -run '^TestPostgresPanelClipRejectsUnknownFailedForeignAndRecycled$' -count=1 -v` 供集成人在本机隔离 PostgreSQL 执行；包含另一账户、跨分镜/任务、过时素材修订、原件摘要不符、生成驳回、输出隔离、提交确认不明和回收。
- 设置两项编解码器和 `TEST_POSTGRES_BIN_DIR`，执行 `go test ./internal/studio -run '^TestPostgresPanelClipReplacementReceiptsDecodedExportAndRestore$' -count=1 -v -timeout=5m`。用例通过正式按镜报价与受理生成替换素材，断开已提交替换的 HTTP 应答，GET 原收据，逐版核对两段画面/原声。停写后分别恢复 PostgreSQL 和私有目录，重新登录并再次解码，核对原报价仅一次冻结、一次实扣及 50,000 微点数，环回媒体 POST 总数为两次（原素材和分镜生成各一）。此处记录验收条件，真实数据库结果由集成人追加。

## 风险与限制

030 的累计 30 秒、8 镜头、128 修订、单片 32 MiB 等边界仍适用；源视频实际容器、时长和解码限额在导出时由 FFmpeg/ffprobe 入口复核。历史版中的已回收或隔离视频不可重新下载。环回模型和本机账本证据不能代替真实收费供应方或商户结算。

## 回滚方式

停用新替换入口，既有 030 版本与收据可继续按原入口读取。需要恢复旧数据格式时使用 038 升级前配套的停写数据库及私有目录备份，保留升级窗口内的原键、账务和新增修订供核对。

## 文档同步

集成人统一更新 README、Wiki、进度、任务源、OpenAPI 与台账索引。223 与 222、工作台及真实 PostgreSQL 证据合并验收后登记 V20-B 的完成范围。

## 集成人的 PostgreSQL 复验

038 真实 PG + HTTP 专项通过。来源不明、失败、其他任务和回收门禁测试 15.73 秒；断开替换回应、原收据、双版独立解码及停写恢复 24.81 秒。后者核对原报价只结算一次 50000 微点数。
