# 151 · V21-B1 项目漫剧分镜工作台

## 目的

创作者在项目分镜本中维护故事、角色卡与场景卡，将同一角色或私图复用于多个镜头，确认固定版本后取得文字可继续编辑的 PowerPoint。

## 范围

2026-11-22，关联 V21-B1、H01、H02、H08、H10。每个持久账户项目独立保存一本分镜本。桌面与窄屏均可操作设定、镜头、审核可见的项目配图与版本。后端的原片授权、版本和收据由同批存储及接口台账记录。

## 实际文件

- [App.tsx](../../apps/workbench/src/App.tsx)、[types.ts](../../apps/workbench/src/types.ts) 将「漫剧分镜」加入工作台导航和项目离开保护。
- [ComicWorkbench.tsx](../../apps/workbench/src/ComicWorkbench.tsx)、[comic.css](../../apps/workbench/src/comic.css) 实现设定卡、故事、角色多选、镜头次序和私图预览。可见版本并发变化时可以核对服务器故事、设定与镜头，保留本页修改。
- [comics.ts](../../apps/workbench/src/comics.ts)、[useComicEditor.ts](../../apps/workbench/src/useComicEditor.ts) 维护项目作用域、可写字段、固定版本、显式作者确认、独立原键与只读核对。
- [comicExport.ts](../../apps/workbench/src/comicExport.ts) 核对原清单与原图，完成超长镜头分页。复用 [presentationExport.ts](../../apps/workbench/src/presentationExport.ts) 的版式测量、受限图片读取与原生 PowerPoint 写入。两类业务分别保留自己的项目身份及版本。
- [comics.test.tsx](../../apps/workbench/src/comics.test.tsx) 和 [comic-browser-smoke.mjs](../../apps/workbench/scripts/comic-browser-smoke.mjs) 覆盖单测与真实 Chromium，供持久化测试夹具调用。

## 接口与数据影响

项目的 `comic` 路由按当前会话读取、保存固定版本与历史。镜头引用稳定的角色/场景卡 ID，可选项目图片同时携带修订。确认只接受当前已保存版本，由用户核对后再次明确提交；新编辑另存为待确认版本。保存与确认分别保留操作种类和原键，网络回执不明只读该操作收据。页面会话仅存用户和项目隔离的操作编号，不保存故事正文或私图。已确认的旧版可独立下载。

工作台从分镜版次专属清单读取每镜图片，验证权限、项目、镜头、修订、尺寸、字节总量和完整 SHA。PowerPoint 完成序列化后再次 GET 同一未经分页的清单，核对成功才向用户提供文件。导出不创建演示文稿数据库对象。图片每镜仅嵌在首张，动作和对白以文字形状交付，故事与所选设定进入演讲备注。超出版面时依实际可读范围按字素另起续页；过长镜头标题完整进入可编辑文字，镜头版次与页序仍可辨识。正常短镜头维持一镜一页。

## 验证结果

- 工作台执行 `node node_modules/vitest/vitest.mjs run --no-cache --configLoader runner`，40 个文件、458 项全部通过，15.23 秒。其中漫剧专项 17 项，原演示文稿专项 23 项。验证长中文、硬换行、字素、顺序和图片页唯一性，覆盖过时输入、收据错配、确认、跨项目与导出最后一次复核时撤权。
- TypeScript 检查通过。Vite 生产构建转换 142 个模块，12.92 秒，分镜入口及原生 PowerPoint 写入工具均按需加载，锁定依赖保持原版本。
- 在本机环回模拟 API 下以真实 Chromium 登录两个窗口，用同一项目 PNG 与同一角色创建两镜，保存/确认各丢弃一次响应后只读核对。实际下载 v1、改名修改镜头的 v2 与重开 v1 三份 PPTX，生成桌面与 390 像素截图。可选 `COMIC_BROWSER_DENSE=1` 又保存确认单镜 v3；独立读取真实下载的 Office XML，6 页原生正文按序拼合共 1061 字符，与预置原文逐字相同。仅首页嵌图，图片 SHA 与预置私图一致。该前端独立检验使用模拟业务响应。真实 PostgreSQL 鉴权、原片、恢复和外部软件渲染由集成人另行实测。
- 浏览器输入使用 `COMIC_BROWSER_URL`、绝对目录 `COMIC_BROWSER_OUTPUT`、本机夹具用户、项目与原图身份。输出 `comic-expected.json` 和可选密集版预期来自脚本填入的原文及夹具原图 SHA，独立于导出的清单和 Office 容器。

## 风险与限制

分镜本需持久化账户模式。回收或隔离原图会阻止带图版本导出；已经下载至用户设备的文件由用户管理。临时修改仅在本页，离开项目或关闭标签页后不提供离线跨会话草稿。该批覆盖作者本人确认与图文分镜，真人多人协作、供应商生成、角色一致性模型和真正视频成片另验。

## 回滚方式

可撤回前端入口并停用导出。保留既有版本、作者确认和私图；退回旧服务端需停止写入并使用升级前的配套数据库、私有文件、对象版本、目录归属和密钥。

## 文档同步

集成人负责[项目首页](../../README.md)、[Wiki](../wiki/README.md)、[变更索引](README.md)、[任务源](../planning/tasks.json)、生成 TODO 与[进度](../planning/progress.md)。本记录与 031 存储、接口、联合验收台账共同描述该子批。

