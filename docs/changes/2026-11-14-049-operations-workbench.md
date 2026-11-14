# 2026-11-14-049：运营核实台前端

## 目的

V14-A / H01、H03、H05、H06、H08：把授权范围内的任务异常、冻结待核实、证据预演和明确密码确认串成可操作的运营界面。未知响应不重复处置；不把当前核实台宣称为完整审核/风控或支付系统。

## 范围

仅前端运营页面、API适配、权限显示、受理不明保护、测试和说明。后端权限/工单/预演/事务由047/048实现；根负责版本、README、Wiki/台账索引、任务源、OpenAPI及050集成验收。没有改动已有创作、账本或账户业务模块内部逻辑，也未增加依赖。

## 实际文件

- `apps/workbench/src/OperationsPage.tsx`：授权工作区、队列/工单、原单定位、顺序预演与密码确认、安全审计及未知核对。
- `apps/workbench/src/operations.ts`：封闭类型、权限/证据/计量/计划验证、有界恢复仓、一次性密码容器、安全错误映射。
- `apps/workbench/src/operations.css`：沿用品牌暗色的工业操作台布局、amber待核实、响应式与reduced-motion。
- `apps/workbench/src/operations.test.tsx`：15项权限、scope、密码、未知恢复、API边界和SSR测试。
- `apps/workbench/src/App.tsx`：按access授权显示导航、运营页独立范围、离开/刷新保护、敏感动作期间禁切换、隐藏创作Inspector。
- `apps/workbench/src/api.ts`、`types.ts`：运营API与View扩展；所有运营请求不发送X-Project-ID。
- `docs/wiki/operations-workbench.md`及本台账。

## 接口与数据影响

使用已冻结的access、exceptions、cases、case/events、plans、execute接口。读请求显式workspace_id，写请求正文携带workspace_id；scope与账户变化取消/隔离旧响应。observer只读，所有按钮以服务端permission而非角色名判断；服务端仍是最终权限边界。

预演effect为本次金额增量，固定精度字符串显示；动作、原因、证据及计量变化使旧预演失效。确认必须有对应摘要、版本、有效期、明确勾选及当前密码。execute只接受匹配plan_id的Case收据作为成功确认。

根独立审查发现两项边界并已修正：授权列表保留完整最大128条输入（65个不同工作区回归）；每次请求用独立Symbol标识恢复记录，旧POST成功/确定拒绝及GET核对只能清自身记录。deferred API交错测试覆盖旧请求未回→重登录核对→新pending→旧200/422迟到，均不丢新记录。

未知响应只刷新工单，不自动重发。App内存恢复按user+workspace保存安全开单body/key或caseID/planID，最多64范围；不保存密码、计量正文或执行请求。离开有提示，刷新/关闭后需从服务端工单查找，不声称跨关闭的离线保护。

## 验证结果

| 命令/检查 | 结果 | 范围与限制 |
| --- | --- | --- |
| `npm --prefix apps/workbench run test -- operations.test.tsx` | 通过，15项 | 权限、禁写、精确计量、预演版本与摘要、有界scope恢复、密码清理、安全错误、仅一次POST及无项目header；最后专项607ms |
| `npm --prefix apps/workbench run test` | 通过，217项/23文件 | 全前端回归1.73秒；既有账本/账户/生成模块保持通过 |
| `npm --prefix apps/workbench run typecheck` | 通过 | 包含App接线和正式API字段 |
| `npm --prefix apps/workbench run build` | 通过 | Vite构建1.82秒；资产JS485.30kB、gzip153.00kB，非生产容量成绩 |
| 服务端协议只读核对 | 通过 | operations_cases/access/http实际字段、64hex摘要、OPERATIONS_*错误、password10—72bytes、events去身份投影 |
| 浏览器/PG联合验收 | 由050记录 | 本代理不运行PG，不把SSR或mock视为真实后台验收；真实外部渠道未调用 |

## 风险与限制

客户端角色显示不替代服务端鉴权；access读取失败时入口关闭。安全数据仅元信息，完整外部证据需运营另行核实。计划5分钟有效，时钟/版本变化可能要求重新读取预演。未知开单没有重发按钮，需在有界列表选择关联工单；长期数据定位与完整运营搜索不在本批。

前端没有在线授角色、赠额、退款、内容审核、封禁或申诉入口。恢复仓仅本页内存，不能跨关闭恢复；密码在当前网络请求之外立即清理，网络传输自身仍须生产HTTPS部署。V14完整验收及真实邮件/支付依赖保持任务源所列状态。

## 回滚方式

可以关闭OPERATIONS_ENABLED并移除运营导航构建，保留已建立的工单、计划、收据及不可变审计。不能清数据库迁移或owner绕过013/014格式栅栏，不能通过前端回滚撤销已提交的账务决定。持久恢复遵循根联合备份方案。

## 文档同步

- README、Wiki/台账索引和任务源/TODO由根050同步，V14-A与后续B/C范围分开。
- [运营核实台Wiki](../wiki/operations-workbench.md)说明实现、敏感数据和未知响应边界。
- [V14-A契约](../planning/v14a-contract.md)由根维护并与实际服务端/前端核验。
- 本台账记录前端实测，PG和浏览器结果以050实际证据为准。
