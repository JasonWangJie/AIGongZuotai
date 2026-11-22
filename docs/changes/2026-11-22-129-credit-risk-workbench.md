# 129 · V14-C3 工作台原报价风控复核

## 目的

用户在生成被工作区自动风控拒绝后，可核对原报价、拟冻结额度、限制原因和期限，向独立风控复核员说明情况。只有本人明确确认后，工作台才会再次发送原生成请求。

## 范围

2026-11-22，关联 V14、H05、H06、H08。覆盖单轮对话、会话新轮、媒体任务及收费工作流子节点的受限报价，复用 React 工作台、当前用户与项目作用域，以及运营工作区授权。

## 实际文件

- 受理错误捕获、按本人工作区/项目绑定的原请求、导航与错误反馈见 [riskOriginal.ts](../../apps/workbench/src/riskOriginal.ts)、[api.ts](../../apps/workbench/src/api.ts)、[App.tsx](../../apps/workbench/src/App.tsx)、[requestFeedback.ts](../../apps/workbench/src/requestFeedback.ts) 和 [types.ts](../../apps/workbench/src/types.ts)。
- 申请、证据、原回执、独立预演与原报价提交见 [RiskReviewPage.tsx](../../apps/workbench/src/RiskReviewPage.tsx) 和 [riskReview.ts](../../apps/workbench/src/riskReview.ts)。[响应式样式](../../apps/workbench/src/content-appeals.css) 在桌面并列显示列表与报价，在窄屏纵向显示。运营入口读取实际授权，见 [OperationsPage.tsx](../../apps/workbench/src/OperationsPage.tsx) 和 [operations.ts](../../apps/workbench/src/operations.ts)。
- 组件/协议回归见 [riskReview.test.tsx](../../apps/workbench/src/riskReview.test.tsx)。桌面/390 像素真实浏览器脚本见 [risk-review-browser-smoke.mjs](../../apps/workbench/scripts/risk-review-browser-smoke.mjs)。

## 接口与数据影响

- 收到 `CREDIT_RISK_HELD_LIMIT` 或 `CREDIT_RISK_RATE_LIMIT` 的真实 429 后显示入口。应用获取本人原报价的资格，展示工作区、项目、拟冻结 CREDIT、策略版本、实际命中规则及报价到期时间。历史按项目分页，详情展示本人说明和公开答复。
- 风控复核员使用 `risk_reviewer` 及 `risk_review` 工作区授权。审议限于查询申请、选择批准或拒绝、登记安全证据编号及公开回复，再预演、核对并输入当前密码。回执不明时只读本人原申请键或运营原计划收据。创作提示词、生成参数与运营密码不写入恢复存储或截图诊断。
- 准入许可只针对原报价和已审范围。工作台核对 `AVAILABLE / EXPIRED / INACTIVE / CONSUMED`、原项目与有效期，仅按原路由、原幂等键、原报价和原正文发起一次显式提交。再次受理不明时保留只读核对状态。价格、冻结账务和正式受理仍由原业务事务决定。

## 验证结果

- `npm --prefix apps/workbench run test -- --run src/riskReview.test.tsx` 通过 21 项，2.45 秒。包含两种实际拒绝码、其他 429/503、换身份迟到结果、项目错配、四个原请求入口、改键/改正文、原许可已消费/失效/过期、未知写入、原收据恢复及批准/拒绝的密码和作用域检查。
- `npm --prefix apps/workbench run test` 通过 37 个文件、403 项，10.23 秒。`npm --prefix apps/workbench run build` 通过 TypeScript 与 Vite，122 个模块，Vite 2.40 秒，主包 495.03 kB（gzip 157.46 kB）。`node --check apps/workbench/scripts/risk-review-browser-smoke.mjs` 通过。独立环回 HTTP 视觉夹具已运行同一 Chromium 脚本，审阅 1280×900 桌面与 390×844 手机，修正了桌面详情宽度和拒绝后重复显示连接错误的问题。
- 集成人在 028 固定副本串行执行 `TestPostgresCreditRiskBrowserAndOriginalReservation`，真实 PostgreSQL、两账户 Chromium 和环回图像服务通过，15.32 秒。工作台从图像工作室领取报价，命中工作区限额后申请；两人分别模拟申请与运营执行已提交但响应丢失，均从原收据核对。审批后只有本人确认才再次提交，原键与完整请求字节相同。Go 验证一次环回生成、一次原冻结、一个审核收据、一个交付、200000 微 CREDIT 的实际结算及联备恢复。已逐张审阅桌面详情、输入密码前的运营预演和 390 像素手机原报价确认三张截图。

## 风险与限制

带正文的原请求仅保留在当前页面内存，最多 48 条。切换身份、离开页面或关闭浏览器可能丢失原正文。已有申请和正式收据仍可按权限读取，丢失原请求或报价到期后须回创作入口手动重新报价。批准不充值、不退款、不自动受理，实际供应商、支付或外部风控效果未在本机测试。

## 回滚方式

先只读核对已提交的原业务、风控申请和运营计划收据，保留实际冻结、消费及授权事实。前端随兼容服务版本回退，028 持久格式的联备与回滚见同批存储/集成台账。

## 文档同步

集成人同步[根 README](../../README.md)、[Wiki](../wiki/README.md)、[台账索引](README.md)、[任务源](../planning/tasks.json)、生成的 TODO 和[推进记录](../planning/progress.md)。
