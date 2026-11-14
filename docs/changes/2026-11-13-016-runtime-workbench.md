# 016 · 工作区容量与限流反馈

日期：2026-11-13。关联批次：V05 可观测与资源准入。模块：H03、H07、H08、H09。前端版本：0.4.0。

## 目的

让创作者看到当前工作区的真实占用与排队情况，并在容量拒绝、暂时不可用或网络故障时知道下一步。保留原幂等请求身份，避免将后续限流误当作最初请求确定未受理而重复生成。

## 范围

Inspector 增加当前工作区容量卡，读取服务端运行／媒体／事件连接及模型占用；有条件低频轮询、手动刷新、错误与过期数据提示。新增 Retry-After 倒计时、请求编号复制、429/503 可行动反馈。生成、草稿、活动和账户界面使用共享反馈组件；未确认生成操作按用户及项目留在本页内存，支持会话过期后原键恢复。本批不访问或展示全局运营指标，不修改服务端配额。

## 实际文件

- `apps/workbench/src/RuntimePanel.tsx`、`runtime.ts`、`runtime.test.ts`、`runtime.css`：容量界面、结构校验、作用域边界、显示口径及样式。
- `apps/workbench/src/requestFeedback.ts`、`requestFeedback.test.ts`、`RequestDetails.tsx`、`useCountdown.ts`：Retry-After、未知状态判断、请求编号与倒计时。
- `apps/workbench/src/pendingRequests.ts`：身份／项目隔离的原请求登记和匹配键清理。
- `apps/workbench/src/api.ts`、`api.test.ts`、`types.ts`：runtime 契约、外部中止信号、请求 ID、错误及冷却元数据。
- `apps/workbench/src/useStudio.ts`、`App.tsx`、`AuthGate.tsx`、`CloudDraftCanvas.tsx`、`ActivityPage.tsx`：接入容量、反馈、冷却和未确认操作恢复，保留草稿原有保护。
- `apps/workbench/src/components.tsx`、`main.tsx`：新增同风格复制图标、样式入口。
- `apps/workbench/package.json`、`package-lock.json`：版本 0.4.0，无新增依赖。
- `docs/wiki/frontend.md`、本台账：当前能力、口径、接口与验证证据。根 README／索引／任务源由集成人负责。

## 接口与数据影响

新增 `GET /api/v1/runtime`，使用同源 Cookie 与当前 `X-Project-ID`；范围为当前工作区全部项目。消费 `runs/tasks {active,limit,queued,oldest_queued_seconds,models}`、`streams {active,limit}` 与 scope/ready/state。占用包括排队、执行、未知及待核实，不显示推算的 running，也不将本地加载的任务列表冒充容量。

侧栏展开、页面可见时请求；每次结束后 15 秒调度下一次 GET。隐藏或关闭时取消计时与在途请求并失效代次；切换身份／项目重建组件，旧快照、错误和冷却不会进入新作用域。原作用域重新打开时继续遵守已有冷却。失败保留旧计数但显式提示可能过期，结构不全不回填假零值。容量只作提示，不作为原幂等操作重试的前端准入门槛。

ApiError 增加 code、requestId、retryAt。响应头 X-Request-ID 优先，后备 error.request_id；只显示最多 128 位安全字符普通文本。429/503 的 Retry-After 支持秒数和 HTTP 日期，限制 0—300 秒，无效或缺失默认 3 秒；隐藏页面暂停秒级更新，恢复后按实际时刻重新计算。

首次结构化 ADMISSION 429 是该次未新受理，输入保留，可在冷却后手动调整提交。503、网络错误及无法识别的 mutation 响应继续未知。原未知请求再次 429／401 不能抹去旧未知；在生成 POST 发出前即登记完整 body/key 和用户／项目，明确成功只清匹配的操作。没有自动 POST／PUT 重试，容量满载也不会禁用原键核对。草稿未知保存重试遇限流同样保留原请求。

## 验证结果

- `npm --prefix apps/workbench run typecheck`：通过。
- `npm --prefix apps/workbench test`：70 项测试、12 文件通过，原 57 项草稿／账户／SSE／隔离等测试保持。新增范围包括真实零值与异常容量拒绝、占用超上限的条形显示、关闭及 A→B→A 后旧作用域拒绝、秒数／日期 Retry-After、五分钟边界、到期和异常值、安全请求 ID、429/401 后仍保留原未知请求、用户／项目隔离及匹配键清理、runtime 请求头、429 明确拒绝、503 mutation 未知、异常网关 HTML 的保守处理。
- `npm --prefix apps/workbench run build`：通过，54 模块。JS 316.32kB（gzip 99.89kB）、CSS 55.45kB（gzip 12.17kB），仅为打包结果。
- 本次没有新增浏览器自动化依赖，没有运行真实付费 Provider。根集成人负责统一后端重启、`smoke:runtime` 与浏览器／截图联合验收；本记录不将尚未收到的浏览器结果写为通过。
- 初次检查发现复制图标类型缺失与 JSX 相邻元素，已修正并完整重新执行类型、测试和构建；最终检查通过。

## 风险与限制

容量是响应时刻的快照，不能保证下一次提交获准，不提供完成时间预测。非终态待核实任务会持续占用，以服务端准入策略为准。客户端轮询与倒计时是展示及频率约束，不替代服务端安全限制；切项目重新取得工作区状态也不会改变服务端配额。

恢复仓只在当前页面内存，刷新、关闭或进程终止后丢失；没有离线队列或持久客户端任务登记。接口未提供实际运行中的独立统计，界面不推算运行数量。runtime 接口旧版本缺失时显示读取失败而不是伪造数据。没有全局监控控制台、运营凭据、容量压测、跨标签页轮询共享、分布式准入或实机移动性能验收。

## 回滚方式

回退本批前端容量与共享反馈组件、API 解析和版本标记，保持服务端既有任务、草稿与审计数据。回退前需核对当前页面受理未知操作，避免丢掉仅在内存保存的原键；不要以重新创建任务代替原操作核对。服务端 runtime 与准入可独立继续运行，前端回退不应关闭后端配额或删除记录。

## 文档同步

已更新 `docs/wiki/frontend.md` 的版本、容量口径、轮询、错误与冷却、原键恢复、文件组织及 70 项测试证据。根 README、Wiki／台账索引、任务源和 V05 放行由集成人统一更新；本台账不自行勾选 V05。批次后端与联合验收证据由关联 014／015 及根集成记录补充。
