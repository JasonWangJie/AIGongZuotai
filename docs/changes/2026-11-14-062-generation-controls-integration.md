# 062 · V14-C1新生成限制与复核联合集成

## 目的

交付工作区新生成限制、用户复核申请、授权处置及答复的本地完整链路。

## 范围

V14-C1，H01/H03/H05/H06/H08/H09。真实渠道及完整V14保持独立待条件。

## 实际文件

- `internal/studio/generation_control_http.go`、`generation_control_http_test.go`、`http.go`：10个路由、严格JSON、身份/作用域、分页、HTTP全链和COMMIT确认丢失恢复。
- `internal/studio/observability.go`：10个固定路由指标桶，拒绝把工作区、限制轮次、申请或原URL作为标签；真实HTTP指标回归覆盖。
- `internal/studio/ledger_admin.go`、`operations_admin.go`、`storage_maintenance.go`、`cmd/operations/main.go`：017精确格式栅栏及moderator命令帮助；角色领域见060。
- `internal/studio/media_moderation_test.go`：隔离SQL-helper测试与后台worker竞争，保留原生产拒绝断言。
- `internal/studio/conversation_test.go`：把旧“下一次COMMIT”故障注入改为PG写入屏障定位真实受理事务，保留无缓存/无Provider调用及原turn恢复断言。
- 根和前端`package.json`及前端锁文件：v0.15.0；前端测试依赖、代码及回归另见061。
- `docs/api/openapi.yaml`、`docs/planning/v14c1-contract.md`、`docs/wiki/decisions/0013-workspace-generation-controls.md`：当前接口、边界和设计取舍。
- `README.md`、`AGENTS.md`、`TODO.md`、任务源及Wiki/台账索引；相关`backend/frontend/development/roadmap/operations-access/billing-operations/storage-maintenance/observability/performance` Wiki。
- `docs/wiki/assets/`本批升级、浏览器、验证、隔离冒烟、性能JSON及截图。忽略目录`.runtime/`内临时测试/备份帮助脚本和本机日志不作为源码提交，绝不包含到凭据台账。

059记录核心/017，060记录准入/角色，061记录前端；根依据真实证据更新状态，不以子代理完成声明替代验收。

## 接口与数据影响

017新增工作区控制、限制episode、用户申请和幂等、运营预演/回执；限制原始事实不可改，解除时间仅有效解除事务一次写入。独立moderator角色。原键重放优先，限制只影响新受理，旧任务/内容审核/账务不被改写。站内拉取式状态，不新增邮件或项目广播。OpenAPI为0.15.0、71路径/83操作/95模型/411引用，所有原API保留。

## 验证结果

本节持续记录实际验证；最终汇总追加于下方，不借用058历史成绩。

- 根`node .runtime/v14b-test.mjs '^TestPostgresGeneration|^TestPostgresOperationsModerator'`：11项真实PG通过28.923s；其中HTTP完整链与限制/解除两种COMMIT确认回包丢失恢复已覆盖。独立首次限制双向workspace锁竞争补测通过2.312s，用`pg_blocking_pids`确认实际阻塞。
- 根`go test ./internal/studio -run '^TestGenerationRoutes|^TestGenerationHTTP' -count=1`：通过0.350s，10路由指标固定分类/私有ID不入标签、严格JSON11子例及账户模式门禁。
- HTTP首次夹具修正：错误密码实际403，原测试误期望401；模拟App恢复前必须关闭原私有目录排他锁。修正测试后HTTP/ACK两项通过8.541s，未改变产品权限。
- 首轮完整PG249.751s仅旧`TestPostgresConversationUnknownCommitReplaysWholeTurn`失败：C1新增只读准入事务使旧“下一COMMIT”注入点命中读事务。定向复现1.467s；改为真实`studio_chat_contexts`插入屏障后通过1.740s、连续3次4.257s。未放宽生产提交条件，重启前明确验证真实turn已提交、原ID/QUEUED存在且Provider未调用。
- 第二轮完整PG270.901s仅旧B2媒体持久夹具出现竞争，正在定位并补精确同步；不将失败轮次标为通过。
- 初轮根`npm run check`等价隔离入口完成Go vet/普通测试9.029s、291前端/27文件、8治理、TS与构建；当时061/Wiki尚未写完，末尾3个文档链接失败。文档落盘后单独`node scripts/check-docs.mjs`通过131份文档/62台账。后续修复页面初读竞争后另行留最终证据。
- 016→017升级前停止本地预览并联备：PG custom dump228311字节、SHA256核对、`pg_restore -l`检查，以及9个参考私有文件/13个资产私有文件。升级后14资产、最高资产修订10、草稿修订5、会话节点数2/4/12、12个run、21条journal、10条reservation及原角色/工单/收据均一致；新control/appeal为0，没有自动授权。比对后才在停机CLI按预演摘要明确授予既有本地独立运营验收员moderator，仅用于QA作用域。
- 本地页面已真实走通限制v2→用户申请→运营预演影响1申请→当前密码确认解除v3→公开答复；手机390宽无横溢。未提交真实AI/邮件/支付请求。

## 风险与限制

C1不等于账户封禁、内容重审或自动额度风控。当前单控制写者不变；未调用真实审核/AI/邮件/支付，未对公网发布。

## 回滚方式

017升级前联备PG、私有目录/owner及启用邮件的队列密钥；回退必须匹配完整联备，不删迁移/策略/决定/owner绕过旧写者栅栏。

## 文档同步

根维护README、Wiki索引、AGENTS、任务源及生成TODO、059—062台账索引；验证齐备才勾选C1本地子项，保留完整V14进行中。

## 最终联合验收与补充修正

第二轮B2夹具失败已复现并修正：SQL-helper测试手工插入INGESTING任务后，真实后台worker可合法领取并把未完成候选改为REVIEW。显式worker步进诊断复现3.140s；最终只在`media_moderation_test.go`先cancel/join后台，再验证手工事务，补PENDINGv1断言，不放宽门禁。定向2.050s、连续5次12.149s通过。该文件虽属前批尚未提交的文件，本轮修改单独登记于062，不改写055历史。

浏览器发现StrictMode首读被旧reading锁挡住，用户与运营页面改为有归属的read-attempt/AbortController。新增6项真实ReactDOM测试先4失败后全通过，包含旧成功/401与重登原键请求交错；具体代码与jsdom29.1.1开发依赖见061。当前Node22.18兼容，生产bundle不包含该依赖。

最终根隔离入口`npm run check`等价执行通过：Go vet、普通studio11.799s、297前端/28文件3.45s、8治理、TS及Vite2.63s；首页JS494.93kB/gzip156.88kB，无500kB警告。最终`npm run test:db`等价全量通过studio282.020s；此前两次失败及修正原因如上保留。

最终六套隔离smoke全部通过，即使父环境启用无效真实邮件/审核/权益配置也不会继承；最终server.exe与5份JSON及perf SHA一致。OpenAPI71路径/83操作/95模型/411引用、重复键/悬空引用/动作正反例检查通过。真实PGACK专项为App重启恢复，不伪称本批OS Kill或真实供应商验收。

CUA独立运营账号限制v2、普通用户申请、预演影响1申请、密码确认解除v3、用户重登刷新读回LIFTEDv2公开答复、手动新demo图片完成并结算0.2演示点；旧隔离作品仍受限，普通账号无运营授权。1440×1000和390×844无横溢，最终console warning/error为0。截图及JSON见[浏览器证据](../wiki/assets/generation-controls-browser-v0.15-2026-11-14.json)，完整命令/数字见[验证汇总](../wiki/assets/generation-controls-validation-v0.15-2026-11-14.json)。

C1本地子项依据上述证据完成；完整V14仍in_progress、11/23整批完成，C2/C3与真实渠道另验。README、Wiki、ADR0013、AGENTS、OpenAPI、059—062索引及任务源/TODO同步。未做生产恢复演练、Linux race远程执行、真实收费或公网发布。

最终治理：`npm run todo:sync`、`npm run check:todo`、`npm run check:docs`、`npm run check:changes`及`git diff --check`均通过；131份文档/62份台账，C1子项通过，完整批次数仍11/23。最终本地预览按当前代码重启，保持demo、邮件关闭及回环访问。
