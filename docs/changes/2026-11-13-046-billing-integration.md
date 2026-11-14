# 046 · V12权益账本联合集成

## 目的

接通工作区权益报价、任务冻结和结算，使后续支付建立在可核对的不可变账本上。

## 范围

V12／H01、H02、H03、H05、H06、H07、H08、H09。本地权益单位与真实付款分开，实际商户属于V13。

## 实际文件

- `internal/studio/billing_pricing.go`及`billing_pricing_test.go`、`config/prices.demo.example.json`：有限价格表、SKU、demo/live隔离、整数费用。
- `internal/studio/billing_http.go`及`billing_http_test.go`、`http.go`：四个HTTP入口、严格报价预检、bootstrap和作用域分页。
- `internal/studio/ledger_admin.go`、`cmd/ledger/main.go`及`billing_admin_test.go`：停机预演、计划摘要、核账、稳定操作键、授予/冲正/核实。
- `billing_gateway_test.go`、`billing_process_test.go`、`durable_process_test.go`：实际HTTP流到PG结算、强制Kill恢复与人工核实。
- `references.go`及`billing_security_test.go`：共享参考ID严格语法，在数据库编码前拦截畸形输入，保持错误请求不熔断服务。
- `cmd/server/main.go`、`.env.example`、`storage_maintenance.go`、`observability.go`、`scripts/lib/process.mjs`及六套smoke/load脚本、根package：入口/012维护栅栏、56固定路由桶、测试权益配置隔离、0.11.0版本。
- `docs/api/openapi.yaml`、`docs/planning/v12-contract.md`、`docs/wiki/billing-operations.md`、`decisions/0009-credit-ledger.md`、`backend.md`、`performance.md`、`development.md`、`observability.md`、`storage-maintenance.md`、`references.md`；根README、Wiki/台账索引、AGENTS规则、任务源/TODO及升级/浏览器/性能证据。043核心、044用量、045前端独立记录。

## 接口与数据影响

012账本迁移、CREDIT六位整数字符串、工作区钱包/流水/冻结单和报价四入口与受理关联；报价10分钟、原输入/会话/模型/模式/价格绑定，报价不执行或冻结。旧任务不追扣，默认关闭，新钱包不赠送。媒体交付SKU与网关成本分离，LLM可信用量才能结算。每项累积消费和所有既存剩余冻结共同占用整数上限，退款保留累计值；在受理前拒绝无法预留累计头寸的请求。

管理CLI不暴露公开写入口。默认在回滚事务内预演，apply匹配数据库UUID/原请求/钱包及冻结单快照的SHA256；原操作重放恢复旧收据，不重复变更。仅终态或parked REVIEW可核实，仍执行/查询的记录拒绝。全额冲正是平台权益反向分录，不代表真实支付退款。

## 验证结果

- 整数价格/SKU/配置单测通过0.045秒；普通`go vet ./...`和Go全量通过（studio7.653秒），不计作PG验收。
- 根首次`npm run test:db`真实PG全仓通过130.489秒，含012、HTTP权限/预检、账本、邮件/会话/存储/租约等回归。
- 根独立CLI与实际进程Kill两函数通过4.532秒：预演不落库、活跃writer拒绝、摘要变更拒绝、同键原收据；一次真实loopback上游POST后Kill，恢复INTERRUPTED/REVIEW，原键不重发、人工核实释放并对平。
- HTTP代理两项真实PG专项通过3.058秒：纯报价、原hash、任务规格/引用ACL、会话预检/CAS、工作区空钱包及有界游标/跨账户隔离。
- 独立审查发现并修复两个交付前问题：累计消费达到上限后退款再受理会令终态无法结算；非法NUL参考ID可使PG编码错误触发全服务fail-closed。前者改为冻结时预留累计头寸，核心全部8 PG函数回归6.956秒；后者共享入口只接受ref_32hex。根修后四函数/五usage子例7.229秒通过，恶意报价422、私有读取404、ready200、零业务/冻结副作用。
- 实际HTTP usage→PG链覆盖正常精确303 micros、缺失/小数非法/超预算/不完整流、过期报价+价格变更后的原键重放；只有有效完整报告自动扣减，其余输出与REVIEW并存。
- 开发库升级前停机联备：dump117691字节，SHA256 `0faff5abecf7214ee685c5361a60fddf48aeb41c8d6a359575cc9c19c126414a`；pg_restore列表可读，参考5文件/资产2文件保全。初次只读比对脚本表名拼写错误在启动迁移前退出，修正后001—011升级001—012成功。8资产、修订10、草稿5、12消息节点、8总Run、邮件/令牌0均保持；无自动钱包/冻结/流水。证据见`billing-upgrade-v0.11-2026-11-13.json`。
- 通过离线预演/匹配摘要为既有本地验收工作区授予100演示创作点，已核对双分录平衡；未支付、未触发真实生成/邮件。
- 2026-11-14修后最终`npm run test:db`全仓通过，studio148.283秒；`npm run check`通过，包含Go vet、普通Go全量studio8.059秒、8项治理、前端202项/22文件、严格TS与生产构建。前端81模块，JS450.05kB/gzip141.88kB，CSS103.93kB/gzip20.86kB。日志保存在忽略的本地`.runtime/v12-final-test-db.log`与`v12-check.log`，不会提交运行日志。
- 六套`npm run smoke`、`smoke:runtime`、`smoke:catalog`、`smoke:references`、`smoke:library`、`smoke:conversations`全部通过；runtime5组、目录3组、参考3组、资产3组、会话5组。故意继承启用权益/邮件及无效配置的父环境，脚本仍强制隔离，未访问真实渠道。五份报告、性能与最终server.exe的SHA256均为`aa9a7844ec21121973dec687de83e8c7c1f444fdf517e1eba8f5bd70a9fcc18e`。
- `npm run perf:local`：memory/demo、关闭PG/鉴权/邮件/权益，bootstrap1000次/32并发P95 26ms、2077.83请求/秒；任务创建+取消100组/8并发P95 14.7ms；12条/4并发demo首delta P95 56.96ms。保留[原始报告](../wiki/assets/performance-local-v0.11-2026-11-14.json)，不是账本容量或生产性能成绩。
- OpenAPI使用PyYAML遍历解析：0.11.0、54路径、65操作、67 schemas、311引用；无无法解析引用及重复operationId。四个权益入口与Go路由核对。
- CUA实际PG/demo浏览器：图片报价0.2点、修改输入使旧报价失效，重新确认后HELD→SETTLED；新空会话不调用AI，聊天报价0.045056点、结算0.000384、释放0.044672；视频16:9/720p/5s报价并结算2点，demo仅海报。最终钱包可用97.799616、累计消费2.200384、冻结及待核实0；三单均结算，状态筛选与流水查看通过。390×844钱包和报价无横向溢出，确认按钮可见可用，console warning/error为0。见[浏览器记录](../wiki/assets/billing-browser-v0.11-2026-11-14.json)、[桌面](../wiki/assets/billing-phase11.jpg)、[桌面报价](../wiki/assets/billing-quote-phase11.jpg)、[手机](../wiki/assets/mobile-billing-phase11.jpg)、[手机报价](../wiki/assets/mobile-billing-quote-phase11.jpg)。

最终`npm run todo:sync`生成成功；`npm run check:todo`通过10模块/23批次/11批已验收；`npm run check:docs`通过95份文档/46份台账；`npm run check:changes`通过407个路径的同步检查。V12三个检查项依据以上实际证据通过，完成日期2026-11-14。V07/V11/V15真实验收保持待条件，不因本批通过改写历史。

## 风险与限制

真实支付与供应商成本核对尚未验收。缺少可信用量、受理不明或越过冻结上限时必须待核实，不按文字长度推算最终费用。离线核账全表扫描有30秒超时，平台仍单控制写者及共享系统账户锁；本地成绩不能代表生产分布式极限吞吐。人工核实是终结原冻结的明确决策，之后用户核对找回更多成果不会再开冻结或二次收费；运营须先核对证据。未实现自动补usage、部分权益冲正或真实资金退款。

## 回滚方式

012之前停写联备数据库及私有目录；保留旧版本备份，不删除迁移登记或已发生分录。

## 文档同步

同步README、Wiki导航/模块/ADR、台账索引、AGENTS当前阶段、任务源与生成TODO。V12标记完成，总计11/23批；后续V14本地链路仍受V11真实验收放行依赖，V16已有依赖齐备，可独立推进。
