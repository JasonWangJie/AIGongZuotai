# 113 · 首帧视频工作流画布

## 目的

V19-B。让用户在工作流画布选择视频模板、指定时长和模型、跨会话确认首帧，逐步授权视频生成并查看私有成品。

## 范围

新模板 `text-image-approval-video` v2 有文本、图片、人工选图和视频四个节点。文本提示与选定图片分别连接到视频的两个输入。既有选图模板 v1 继续以人工选图为终点。

## 实际文件

- `apps/workbench/src/types.ts`、`modelCatalog.ts`、`workflows.ts` 读取公开模型的首帧能力、完整冻结来源及节点输入。视频模型必须明确启用 `video_first_frame_supported`。
- `App.tsx`、`WorkflowWorkbench.tsx`、`WorkflowEditor.tsx`、`WorkflowRun.tsx`、`workflow.css` 提供两套模板、双端口、完整视频参数预设、独立确认/报价、私有播放器和窄屏布局。身份切换继续按项目和用户区分组件；已选的画布工具不被稍后到达的登录读取重置。以上文件位于 `apps/workbench/src/`。
- `apps/workbench/src/workflowVideo.test.tsx` 覆盖视频模板和运行。浏览器脚本 `apps/workbench/scripts/workflow-browser-smoke.mjs` 以 `WORKFLOW_BROWSER_VIDEO=1` 选择视频分支。

## 接口与数据影响

图片确认后进入 `VIDEO_READY`，用户另行授权本步。读取的 `first_frame` 须与原工作流已选资产的项目来源、资产 ID、子任务、产物、修订和内容版本对应，完整的服务器输入直接用于独立报价。POST 仍使用稳定原键。进度与回执采用项目内 GET。运行详情完成授权补读后，播放器仅使用该视频子任务当前可见的私有库内容地址。普通上传、外网视频地址和浏览器指定任意首帧均无入口。

## 验证结果

- `npm --prefix apps/workbench run test -- --run src/workflowVideo.test.tsx` 七项通过，2.34 秒。覆盖模型能力、两版图隔离、时长规格、独立连接、三种首帧错配、显式报价及已授权视频成品。
- 全工作台 `npm --prefix apps/workbench run test` 共 33 个文件、336 项通过，8.33 秒。TypeScript 和 Vite 生产构建通过。
- 本机环回前端夹具与 Chromium 完成两次独立登录、分别确认三个生成步骤、选择图片、读回冻结版本及 HTML 视频元数据。390 像素宽视口通过私有成品实际命中检测，桌面编辑和窄屏画面已检查。截图位于 `.runtime/v19b-mock-browser/`。此夹具用于界面验证；PG 与供应端输入字节由集成人的独立联调验收。
- 集成人串行执行实际 PostgreSQL 与受控媒体环回的 Chromium 视频专项通过，16.90 秒。已核看 `.runtime/v19-video-browser/workflow-video-completed-mobile.png`，已选图片、私有视频和三类子资源均可辨识，播放器进入可解码状态。

## 风险与限制

公开目录只能表明运维声明了首帧能力。浏览器检查节点身份与私有播放；供应商是否真实接收选中图片由服务端联调确认。工作流重跑、撤销、实际供应商效果另有验收范围。

## 回滚方式

先保留受理中视频任务的原 handle、引用文件及权益证据。前端回退会移除新模板和播放器；持续运行的任务须按其原执行记录处置。完整回退遵循相应持久格式的停机联备恢复。

## 文档同步

集成人同步根 README、Wiki、任务源、生成 TODO 和台账索引，按[工作流实施契约](../planning/v19-contract.md)记录 PG 联调与后续阶段。

