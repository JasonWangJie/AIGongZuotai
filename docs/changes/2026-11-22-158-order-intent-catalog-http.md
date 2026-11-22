# 158 · 订购意向商品目录与本人记录接口

## 目的

账户用户可核对商品标价和对应的创作权益数量，再显式登记或取消订购意向。目录调整后，用户仍可查看当时确认的商品与价格。

## 范围

V13-A 涉及 H01、H05，工作台入口归 H08。此项提供本机文件目录、商品快照与本人查询接口。157 提供持久记录及操作收据。本阶段意向状态为待处理和已取消，未接入商户、支付回调、实际退款或权益发放。V11 与完整 V13 的外部验收仍单列。

## 实际文件

- `internal/studio/order_catalog.go` 加载订单目录，校验商品、最小货币单位与六位 CREDIT，生成商品及目录摘要。
- `config/order-catalog.example.json` 给出单个 CNY 商品的本机目录示例。
- `internal/studio/order_intent_http.go` 提供能力、商品、创建、取消、按原键读收据及本人列表和详情。
- `internal/studio/order_catalog_test.go`、`internal/studio/order_intent_http_test.go` 覆盖文件、数据类型、摘要、协议字段和变价、关闭目录后的持久记录。

## 接口与数据影响

显式设置 `ORDER_CATALOG_FILE`，同时配置 PostgreSQL 和 `AUTH_MODE=required`，启动时加载本地普通文件。目录上限 64 KiB，商品 1 至 32 个，商品 ID 唯一。货币小数位必须显式声明。支持 CNY、USD、EUR、HKD、GBP 的两位、JPY 的零位、KWD 的三位。标价以最小货币单位的正整数字符串提供，最高 10¹²；权益独立以 CREDIT 的百万分之一计，最高 10¹⁵。输入拒绝浮点、前导零、重复或额外字段、未支持的币种及小数位错配。

目录按商品 ID 排序后对规范化字段计算 SHA-256。商品内容另有 SHA-256，操作人员复用版本标签但更改价格仍会触发新摘要。GET `/api/v1/order-intents/catalog` 返回能力、商品和摘要。POST `/api/v1/order-intents` 提交 `product_id`、`catalog_digest`、`product_digest` 及原键，仅服务端商品可定价。当前商品不符返回 409，用户应核对最新价格。旧键优先回放原创建收据。

GET 同基址分页读本人记录，GET `/{order}` 读当前状态。POST `/{order}/cancel` 携带 `expected_version` 及独立的操作原键。响应未知时，GET `/receipt?operation=create|cancel` 携带原 `Idempotency-Key`，返回对应操作提交时的订单快照。关闭目录后，持久账户仍可查询、取消原意向并读回原收据。

## 验证结果

集成人对冻结的 033 运行 15 项订单与个人资料专项，共 62.099 秒。两账户 Chromium 7.58 秒，关闭目录后的新库恢复 7.53 秒；真实数据库中未产生权益分录或媒体任务。

`go test -count=1 ./internal/studio -run '^Test(OrderCatalog|OrderIntentHTTP)'` 本地通过，0.257 秒。三项检查目录默认关闭、目录启用前置条件、精度上下界、超长或非法字段、规范摘要及变价识别、内存工作台能力和客户端无法提交自定金额。

真实 PostgreSQL/HTTP 复跑用例为 `go test -p 1 -count=1 -v ./internal/studio -run '^TestPostgresOrderIntentHTTPFrozenCatalogAndDisabledHistory$'`。检查原目录与变价目录的两次启动、关闭目录后的本人取消与原键快照、跨账户隔离及权益表无写入。编写期间未连接 PostgreSQL，实测由集成人串行登记。

## 风险与限制

目录是启动配置，变更须重启。商品标价及对应权益数量仅描述意向内容。资金和权益的完成依据、商户凭据、回调验签及真实支付退款须在后续批次独立验收。

## 回滚方式

清除 `ORDER_CATALOG_FILE` 并按本机停写流程重启，停止接受新意向，保留原订单和操作收据。需要恢复至 032 时，使用升级前的数据库、软件及私有目录配套备份。

## 文档同步

集成人同步启动配置、README、Wiki、OpenAPI、台账索引和 V13 任务记录。完整 V13 的状态仍以任务源及实际商户验收为准。
