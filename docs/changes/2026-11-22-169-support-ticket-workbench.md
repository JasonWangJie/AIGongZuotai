# 169 · 站内客服工单工作台

## 目的

用户可在自己的工作区建单、追问与关闭，独立客服可在获授的工作区阅读工单和回复。双方都能在提交响应丢失后只读核对原收据。

## 范围

V22-B 的 H08 工作台使用持久账户和 167、168 的客服数据及 HTTP 接口。普通用户从「我的客服工单」进入。客服需在同一条授权上同时具备 `support` 角色及 `support_ticket` 权限，侧栏才显示「客服收件箱」。客服入口独立于项目和运营异常处置台。邮件提醒、实时聊天、资金处置和公开值守服务继续单列。

## 实际文件

- `apps/workbench/src/App.tsx`、`apps/workbench/src/types.ts` 和 `apps/workbench/src/operations.ts` 接入两个入口，并按独立客服授权选择工作区。
- `apps/workbench/src/SupportTicketPage.tsx`、`apps/workbench/src/supportTickets.ts`、`apps/workbench/src/support-tickets.css` 实现工单元数据列表、分页、私有对话、当前版本、原键核对以及窄屏布局。
- `apps/workbench/src/supportTickets.test.tsx` 验证权限、输入边界、收据与详情校验、错误/迟到状态、冲突与撤权后的界面。
- `apps/workbench/scripts/support-ticket-browser-smoke.mjs` 使用三个独立浏览器会话验证用户、客服、外人和桌面、手机界面。

## 接口与数据影响

本人接口为 `/api/v1/support/tickets`；客服接口为 `/api/v1/operations/support/tickets`，显式带授权工作区。两者均使用登录会话，不传项目覆盖参数。列表只使用主题、分类、状态、版本和时间等字段，拒绝混入消息正文的列表。详情按版本展示创建、追问、答复和关闭；校验工作区、工单身份、消息顺序与终态。主题上限 120 字及 480 字节，正文上限 2000 字及 6000 字节。

写入前在本标签页记录用户、工作区和用户/客服身份共同隔离的原键、操作、资源 ID 与预期版本。正文、主题和当前密码不进入恢复仓。客服每次明确提交时读取并清空当前密码。请求不明时只用原键 GET 收据，再单独 GET 当前工单。收据显示该操作发生时的版本。CAS 冲突保留本页输入，读取新消息后由操作者显式确认采用当前版本并重新输入密码。客服草稿尚在编辑时锁定切换授权工作区与重新加载授权。只读刷新发现权限失效，会清除当前页面的私有对话和草稿。关闭工单需要第二次显式确认；关闭后仍可查阅全部对话。

## 验证结果

034 工单专项十项通过，共 48.800 秒。三账户真实 Chromium 10.69 秒，停写撤权后在新库恢复 10.57 秒，四种操作丢失提交确认后的原键回读共 11.57 秒。浏览器核对四个版本及原收据，数据库确认追问和答复各自的操作者，权益与媒体均无新增。

| 命令或检查 | 结果 | 实际范围 |
| --- | --- | --- |
| `node node_modules/typescript/bin/tsc --noEmit -p tsconfig.json` | 通过 | 034 工作台类型检查。 |
| `node node_modules/vitest/vitest.mjs run --no-cache --configLoader runner` | 44 个文件，518 项通过，19.03 秒 | 含 14 项工单专项，验证授权与个人作用域、UTF-8 和字符边界、工单列表字段、消息版本、CAS、当前密码、关闭及原操作核对。 |
| `node node_modules/vite/bin/vite.js build --configLoader runner` | 155 个模块，11.73 秒 | 工单页面独立 JS 14.00 kB，gzip 5.45 kB。 |
| `node apps/workbench/scripts/support-ticket-browser-smoke.mjs` | 真实 Chromium 配本机隔离 HTTP 示例通过，7.69 秒 | 三账户，两处界面。成功创建、回复与关闭后分别丢弃一次回包，客户端均从原收据核对。用户追问后，旧版客服回复返回 409，重输错误密码返回 403，确认最新修订和当前密码后回复返回 200。外人的本人详情、原收据及客服详情均返回 404。 |

撤权交错回归先在修正前观察到详情重新出现。修正后，确认无权读取会作废之前发起的队列、详情与写操作响应。较早授权读出的正文不能回填；已发送回复仅保留原操作编号，须在重新取得权限后查询。单独执行 `node node_modules/vitest/vitest.mjs run --no-cache --configLoader runner src/supportTickets.test.tsx`，14 项通过，2.72 秒。另一次用真实 Chromium 和受控环回 HTTP 缓住已获 200 的完整详情，待后续队列请求实际返回 404 并清屏后才交付旧响应。浏览器处理完该响应后正文及工单条目均保持清空，这次独立检查用时 5.77 秒。

已目检桌面 1280×900 的客服队列与私有对话，以及 390×844 的本人关闭记录。浏览器脚本使用 `SUPPORT_BROWSER_URL` 环回地址、`SUPPORT_BROWSER_OUTPUT` 截图目录、`SUPPORT_WORKSPACE_ID` 和三组独立的 `SUPPORT_{OWNER,AGENT,OTHER}_{EMAIL,PASSWORD}`。独立 PostgreSQL 三账户浏览器、SQL 与联备验收由集成人串行执行。

## 风险与限制

待核对的操作编号仅在当前标签页按身份保留。关闭标签页后可重新登录查看当前服务端记录。页面内未提交文字不会跨关闭保存。客服队列以实际受理与回复为证据，值守时间、服务队列响应时限、外部通知和公开生产环境需另验。

## 回滚方式

撤回两个入口、工作台组件及测试可恢复此前界面。已登记工单、消息和原操作收据继续保存在服务端。持久层格式回退须使用配套升级前联备，详见本批服务端及集成台账。

## 文档同步

集成人维护 V22-B 顶层 README、Wiki、接口文档、任务源、生成 TODO、进展与台账索引。本条记录工作台与 Chromium 验收。

