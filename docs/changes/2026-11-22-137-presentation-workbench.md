# 137 · V21-C 项目演示文稿编辑、放映与导出

## 目的

用户可将一段材料整理成有顺序的投影片，逐页编辑文本、演讲备注和私有配图，保存固定版本，再交付可在 PowerPoint 中继续编辑的文件。桌面和窄屏都保留文稿、页面与版本的明确选择。

## 范围

2026-11-22，关联 V21-C、H08 前端体验、H10 工作流与专项创作。工作台导航增加“演示文稿”。PostgreSQL 与账户登录可用时开放当前项目的文稿及素材；内存演示和免登录模式显示所需条件。

## 实际文件

- [PresentationWorkbench.tsx](../../apps/workbench/src/PresentationWorkbench.tsx)、[presentation.css](../../apps/workbench/src/presentation.css)、[App.tsx](../../apps/workbench/src/App.tsx)、[types.ts](../../apps/workbench/src/types.ts) 提供项目入口、材料拆页、三种主题与版式、增删排序、选图、保存、版本切换、放映和打印。项目内导航保留当前编辑，切换项目或关闭页面时提示尚未保存或待核对的内容。
- [presentations.ts](../../apps/workbench/src/presentations.ts) 处理文稿结构、输入限制、原保存键、分页、版本请求和返回作用域。[presentationExport.ts](../../apps/workbench/src/presentationExport.ts) 验证固定版本清单、授权图片字节、大小与摘要，创建 PPTX 或离线 HTML，提供相同版式的放映和打印内容。
- [前端依赖](../../apps/workbench/package.json) 和[锁文件](../../apps/workbench/package-lock.json) 精确固定 `pptxgenjs@4.0.1`；只有下载 PowerPoint 才加载此库。[组件和协议回归](../../apps/workbench/src/presentations.test.tsx)、[Chromium 验收脚本](../../apps/workbench/scripts/presentation-browser-smoke.mjs) 和[本机浏览器工具](../../apps/workbench/scripts/local-browser.mjs) 验证正式 UI 流程及实际下载。

## 接口与数据影响

- 材料按空行、Markdown 标题在本机拆分。每份 1–30 页，封面、正文、图文均支持标题、正文与演讲者备注。主题提供暖白、午夜蓝、薄荷绿。新文稿完整输入、配图修订与页面顺序在用户点击保存后才写入项目服务。
- 保存使用期望版本与幂等键。刷新目录保留草稿，读旧版/新建前可保留当前编辑。版本冲突时可将当前编辑另存新稿。保存响应丢失只读原收据；会话恢复存储只记录作用域、原键、文稿 ID 与期望版本。用户输入、图像字节与备注均不存入浏览器恢复记录。
- 所有交付先读取指定项目的固定版本和导出清单，再按同源固定版本/稳定页号路径读取每张可用私图。禁止重定向，核验 PNG/JPEG 头、真实解码尺寸和 SHA-256；每页上限 8 MiB、1600 万像素，最多 4 个图页及全稿 32 MiB。任一图片不可交付就停止整稿导出。密集页可以保存，提交交付前统一检查可读字号、文本预计行数和实际浏览器溢出，提示拆页或移入备注。
- 原生宽屏 PPTX 使用文本框、嵌入原图和讲者备注。静态离线 HTML 内嵌图像，使用转义文本和禁止脚本/外连的内容安全策略。放映支持键盘翻页，同版打印 / PDF 使用无边距 16:9 整页；正文、顺序与配图来自固定版本。工作台导出不发起模型请求。

## 验证结果

- `npm --prefix apps/workbench run test -- --run src/presentations.test.tsx` 通过 23 项，5.91 秒。涵盖材料保留、只读字段剔除、文本/页/图片上限、两种过密资料、静态 HTML 转义、跨项目/跨版本/外部图地址拒绝、下载前限制、版本冲突、保存回执不明与只读核对。
- `npm --prefix apps/workbench run test` 通过 38 个文件、426 项，13.70 秒。`npm --prefix apps/workbench run build` 通过 TypeScript 与 Vite，133 个模块，Vite 3.28 秒；主包 495.85 kB（gzip 157.66 kB），独立 PPTX 下载块 372.60 kB（gzip 126.52 kB）。
- 独立环回 HTTP 视觉夹具运行完整 Chromium 脚本，两个页面会话、桌面和 390 像素布局通过，产生两次修订与旧版回读的 3 份 PPTX、静态 HTML、PDF、截图和基于原始 UI 输入的期望清单。首测发现浏览器打印默认 Letter 切掉右侧内容，随后对实际打印树修正；`pdfinfo` 验出 3 页、960 × 540 pt，Poppler 实际渲染第二页的标题、正文、私图和页码完整。集成人在真实 PostgreSQL 上执行文稿专项与相关子例，46.647 秒；其中双会话 Chromium 下载、原项目图片与固定版本验收 10.28 秒。已审阅桌面编辑、放映和 390 像素界面。独立 python-pptx 对照已知正文、备注与原图 SHA-256，改写标题后回存；LibreOffice Impress 实际打开和渲染 4 份文件，连同浏览器打印计 5 份 PDF，详见 138 联合验收。

## 风险与限制

原材料、未保存编辑只保留在当前页面。浏览器关闭后可读取已保存版本和原保存收据。输入按段落拆页的规则不做外部模型推理。导出后的本地文件可能含有用户原已获准读取的图片，后续撤权约束新的在线读取。实际第三方云端 Office 兼容性、多人协同和生产渠道不在本页验证范围。

## 回滚方式

先核对待确认保存及需要保留的各文稿版本，按 029 文稿服务的配套方式回滚前端入口与依赖。数据库和私图采用同批联备，保留已下载原文件。

## 文档同步

集成人同步[根 README](../../README.md)、[Wiki](../wiki/README.md)、[台账索引](README.md)、[任务源](../planning/tasks.json)、生成 TODO 及[推进记录](../planning/progress.md)。029 持久化、项目授权与集成文件验收在同批独立台账记录。
