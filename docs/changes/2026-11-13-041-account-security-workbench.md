# 041 · 账户安全与自助恢复工作台

日期：2026-11-13。关联批次：V11 账户安全与自助恢复。模块：H01、H06、H08、H09。前端版本：0.10.0。

## 目的

为现有 Cookie 账户接入邮箱验证、找回／重置密码、活动登录会话撤销、改密及脱敏账户安全记录。敏感写请求与创作请求使用不同恢复语义：密码及令牌不进入恢复仓，不自动重发不明提交，避免把生成任务的原键重试文案应用到账户安全操作。

## 范围

登录页找回入口、独立验证／重置链接页面、登录后的账户安全页、邮箱能力与验证状态、最多10个未过期会话、单会话／全部会话撤销、修改密码和安全活动分页。账户菜单及侧栏均有入口，沿用深墨色、紫晶与青绿工作台设计；窄屏卡片改为纵向排列。

密码修改、重置和撤销全部会话明确说明当前会话失效。登录内的操作提交前有确认步骤和未保存内容提示；处理期间阻止导航、冻结云草稿编辑与新参考图提交，原有按身份隔离的恢复仓保留。页面提示内存恢复不等于跨关闭备份。浏览器更改真实用户密码由用户接管；本代理未操作真实账户或外发邮件。

## 实际文件

- `apps/workbench/src/accountSecurity.ts`、`accountSecurity.test.tsx`：账户契约类型、启动链接解析／清理、不可序列化的一次性令牌容器、密码验证、安全错误文案、作用域判断与组件静态检查。
- `apps/workbench/src/AccountRecovery.tsx`：找回申请、显式验证／重置、双输密码、不明结果后核对路径；敏感值提交时清空。
- `apps/workbench/src/AccountSecurityPage.tsx`、`security.css`：邮箱／密码／会话／安全事件页面、确认对话框、禁用／未保存提示与响应式样式。
- `apps/workbench/src/main.tsx`、`App.tsx`：StrictMode之前的一次性启动解析、后续hashchange清理、恢复页路由、账户页入口、编辑保护与0.10版本显示。
- `apps/workbench/src/AuthGate.tsx`：找回入口及离开时清除密码。
- `apps/workbench/src/api.ts`、`api.test.ts`、`types.ts`：同源账户请求、公开能力和邮箱状态、会话与事件契约。
- `apps/workbench/src/RequestDetails.tsx`、`requestFeedback.ts`、`requestFeedback.test.ts`、`useCountdown.ts`：敏感模式只引导核对状态；账户冷却单独有界900秒，创作仍300秒。
- `apps/workbench/src/pendingRequests.ts`、`pendingRequests.test.ts`、`useStudio.ts`：旧生成请求跨401恢复仓／inflight所有权修正，身份只读刷新与安全能力保留。
- `apps/workbench/index.html`：no-referrer策略；`package.json`、`package-lock.json`：0.10.0，无新依赖。
- 本台账、`docs/wiki/frontend.md`：实现、限制及验证。根 README／索引／任务源由集成人维护。

## 接口与数据影响

按[V11契约](../planning/v11-contract.md)接入验证request／confirm、密码reset request／confirm、password change、sessions列表／单个与全部撤销及security events。匿名session也读取account_security，缺少或关闭邮件能力时禁用找回与发送。202只显示“符合条件时已安排发送”，不宣称邮箱存在或邮件送达。

验证／重置只接受 `/#auth=verify|reset&token=...` 的43字符base64url令牌；启动路径在React挂载前读取并replaceState移除fragment，避免StrictMode二次初始化丢值。不把令牌放URL查询、HTML隐藏输入、浏览器存储、日志或恢复仓。后续hashchange同样及时清理。容器使用私有字段，不可通过普通JSON序列化导出；只在用户显式确认后取出一次并立即清空，验证不自动建立或切换登录账户。

新密码双输确认仅在客户端使用，API只提交一份new_password；UTF-8字节10—72，拒绝NUL和不成对代理项。敏感POST不附生成幂等键、不缓存正文，也没有自动重试。提交开始时清空密码输入；任何不明结果只保留已脱敏的错误／请求编号，链接已取出不能在本页再次消费。错误文案不回显服务器原始文本中的潜在秘密。

账户读取与写回使用用户身份及请求代次检查，旧组件回包不得更新新用户或退出新会话。账户页仅显示服务端声明的创建／到期时间，不伪造设备、IP、地理位置或last_seen。撤销后的既有SSE按服务端下一次身份检查退出，前端不宣称瞬间断开全部TCP。

同时修正既有创作请求跨401交错：旧scope的失败不再删掉重登后正在核对的原键；inflight使用每次claim的独立对象，旧同键请求finally不能释放新尝试的锁。原成功确认仍按键清理，同用户重登可继续原请求，其他账户不可读取。

## 验证结果

- `npm --prefix apps/workbench test`：173项／21文件通过，保留原154项回归。
- `npm --prefix apps/workbench run typecheck`：通过。
- `npm --prefix apps/workbench run build`：通过，76个模块；JS433.49kB（gzip136.22kB）、CSS98.68kB（gzip19.80kB）。构建体积不代表性能压测或手机实机结果。
- 新增覆盖：合法／错误／重复／异常长度fragment清URL、不清普通页面锚点、URL无法清理时拒绝令牌、私有一次性容器与重复StrictMode渲染不触发POST、验证／重置明确按钮、未配邮件禁用、未保存提示、密码字节／确认／NUL／代理项、安全错误不回显、敏感错误不建议原请求重试、账户／代次失效、HTTP正文及无自动重发、会话API不带项目header、旧401与同键inflight锁隔离。
- Go代理只读复核发现并修复两处契约细节：旧密码错误的401 INVALID_CREDENTIALS留在安全页、仅GET核对真实会话；实际失效才退出。账户Retry-After=900使用独立900秒倒计时，创作仍保持300秒。已补行为与边界测试。敏感200响应缺少ok／应有reauthenticate字段按未知处理，不误报成功。
- `node scripts/check-docs.mjs`：通过85份文档／42份台账，本地链接与必需字段有效。
- 组件静态渲染不是浏览器端到端验证。该代理未启动服务、未运行PostgreSQL、未修改主QA账户密码、未发送邮件；实际HTTP／PG／CUA及渠道验收由集成人另记。

## 风险与限制

真实SMTP渠道尚未配置与受控验收，默认按钮禁用；本地fixture通过不能把V11整批真实渠道条件标为已完成。密码与令牌只在用户输入／单次请求所需的短暂内存中存在，不承诺浏览器内存物理零化。浏览器关闭提示受自身限制，现有本地恢复只覆盖当前页面会话。

敏感写结果不明后必须核对登录状态；本页不提供原敏感正文重试。验证邮件同页受理后提示检查邮件，不承诺即时到达。安全记录只读分页，不是完整风控控制台；没有MFA、WebAuthn、设备指纹或跨实例全局限流。是否限制未验证账户创作由未来策略决定，邮箱验证本身不增加工作区权限。

## 回滚方式

恢复上一前端构建可回到原登录／创作页，但不会回滚已修改密码、已撤销会话或服务端令牌状态。数据库011的回退按后端备份和格式规则执行，不删除迁移登记或放宽认证。保留原静态构建及数据库／私有文件备份，不清理用户作品或恢复仓来掩盖错误。

## 文档同步

已更新[前端Wiki](../wiki/frontend.md)，明确本地实现、真实邮件未验收及敏感重试边界。根README、Wiki／台账索引、TODO与任务唯一状态源由集成人同步，V11本地链路和实际渠道验收分别记录。

## 联合验收文案修正

根代理在1440桌面／390窄屏及真实测试会话撤销后发现，通用确认提示中的“新凭据”可能让用户误以为仅撤销会话也会改密码。现统一改为“再次进入时，请使用当前有效的账户凭据登录。”；没有改变撤销、改密或失效逻辑。本次只调整该提示与本记录，继续保持其余前端冻结。

本次 `npm --prefix apps/workbench run typecheck` 与 `npm --prefix apps/workbench run build` 均通过；未重跑全量测试。最终产物为 `index-DBMs9T-X.js`（433494字节，gzip136.22kB）与 `index-BS84leOE.css`（98677字节，gzip19.80kB），76个模块。
