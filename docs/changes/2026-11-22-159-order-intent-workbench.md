# 159 · 本人订单意向工作台

## 目的

让账户用户在登记前分别核对商品标价与拟购的创作权益，并能查回原订单或取消自己的待处理意向。支付渠道尚未接入，页面明确说明此处登记和取消均无资金或 CREDIT 变动。

## 范围

V13-A 的 H08 工作台入口依赖 157 的持久状态与原操作收据，以及 158 的服务端受信目录与账户作用域。工作台分别显示待处理、已取消、原操作回执待核对。完整 V13 的商户支付、回调、退款和实际履约继续单列。

## 实际文件

- `apps/workbench/src/App.tsx`、`apps/workbench/src/types.ts` 接入侧栏「本人订单」与独立按需加载页面。
- `apps/workbench/src/OrderIntentsPage.tsx`、`apps/workbench/src/orderIntents.ts`、`apps/workbench/src/order-intents.css` 实现目录、显式确认、本人分页、详情、原键收据与窄屏布局。
- `apps/workbench/src/orderIntents.test.tsx` 验证整数精度、目录与回执校验、账户隔离、开关、确认与响应未知。
- `apps/workbench/scripts/order-intent-browser-smoke.mjs` 通过独立 Chromium 会话验证两账户的登记、取消、回读、隔离与桌面及手机画面。

## 接口与数据影响

页面从 `GET /api/v1/order-intents/catalog` 读取商品、版本与能力。货币标价依商品的币种与最小单位转换，CREDIT 独立按六位小数显示，均直接处理整数字符串。确认创建只发送商品 ID、商品与目录摘要和独立的稳定操作键；取消按原订单 ID、预期版本和独立操作键提交。目录变价后须重新核对。目录停用时，具备账户管理能力的用户可继续读取和取消原意向。

创建与取消各自保留上次成功操作编号。本标签页按用户、工作区隔离，仅保存处理回执所需的键、商品摘要或订单身份与版本。响应未知时阻止新写入，用 `GET /receipt?operation=create|cancel` 及原键请求头核对。对已取消订单查看当时的创建回执，会同时读出原收据的待处理状态与订单当前的已取消状态。请求均使用当前账户会话，无项目覆盖参数。长期订单与收据来源为服务端，不依赖浏览器缓存。

## 验证结果

集成人对冻结的 033 运行 15 项订单与个人资料专项，共 62.099 秒。两账户 Chromium 7.58 秒，关闭目录后的新库恢复 7.53 秒；真实数据库中未产生权益分录或媒体任务。

| 命令或检查 | 结果 | 实际范围 |
| --- | --- | --- |
| `node node_modules/typescript/bin/tsc --noEmit -p tsconfig.json` | 通过 | 033 准备镜像内工作台类型检查。 |
| `node node_modules/vitest/vitest.mjs run --no-cache --configLoader runner` | 42 个文件、488 项通过，18.92 秒 | 含货币零、二、三位与六位 CREDIT、拒绝错误快照、目录关闭、原创建收据与当前状态独立、限定本人和工作区等。 |
| `node node_modules/vite/bin/vite.js build --configLoader runner` | 149 个模块，12.00 秒 | 订单页面独立 JS 11.60 kB，gzip 4.24 kB。 |
| `node apps/workbench/scripts/order-intent-browser-smoke.mjs` | 本机 Chromium 与隔离 HTTP 示例通过，启动脚本用时 4.59 秒 | 两账户、实际浏览器点击，创建和取消均各送出一次 POST。对两笔已成功写入各注入一次响应丢失，对应原键收据和当前状态均可只读恢复。第二账户列表无对方订单，直接读对方详情为 404。目检 1280×900、390×844 两图通过。 |

真实 PostgreSQL 的注册账户、事务、权限及联备由集成人在 033 独立环境另验。浏览器脚本从 `ORDER_INTENT_BROWSER_URL` 取得环回服务，从 `ORDER_INTENT_BROWSER_OUTPUT` 取得截图目录，并使用两组独立的 `ORDER_INTENT_{BUYER,OTHER}_{EMAIL,PASSWORD}`，可指定 `ORDER_INTENT_PRODUCT_ID`。脚本仅输出检查状态。

## 风险与限制

关闭标签页会丢失本页保存的原操作键；重新登录后仍可从本人记录读取服务端当前状态。商品目录取服务端启动配置。实际支付、商户对账、退款以及发放权益须在后续批次验证。

## 回滚方式

撤回本条工作台入口及其独立页面可以恢复此前界面，保留数据库内的订单与收据。关闭服务端目录可以停止新意向，原意向继续按 158 的账户接口管理。数据格式降级遵循 033 升级前的配套联备。

## 文档同步

集成人汇总 V13-A 的 README、Wiki、任务源、生成 TODO、进展与台账索引。157、158 分别记录持久层和 HTTP/目录，159 记录用户操作与前端验证。

