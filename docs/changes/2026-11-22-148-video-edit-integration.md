# 148 · 私有视频剪辑、双版本成片与恢复验证

## 目的

让已登录的创作者从项目中选择已批准的视频，裁切画面和原声，按分镜顺序导出可以独立播放的 MP4。

## 范围

V20-A 涉及 H02、H03、H04、H08、H10，依赖 V08 私有资产和已有的视频交付。本机使用 PostgreSQL 和 Windows 安全进程限制。现有素材的剪辑可以独立于 V07 外部供应端验收。

## 实际文件

- `internal/studio/video_timeline_http.go`、`http.go`、`presentation_http.go` 注册八项项目作用域操作，提供严格输入与分页。稿件、原件及导出分别见 [146](2026-11-22-146-video-timeline-store.md) 和 [147](2026-11-22-147-timeline-private-export.md)。
- `cmd/server/main.go`、`internal/studio/app.go`、`.env.example`、`scripts/lib/process.mjs` 接入三项显式配置、启动/停止及自动化配置隔离。
- `internal/studio/ledger_admin.go`、`operations_admin.go`、`storage_maintenance.go`、`storage_maintenance_delivery_test.go` 要求精确 030 格式；`db/migrations/030_video_timelines.sql` 为增量。
- `internal/studio/video_timeline_browser_test.go`、`media_delivery_integration_test.go`、`apps/workbench/scripts/video-edit-browser-smoke.mjs` 生成两个真实的本机音视频输入，经过正常任务、审核、账户授权和浏览器完成操作。
- `scripts/verify-video-edit.py` 用 FFmpeg/FFprobe 独立解码原件与下载件。[真实编辑器截图](../wiki/assets/video-editor-desktop-2026-11-22.png)及[逐帧摘要](../wiki/assets/video-edit-render-2026-11-22.json)已归档。

## 接口与数据影响

草稿写入要求原操作键和预期版本。旧稿留存顺序、裁点与冻结的素材身份。读取原片及当次导出均复核当前权限和内容。已下载的副本由下载人保管。剪辑不创建生成任务或权益冻结，媒体执行器实际接受的画布、输入及音轨要求见 [144](2026-11-22-144-bounded-video-export.md)。

## 验证结果

- PostgreSQL 首组 11 项 88.149 秒通过，含 029→030、精确维护栅栏、并发容量、原键 COMMIT 应答丢失和实际 Chromium。浏览器用两个窗口改序、裁切、核对旧键、处理版本冲突、恢复为 v3；实际只生成两份原素材，剪辑与下载时无新增生成或权益冻结。
- 停写、备份并恢复到新数据库和私有目录的专项 19.579 秒通过。重登后从原固定版本读回两份文件，逐字节相同。私有下载专项 11.851 秒通过，覆盖匿名 401、跨账户 404、Range 原字节、回收后同版导出拒绝与未受影响镜头继续可读。
- `python scripts/verify-video-edit.py <浏览器输出> --ffmpeg <工具> --ffprobe <工具>` 逐帧还原 v1 的 67 帧和 v2 的 58 帧，分别为 2792、2417 毫秒。四个镜段的原片运动标记位置误差均为 0 像素。音轨随重排在 440.1 Hz 与 880.2 Hz 之间切换。输出均为 1280×720、24 fps、48 kHz 双声道。
- 前端 39 文件 441 项通过，TypeScript 与 Vite 构建通过。集成人在独立副本执行 `npm run check` 和全部六套冒烟，10 项任务治理、441 项前端、Go 测试/vet、构建与文档均通过。
- 固定 030 在 PostgreSQL 16.14 执行 `npm run test:db`，studio 1461.356 秒、真实视频执行器 3.163 秒。八项浏览器链路、十万/二十万数据、备份与故障专项均运行，无失败或跳过。剪辑浏览器 22.57 秒，回收与范围下载 11.14 秒，联备恢复 21.40 秒。全量浏览器的两份 MP4 与首次专项的 SHA-256、帧位及音调逐项一致。

## 风险与限制

本机验收基于可重复的合成素材。真实供应商的视频编辑、续写、角色一致性与生产并发性能需要相应渠道和测试环境。导出打开前需要服务器管理员配置 Windows 编解码工具、独立私有临时目录及 PostgreSQL 账户认证。

## 回滚方式

停用编解码配置并重启可关闭新导出。需要回退到 029 时，先停写，保留 030 数据库、私有目录、对应对象版本和密钥，再恢复升级前的一致备份及配套程序。逐项核对恢复点后的稿件和业务写入。

## 文档同步

同步根 README、AGENTS 协作约束、Wiki 导航、[030 架构与用法](../wiki/decisions/0030-video-timelines.md)、数据库迁移说明、OpenAPI、任务源与生成的两份 TODO、推进记录和台账索引。
