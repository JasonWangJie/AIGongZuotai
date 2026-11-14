# 文本多轮 Provider 与上下文预算

V10 的文本适配器接收结构化、已选定的 `user/assistant` 消息数组，通过现有 Chat Completions 兼容端点流式执行。Provider 不读取项目历史、不判断消息归属、不按今天的目录补模型、不自行裁剪上下文；这些由核心受理流程完成。接口和预算实现见 [chat_provider.go](../../internal/studio/chat_provider.go)，本批台账见[036](../changes/2026-11-13-036-chat-provider.md)。

## 内部执行契约

`ChatMessage` 只含 `Role`、`Content`。`ChatRequest` 含 `Messages`、`MaxOutputTokens`、`OutputLimitField`；整份请求是私有执行快照。`StreamMessages(ctx, upstreamModel, request, emit)` 在单次局部请求中使用受理时的模型与消息，不修改共享 client/Transport 或 provider 默认模型。

上游 JSON 只发送 `model`、`messages`、`stream:true` 和选定的一个输出限制字段。每条文本保持原角色、原顺序、Unicode、换行；不能把历史拼成一条 user 字符串，也不能把正文里的 `system:` 等文字提升为角色。平台项目/用户ID、平台幂等键、预算摘要、私有协议字段名均不作为额外请求字段传给网关。

本批仅支持文本 `user/assistant`，从 user 开始、交替排列、以当前 user 结束；最多129条非空UTF-8消息，正文总和至多256KiB。角色、内容或大小无效时在联网前返回安全错误。尚不支持用户自定义system/developer、工具调用、图片/音频消息、供应商对话状态、自动摘要或RAG。

旧 `Stream` / `StreamModel` 保留单轮兼容，仍只发送原单条user且不隐式增加输出预算字段；新的多轮流程使用 `StreamMessages`。demo实现接收并校验相同请求结构，但脚本回复仍以末条user为基础，不能将演示回复视为真实多轮推理的验证。

## 模型配置

目录中的chat模型增加两项公开能力和一项私有协议设置：

| 字段 | 默认 | 校验与含义 |
| --- | --- | --- |
| `capabilities.context_window_tokens` | 32768 | 2048—1048576；运营者配置的模型上下文窗口，不代表供应商已验证能力 |
| `capabilities.max_output_tokens` | 4096 | 128—32768；每次新执行保留并发送的输出上限 |
| `chat_output_limit_field` | `max_tokens` | 模型级私有配置；仅允许 `max_tokens` 或 `max_completion_tokens`，不返回浏览器 |

配置还要求 `max_output_tokens + 272 < context_window_tokens`，为至少一条输入留下空间。`max_prompt_bytes` 仍是单输入字节上限，不能把它当作必须填满的最小窗口；例如2048窗口配128输出和16000单输入上限可以启动，具体长输入由受理时预算检查返回422。

旧目录省略（或以0表示未设置）两个预算数值时补上述默认；只对chat补默认。image/video必须没有chat预算或协议字段。公开目录不含上游模型名或私有字段，协议字段改变会改变该chat模型和目录的opaque版本；其他未改的媒体模型版本不受影响。旧历史能力快照不被当前默认值回填。

示例见 [models.example.json](../../config/models.example.json)。无需新增网关环境变量或依赖，仍使用 `NEWAPI_BASE_URL` / `NEWAPI_API_KEY` 和目录中的私有 `upstream_model`。不设置目录文件的旧默认chat模型采用32768/4096和 `max_tokens`；要使用另一输出字段，必须显式配置模型目录。

## 预算算法与裁剪边界

`EstimateChatContext(messages)` 执行角色/编码/条数/正文限制校验，并返回：

```text
input_tokens_estimate = sum(每条Content的UTF-8字节数) + 16 × 消息条数 + 256
```

这里的16用于消息封装，256是固定保守余量。中文、emoji均按实际UTF-8字节计算，不按字符数除以4。`ValidateChatRequest(request, contextWindow)` 再要求：

```text
input_tokens_estimate + output_tokens_reserved <= context_window_tokens
```

这是本平台的保守准入估算，**不是实际token计数、计费数据、特定tokenizer的严格上界或供应商成功承诺**。网关可能增加自己的指令或使用不同分词规则；真实供应商仍可能拒绝窗口不足，服务端不会自动删消息、换字段、减少预算并重新收费调用。V12账本不能使用此估算代替上游usage。

核心流程负责选择分支、保留末条用户输入、按完整旧轮次裁剪，以及生成 `context_summary`：`input_tokens_estimate`、`output_tokens_reserved`、`context_window_tokens`、`included_messages`、`omitted_messages`、`estimator`。Estimator标识为 `utf8_bytes_v1`。Provider只验证选定的快照；不能自行删除一半assistant、截断最后用户输入或混入其他分支。继续生成是用户明确触发的新请求，不是重新连接原TCP流；其实际受理/持久化与权限验收以核心台账为准。

## 协议、连接与失败

输出字段必须按实际网关协议选择。OpenAI官方接口把 `max_completion_tokens` 定义为包含可见输出与推理在内的生成上限；`max_tokens` 已弃用且不兼容部分模型。本项目保留 `max_tokens` 作为旧New API兼容默认，**不据此声称用户二次开发网关已验证兼容**；不按模型名字自动推断，也不同时发送两个字段。见[官方 Chat Completions 参数](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)。

官方usage可包含不可见生成及结构开销，因此可见文本长度不等于总输出token；预算也不保证最终可见字符数。见[官方 token 计数说明](https://developers.openai.com/api/docs/guides/token-counting)。

沿用一个共享Transport：HTTP仅允许回环、远程需HTTPS、禁环境代理及重定向；client不设置覆盖整段流的短超时。请求没有可重放GetBody，也不转发平台Idempotency-Key；网关400/网络错误不会触发换参数自动重试。上游非成功状态只返回安全的HTTP状态说明，不回显响应正文、凭据或提示词。

SSE继续支持网络分片、CRLF、多行data、usage空choices及错误事件，每上游事件至多256KiB；必须 `[DONE]` 才正常完成，提前EOF保留部分输出但失败。工作台已有128KiB输出字节和运行上下文限制仍生效；这里没有新增供应商usage账本或token流持久日志。

## 已验证与未验证

新增5个本地测试函数覆盖：多条角色/原文完整传输、并发模型共用Transport、两种互斥输出字段、旧单轮线协议、非法上下文零网络请求、供应商400不重试/不泄错误正文、UTF-8和窗口等号边界、129消息/256KiB硬限、配置隐私/默认/版本和媒体字段拒绝。

Provider与原目录专项通过0.280秒；普通全仓Go测试通过studio6.478秒，vet通过。此时核心多轮接线仍在并行实现；不能把这些结果当作真实PG对话、UI分支或真实网关验收。最终纵向链路证据由集成台账补充。没有使用真实服务密钥或发送收费请求，也未新增依赖或运行PG测试。
