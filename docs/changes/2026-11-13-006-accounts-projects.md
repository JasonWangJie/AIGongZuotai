# 006：账户会话、项目隔离与能力驱动工作台

日期：2026-11-13。范围：本轮前端增量；Go 认证／持久化和媒体适配见各自台账。

## 目的

把第一阶段匿名单项目工作台推进为真实 Go 会话支持的注册、登录、退出与项目工作台，确保跨项目切换时旧异步回包不会污染当前页面，同时根据服务端能力开放真实媒体入口。

## 范围

新增延续深墨色／紫晶视觉的 AuthGate、账户菜单和项目选择／创建。认证由服务端 HttpOnly Cookie 完成，不以 localStorage 模拟登录。重构请求与 SSE 生命周期，增加项目代次保护、401 和会话过期处理、本地草稿身份／项目分键、动态生成与取消能力，并增加针对关键契约的测试及真实 HTTP 集成脚本。

## 实际文件

- `apps/workbench/src/AuthGate.tsx`、`auth.ts`、`auth.css`：登录注册界面、账户与项目控件样式、密码字节验证。
- `apps/workbench/src/scope.ts`、`capabilities.ts`：作用域代次、跨项目资源检查、能力读取与旧契约兼容。
- `apps/workbench/src/projectPreference.ts`、`projectPreference.test.ts`：按用户／demo 分键记录最后项目，刷新时用服务端拥有的项目列表核对，不信任本地偏好授权；覆盖无效项目、账号隔离、匿名状态与浏览器存储不可用四项测试。
- `apps/workbench/src/useStudio.ts`：会话初始化、登录退出、项目切换、迟到响应隔离、SSE 关闭与重建、受理不明状态按项目管理。
- `apps/workbench/src/api.ts`、`types.ts`、`state.ts`：账户／项目 API、显式项目请求头、能力类型、INTERRUPTED 和 SUBMIT_UNKNOWN 状态。
- `apps/workbench/src/App.tsx`、`components.tsx`、`main.tsx`：账户入口、项目创建／切换、草稿分键、动态媒体／取消控件、样式引入。
- `apps/workbench/src/auth.test.ts`、`api.test.ts`、`scope.test.ts`、`capabilities.test.ts`：18 项新增测试，与既有 10 项组成 28 项。
- `apps/workbench/scripts/integration-auth.mjs`、`package.json`：隔离本地真实 Go／PostgreSQL 联调脚本与 `test:integration` 命令。
- `apps/workbench/package.json`、`package-lock.json` 与侧栏标识：同步版本为 0.2.0／STUDIO 0.2，未新增第三方依赖。
- `docs/wiki/frontend.md`、本台账：能力、边界和验证同步。

## 接口与数据影响

新增前端调用 `/api/v1/auth/session|register|login|logout` 与 `/projects`。登录／注册只发送邮箱、密码及注册名称；身份不存客户端令牌。每个 GET 显式携带项目头，SSE 添加 `project_id`；POST 创建继续在 body 中使用服务端项目 ID。认证过期清空工作台并要求重新登录，断网恢复不会自动重发生成。

bootstrap 可声明 `capabilities.chat/image/video`，前端使用 `enabled`、`cancel_supported`、`max_count`、`aspect_ratios`。有真实媒体配置时允许对应模式提交；供应商无撤回能力时不显示取消按钮。`SUBMIT_UNKNOWN` 明确表示待核实，禁止当作已失败自动重生成；`INTERRUPTED` 是终态。旧 bootstrap 没有能力字段时保持保守兼容。

草稿 key 改为 `xingtu-local-draft-v2:<user-or-demo>:<project>`，不自动迁移 v1 草稿到新账户，不改变服务端数据表。测试脚本会在隔离数据库创建随机测试账户／项目／demo 运行，不删除已有数据、不输出敏感字段、不调用 live 模型。

同轮对齐后端历史分页：任务与对话 GET 发送 `limit=50&offset=N`，读取 `has_more`，提供加载更早记录控件与“已加载”数量，按资源版本合并重叠页；项目切换重置分页，旧项目分页回包同样被忽略。增加分页请求与重叠页保留 SSE 新版本两项测试。尚未宣称列表全量或万级数据性能。

## 验证结果

- `npm --prefix apps/workbench test`：6 个测试文件、28 项通过。新增覆盖密码 UTF-8 字节边界、Cookie 请求、同时请求不同项目、401 分类、A→B→A 迟到回包、跨项目资源过滤、退出失效、动态媒体能力与中断状态。
- `npm --prefix apps/workbench run typecheck` 与 `npm --prefix apps/workbench run build`：前端类型检查及生产构建通过；阶段产物约 JS 281kB / gzip 89kB、CSS 44kB / gzip 10kB。
- `$env:STUDIO_TEST_URL='http://127.0.0.1:8080'` 后执行 `npm --prefix apps/workbench run test:integration`：在根负责人启动并授权的专用空开发库上通过。真实 Go + PostgreSQL required/demo 联调覆盖匿名 401、注册 HttpOnly Cookie、登录／退出、项目创建与显式切换、列表隔离、另一账户访问资源 404、退出后 401、重新登录读取历史；只创建随机 `example.invalid` 测试账户，未调用 live AI。
- 只读竞态复核确认旧 POST 回包经 scope／代次校验后才能写状态，迟到错误被忽略，旧请求 finally 按操作键避免清除新请求锁。28 项单元测试包含代次／延迟列表／页面重叠验证，尚未使用 React hook 挂载测试覆盖完整旧 POST 生命周期；不能把纯逻辑测试等同于所有浏览器竞态已验证。
- 没有在代码、台账或工具输出中记录实际密码、Cookie、网关密钥或数据库连接密码。

联调后补齐最新后端状态：`PARTIAL` 为“部分完成”终态；`RECONCILE_REQUIRED` 为“需要核对”，与 `SUBMIT_UNKNOWN` 一样不自动重提交、不提供取消。此修正避免将部分完成误计为运行中，也不把需要对账包装成可重新生成的普通失败。

刷新验收修正：记录用户最后选中的项目，避免刷新后无提示返回第一个项目而误以为历史丢失。先验证真实会话返回的项目归属，只有匹配的偏好才传给 bootstrap；不存在时回默认，浏览器存储受限时平稳降级。没有在 localStorage 保存密码／令牌。顺带修正离线文案，不再声称缺少有效项目时仍可编辑项目草稿。

最终安全恢复补充：读取逐任务 `can_reconcile`，仅对服务端允许的待核对状态提供“重新核对”，调用 `POST /tasks/{id}/reconcile`（无生成 body），明确只查询原任务、不重新生成。不接受客户端上游 ID；无 ID 的受理不明任务不显示按钮。增加能力门控与请求不携带生成参数两项测试。

最终前端验证：`npm --prefix apps/workbench test` 为 7 个文件、34 项通过；`npm --prefix apps/workbench run build` 的严格 TypeScript 与 Vite 构建通过。版本 0.2.0，JS 282.44kB / gzip 89.46kB，CSS 44.39kB / gzip 10.14kB。最后项目偏好四项测试与核对两项测试均已包含；真实注册／项目 HTTP 脚本此前已通过，核对恢复的上游状态场景由后端 fixture 验证，未在真实供应商执行收费请求。

OpenAPI 对照后的最后标签修正：Task／Run 类型接收逐记录 `provider_mode`，历史任务卡、素材和对话标签优先使用记录来源，只有旧记录缺失字段时回退当前配置。避免在服务从 demo 切换 live 后把历史示意结果标为真实。新增持久记录来源优先与旧记录兼容两项测试。

标签修正后冻结验证：7 个测试文件、36 项通过，TypeScript／Vite 构建通过；JS 282.67kB / gzip 89.54kB，CSS 44.53kB / gzip 10.16kB。未新增依赖，未操作现有服务或执行真实 AI 请求。

## 风险与限制

这是账户／项目基础链路，不是完整公众运营账户体系；邮件验证、密码找回、MFA、组织邀请、审计运营界面仍未实现。生产上线必须继续验证会话策略、限流、TLS、隐私与故障恢复。客户端隔离是避免显示污染的第二道保护，真正授权仍由 Go 执行。

每个标签页仍独立订阅；会话失效依赖 API 401 或连接中断后的会话检查，没有跨标签页 leader 协调。历史已实现 offset 分页，但尚未实现游标分页和列表虚拟化。项目草稿仍为本地明文内容，按身份分键不等于加密，退出不会删除此设备已有草稿。项目切换会清空未提交描述，避免混入其他项目。

## 回滚方式

回退本轮前端文件和 Wiki／台账引用，重新构建部署旧版本；后端是否回退由相应模块记录管理。原 v1 草稿仍在原 key，v2 草稿可以保留而不被旧版本读取。不要通过清空数据库或全部浏览器存储执行前端回滚。

## 文档同步

已更新 `docs/wiki/frontend.md` 的账户、项目、动态能力、测试与当前限制。根负责人负责 README、Wiki／台账导航和本轮总体状态，避免并行覆盖。交付前执行文档治理检查，并将实际联调与浏览器结果追加到本条记录。
