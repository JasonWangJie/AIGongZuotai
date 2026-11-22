# 145 · V20-A 项目私有视频剪辑工作台

## 目的

让创作者从当前项目已交付的 MP4 选取镜头和裁点，检查原片、保存固定版本，明确发起一次合成并下载完整成片。

## 范围

2026-11-22，关联 V20-A、H02、H03、H04、H08、H10。该入口依赖持久化项目、认证会话与已登记的原视频。播放和裁点可在本机合成关闭时使用。

## 实际文件

- [App.tsx](../../apps/workbench/src/App.tsx)、[types.ts](../../apps/workbench/src/types.ts) 接入导航与项目离开保护。
- [VideoTimelineWorkbench.tsx](../../apps/workbench/src/VideoTimelineWorkbench.tsx)、[TimelinePlayer.tsx](../../apps/workbench/src/TimelinePlayer.tsx)、[video-timeline.css](../../apps/workbench/src/video-timeline.css) 实现镜头素材栏、原生播放器、毫秒裁点、顺序预览、固定版本与历史对照。
- [videoTimeline.ts](../../apps/workbench/src/videoTimeline.ts) 绑定同源 API、固定版本、保存原键、输入校验与合成流读取。
- [videoTimeline.test.tsx](../../apps/workbench/src/videoTimeline.test.tsx) 覆盖前端边界与故障恢复。[video-edit-browser-smoke.mjs](../../apps/workbench/scripts/video-edit-browser-smoke.mjs) 使用 [local-browser.mjs](../../apps/workbench/scripts/local-browser.mjs) 的实体点击激活有声视频，下载成片与独立期望清单，保存桌面和 390 像素截图。
- [OpenAPI](../api/openapi.yaml)、[030 架构决定](../wiki/decisions/0030-video-timelines.md)、[数据库迁移](../wiki/database-migrations.md) 与实际项目路由、约束和升级方式同步。

## 接口与数据影响

项目剪辑列表、历史、保存、原键收据、能力、固定版本、指定镜头原片和空正文成片导出均通过本平台。新稿取零作为预期版本，后续保存按固定版本核验。草稿仅提交镜头名称、稳定镜头 ID、库资产身份与修订、入出点和顺序。保存回执未知时只读原键。并发修订保留本页更改，用户可另存新稿。恢复历史版会追加版本。

镜头总长最多 30000 毫秒，单一源出点最多 300000 毫秒，每稿 1–8 镜，所有累计边界按 24 fps 计算，每镜至少一帧。浏览器从授权原片读取实际时长，提供入/出点定位与播放。导出选中的固定版本，下载前检查响应 MIME、附件、字节数、64 MiB 独立浏览器上限、MP4 容器头、实测时长和完整 SHA-256。断线、取消或未确认下载后可 GET 原版本再自行决定是否合成。暂存的操作编号按本人和项目隔离，不包含影片字节、指令或凭据。

## 验证结果

- 从工作台目录执行 `node node_modules/vitest/vitest.mjs run --no-cache --configLoader runner`，39 个文件 441 项全部通过，18.94 秒。包括本批 15 项，覆盖帧边界、可读资产、不可用工作线程、只读字段、旧版冲突、另存、未知保存、取消、原版本核对、响应头与真实 SHA 校验。
- `node node_modules/typescript/bin/tsc --noEmit -p tsconfig.json` 通过。Vite 生产构建转换 137 个模块，剪辑器独立按需加载；本轮构建 13.35 秒。
- 在隔离环回模拟 API 下，以本机真实 Chromium、FFmpeg 生成的红蓝 MP4 运行浏览器脚本，桌面和 390 像素截图均生成。两窗口验证保存失联查原键、v1/v2 改序和裁点、冲突留稿、以旧版恢复 v3、原声片段定位及两次 MP4 下载。集成人随后在真实 PG 与 HTTP 私有下载运行同一浏览器，45.42 秒通过；下载件的独立逐帧、音轨核验以及联备恢复见 148。

## 风险与限制

临时剪辑保留在当前页面；关闭标签页会丢失尚未保存的文本与裁点。已经下载的文件由用户本地管理。操作中断不会恢复网关请求、账务或供应商任务。该子批交付现有获准片源的本机剪辑。参考视频编辑、角色延长、商业网关与生产并发容量另验。

## 回滚方式

隔离变更可撤回本批导航、编辑组件与脚本，同时保持 030 服务器与数据库配套。需要回到 029 时停写并恢复升级前整套数据库、私有目录、对象版本和密钥，然后启动配套程序。保留用户既有下载与联合验收证据。

## 文档同步

本记录同步上列 API、030 架构说明及迁移页。集成人负责[根 README](../../README.md)、[Wiki 导航](../wiki/README.md)、[台账索引](README.md)、[任务源](../planning/tasks.json)、生成的 TODO 与[进度记录](../planning/progress.md)。

