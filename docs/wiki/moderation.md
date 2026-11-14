# 文本审核与输出隔离

本页对应V14-B1文本纵向子批次。当前默认关闭，范围为新受理的文本生成；媒体生成前后审核属于B2，申诉、人工放行、封禁及风控属于后续子批次。它不代表完整V14或真实审核渠道已经验收。

## 模式和配置

| 变量 | 用途 |
| --- | --- |
| `MODERATION_MODE` | `disabled`（默认）、`rules`或`http` |
| `MODERATION_POLICY_FILE` | 只在启动时读取的服务端政策JSON，最多64KiB |
| `MODERATION_ENDPOINT` | `http`模式的完整服务端审核端点，不从浏览器接收，不存入政策表 |
| `MODERATION_API_KEY` | 独立Bearer密钥，16—2048字节可见ASCII，不含空白，不落库/日志/浏览器 |
| `MODERATION_CREDENTIAL_ID` | 非秘密的稳定凭据标签，最多64个安全字符 |

HTTP端点必须HTTPS，不接受userinfo、查询参数或fragment。只有demo fixture允许HTTP且主机必须为loopback IP字面量；普通域名和`localhost`不属于HTTP例外。出站不使用环境代理、不跟随redirect、不保存Cookie、不自动重试；整个核验最多10秒、最多4路并发，响应正文最多8KiB。无法确定结果时拒绝放行，不回显适配器原始错误、URL或正文。

`rules`只允许`PROVIDER_MODE=demo`。它是有限、区分大小写的字面包含规则，不具备语义、图像或视频审核能力，不能用于真实渠道上线声明。启用文本审核后，新媒体生成明确拒绝；旧任务、旧文本和既有参考图/资产不追溯改为已审核或待审。关闭入口或模式不会把本批既有隔离结果自动变为可见。

本地演示配置：

```powershell
$env:PROVIDER_MODE='demo'
$env:MODERATION_MODE='rules'
$env:MODERATION_POLICY_FILE='config/moderation.example.json'
npm run dev:db
```

示例中的四个marker只用于验证完整链路：

| 输入包含 | 预期 |
| --- | --- |
| `[XINGTU_INPUT_BLOCK]` | 输入BLOCK，未调用生成、未创建冻结 |
| `[XINGTU_INPUT_REVIEW]` | 输入REVIEW，未调用生成 |
| `[XINGTU_OUTPUT_BLOCK]` | 输入通过；demo回复回显marker后输出BLOCK，候选正文不公开 |
| `[XINGTU_OUTPUT_REVIEW]` | 输入通过；demo回复回显marker后输出REVIEW，候选正文不公开 |

不含marker的普通演示文本通过有限规则。block优先于review；规则的phase可为input、output或both。匹配规则最多64项，每项contains为1—256 UTF-8字节且无NUL；规则只接受block/review，未匹配才返回allow。

政策文件要求全部字段：`schema_version:1`、`policy_id`、`policy_version`、`max_input_bytes`、`max_output_bytes`、`rules`。ID/版本为1—64个安全ASCII字符。最大输入为完整messages正文合计256KiB，最大输出为128KiB，可通过政策进一步收紧；不截断文本后冒充完整审核。禁止未知字段、大小写别名、重复键、非法UTF-8和错误类型。HTTP政策的rules必须为空数组；具体语义审核由适配器按指定政策实现。

## 受理与冻结政策

输入校验针对服务端实际发送给模型的完整`ChatRequest.Messages`，包括选中的历史user/assistant消息，不只检查最后一句。原幂等重放优先于当前政策；新请求的输入审核在全局状态锁之外执行，返回后重新核对原键、作用域、准备后的上下文与输入摘要，再进入受理事务。

新受理Run保存安全公开summary和完整非秘密政策快照。快照包含策略ID/版本、规则、字节限制、运行模式、text-v1契约和适配器身份散列；不含endpoint、API密钥、提示词或候选输出。适配器身份散列绑定目标、凭据标签和密钥身份。政策内容另有SHA256摘要，不把人工版本号相同当作规则相同。

后续输出使用原快照。更新政策文件仅影响新受理；rules旧任务按原规则判断，HTTP请求携带原政策及摘要。适配器身份变化、启用模式改变或服务不可用会返回REVIEW，不使用新配置猜测原输出是否通过。当前非终态文本恢复会丢弃未公开候选并停止自动重生成，旧政策留作核实，不恢复一次不明的模型提交。

## 私有text-v1适配契约

这是本平台定义的接口，不是假定任何厂商原生API已经兼容。真实服务需单独适配、授权、验收；本批只调用本机fixture。

请求为POST JSON，HTTP头为`Authorization: Bearer <私有密钥>`。所有请求具有：

| 字段 | 含义 |
| --- | --- |
| `contract` | 固定`text-v1` |
| `request_id` | 每次审核的随机`mreq_`标识 |
| `policy_id`、`policy_version`、`policy_digest` | 原冻结政策身份与摘要 |
| `phase` | `input`或`output` |
| `content_digest` | 本次明确内容结构的SHA256绑定值 |
| `messages` | input时为实际有序user/assistant文本数组；output不携带 |
| `text` | output时为完整候选文本；input不携带 |

内容摘要由Go JSON编码的`{messages: [...]}`或`{text: ...}`计算，字段顺序与编码以`moderation_http.go`为契约实现。适配器必须审核收到的完整内容并原样回传绑定字段；不需要通过额外URL获取用户内容。不会发送用户/工作区ID、模型网关密钥、供应商地址或其他请求的正文。

成功响应必须HTTP 200，Content-Type为application/json，可带UTF-8 charset。响应必须**恰好**包含请求的前7个绑定字段，再加`decision`和`reason_code`；不得返回messages/text、解释正文或未知字段：

| decision | reason_code | 平台结果 |
| --- | --- | --- |
| `allow` | `allowed` | ALLOWED |
| `block` | `policy_block` | BLOCKED |
| `review` | `policy_review` | REVIEW |

request_id、政策ID/版本/摘要、phase、content_digest任何一项不匹配均REVIEW。未知decision、任意reason文本、重复键、大小写别名、尾随JSON、非JSON内容类型、超限、网络错误及超时均不能得到ALLOWED。审核请求不会自动重试；用户后续重试输入校验也不授权重发已受理生成。

## 输出可见性与事务

启用审核的新文本生成采用`after_review`交付。未审核候选只在当前执行内存，不逐段发送给浏览器，不写入Run输出、assistant消息、历史、快照、日志或Outbox。只有summary为ALLOWED且Run为COMPLETED才公开正文；仅有其中一项不足以放行。

公开summary字段为`policy_id/policy_version/phase/state/version/reason_code?`，phase固定output，state仅PENDING、ALLOWED、BLOCKED、REVIEW，version为安全整数。PENDING不含原因，ALLOWED仅allowed，BLOCKED仅policy_block。REVIEW使用固定原因：policy_review、check_unavailable、check_protocol、check_timeout、check_busy、invalid_content、content_too_large、policy_unavailable，以及执行恢复的execution_incomplete/process_restarted/cancelled（generation_incomplete保留为同类内部原因）。不把远端错误正文当reason公开。

015增加`studio_run_moderation`和`studio_moderation_decisions`。受理时PENDING/version1与Run、幂等、会话、冻结和Outbox同事务；最终安全结果与Run/assistant正文或隔离终态同事务。政策不可改删，当前结果仅允许PENDING→一个最终状态且版本+1；精确重复结果只读，最终结果不能再覆盖。决定历史禁止修改/删除，无人工放行入口。

候选输出审核BLOCK/REVIEW或生成未完整结束时，输出清空，已执行冻结按账本规则保留REVIEW，不根据“用户看不见输出”假设供应商未执行。取消、进程恢复、提交确认不明与旧响应均不能绕过隔离或触发自动重新生成。财务核实也不等于内容放行。具体主链路验证由本批集成记录汇总。

## 升级、验证和未完成事项

015为新格式；升级前停写联备数据库、私有目录及启用邮件时的必要密钥。旧任务和媒体未追溯审核，数据库不会被批量改写为“审核通过”。当前CLI格式栅栏须随集成升级，不能删迁移登记、审计或owner强退。关闭模式只影响后续受理，不回滚已提交处置；回退使用匹配版本的联备并核对升级后的新增业务。

引擎及HTTP专项结果见[051台账](../changes/2026-11-14-051-text-moderation-engine.md)。本机fixture与规则命中只验证隔离和协议，不证明真实语义审核质量。尚未完成：真实审核渠道选型与联调、媒体输入/产物隔离审核、旧资产追溯扫描、人工审核放行、申诉封禁以及生产风控放行。服务仍为单控制写者。
