# 2026-11-14-053：文本审核状态与前端交付隔离

## 目的

V14-B1 / H03、H06、H08、H09：让用户理解文本为何等待、被限制或待核实，并确保客户端不把迟到流事件、缓存或分支摘要重新显示为可用内容。只交付文本本地闭环，不将当前工作台描述为完整多模态审核产品。

## 范围

新增安全审核摘要、统一状态条和正文显示规则，接入旧单轮、多轮、分支及输入拒绝；审核开启时阻止新媒体创建，但保留原未知请求核对。没有修改资产、上传、媒体结果展示或运营处置规则；没有新增依赖、供应商调用、人工放行、重审或申诉入口。根维护包版本、README、索引、任务源及054联合验收。

## 实际文件

- `apps/workbench/src/moderation.ts`：字段、状态、版本合并、可见性、固定错误和媒体能力判断。
- `apps/workbench/src/ModerationStatus.tsx`、`moderation.css`：状态条、规则/HTTP模式说明、响应式与reduced-motion。
- `apps/workbench/src/moderation.test.tsx`：20项审核及未知请求回归。
- `apps/workbench/src/App.tsx`：旧单轮显示、媒体新建门槛、原任务核对保留、STUDIO / 0.13显示。
- `apps/workbench/src/ChatWorkspace.tsx`：可独立验证的消息视图、正文/错误/复制/继续隔离、分支摘要与草稿目标说明。
- `apps/workbench/src/types.ts`、`state.ts`、`conversationState.ts`：正式摘要类型、Run/消息/分支乱序合并与上下文目标限制。
- `apps/workbench/src/useConversations.ts`、`useStudio.ts`：安全投影、SSE交付说明与新媒体提交门槛，原未知操作不重建。
- `apps/workbench/src/api.ts`、`requestFeedback.ts`：安全审核错误、精确前置503例外和原未知保留说明。
- `docs/wiki/moderation-workbench.md`、`docs/wiki/frontend.md`及本台账；前端总览同步v0.13文本范围、普通新提问与整轮省略、媒体边界及根已回报的本地验收证据。

## 接口与数据影响

使用根冻结的Bootstrap moderation能力及Run/MessageNode/Branch安全摘要；不增加HTTP路由。文本只有摘要ALLOWED且业务COMPLETED才显示；nil历史不标为通过。审核version与文本seq分别合并，旧或缺失摘要不解除隔离；误发delta只恢复原快照。客户端无法撤回已经到达浏览器的数据，因此服务端不发未审正文仍是前提。

输入拒绝采用安全固定说明，503仅MODERATION_UNAVAILABLE明确表示本次前置未受理。此前未知的原请求继续保留body/key/模型/报价/分支，不能被这条例外清掉。未知、断流或被隔离结果不触发自动新生成。新媒体入口停用不限制原未知重试或历史核对；上传和旧素材不因此获得审核标签。

Go代理独立复核发现前端曾同时阻止受限终态之后的reply和continue，与服务端允许新问题并略去受限轮次的行为不符。交付前已修正：普通reply保留原消息树父节点，只提交新输入；continue仍拒绝。新增明确回归验证BLOCKED/REVIEW后的reply可用、continue不可用、PENDING仍不能接新轮，以及迟到缓存正文不会进入新POST。界面同步说明服务端略去受限完整轮次。

## 验证结果

| 命令/检查 | 结果 | 实际覆盖与限制 |
| --- | --- | --- |
| `npm --prefix apps/workbench run test -- moderation.test.tsx` | 20项通过 | 最终审核专项，含独立复核后的普通新问题/继续边界修正 |
| `npm --prefix apps/workbench run test` | 237项/24文件通过，2.43秒 | 全前端回归；状态、SSR与HTTP fixture，不是供应商实测 |
| `npm --prefix apps/workbench run typecheck` | 通过 | 正式能力/摘要类型、事件与UI接线 |
| `npm --prefix apps/workbench run build` | 通过，Vite 1.66秒 | JS494.52kB/gzip156.17kB，CSS115.90kB/gzip23.18kB；非生产容量成绩 |
| 源码与契约核对 | 已核对 | 前置明确错误、最终run.snapshot、ALLOWED+COMPLETED、nil兼容及媒体范围 |
| PG/浏览器/真实渠道 | 本代理未执行 | 根054记录Go/PG与桌面手机联合证据；真实文本审核渠道待选型，不以fixture替代 |

初次新增测试的严格构建发现负例类型转换未显式经过unknown，已按刻意构造非法协议值修正；未放宽生产类型或断言。既有单轮/会话状态测试保持通过，未为通过测试而降低终态保护。

## 风险与限制

rules仅演示控制链，HTTP适配未代表真实审核供应商验收。前端不进行敏感分类，不展示命中内容；unknown reason使用通用提示。正文未开放时账务仍按独立服务端冻结状态显示，不推定免费。旧资源nil不追溯，多模态B2、人工重审、申诉、封禁和完整V14仍待后续。

输入及原未知请求沿用页面内存保护；刷新或关闭后不能承诺草稿/原键跨关闭恢复。政策变更需要服务端重启，Bootstrap不是热更新渠道。客户端不能取代服务端历史/SSE/祖先链隔离；服务端若已泄漏正文，事后UI隐藏无法收回。

## 回滚方式

可以关闭新审核准入配置并回退前端展示，但必须保持已受理运行的冻结策略和隔离投影；不能通过关闭开关或删除moderation字段开放旧受限输出。保留015数据库格式栅栏及根联合备份要求，不删除迁移、owner或原幂等记录。无前端外部渠道或静态资源需要销毁。

## 文档同步

- [文本审核工作台Wiki](../wiki/moderation-workbench.md)记录实际前端范围、合并和恢复边界。
- [前端总览](../wiki/frontend.md)更新当前v0.13并追加B1本地验收：全文审后展示、受限终态后正常提问、服务端整轮省略、新媒体禁用及旧nil非批准；根PG与浏览器已验证事实注明来源，桌面／手机最终联合结果由054收口。
- [V14-B1契约](../planning/v14b1-contract.md)由根统一维护，字段与本批实现对应。
- README、Wiki/台账索引、任务源与TODO由根054更新；B1本地、真实渠道、B2和V14-C分开维护，不提前勾选完整V14。
