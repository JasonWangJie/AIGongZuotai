# Provider 用量证据与结算边界

V12 / H03、H05、H06。本页说明 Provider 能提供的计量证据，不把它当成供应商发票或成本对账单。账本按冻结的平台价格和事务政策决定冻结、结算、释放或待核实；[多轮上下文预算](chat-provider.md)仍只是受理估算，不能替代实际 token 用量。

## LLM 接口与兼容

新增可选 `ChatUsageProvider` 接口：

```go
StreamMessagesUsage(ctx context.Context, model string, request ChatRequest,
    emit func(string) error) (ChatUsage, error)
```

它使用原 `ChatRequest` 中冻结的角色/内容、输出限制和私有模型，不再挑选历史。只有这个新方法添加 `stream_options: {include_usage: true}`；旧 `Stream`、`StreamModel`、`StreamMessages` 的请求字段保持兼容。共用原 HTTP client/Transport，仍禁止重定向、自动改变参数重发和向网关转发本地幂等键。网关不支持 include_usage 时直接返回本次错误，不能改成不含该字段的第二次生成。

返回值为私有结构化证据：

| 字段 | 含义 |
| --- | --- |
| `status` | `reported`、`missing`、`invalid`、`demo` |
| `prompt_tokens` / `completion_tokens` / `total_tokens` | int64，用量来源明确；0与“没有用量”由status区分 |
| `source` | `gateway` 或 `demo`，不接受浏览器指定 |
| `complete` | 是否完整读取 `[DONE]`，或演示流正常结束 |
| `reason` | 固定闭集：`request_not_started`、`stream_incomplete`、`usage_missing`、`usage_invalid`、`usage_conflict`；成功时省略 |

`reported`只代表已配置网关返回的数字通过当前结构校验；不是签名发票、成本核验，也不证明上游确实按这些 token 扣费。不保存上游原始正文、任意错误文本或请求密钥作为公开账务字段。

## 解析规则

三个必要计数必须全部存在，且为 JSON 十进制整数，每个0—1,000,000,000，输入+输出必须精确等于总数。负数、小数、科学计数法、数字字符串、null、缺字段、溢出、重复字段或总和矛盾均不能用于自动结算；不做 float 转换、截断或四舍五入。

usage:null 和未出现usage都不等于0。合法用量可能位于 `choices: []` 的独立尾块。重复完全一致的三个计数只保留一份，不累加；重复冲突进入 invalid，即使后续又出现合法值也不恢复自动结算。额外 `prompt_tokens_details`、`completion_tokens_details` 不作为追加单位：缓存和 reasoning 细项属于总计数的一部分。本批不据这些未接入的细项实现缓存折扣、推理附加价或供应商成本还原。

SSE事件继续使用256KiB上限，支持网络分片、CRLF、多行data。计量对象最多32个顶层字段，整个流只保存固定大小汇总。非法usage本身不会丢弃已有文本或自动重发生成；流可正常结束，但计量保持invalid供账本核实。

## 结束、取消和失败

| 观察结果 | Provider证据 / 账本要求 |
| --- | --- |
| `[DONE]` + 合法一致usage | reported、complete=true；仍须业务Run为COMPLETED、方法err=nil、价格与冻结额度校验通过才候选自动结算 |
| 正常结束但usage缺失/null | missing、usage_missing、complete=true；进入待核实，不能按0token结算或释放 |
| 正常结束但usage非法/冲突 | invalid、对应固定reason、complete=true；待核实 |
| 先收到usage，随后断流/取消/emit失败 | missing、stream_incomplete、complete=false；可保留已观察数字作为私有核实线索，不能自动结算 |
| 400/429/5xx、连接或协议错误 | 没有完整usage；不能仅凭HTTP状态推断供应商未收费，也不自动fallback重发 |
| `finish_reason=length` 后完整usage和DONE | 可保留为完整reported证据；用户输出截断与计量完整性分别处理 |
| 执行前本地校验拒绝 | request_not_started；是否可释放由核心持久“未启动”证据决定，不以错误文案推断 |

收到usage不等于收到DONE；收到DONE不等于收到usage。浏览器断线不取消上游已有执行，显式取消则保留账务未知的可能性。已取消/失败的业务状态不能被迟到reported覆盖为自动结算。

演示Provider固定返回128输入+64输出=192 synthetic token单位，与内容长短完全无关，`source=demo,status=demo`。取消演示流仍missing/complete=false。这只是可重复的测试计量，不是字符估算、tokenizer或真实AI账单。

## 媒体网关能证明什么

2026-11-13再次只读访问[用户网关官方文档](https://api.aiimg.lol/async-image-api-docs)，公开SPA对应文档脚本为 `static/js/async/6168.9dd1da9042.js`，未带密钥、未调用生成接口。

文档6.1定义图片响应的 `quota`、视频的 `quota`/`cost` 为已知费用，示例包括图片 `billing_status=succeeded`、视频 `billing_status=settled`；未给出完整计费单位、币种、精度、汇率、倍率、退款或结算修订契约。因此本批不把这些字段转换为平台收费，不从示例推算倍率，也不把failed或HTTP错误直接等价于“未扣费”。当前媒体适配器仅用billing/storage状态识别需要核对的异常。

平台依据已冻结的模型/规格SKU，按真正导入私有存储并同事务注册的产物数量执行交付价格政策；这是平台权益收费，不是供应商成本转抄。明确PARTIAL按已交付数量收费并释放差额；缺少产物、过期、608执行未知、609后处理失败保留核实。请求 `n`、`seconds/duration`、进度或上游URL数量本身不证明已经交付。尤其视频秒数是请求参数，现有容器头检查不等于测得实际可播放时长，不能宣称按实测秒计费。

文档6.2要求completed+succeeded且data非空才判结果可用，过期结果仍可能保留succeeded。产物已经在本平台安全导入后，可按冻结政策记录交付证据；供应商后续URL过期不能反向擦除已有交付历史。后续供应商成本对账需要独立取得单位、结算ID、退款/更正及查询时效等完整契约。

## 来源与验证

[OpenAI Chat Completions流事件官方文档](https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events)说明include_usage的尾块以及取消/中断可能收不到它；[创建接口](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)定义请求开关。这里只将它们用作兼容协议依据，不保证二次开发New API实现所有字段。

验证只使用本机HTTP fixture：六个usage函数及子例0.854秒通过，覆盖严格整数/上限/总和、null和缺失、相同重复/冲突、分片、多种流终止、取消、共享Transport、旧协议保持和不回退重发；23个Provider/目录相关函数1.224秒通过，vet通过。没有真实收费调用。具体命令、结果和限制见[044台账](../changes/2026-11-13-044-provider-usage.md)，报价及只读账务契约见[V12接口说明](../planning/v12-contract.md)。
