# 043 · V12 权益账本核心与同事务结算

日期：2026-11-13。关联V12/H06；根维护任务源、README、索引、配置/CLI和最终联合验收。

## 目的

把生成受理、冻结和结算放入可核对的持久事务，防止重复受理重复扣款、并发透支、未知执行被误当零消费释放，以及已停止运行因迟到usage重新收费。

## 范围

Go账本领域、012迁移、Run/Task/会话受理与终态/恢复事务hooks、固定精度和公共读型、只读核账/离线操作领域函数。报价HTTP和usage适配、价格文件/CLI、UI及联合验证分别由同批其他记录覆盖。无真实支付、外发邮件或供应商收费调用。

## 实际文件

- `internal/studio/ledger.go`：CREDIT/scale、公共金额字符串读型、报价与原请求hash、整数计价。
- `internal/studio/ledger_transactions.go`：报价容量/单次消费、账户锁、不可变分录、冻结/结算/释放/冲正/人工核实及分页读取。
- `internal/studio/ledger_hooks.go`：准入、租约后同事务媒体结算、只读核账。
- `internal/studio/ledger_test.go`：普通整数/hash用例和真实PG事务/并发/未知commit专项。
- `internal/studio/migrations/012_ledger.sql`：7类账本表、索引、不可变触发器与commit时双分录平衡约束；无历史资源追扣。
- `internal/studio/app.go`、`model.go`、`persistence.go`、`conversation.go`：配置、Billing快照、quote_id、可确认提交后换缓存、计量适配调用及恢复。
- `docs/wiki/ledger.md`及本记录。根负责README、Wiki/台账索引、OpenAPI与版本栅栏同步。

## 接口与数据影响

金额int64/PGbigint，公开JSON十进制字符串；CREDIT精度6，上限9,000,000,000,000,000 micros。新增`quote_id,omitempty`保持旧空字段幂等字节兼容，完整原键重放早于报价/余额/目录/CAS检查。报价绑定原输入且消费一次，受理事务同时保存资源、冻结、幂等、审计和Outbox。

Billing状态HELD/REVIEW/SETTLED/RELEASED/REFUNDED与生成状态独立。LLM仅完整合法usage+COMPLETED+冻结预算内自动结算；媒体仅已注册交付数量按冻结平台SKU结算。缺失/未知/已开始失败保持冻结待核实，旧数据不补扣。反向冲正保留原journal，离线操作幂等持久；人工核实限制终态/parked资源，仍在查询的REVIEW不能提前释放。

交付前独立审查发现累计消费达上限、冲正恢复余额后新任务仍可能被受理而终态无法结算。已在钱包锁内新增累计已结算+所有剩余冻结+新冻结容量检查，使用冻结余额投影与差值运算，提前409 `LEDGER_CAPACITY`且不消费报价、不写资源。冲正不清零累计消费，已受理hold保留最大结算空间；原键重放顺序保持。

## 验证结果

- 普通`go test ./internal/studio -run '^TestLedgerInteger' -count=1`通过0.044秒；精确一次向上取整、超界、JSON金额字符串、原请求/quote/conversation hash隔离。
- 真实PG首轮5函数+10状态子例通过4.353秒；包括012真实迁移、结算/冲正/分录不可改、usage缺失/不完整/越界/来源错配、未开始释放、8独立连接竞争余额、事务回滚、部分交付、恢复、64报价限额及旧资源不追扣。
- 补充真实PG同键4连接只授予一次、COMMIT ACK代理实际丢包后的持久冻结/原幂等恢复通过；背压/lease用例修正测试列名`projected_at`后专项通过0.803秒。初轮SQL金额参数被PG判为unknown减unknown，已显式cast bigint并复验。
- `go test ./...`通过，studio7.757秒；该命令未设置TEST_DATABASE_URL，PG用例明确skip，不把它计作数据库全量。`go vet ./...`通过。
- 后续人工核实进行中限制与缺账户投影检测的2个受影响PG函数通过1.668秒：PROVIDER_STATE_UNKNOWN保留REVIEW/自动查询，但人工提前核实拒绝；后续PARTIAL正常结算；缺少零余额reserved账户也能核账检出。核心累计7个PG函数及10个运行状态子例通过。根独立HTTP/CLI/真实进程Kill和最终全量以联合台账为准，不以子模块报告代替。
- 累计容量修正新增真实PG函数的2个边界子例通过1.570秒：合法Max授予→Max消费→全额冲正→新请求提前拒绝且无资源/幂等/分录/报价消费；多个HELD/REVIEW占满尾部空间，取消释放后新受理成功且已接受任务全部结算。随后本模块全部8个PG函数回归通过6.956秒，`go vet ./internal/studio`通过；未触及主开发库。

## 风险与限制

仍为本地单控制写者，共享系统账户锁是当前吞吐边界，不宣称分布式账务或高并发极限成绩。REVIEW可长期占额度，需可信外部证据由离线控制面核实；没有用字符数补usage、后台猜供应商成本或自动退款。已人工释放后后续核对拿到作品也不会再次收费，操作人须审慎结束原冻结。当前支持一次全额冲正，不支持部分退款或支付入账。

## 回滚方式

停服务并恢复升级012前的PG及私有目录联备，邮件启用时同时保全独立队列密钥。恢复独立库/目录后核对owner身份和迁移版本；不能直接删迁移登记或owner marker回退旧exe。已产生权益分录的数据库不能通过删除新表“回滚”而丢失对账证据。

## 文档同步

[账本Wiki](../wiki/ledger.md)解释单位、事务、状态、恢复、人工核实和限制；与[权益配置与离线核账](../wiki/billing-operations.md)、[聊天用量证据](../wiki/chat-usage.md)、[ADR0009](../wiki/decisions/0009-credit-ledger.md)互链。根同步README、Wiki/台账索引、V12状态/证据与OpenAPI。
