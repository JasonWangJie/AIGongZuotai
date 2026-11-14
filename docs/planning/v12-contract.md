# V12 权益报价与账本 HTTP 契约

本批实现内部 CREDIT 权益，不是现金、支付订单或供应商发票。默认关闭；启用必须有 PostgreSQL、账户模式和匹配 Provider 模式的价格配置。真实网关 usage 与商户渠道仍需后续受控验收。详见 [Provider 用量证据](../wiki/chat-usage.md)。

## 能力与金额

`GET /api/v1/bootstrap` 增加 `billing`：`enabled`、`mode`（`disabled`/`credits`）、`unit:"CREDIT"`、`scale:6`，启用时有 `pricing_version`，停用时有 `reason:"billing_disabled"`。既有生成请求增加可选 `quote_id`；启用计费时新受理必须有匹配报价。历史已受理请求原键重放优先，不要求更换报价或改变原 body。

所有 `*_micros` 金额为十进制 JSON 字符串，不能用浮点数存储或运算。`1 CREDIT = 1,000,000 micros`；CREDIT不代表人民币汇率。0费用、未提供用量和待核实是不同状态。

## 报价

`POST /api/v1/billing/quotes`，application/json，正文上限64KiB。需要登录；关闭时409 `BILLING_DISABLED`。必须提供 `resource_type` 且与唯一请求分支匹配，禁止未知/重复字段，内部输入不能包含 `quote_id`（包括空字符串）。

```json
{"resource_type":"run","run_input":{"project_id":"prj_…","model_id":"studio-chat","model_version":"…","input":"…"}}
```

```json
{"resource_type":"task","task_input":{"project_id":"prj_…","kind":"image","model_id":"studio-image","model_version":"…","operation":"generate","inputs":{"prompt":"…"},"parameters":{"count":1,"aspect_ratio":"1:1"}}}
```

```json
{"resource_type":"conversation_run","conversation_id":"conv_…","conversation_input":{"expected_version":1,"model_id":"studio-chat","model_version":"…","operation":"reply","input":"…"}}
```

会话请求使用当前 `X-Project-ID` 作用域；其余请求的 project_id 必须属于当前工作区，显式项目header/query与body冲突时400。会话的父消息、编辑节点、版本、完整轮上下文预算按真正受理链路预检。媒体校验完整规格、数量和参考素材项目权限；报价不构造multipart、不调用供应商、不创建Run/Task/消息、不冻结钱包。

成功201返回：

| 字段 | 语义 |
| --- | --- |
| `id` | 报价ID；随后放入原请求的 `quote_id` |
| `workspace_id` / `project_id` | 归属 |
| `resource_type` | 实际资源 `run`/`task`；会话报价也是run，同时有 `conversation_id` |
| `model_id` / `model_version` / `price_version` | 冻结的公开模型与价格版本 |
| `provider_mode` | `demo`/`live`，禁止跨模式消费 |
| `unit` / `scale` | CREDIT / 6 |
| `reserve_micros` | 最大预留金额字符串；不是已经扣费 |
| `created_at` / `expires_at` | DB时间，10分钟有效 |
| `basis` / `policy_version` | `chat_tokens`/`delivered_artifact` 与冻结政策版本 |
| `input_per_million_micros` / `output_per_million_micros` / `artifact_micros` | 整数字符串费率，未适用项为0 |
| `max_input_tokens` / `max_output_tokens` / `requested_units` | 冻结预算/请求产物数量，整数 |

报价绑定去掉quote_id后的原始typed input哈希，含会话ID/父节点/模型版本/完整参数/引用顺序。报价后修改输入须重新报价。每工作区最多64个未消费且有效报价，全局10000；429 `QUOTE_LIMIT`，`Retry-After:60`。报价接口不要求幂等键；响应丢失可重新报价，但前一报价保留并占限额至过期。

受理时将报价消费、钱包预留、生成资源、原请求幂等及业务事件同事务保存。报价过期、已使用、参数/归属不符分别409 `QUOTE_EXPIRED`/`QUOTE_USED`/`QUOTE_MISMATCH`；余额不足402 `INSUFFICIENT_CREDITS`且未受理。模型或会话变化仍按已有422/409契约拒绝。执行响应不明必须保留原body/quote_id/Idempotency-Key；不能借重新报价换键重复生成。

## 钱包与只读分页

`GET /api/v1/billing/wallet` 只返回当前工作区：workspace_id、unit、scale、available_micros、reserved_micros、review_micros、charged_micros、refunded_micros、version、updated_at。未建立钱包时返回零余额、version=0、updated_at=null，不自动赠送额度；review是reserved中待核实部分，不应再相加。

`GET /api/v1/billing/entries` 与 `GET /api/v1/billing/reservations` 返回 `{ "items": [], "next_cursor": null }`，有后续页时next_cursor为不透明字符串。参数：limit默认20、1—50；cursor最多1024字符；可选project_id。省略project_id表示整个当前工作区，单独X-Project-ID不会把账务列表变成项目筛选；提供project_id时仍验证项目归属，且不能与header冲突。

流水可选 `kind=CREDIT|RESERVE|CHARGE|RELEASE|REFUND|REFUND_RELEASE`；预留单可选 `state=HELD|REVIEW|SETTLED|RELEASED|REFUNDED`。游标绑定当前账户、工作区、项目筛选、页面类型和过滤值，使用created_at/id倒序键集，不提供无限列表。错误400 `INVALID_LIMIT`/`INVALID_BILLING_FILTER`/`INVALID_BILLING_CURSOR`；项目越权404。所有查询SQL再次限定工作区，不依赖游标作为权限凭证。

流水字段：id、kind、可选project_id/resource_type/resource_id/reservation_id/quote_id/reversal_of、amount_micros、available_delta_micros、reserved_delta_micros、unit、scale、created_at。预留单字段：reservation_id、quote_id、price_version、state、reserved/charged/released/refunded_micros、unit、scale、version、可选review_reason，以及project_id、resource_type、resource_id、model_id、created_at、updated_at。不公开提示词、usage原文、私有模型映射、请求哈希、密钥或运营证据正文。

## 结算与失败

LLM只有业务COMPLETED、完整DONE和合法一致usage、来源与Provider模式相符且未超冻结预算时自动结算。演示固定synthetic计量明确标为demo；不能按字符估算实际token收费。缺失、取消、断流、冲突、过量和提交不明进入REVIEW并保留预留。执行前有持久未开始证据才可释放。

媒体采用平台冻结SKU的安全导入产物交付价；明确PARTIAL交付按实际产物收费并释放差额，未知/缺少可靠交付证据保留核实，不把供应商quota/cost当平台币种或账单。用户侧无赠额/退款/核销写接口；运营操作由有审计和幂等的离线CLI执行。支付和自动充值不在V12范围。

人工核实是原冻结单的最终决定：已SETTLED/RELEASED/REFUNDED不因后续结果变化重新冻结或二次扣费。停放媒体如果保有原handle，人工结账后仍可显式核对并找回成果；该恢复不再收费，这是本批明确的产品政策。运营必须先核实证据再决定释放或收费，不能将“未查到结果”直接当作供应商未执行。仍会自动执行/轮询的活动任务不能人工核销。

## 本批验证

两个普通HTTP辅助测试覆盖严格报价输入、关闭能力和游标边界；与六个usage函数联合运行0.884秒通过，`go vet ./internal/studio`通过。两个真实PostgreSQL HTTP函数3.058秒通过：报价哈希与无业务副作用、会话CAS、参考素材/项目权限、钱包零读、账务keyset分页、跨账户/工作区/筛选拒绝及预留单读取脱敏。测试使用隔离schema、演示Provider与本地价格文件，没有真实收费调用。示例价格必须与 `config/models.example.json` 的精确preset ID配套使用；默认兼容目录使用不同preset ID，不能混配后声称媒体价格已配置。

实现与HTTP隔离测试归集成台账046；Provider新增解析与边界归[044](../changes/2026-11-13-044-provider-usage.md)。正式OpenAPI由集成人同步；本页不是生产验收声明。
