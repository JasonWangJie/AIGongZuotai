# 108 · 项目工作流画布与人工选图

## 目的

V19-A。在“创作草稿 → 工作流画布”实例化文本、图像和人工确认模板。既有文字草稿继续单独编辑。用户可指定已保存的草稿修订、源卡片和目标节点，把正文复制到流程编辑区。工作流执行需要已登录的 PostgreSQL 项目。

## 范围

V19-A 工作台，含旧稿映射、编辑、显式执行、人工确认和跨登录恢复。

## 实际文件

- `apps/workbench/src/App.tsx`、`drafts.css` 接入画布切换、项目离开提示和按需加载。
- `apps/workbench/src/workflows.ts`、`workflowApi.ts`、`api.ts` 管理模板、受类型约束的连接、固定版本及项目作用域 API。
- `apps/workbench/src/WorkflowWorkbench.tsx`、`WorkflowEditor.tsx`、`WorkflowRun.tsx`、`workflow.css` 提供节点模型与规格编辑、历史、版本与子资源详情、明确报价确认、人工选图与窄屏布局。
- `apps/workbench/src/workflows.test.tsx` 与 `apps/workbench/scripts/workflow-browser-smoke.mjs` 验证公开交互、原键收据、真实浏览器登录与选图。

## 接口与数据影响

读写均使用项目内的 `workflow-definitions` 和 `workflow-runs`。新实例先保存不可变版本，启动只绑定指定版本和输入。每步由授权用户确认。启用创作点时读取服务器生成的实际节点输入，取得报价并确认后提交。未决操作在浏览器会话存储中仅存作用域、操作类型、资源标识和稳定键；同标签刷新后可用原键做 GET 核对。无回执时保留原键。选图使用服务器复核的本次运行候选，查看原图走项目鉴权入口。刷新及被动补读只访问 GET。

## 验证结果

- 工作台 `npm run test -- --run src/workflows.test.tsx` 七项通过，涵盖模板与端口、存储/账户条件、旧草稿映射、只读核对、作用域及有价步骤显式确认。工作台 TypeScript 和 Vite 构建通过。
- `node apps/workbench/scripts/workflow-browser-smoke.mjs` 使用环回地址与本机 Chromium。集成人串行运行的 `TestPostgresWorkflowBrowser` 第二轮通过，12.90 秒。首次脚本误点左栏同名状态，已按按钮完整名称定位。真实 PG、文本和媒体环回、私有 PNG、独立重新登录、固定版本及两类子资源均被实际读取。
- 真浏览器截图审阅发现窄屏设置面板遮盖完成图片，打开工作流时收起该面板。详情与左栏当前选中运行使用同一已读状态。补加手机视口图片命中检测，独立浏览器夹具通过。集成人在集成台账补录最终 PG 回归。

## 风险与限制

同一标签页可以恢复未决操作标识，跨登录通过已提交的项目运行记录继续。未保存的流程编辑只在本页内存。浏览器测试使用隔离服务，无真实收费验收。视频图像输入、工作流重跑与撤销属于 V19-B/C。

## 回滚方式

连同 V19-A 后端与 023 迁移，按匹配版本恢复联备数据库及私有资产。单独回退前端入口时应先核对未决操作收据和等待选图的运行。

## 文档同步

集成人维护 README、Wiki、变更索引、任务源和生成的 TODO，并保留[工作流实施契约](../planning/v19-contract.md)中的后续范围。

