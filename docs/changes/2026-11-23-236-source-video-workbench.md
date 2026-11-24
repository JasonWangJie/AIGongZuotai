# 236 · 视频工作室选择原视频编辑与延长

## 目的

让登录用户在视频工作室选择原片和修订，预览报价并追踪原请求。

## 范围

V20-C 使用视频工作室的现有模型、描述、报价与提交入口。可用模型目录明确列出原视频编辑或延长的 Data URI 规格后，已登录的持久项目可选择对应操作。前端与 234 供应适配器、235 及格式 039 的受理接口配套。

## 实际文件

- `apps/workbench/src/App.tsx`、`ModelControls.tsx`、`BillingComponents.tsx` 根据操作过滤模型和完整规格。报价显示操作、登记素材身份、修订和显式方向；更换素材或修订会使旧报价失效。
- `apps/workbench/src/SourceVideoPicker.tsx`、`sourceVideo.css`、`sourceVideo.ts` 从当前项目的素材库分页读取已登记的生成 MP4，只允许有效修订、可读状态与任务产物关联。刷新重新选择修订；作用域或操作改变会取消旧读取。`types.ts` 与 `modelCatalog.ts` 复核能力、规格和方向，生成报价和提交共用的请求。
- `apps/workbench/src/useStudio.ts`、`pendingRequests.ts`、`api.ts` 在当前页面内存保留原请求、用户、工作区、项目和操作键。响应不明时，原项目只能使用原键读取任务收据；404 或修订、模型、规格、方向不符均继续保留。收到可匹配的任务后并入现有任务中心。
- `apps/workbench/src/locales/{en,ja,zh-TW}.json` 增加界面及相关平台错误翻译，简体使用源文案；`apps/workbench/src/sourceVideo.test.tsx` 使用真实 React 入口与本地 fetch 样本验证请求序列。

## 接口与数据影响

首次生成沿用 `POST /api/v1/tasks`，操作为 `video_edit` 或 `video_extend`；`inputs.source_video` 仅含项目登记的 `asset_id`、`asset_revision`。延长可以选择模型声明的 `inputs.extension_direction`，编辑不发送方向。启用权益时以同一输入请求报价，明确确认后仅增加 `quote_id`。原收据读取为 `GET /api/v1/tasks/receipt`，携带 `X-Project-ID`、`Idempotency-Key`。本批前端不新增 SQL、浏览器媒体正文或供应商密钥存储。

## 验证结果

- 在 236 隔离工作台执行 `npm run build`，TypeScript 和 Vite 均通过，183 个模块。Vite 对现有主包保留大于 500 kB 的提示。
- 执行 `npm test -- --maxWorkers=2 src/sourceVideo.test.tsx src/modelCatalog.test.ts src/creationRender.test.tsx src/pendingRequests.test.ts src/api.test.ts src/locale.test.ts src/billing.test.ts src/videoTimeline.test.tsx`，8 文件、101 项通过。编辑和延长分别经视频工作室读取素材、报价、核对修订、提交。每个场景仅 1 次生成 POST；连续三次原键 GET 分别返回未找到、修订不符、匹配回执。首次报价后素材修订变化，必须重新选择并重新报价。
- 隔离工作台完整执行 `npm test -- --maxWorkers=2`，56 个文件、607 项全部通过，耗时 51.57 秒。
- 本记录的证据使用 jsdom 和 HTTP 响应样本；真实 Chromium、串行 PostgreSQL、联备和真实供应商分别按集成验收记录核对。

## 风险与限制

239 的 Chromium 验收发现，原片选中状态曾绑定整库事件水位。第一条创作登记新产物时，第二条创作的原片、方向及报价可能一并清空。现由普通工作室按所选资产 ID 走原项目鉴权的详情 GET 复核。相同有效修订保留原选择、方向及报价，复核进行中或暂时失败时禁止新的报价及提交。修订不符、已回收、权限丢失或原件不可读时清除旧选择与报价，用户须重新选取。翻到后续页的已选项也按 ID 单独核验。

普通工作室的受理不明请求仅保存在当前页面内存；刷新或关闭页面会丢失这份本地记录。权限、原件完整性与交付状态最终由服务端复核。

## 回滚方式

本批回滚可整体撤销上述前端与翻译改动；已受理任务、039 绑定及其私有正文仍按服务端要求保存。

## 文档同步

总集成人同步根 README、Wiki、任务源、两个 TODO、进度和台账索引。

## 完整原请求核对

工作室还核对原输入正文和原报价 ID。两个组件场景各执行 1 次生成 POST、5 次同键 GET，依次覆盖查无收据、修订不符、正文不符、报价不符和匹配。集成人运行 `npm --prefix apps/workbench run test -- --maxWorkers=2 src/sourceVideo.test.tsx`，3 项通过。

集成人在主工作区对最新原片复核实现运行上述八份前端测试。101 项通过，耗时 9.86 秒；同一构建随后通过 239 的两个实际浏览器场景。
