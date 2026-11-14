# 011 · 云端草稿与项目活动工作台

日期：2026-11-13。关联批次：V03 项目云端草稿与版本恢复、V04 关键业务审计与活动记录。模块：H01、H02、H06、H08、H09。前端版本：0.3.0。

## 目的

将已完成账户／项目隔离的工作台推进到可跨会话读取的项目文字草稿与可追踪的操作记录。解决本地草稿不能跨设备保存、并行窗口覆盖、保存受理不明，以及用户无法查看关键项目操作的问题；本批不实现模型工作流执行。

## 范围

PostgreSQL 模式增加独立 CloudDraftCanvas：空稿、手动保存、节点编辑、JSON 导出、旧本地草稿显式导入、版本列表及预览、恢复为新版本、CAS 冲突处理、原幂等请求重试。memory 模式继续原本地草稿，并在导航中区分 CLOUD／LOCAL。

增加当前项目只读活动记录，呈现白名单动作与有限元数据，按游标加载更早活动。增加未保存离开确认、创建项目／退出等待期编辑冻结、自动会话过期后的本页内存恢复；恢复仓按用户及项目隔离，不写入 localStorage/sessionStorage，不存密码或 token。

## 实际文件

- `apps/workbench/src/CloudDraftCanvas.tsx`、`src/draftState.ts`、`src/draftState.test.ts`：云端编辑、状态机、版本与冲突处理、同步状态测试。
- `apps/workbench/src/draftRecovery.ts`、`src/draftRecovery.test.ts`：App 生命周期中的未保存草稿与原 pending 请求恢复、身份／项目隔离、显式丢弃和迟到响应保护。
- `apps/workbench/src/ActivityPage.tsx`、`src/activity.ts`、`src/activity.test.ts`：只读活动页、白名单展示、游标合并与隔离。
- `apps/workbench/src/api.ts`、`src/api.test.ts`、`src/types.ts`：草稿／审计契约、PUT 幂等请求、错误 code 及受理不明分类。
- `apps/workbench/src/App.tsx`、`src/useStudio.ts`、`src/main.tsx`、`src/drafts.css`：页面接入、项目／退出保护、会话过期回调、界面样式及版本标记。
- `apps/workbench/scripts/integration-drafts.mjs`：真实本地 PostgreSQL HTTP 联调脚本。
- `apps/workbench/package.json`、`package-lock.json`：版本更新 0.3.0，新增 `test:drafts` 命令，无新增第三方依赖。
- `docs/wiki/frontend.md`、本台账：当前能力、请求恢复、使用限制与验证证据。

根 README、Wiki／台账索引、任务源和生成 TODO 由集成负责人维护，本次前端代理不并发覆盖这些文件。

## 接口与数据影响

新增调用 `GET/PUT /api/v1/projects/{id}/draft`、`GET /draft/revisions`、`GET /draft/revisions/{revision}`。PUT 携带 `expected_revision`、限定字段 nodes 与 `Idempotency-Key`，成功后更新当前版本。网络或服务错误保留原键／原 body，不自动创建新操作；409 冲突保留编辑、另读服务器版本，由用户明确选取后再次保存。恢复旧版仅将内容载入编辑区，以当前 revision 和新键另存；不会改写历史。

服务端允许最多 40 节点、标题 80 个 Unicode 字符、正文 5,000 个 Unicode 字符；前端输入框使用保守的 UTF-16 单元限制，另以纯函数校验安全且唯一的 ID、字符数及 NUL。服务端最多 1,000 个版本；达到上限后的错误如实呈现，不自动删历史。

活动查询使用 `/api/v1/audit?scope=project&limit=50&cursor=...` 与独立项目请求头。界面不输出任意 action/details，只呈现允许的动作、状态、类型、版本和节点数。记录不作为消费账单，也不含提示词、生成正文、草稿正文、邮箱或密钥。

保存与读取回包受项目身份、操作键和生命周期代次保护。自动 401 卸载前，编辑动作已同步写入 App 内存恢复仓；同一账户／项目重新打开草稿后恢复，pending 保持原请求。确认放弃后旧响应不能复活已丢弃内容；若离开请求失败后发生新的显式编辑，则重新启用恢复保护。页面刷新、关闭或进程结束会清除此内存仓，浏览器提示不能替代离线备份。

## 验证结果

- `npm --prefix apps/workbench run typecheck`：通过。
- `npm --prefix apps/workbench test`：57 项测试、10 个文件通过。包含原功能回归、同步标签、PUT 受理不明、精确键／body 重试、CAS 保留本地、恢复另存、跨项目／旧操作响应、节点边界、活动白名单和分页去重、会话过期恢复、显式丢弃后迟到响应、离开失败后新编辑恢复。
- `npm --prefix apps/workbench run build`：通过。Vite 47 模块；JS 306.27kB（gzip 96.41kB）、CSS 51.62kB（gzip 11.45kB）。这是构建体积，不是 LCP／INP／容量验收。
- 设置 `STUDIO_TEST_URL=http://127.0.0.1:8080` 后执行 `npm --prefix apps/workbench run test:drafts`：两次对实际 Go + PostgreSQL、AUTH_MODE=required、Provider=demo 的本地开发实例通过。验证 revision 0、保存 1→4、旧键重放不回退当前版本、同键异参拒绝、CAS 冲突、显式另存、版本分页和旧版恢复、其他账户 404、不同项目隔离、审计游标分页、四次保存仅四条活动与正文／凭据缺席。脚本创建随机 example.invalid fixture 账户，留在隔离开发数据库，不输出密码／Cookie，不调用模型网关。
- 集成负责人实际浏览器观察：v0→v1→v2；预览 v1 后另存 v3；第二窗口保存 v4 后第一窗口收到冲突并保留编辑；选择保留我的内容后保存 v5；活动页恰有五条脱敏 draft.saved。初次未保存离开验收遇 HMR 重载，未计为通过；最终保护／移动／刷新验收由集成负责人在冻结代码上继续，完整结果追加至 013 集成台账。
- 独立只读审阅发现并修正初始读取失败误报同步、StrictMode 旧响应、会话过期卸载丢编辑、异步创建／退出中继续编辑、显式放弃后请求失败的新编辑恢复五类边界。

## 风险与限制

草稿只是文字节点集合与版本历史，无拖拽、连线、DAG 执行、协作锁、实时合并或 AI 自动执行。手动保存，不在每次输入时调用云端。最多 1,000 个版本到顶后不可继续保存，本批没有历史归档／压缩管理。

未保存编辑只在当前 App 页面内存中恢复；主动刷新并确认离开、关闭页面、进程结束、硬故障后不可恢复。会话过期后需同账户重新登录并打开同一项目草稿；其他身份不可见。导出 JSON 含正文，用户自行保管。用户主动放弃是明确丢弃编辑，不等于撤回已经送出的服务端保存。

memory 模式没有云端草稿，仍存当前设备；云草稿历史与活动分页没有虚拟化或万级数据性能验收。活动页为项目只读列表，没有工作区全量控制台、实时推送或消费对账。Provider demo 标识及现有真实媒体能力开关保持原语义。本批 HTTP 测试与浏览器均为本地开发条件，不代表真实外部模型或生产放行。

## 回滚方式

回退前端本批组件、API 调用、导航和版本号，恢复原 local-only 草稿入口，并保留当前旧 v2 localStorage 内容。后端已有云端版本与审计数据不由前端删除；不要为界面回退移除 PostgreSQL 记录或降低 revision。已提交的云端保存由服务端幂等结果核对，不能通过回退浏览器取消。

## 文档同步

已同步 `docs/wiki/frontend.md` 的云端草稿、活动、会话恢复、API、文件结构、验证命令和当前边界。根 README、Wiki 与台账索引、V03/V04 任务状态及最终联合验收由根集成负责人按实际证据同步；011 仅记录本次前端与 HTTP 证据，不代替批次放行结论。关联服务端台账与最终浏览器证据见对应后端记录及 013。
