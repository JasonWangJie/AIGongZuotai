# 2026-11-22-157 本人订单意向与取消收据

## 目的

让已有账户保存自己对指定商品的购买意向，查明当时见到的标价及拟授予的创作点，并可在没有收款渠道时主动取消。网络断开后可凭原操作键核对事务是否实际提交。

## 范围

V13-A 的本人订单记录，涉及 H01 账户隔离和 H05 账务与支付。033 保存真实用户提交的意向及操作收据。状态限 PENDING（等待收款渠道）和 CANCELLED（本人已取消）；款项与 CREDIT 均无资金或权益流水。商品文件、HTTP 契约、工作台与浏览器验证见同批台账。

## 实际文件

- `db/migrations/033_order_intents.sql`
- `internal/studio/order_intent_store.go`
- `internal/studio/order_intent_store_test.go`
- `internal/studio/order_intent_store_transactions_test.go`
- `docs/changes/2026-11-22-157-order-intent-store.md`

## 接口与数据影响

- 新增 `studio_order_intents` 和 `studio_order_intent_receipts`。本人每笔订单属于提交时的工作区，创建时冻结商品 ID／名称、目录版本／摘要、商品摘要、币种、小数位、货币最小单位金额及六位 CREDIT 权益数量。两个数量独立以十进制整数字符串对外，PG 各自使用整数，货币最小单位 1 至 10¹²，权益微单位 1 至 10¹⁵。商品快照来自服务器已校验目录。
- 创建只接受商品 ID、目录摘要和商品摘要。每次操作在事务内重新核验账户状态、凭据和当前会话；先查工作区、本人、操作及原键 SHA-256，再检验目录和容量。相同键不同输入冲突。取消绑定订单 ID 与预期版本，必须是本人当前工作区的 PENDING 版本 1；状态与取消收据一并提交为版本 2。原创建收据保留版本 1，原取消收据保留版本 2。当前详情单独读订单现态。目录变价、关闭或重启后仍可读旧收据和取消待处理意向。
- 每个真实账户最多持有 32 笔 PENDING 意向；容量跨工作区计算，仅返回是否能受理。稳定用户行先于并发容量与订单操作加锁，同一事务通过待处理部分索引最多读取 32 条。历史无累计笔数上限；取消即释放待处理名额。列表按本人及当前工作区用时间、订单 ID 稳定翻页，每页最多 50 条。详情和收据均要求两个作用域条件。
- 033 禁止改删历史及商品信息，只允许对原 PENDING 意向进行一次取消。创建及取消各最多一份不可变收据。此增量不更改任何早期迁移、旧协作授权、草稿或 V12 权益表；订单读取及状态变化不会创建钱包、journal、posting 或模型任务。

## 验证结果

集成人对冻结的 033 运行 15 项订单与个人资料专项，共 62.099 秒。两账户 Chromium 7.58 秒，关闭目录后的新库恢复 7.53 秒；真实数据库中未产生权益分录或媒体任务。

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./internal/studio -run '^TestOrder(IntentHTTPInputAndDisabledCapabilities\|CatalogRequiresExplicitPersistentAccountConfiguration\|CatalogValidatesPricesAndFreezesCanonicalDigests)$' -count=1` | 通过，最终复跑 0.260 秒 | 本地非 PG 商品文件、摘要、数值范围、严格 HTTP 输入及禁用能力。数据库专项同时参与编译。 |
| `TestPostgresOrderIntentPrivateSessionsAndStableHistory` | 真实 PostgreSQL 通过，3.76 秒 | 同工作区不同用户与同账户不同工作区均无权读取、取消或使用游标；同原键跨作用域独立，旧创建快照和当前取消版本分别可查。 |
| `TestPostgresOrderIntentCapacityConcurrentCreateAndCancel` | 真实 PostgreSQL 通过，5.75 秒 | 260 笔历史订单不占待处理名额。两个独立连接争夺最后一位，本人跨工作区登录仍受同一上限约束且看不到其他工作区记录。并发取消只有唯一胜方，成功取消后可继续创建，原键满额可读，权益和生成记录保持原值。 |
| `TestPostgresOrderIntentLostCreateAndCancelAcknowledgements` | 真实 PostgreSQL 通过，5.56 秒 | 创建、取消两个子项各定向丢弃实际 COMMIT 确认，从无商品目录的新工作台连接只读原键和详情，检查唯一订单、对应收据及账务。 |
| `TestPostgresOrderIntentUpgrade032PreservesMemberAndDraftEvidence` | 真实 PostgreSQL 通过，1.80 秒 | 使用原 032 迁移，先创建 ACTIVE 协作成员、邀约与接受回执、不同作者草稿及原键，升级后核对原数据与空订单表，并验证旧写者拒绝 033。 |

## 风险与限制

无商户配置、真实付款、退款、入账、支付回调或对外收费。此处的创建和取消只证明本人记录了相应意向。未来支付必须另行核查商户身份、交易、金额与币种、退款及对账证据，不能根据这些原意向追认已收款或发放 CREDIT。

## 回滚方式

先停写、保存 033 数据库及原私有目录／身份、有关对象和已启用的密钥。若需要返回 032，使用升级前联备与匹配的旧程序在独立位置恢复，核对备份时点以后发生的真实订单意向、取消收据与其他业务写入。保留 033 副本用于对账，禁止删除迁移标记来启动旧写者。

## 文档同步

集成人统一更新 README、Wiki、台账索引、任务源、生成的 TODO 与进度记录，分别登记本人订单意向及仍待外部条件的商户支付退款验收。
