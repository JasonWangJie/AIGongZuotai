# 2026-11-14-061：工作区生成权限与复核前端

## 目的

让用户看清工作区新生成限制、提交复核并读取公开答复；让独立 moderator 在明确范围内预演和确认限制、维持或解除。限制不能破坏原请求核对，也不能被误解为放行旧内容审核结果。

## 范围

V14-C1 / H01、H03、H05、H06、H08、H09 的前端纵向链路，沿用暗色工业工作台风格。后端领域、HTTP、017 迁移与权限由并行台账记录；根 README、导航、版本和任务源由集成人维护。本记录只声明实际完成的前端验证，不代替真实 PG 与浏览器验收。

## 实际文件

- `apps/workbench/src/GenerationAccessPage.tsx`、`GenerationStatus.tsx`、`OperationsGenerationPanel.tsx`：用户状态/申请/答复、运营预演/密码确认/收据。
- `apps/workbench/src/generationControls.ts`、`useGenerationAccess.ts`、`generation-controls.css`：类型、闭集校验、独立网络 attempt、内存恢复、可见轮询与视觉样式。
- `apps/workbench/src/LazyPage.tsx`：页面按需加载、失败后手动重载模块，恢复仓继续由 App 持有。
- `apps/workbench/src/App.tsx`、`types.ts`、`api.ts`：普通权限入口、独立 scope API、工作区状态 hook、原请求与新请求分开、版本角标。
- `apps/workbench/src/OperationsPage.tsx`、`operations.ts`：新增 moderator 显式权限和独立业务区域，不扩展旧财务角色权限。
- `apps/workbench/src/ChatWorkspace.tsx`、`useConversations.ts`、`requestFeedback.ts`：多轮新生成/报价门禁、403 确定未受理说明、原未知请求优先保留。
- `apps/workbench/src/generationControls.test.tsx`、`creationRender.test.tsx`：新增 21 项回归；后者保留 B2 空引用编辑回归。
- `apps/workbench/package.json`、`apps/workbench/package-lock.json`：锁定开发依赖 jsdom 29.1.1；版本号继续由根维护，不增加生产依赖。
- `docs/wiki/generation-controls-workbench.md`、本记录：实现、验证及限制说明。

## 接口与数据影响

按 [C1 契约](../planning/v14c1-contract.md)使用用户生成权限/申请路由及运营生成权限/申请/预演/执行/收据路由。用户 scope 来自会话，运营明确传授权工作区；均不借用 `X-Project-ID`。普通申请采用原键幂等，运营敏感执行不自动重发。新生成拒绝码 `403 GENERATION_RESTRICTED` 为确定未受理；此前未知仍保留原正文、报价和键。

浏览器不新增持久化表或 localStorage 业务正文。内存仓按用户/工作区隔离，申请原键与正文跨页面保留，但每次网络 POST 新建 attempt，旧响应不能清后来的请求；密码、执行正文、预演摘要不存恢复仓。列表有界；用户申请 detail 才取说明/公开答复。OPEN/RESTRICTED 的释放不会修改旧任务或内容审核摘要。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `npm --prefix apps/workbench run typecheck` | 通过 | 当前前端类型检查通过 |
| `npm --prefix apps/workbench run test` | 通过 | 28 个文件、297 项；最后一轮 5.01 秒，包含新增 27 项 |
| `npm --prefix apps/workbench run build` | 通过 | 99 模块，Vite 构建 2.74 秒；首页 JS 494.93 kB/gzip 156.88 kB，用户页 10.80 kB、运营页 42.06 kB；不再有 500 kB 分包警告 |
| 独立审阅后修正 | 已覆盖 | 首次 restrict 预演返回新 `restriction_id`；同 episode 另一标签不同 body 不清原未知；同键网络重试用 fresh attempt 阻止旧成功/401 清新 pending |
| 实际 PG / 桌面与手机浏览器链 | 集成人执行 | 本代理未运行 PG 或 CUA；结果见 [062](2026-11-14-062-generation-controls-integration.md)，不以 SSR 和 fixture 替代 |

最初 288 项通过并未覆盖首次限制的返回 ID，独立审阅发现后新增对应回归；其后又补不同申请正文与同键网络尝试交错。该轮 291 项包含这些修复，保留此过程以明确旧测试证据的边界。

浏览器后续发现 StrictMode 初始读取可能被上个 effect 的共享锁挡住。新增真实 ReactDOM/StrictMode 挂载测试 6 项，先复现 4 项失败，再改为 AbortController 与独立 read attempt；旧 cleanup/响应/finally 无权解锁或覆盖新读。两项实际挂载的旧 POST 成功/401 与重登原键 retry 交错也通过。最终总计 297 项，新增 6 项不依赖真实网络、PG 或收费渠道。运营总说明和财务区域尾注同步明确新生成复核已在专用区域开放，内容重审/账户封禁仍未开放。

jsdom 29.1.1 的 registry engines 为 `^20.19.0 || ^22.13.0 || >=24.0.0`，已核对本机 Node 22.18.0；31.0.1 需要 22.22.2 起，未采用。安装锁定开发依赖时完整审计 0 漏洞，最终 `npm --prefix apps/workbench audit --omit=dev` 亦为 0。产品依赖未增加，最终生产 chunk 大小已重新实测。官方依据见 [Wiki](../wiki/generation-controls-workbench.md)。

## 风险与限制

站内答复通过可见时有界轮询和用户显式读取，不宣称实时推送。内存恢复只存在于当前 App 生命周期，刷新或关闭会丢失原输入/核对编号；用户必须先完成核对。403/计划过期/撤权均以服务端最终校验为准。

本批只限制新生成：没有账户登录封禁、内容人工放行、已隔离候选查看、自动退款或自动恢复生成，也没有 C2/C3 自动策略。真实审核渠道仍待选型；构建体积变小不能作为生产性能验收。浏览器完整链还须以 062 的集成证据确认。

## 回滚方式

前端可回到前一版本页面，但后端持久限制仍必须在每次新请求受理时执行，不能因旧界面缺少入口而解除限制。017 数据不由前端回滚；须遵守服务端格式栅栏和联备恢复流程。不要删除限制、申请、预演、收据或 owner 文件绕过校验。回退前先处理本页未知请求及未提交说明，避免丢失内存上下文。

## 文档同步

- README、Wiki/台账索引、任务源及 TODO：由根集成人同步 v0.15 与本记录链接，最终验收按 062。
- Wiki：新增 [生成权限与复核界面](../wiki/generation-controls-workbench.md)，说明权限、原请求优先、预演/密码/收据、公开答复、按需加载和未验收边界。
- 契约：引用 [V14-C1](../planning/v14c1-contract.md)，不另外发明前端操作协议。

## 根联合验收补充

2026-11-14，根已完成README/Wiki/台账索引、任务源/TODO同步，C1本地子项通过；297项前端、Go vet/构建、真实PG全量282.020s、六套隔离smoke、017联备数据比对、桌面/390手机限制→申请→解除答复→手动新生成及文档治理均通过。前述待集成事项以[062最终记录](2026-11-14-062-generation-controls-integration.md)和其JSON证据为准；完整V14仍进行中，真实渠道、C2/C3及生产能力未提前放行。
