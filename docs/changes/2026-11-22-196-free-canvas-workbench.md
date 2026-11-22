# 196 项目自由画布工作台

## 目的

V17 的创作草稿增加自由画布。作者可以把项目笔记、已审核图片和已有对话放在一张可平移的图上，建立连线、分组并保存修订。原文字草稿和工作流继续使用各自的入口。

## 范围

H07、H08 / V17-G。正常入口为「创作草稿 → 自由画布」，首次进入才下载画布组件及样式。个人项目使用 195 的 PostgreSQL、登录和 037 版本接口。内存演示入口说明使用条件。界面支持键盘、鼠标及触屏，窄窗口将节点检查器放在视口下方。

## 实际文件

- `apps/workbench/src/App.tsx` 接入画布工具、当前个人项目、离开前的草稿提醒和懒加载。
- `apps/workbench/src/FreeCanvasWorkbench.tsx`、`CanvasInspector.tsx`、`FreeCanvasViewport.tsx`、`free-canvas.css` 提供视口、缩放、拖动、查找、属性、连线、分组、修订和来源操作。
- `apps/workbench/src/canvasScene.ts` 为节点和线段建空间索引。远景用 Canvas 2D 绘制标记和线段，近景使用可聚焦的真实节点按钮。拖动期间按动画帧更新受影响的节点和连接，释放指针后更新草稿坐标。
- `apps/workbench/src/freeCanvas.ts`、`useFreeCanvas.ts`、`canvasMerge.ts` 管理项目请求、来源、大小限制、原键回执及显式冲突合并。
- `apps/workbench/src/durableEvents.ts`、`useStudio.ts`、`activity.ts` 接收已有项目通知，显示保存在项目活动中的记录。
- `apps/workbench/src/freeCanvas.test.ts`、`freeCanvasWorkbench.test.tsx`、`durableEvents.test.ts` 覆盖空间索引、组件和项目通知。

## 接口与数据影响

- 写入仅使用项目的 `PUT /api/v1/projects/{project}/canvas`，携带预期修订和操作键。节点引用已有图片 ID 与元数据修订，或已有 Run ID。应用在传输前限制 UTF-8 整个请求为 2 MiB。画布操作不会发起生成、审核、报价或账务操作。
- 当前画布、固定修订、修订摘要列表、来源状态及原保存回执均为 GET。保存回执未确认时，只允许读取同一账户与项目下的原键。会话存储中仅保留待查键，每个浏览器页最多有 16 个待核对的作用域，不保存画布正文。切换项目会中止在途读写，迟到响应不能写回新项目。
- 候选图片需为本项目已审核可读的 PNG 或 JPEG，含缩略图，原件不超过 8 MiB。画布只展示服务端固定到项目、修订、节点的已授权缩略图。图片权限变更先撤下旧缩略图；Run 进展仅重读该修订的轻量来源，不重传整图。Run 只展示状态。切走画布卸载视口及图片，保留当前项目本页的未保存编辑。
- 收到新的画布修订后读取摘要。用户可以保留本页、回读指定版本，或合并本页和服务端修改。双方同时改动同一条记录时，界面显示冲突项并让用户选择保留哪一方；实际保存仍须再次操作，并以核过的最新修订执行 CAS。

## 验证结果

在独立工作台目录运行 `node node_modules/vitest/vitest.mjs run --maxWorkers=1 src/freeCanvas.test.ts src/freeCanvasWorkbench.test.tsx src/durableEvents.test.ts src/creationRender.test.tsx`，四个文件共 44 项通过，Vitest 用时 5.25 秒。相同源码的 TypeScript 严格检查通过。`node node_modules/vite/bin/vite.js build` 用时 3.37 秒，转换 167 个模块。画布按需 JS 为 31.29 kB（gzip 11.91 kB），CSS 为 5.06 kB（gzip 1.68 kB）。主入口 JS 为 509.65 kB，Vite 报告超过既有 500 kB 的体积提示。

测试构建了 500 节点 / 1,000 边和 2,000 节点 / 4,000 边两张索引，核对实际线段穿过视口、拖动端点、折叠组、远景选中态及 150 / 100 张详细卡上限。此外覆核了键盘移动、历史回读、并发修订保留本页、原键读回、超大 UTF-8 请求先行拒绝、图片换权、隐藏及项目切换后的迟到请求。这些测试中的几何和 DOM 数值不提供实机 FPS 结论；真实 PostgreSQL、浏览器拖动和 60 秒帧耗时另由 195、197 的联合验收登记。

## 风险与限制

单项目每版最多 2,000 个节点、4,000 条连线、128 个组、16 个图片绑定及 16 个 Run 绑定。折叠组通过代理卡呈现；远景和超出详细上限的节点绘成标记，可通过定位和检查器读取。打开时仍需读取该版完整文档，显式保存也提交完整文档。标题、节点、连线和分组分别参与合并；同一节点的不同字段由作者作为一处记录冲突处理。未保存草稿仅保留在当前页面内存。浏览器关闭后编辑内容可能丢失。

## 回滚方式

应用层移除自由画布懒加载、入口与项目通知接线，保留已存 037 修订、原回执及来源绑定。数据库及私有文件回退遵循 195 的格式栅栏及联备规则。

## 文档同步

由集成人在同批验收后同步根 README、Wiki、变更索引及 V17 任务源。性能、实际存储和浏览器验收分别引用 195 与 197 证据。

## 037 画布实际平移验收

在本机 Intel 集显、Chromium 157、1920×1080 下，两档各实际拖动平移 60 秒，期间私有缩略图已解码、已受理 Run 持续活动。500 节点/1,000 边的 3,599 个帧间隔 P95 为 16.8 毫秒，最多 25 个详细节点；2,000 节点/4,000 边的 3,601 个间隔 P95 为 16.8 毫秒，最多 100 个详细节点。两个项目实际保存并重开 4 个修订，编辑与平移新增生成 POST 为零。 两组最大间隔分别为 67.2 和 33.3 毫秒。137.271 秒专项通过。报告使用前台浏览器 rAF 间隔，系统可用内存约 31.5 GiB；当次网络按 20 Mbps / 80 ms 配置，实际往返与吞吐校准单列。桌面与 390 CSS 像素截图已人工核对。 [全部帧间隔与逐秒样本](../wiki/assets/performance-canvas-037.json)。

## 037 交付前复核修正

在同一项目有持续 Run 通知时，读取固定修订来源的 GET 可能比相邻通知间隔更慢。原实现每次通知都会取消正在等待的 GET。另一个边界出现在会话存储配额已满时，读取旧表仍能成功，新增待核对键和核对成功后删除原键均可能写入失败。项目权限丢失时，原实现清空了画布正文，但离开项目仍提示保存已清除的编辑。

本次只改 `apps/workbench/src/useFreeCanvas.ts` 和 `apps/workbench/src/freeCanvasWorkbench.test.tsx`。同一项目与修订只保留一个在途来源 GET；期间接到的通知合并为一次后续读取。图片权限发生变化后立即撤下图片，此前发出的整稿或来源读取只能补入 Run 状态，必须等权限变化后的读取返回才能再次显示图片。切换项目、修订、隐藏视口和失权仍会取消旧读取。权限明确丢失时清除编辑提醒，已有待查键继续保留。会话存储写满后，本页从最多 16 个作用域的内存副本读写待查键，存储恢复时同步整份副本。关闭页签会丢失尚未写回的键。

在原 037 工作台上先加入第一组回归，单文件 13 项中 3 项失败。实际观察到慢查询被重复取消、失权后仍报告未保存编辑、存储满后又允许第 17 个作用域发送保存。终版增加固定修订重读、权限变化时的迟到整稿、删除失败后不复活旧键等断言。运行 `node node_modules/vitest/vitest.mjs run --maxWorkers=1 src/freeCanvasWorkbench.test.tsx src/freeCanvas.test.ts src/durableEvents.test.ts src/creationRender.test.tsx`，四个文件共 50 项通过，Vitest 用时 4.34 秒。沿用工作台 tsconfig 对修改的 Hook、测试及传递引用执行 TypeScript 严格检查，通过。本次修正没有单独启动 PostgreSQL、Chromium 或帧率压测。

## 主树工作台和隔离冒烟

集成修正后，037 主工作树执行 npm run check，通过 Go vet、普通 Go 测试、工作台 52 文件共 590 项、169 模块生产构建与文档检查；随后分别执行 smoke、smoke:runtime、smoke:catalog、smoke:references、smoke:library、smoke:conversations，六项全部通过。
