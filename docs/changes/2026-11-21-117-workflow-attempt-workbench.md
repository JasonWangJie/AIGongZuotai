# 117 · 工作流显式重跑、取消与原键核对

## 目的

V19-C。已读运行可预览重跑影响范围、建立独立尝试或停止后续节点，保留对原执行、选择、审核和费用的查看入口。

## 范围

操作位于工作流运行详情的“查看重跑与取消”。兼容 v1 三节点选图和 v2 四节点首帧视频模板。新尝试可从图内的文本、图片、人工选图或视频起点开始。服务端判定前置结果和可用范围。

## 实际文件

- `apps/workbench/src/WorkflowAttemptControls.tsx`、`workflowAttempts.ts` 显示原子任务、审核/权益状态、受影响或沿用的节点/结果，以及明确的重跑、取消确认。
- `apps/workbench/src/WorkflowWorkbench.tsx`、`workflowApi.ts`、`workflows.ts`、`workflow.css` 增加只读预览、项目内动作、独立原键收据、新/原运行导航与按项目失效补读。集成人在 App 接入项目事件版本。
- `apps/workbench/src/WorkflowRun.tsx`、`RequestDetails.tsx`、`types.ts` 与 `workflowVideo.test.tsx` 区分当时选图修订和当前可访问内容，校验子报价的尝试身份，核对期间只提示读取原收据。
- `apps/workbench/src/activity.ts`、`activity.test.ts` 标识媒体原提交受理不明的项目审计事件。
- `apps/workbench/src/workflowAttempts.test.tsx`、`apps/workbench/scripts/workflow-browser-smoke.mjs` 增加并发状态、原键核对以及可选 C 浏览器路径。

## 接口与数据影响

用户主动选择起点后，GET 预览必须与该项目已读运行 ID、版本、起点、下游及继承集合一致。生成节点可修改本次起点指令，文本起点还可调整本次主题。确认提交新操作键，返回独立尝试，后续生成仍逐节点授权和报价。REVIEW 只提供停止后续节点；原任务受理或费用状态继续由授权读取核实。取消确认绑定当前运行版本。重跑与取消回执均以原操作键请求头及各自的 GET 路径核对。刷新、重新打开、项目通知与收据读取不会自动生成。原选图链接只对当前项目仍可读且匹配同一任务、产物、资产与内容版本的候选显示，单纯改名或标签修订保留读取能力。新尝试的报价保留节点输入上的 `workflow_attempt_id`，旧尝试或缺失身份的输入不能继续。取消仅阻止后续步骤，已结算金额保留，待核实冻结按原执行证据处理。

## 验证结果

- `npm --prefix apps/workbench run test -- --run src/workflowAttempts.test.tsx` 十一项通过，2.62 秒。覆盖预览与实际图匹配、提示词与原结果区分、过时取消、请求头收据、异源回执留在原未知状态、未保存编辑遭遇项目更新，以及 REVIEW 后续停止。
- 全工作台 `npm --prefix apps/workbench run test` 为 34 个文件、348 项通过，5.66 秒。TypeScript 和 Vite 构建通过。
- 配置 `WORKFLOW_BROWSER_ATTEMPTS=1` 的环回前端 Chromium 夹具实际编辑重跑起点、新建子 Task、显式取消并返回原选图。脚本在两个 POST 已返回成功之后分别丢弃一次响应，后续仅用原键 GET 读取，并核对原图一致。窄屏截图在 `.runtime/v19c-mock-browser/`。
- 复核后再执行全工作台 `npm --prefix apps/workbench run test`，34 个文件、356 项通过，5.47 秒。覆盖旧选图内容失效、同内容元数据修订、其他尝试报价与取消收据服务暂不可用时的提示。生产构建通过，Vite 3.18 秒；再次运行 C 环回前端 Chromium 夹具通过。
- 实际 PG 和跨进程恢复由集成人串行运行并另行登记。A、B 浏览器默认路径保持既有调用次数。

## 风险与限制

浏览器呈现服务端返回的已提交证据；审核、供应商执行和费用终态按原子资源读取。服务端是否已看到上游响应不由界面推断。浏览器表单离页后清空，同标签会话存储中仅保留原操作身份以待核对。

## 回滚方式

先核对已提交的取消及重跑收据，并保留原执行和所有已派生尝试。前端可随匹配的后端版本和持久格式一并回退；旧程序、库和私有资产按联备恢复。

## 文档同步

集成人同步 README、Wiki、进度、任务源、生成 TODO 和台账索引，并对照[工作流实施契约](../planning/v19-contract.md)登记集成证据。

