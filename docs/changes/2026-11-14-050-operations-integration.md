# 050 · V14-A 运营异常核实联合集成

## 目的

将异常任务及待核实权益从停机人工定位推进到有作用域、可预演和可追溯的运营处理流程。v0.12.0完成V14-A本地子批次，完整V14保持进行中。

## 范围

V14-A本地子批次，横切H01/H03/H04/H05/H06/H07/H08/H09。完整V14还包括生成前后内容审核、申诉、封禁与风控，以及V11真实渠道依赖；本子批次不得替代整批放行。三个本地check通过，整体仍11/23整批完成；子项证据不是工期百分比。

## 实际文件

- 根集成负责`internal/studio/app.go`配置、`http.go`路由、`observability.go`固定标签、`ledger_admin.go`和`storage_maintenance.go`版本栅栏，`cmd/server/main.go`及`main_test.go`严格布尔配置。
- `internal/studio/operations_integration_test.go`：配置约束、真实PG协议代理丢失COMMIT确认回包、App重启恢复唯一收据及禁止重复处置。
- `scripts/lib/process.mjs`、`scripts/smoke.mjs`、`runtime-smoke.mjs`、`catalog-smoke.mjs`、`reference-smoke.mjs`、`library-smoke.mjs`、`conversation-smoke.mjs`、`load.mjs`：隔离运营环境，保留既有邮件/权益隔离。
- `.env.example`、`package.json`、`apps/workbench/package.json`及锁文件：默认关闭和0.12.0；无新增依赖。`docs/api/openapi.yaml`补8个HTTP操作及82 schemas总目录。
- `README.md`、`AGENTS.md`、`TODO.md`、`docs/planning/tasks.json`、`v14a-contract.md`、Wiki/台账索引、`docs/wiki/decisions/0010-scoped-operations.md`、`development.md`、`frontend.md`、`roadmap.md`、`performance.md`。
- `docs/wiki/assets/operations-{validation,browser,upgrade}-v0.12-2026-11-14.json`、`performance-local-v0.12-2026-11-14.json`、`operations-phase12.jpg`、`operations-plan-phase12.jpg`、`mobile-operations-phase12.jpg`、`mobile-operations-plan-phase12.jpg`：脱敏联合证据。
- 权限与CLI、工单领域、工作台分别记入047/048/049；配套billing-operations/storage-maintenance/backend/ledger/observability说明分别在对应台账登记。私有运行日志、fixture凭据、备份和临时编排脚本只在被忽略的`.runtime/`。

## 接口与数据影响

新增默认关闭的`OPERATIONS_ENABLED`，只接受true/false，启用须PG及required鉴权。注册及owner不自动获得运营角色；grant/revoke仅停机CLI管理。7个路径/8个操作包括access、异常资源、工单列表/创建、详情/事件、plans和execute。按工作区实时鉴权；普通用户没有权限，运营不能据此读取创作私有正文或素材。

敏感执行要求5分钟有效的精确预演和当前密码，事务内重核凭据/会话/grant/资源/账本版本。SAVEPOINT实际验证并回滚；业务、工单、计划消费、收据、适用账本、审计与Outbox同事务，提交后才更新缓存。原handle核对只恢复查询；媒体核对或财务处置撤销旧租约，不再次POST生成。REVIEW释放/结算不接受任意金额，仍按冻结规则；no_action只关闭工单。

013/014增加授权/审计/工单/计划/收据，完整格式栅栏改001—014；旧分录不可改删，无旧资源追扣。观测路由使用63个固定有界桶，运营标签不含用户、工作区、工单ID、原因、密码、证据或正文。响应不明只读原工单/收据，密码不持久化；前端旧请求迟到不能清掉新的未知操作。

## 验证结果

2026-11-14最终验证如下；专项历史结果详见047/048/049，汇总见[验证JSON](../wiki/assets/operations-validation-v0.12-2026-11-14.json)。

| 命令/操作 | 实际结果 | 口径 |
| --- | --- | --- |
| `npm run check` | 通过 | 8项治理、Go vet/普通测试、217项前端/23文件（1.51s）、严格TS、前端构建1.58s及Go构建；文档结构/链接通过 |
| `npm run test:db` | 通过，studio169.657s | 独立schema真实PG全量，包含V14角色、版本/撤权、密码proof、CLI、工单、HTTP严格输入及完整既有业务回归 |
| `go test ./internal/studio -run '^TestPostgresOperationsUnknownCommitRecoversOneReceipt$' -count=1 -v` | 通过，2.540s | 实际PG提交后代理丢失确认回包，503且不提前写缓存；App重启读回唯一RELEASED收据；再次execute冲突，不新增账本分录 |
| `npm run smoke`、`smoke:runtime`、`smoke:catalog`、`smoke:references`、`smoke:library`、`smoke:conversations` | 六套通过 | 最终server.exe；父环境开启运营/权益/邮件并给无效渠道参数，脚本强制隔离，未外发 |
| `npm run perf:local` | 通过 | 1000次/32并发bootstrap P95 21.97ms、2350.71次/秒；任务创建+取消P95 12.39ms；demo首delta P95 56.95ms，仅回环memory/demo |
| Python PyYAML递归引用及operationId唯一校验 | 通过 | 版本0.12.0，61路径、73操作、82 schemas、340个本地引用均可解析；非完整外部OpenAPI认证 |
| CUA真实Go+PG/demo浏览器 | 通过 | 普通用户隐藏入口；operator/finance及observer独立范围；开单、修改证据撤销预演、错误密码拒绝、明确确认、收据/审计只读补回；桌面/手机无横向溢出 |
| `npm run todo:sync`、`check:todo`、`check:docs`、`check:changes` | 全部通过 | 10模块/23批/11整批，104份文档/50份台账；440个变更路径同步检查通过，V14仅三个已验收check通过 |

最终二进制SHA256 `eed5c52525ff99f4d74d583766bc2efdefe4541d3c4bc6f9f211f74df63dc40a`，与五份冒烟JSON及性能报告一致。前端产物JS485.31kB/gzip153.00kB，CSS113.61kB/gzip22.73kB。该大小与本机吞吐不是前端生产性能或PG运营容量成绩。

浏览器使用专用本地fixture运营账户，普通QA账户未自动变为管理员。DEMO取消任务的0.045056创作点处于REVIEW，经工单v1/资源v3/冻结v3预演并正确密码确认后，工单v2/资源v4/冻结v4、RELEASED、消费0、释放0.045056。界面显示唯一收据，切换scope返回仍能读取；执行按钮消失，5条审计为建单、3次预演、1次提交。错误密码未退出登录；密码提交后清空。1440×1000及390×844文档宽度与viewport一致，最后采集warn/error为0。见[浏览器证据](../wiki/assets/operations-browser-v0.12-2026-11-14.json)。COMMIT未知恢复在PG故障测试验证，不声称由浏览器制造该故障。

升级前关闭原开发写者，`pg_dump -Fc`、`pg_restore -l`验证可读并复制私有REFERENCE_DIR/ASSET_DIR，备份位于忽略目录`.runtime/backups/pre-v0.12-20261114T032825194Z`。dump156234字节、SHA256 `90797d0f6c872d04a5119835d3373bc2b38a5a772820283a929f3759768efff4`，5个参考文件和2个资产目录文件联备；本地邮件关闭，不存在待投递密文。012→014后10个资产、资产修订10、草稿修订5、会话节点数2/12、9个运行、8个journal、3个reservation及零邮件/安全token保留，初始运营grant/case为0。见[升级比对](../wiki/assets/operations-upgrade-v0.12-2026-11-14.json)。本次检查dump可读及升级后旧数据，不是完整灾备恢复演练。

集成修正记录：初始Node私有目录复制在Windows异常退出，保留不完整临时目录，改用原生PowerShell完成新的完整联备后才升级；fixture脚本错读模型enabled层级在生成前停止，修正并沿用已注册账户，无真实调用。文档初检指出047标题不符，调整为标准八章后通过。独立复审修复execute路径NUL提前进PG、角色撤销版本容量、前端65个scope被64条截断及旧POST迟到清新pending；修改后专项与本次全量均通过。没有改写既有历史验收来掩盖失败。

## 风险与限制

本轮未进行真实收费AI、外部邮件或支付调用。未实现的内容审核/申诉/封禁/额度风控继续留在V14-B/C；V11真实邮件按用户安排后续配置，完整V14依赖未放行。真实供应商及支付条件分别保留V07/V15和V13，不以fixture代替。

服务仍单控制写者及绑定本地私有目录，不支持全站多实例或生产万级流。当前密码确认不是MFA或双人审批；安全元信息不是内容审核，证据编号本身不能证明供应商执行真相。工单、计划、授权与前端恢复仓均有界；异常列表只读元信息，未知敏感请求恢复仅当前页内存，关闭后应从持久工单定位。运营人工决定是当前冻结的最终决定，不是支付退款。

Go race的本机CGO/C编译器缺口及Linux CI未远程执行状态保持历史记录，本轮不声称通过race；S3等未改专项未重复。性能脚本不覆盖PG运营查询/锁竞争、bcrypt或SAVEPOINT，应由V16/V17固定环境专项测量。

## 回滚方式

关闭`OPERATIONS_ENABLED`可停用入口，但不会撤销授权、工单或已提交业务处置。撤权用停机CLI新操作记录，不删除审计或复用旧grant键。历史账本冲正只能按既有政策追加反向分录，不能修改分录冒充回滚。

代码/结构回退须停写并恢复升级前数据库、私有目录及启用邮件时的队列密钥，使用相匹配旧版本；恢复旧备份会失去备份后的业务，需先保存和核对新增记录。禁止删迁移、owner、工单收据或账本历史强行降级。本次未执行开发库回退或删除用户文件。

## 文档同步

README更新v0.12能力、默认配置、CLI、验收及下一步；Wiki新增权限/工单/前端、ADR0010、契约与图文证据，现有模块补充当前边界；AGENTS加入实时scope、密码、预演/事务、未知只读、旧lease隔离和013/014规则。047—050索引与任务源同步，TODO由任务源生成。

V14前3项check通过、B/C待办、完整依赖blocked，整批状态in_progress且completed_on为空；11/23整批不变。下一步执行V14-B可版本化审核策略、生成前准入、输出隔离与审核失败/恢复，再推进V14-C申诉封禁与风控。
