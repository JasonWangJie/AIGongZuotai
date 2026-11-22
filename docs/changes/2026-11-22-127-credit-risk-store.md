# 127 · V14-C3 收费受理的自动风控与单次许可

## 目的

在每次新收费受理时核对工作区剩余冻结空间和当前自然分钟的受理次数。实际执行不明的额度继续计入风险。用户取得原报价的独立复核许可后，可显式提交原业务并核对正式冻结收据。

## 范围

2026-11-22，关联 V14、H05 账务与支付、H06 审核与运营和 H07 可靠性与性能。普通 Run、多轮会话、媒体 Task 和工作流收费子节点使用同一冻结入口。策略缺省关闭。策略管理、复核运营入口、工作台及集成验收由同批相邻台账记录。

## 实际文件

- [028 增量](../../db/migrations/028_credit_risk.sql) 增加按工作区追加的不可变策略版本、精确操作收据、原报价申请人、复核申请/原键/预演/收据、风险复核角色及一次许可。027 原报价保留原字段和可用期限，缺身份的原报价沿用原受理语义。
- [额度风控](../../internal/studio/credit_risk.go)、[账务事务](../../internal/studio/ledger_transactions.go)、[账务表示](../../internal/studio/ledger.go) 读取真实钱包、窗口及报价，按相同证据评估新受理与申请资格。报价清理保留申请引用的历史报价。
- [报价 HTTP](../../internal/studio/billing_http.go)、[受理写入](../../internal/studio/persistence.go) 和[多轮对话](../../internal/studio/conversation.go) 传递服务端认证主体。客户端提供的 ID 不能指定许可使用人。
- [存储专项](../../internal/studio/credit_risk_store_test.go) 验证并发、UTC 窗口、待核实冻结、精确许可原子性以及锁等待跨过报价到期。[旧账务用例](../../internal/studio/ledger_test.go) 适配内部受理签名。

## 接口与数据影响

启用策略的上限为正数，`max_held_micros` 采用 CREDIT 六位整数，`max_per_minute` 最大 1000。超过任一规则返回 429 和 `CREDIT_RISK_HELD_LIMIT` 或 `CREDIT_RISK_RATE_LIMIT`。达到冻结上限的最后一笔可以受理。自然分钟按 UTC 计算，启用前同窗已经受理的付费业务也计入次数。冻结比较和频率探测位于钱包行锁内，报价到期、窗口位置和正式冻结的 `created_at` 共用一次锁后的数据库时钟。历史分录、原价、可用余额与硬性账本上限沿用 012 语义。

许可匹配申请人、工作区、项目、原报价、完整输入摘要、价格版本、冻结数量、当前策略修订、原截止时间和已批准的触发原因。批准后业务仍须显式受理。新受理与许可核销、原报价、正式冻结、分录及原业务一起提交；回滚后原许可保留。后续调整策略、换报价或新增未经复核的触发规则，都需按当前条件处理。已经确认的原业务原键沿原路径读取。

## 验证结果

- `go test ./internal/studio -run '^(TestCreditRiskPolicyCommandBounds|TestLedgerIntegerArithmeticAndHashCompatibility)$' -count=1 -v` 通过，包耗时 0.191 秒。覆盖 CLI 上限和原账务整数/摘要兼容。
- 集成人真实 PG 首轮 `TestPostgresCreditRiskConcurrentWindowAndReviewBalance` 1.71 秒、`TestPostgresCreditRiskQuoteExpiresWaitingForWallet` 2.96 秒通过。并发测试使用五个独立连接争夺最后一格，校验真实执行后仍在 REVIEW 的冻结、恰好等于上限、受理次数跨 UTC 边界、拒绝无消费与账本核对。真实 HTTP 原报价单次使用与回滚也通过。见 `.runtime/v14c3-first-pg.log`。
- 首轮 `TestPostgresCreditRiskPermitMatchesExactOwnerQuoteAndRollsBack` 在试具注入回滚后触发控制连接的数据库故障保护，后续成功用例因此返回 503。改用独立测试连接注入事务回滚，保留运行服务的故障保护。
- 集成人修正后组合专项全部通过，包用时 48.091 秒。精确许可与回滚 2.40 秒，五连接冻结/频率竞争 3.10 秒，钱包锁等待使报价过期 2.78 秒，浏览器核对原冻结 15.32 秒，申请与审批丢失 COMMIT 回执后的只读核实 6.49 秒，升级和离线门禁也通过。记录见 `.runtime/v14c3-second-focused.log`。

## 风险与限制

每工作区钱包的控制行串行化收费受理；窗口探测复用既有 `(workspace_id,created_at,id)` 索引与最多 1000 笔的阈值，生产峰值和跨实例容量仍需实测。旧报价没有可靠申请人时不能出具个人豁免。批准范围仅及有效期内的本次内部 CREDIT 报价。真实审核渠道、真实支付与外部退款单独验收。

## 回滚方式

停机恢复到升级前同一 027 数据库、对应私有目录、密钥和对象版本，再使用配套 027 程序。切换前核对新增收费收据、授权、策略和申诉，保留已发生的账户与冻结变更记录。

## 文档同步

集成人更新[根 README](../../README.md)、[Wiki](../wiki/README.md)、[台账索引](README.md)、[任务源](../planning/tasks.json)、生成的 TODO 和[推进记录](../planning/progress.md)。
