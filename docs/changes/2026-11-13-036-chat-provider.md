# 2026-11-13-036：多消息文本传输与上下文预算

## 目的

V10 / H03、H07、H09：将LLM从单条prompt传输扩展为原角色/原顺序的多消息请求；通过模型窗口、输出预留和平台硬限拒绝不合法上下文，保留共享连接、隐私和不自动重发的边界。

## 范围

Provider执行契约、预算估算/校验、目录预算和私有输出字段、示例配置、本地HTTP fixture与Wiki。核心对话/消息权限、分支裁剪、受理幂等和持久化由同批核心负责；前端由037负责；真实纵向验收由038汇总。没有修改app/persistence/http或增加依赖。

## 实际文件

- `internal/studio/chat_provider.go`：ChatMessage/ChatRequest、常量、EstimateChatContext/ValidateChatRequest、demo多消息方法。
- `internal/studio/provider.go`：openAIChat.StreamMessages、共享单次请求编码，保留Stream/StreamModel兼容。
- `internal/studio/catalog.go`：公开context_window_tokens/max_output_tokens，私有chat_output_limit_field及版本/默认/范围校验。
- `internal/studio/chat_provider_test.go`、`internal/studio/catalog_chat_test.go`：5个新增函数及边界子例。
- `config/models.example.json`：明确chat的32768/4096预算与max_tokens兼容字段。
- `docs/wiki/chat-provider.md`：契约、配置、算法、限制与官方协议来源。

## 接口与数据影响

`StreamMessages(ctx, upstreamModel, ChatRequest, emit)` 发送已冻结的user/assistant数组；ChatRequest包含Messages、MaxOutputTokens、OutputLimitField，仅作为私有执行快照。旧单轮方法保留原线协议。Provider不重新查询历史或裁剪。

预算为UTF8字节总数+每消息16+安全余量256，叠加输出预留后必须不超过配置窗口。平台限定129消息/正文256KiB；窗口2048—1048576、输出128—32768，要求输出+272小于窗口。超预算422 CONTEXT_TOO_LARGE，非法结构422 INVALID_CHAT_CONTEXT，均可在联网前拒绝。不是实际token计量或账单。

目录chat预算默认32768/4096，私有输出字段默认max_tokens，可显式max_completion_tokens；只发一个字段且不自动fallback重试。字段不公开，但改变模型版本。媒体模型拒绝chat设置，旧历史快照不被新默认改写。

## 验证结果

| 命令/检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./internal/studio -run 'TestChat\|TestCatalog\|TestOpenAI\|TestLive' -count=1` | 通过 | studio0.280秒，17个普通函数（含新5函数）及边界子例；该正则不包含Postgres命名测试 |
| `go test ./...`（未注入DB） | 通过 | studio6.478秒、cmd/server0.041秒；核心对话仍在并行接线时执行，不代替最终集成回归 |
| `go vet ./...` | 通过 | Provider/目录实现与测试落盘后执行 |
| 本地HTTP fixture | 通过 | 两种字段互斥、结构化3条消息、UTF8原文、模型并发与旧单轮均验证；非法请求没有网络调用，HTTP400只请求1次且错误正文不泄漏 |
| 真实供应商/PG多轮/公网容量 | 本台账未运行 | 未使用真实密钥、不收费；核心/根后续验收另记，不把fixture当真实模型质量或费用结论 |

来源核验仅访问官方文档，无收费API调用。采用OpenAI Docs技能核对参数与计数语义；链接和适用差异见[Provider Wiki](../wiki/chat-provider.md)。

## 风险与限制

按UTF8字节估算可能较真实token保守，也不保证覆盖自定义网关注入或特殊tokenizer；不能作为账务来源。模型窗口是运营配置，不是供应商自动发现。max_completion_tokens可能消耗不可见推理token，可见输出受预算和既有128KiB字节上限共同限制。

仅支持user/assistant文本和最后user结尾，不支持工具/多模态/system自定义；demo只是脚本回复。新多轮必须调用StreamMessages，不能用旧Stream静默丢弃历史。Provider不负责客户端授权；核心必须以所属项目的消息链构造并冻结快照。真实网关字段兼容性仍须V15独立验证，失败不得换字段重发可能计费的请求。

## 回滚方式

部署前保存目录和一致数据库/私有文件备份。本台账自身不新增数据库迁移；同批核心若升级数据格式须按其迁移栅栏回滚。旧二进制不认识chat配置字段时，应恢复匹配旧版本的目录；不得删除已受理快照或迁移登记强行降级。保留原幂等请求，不用回退触发第二次生成。

## 文档同步

- Wiki：[文本多轮Provider](../wiki/chat-provider.md)提供完整边界；根同步models/backend与导航。
- README/台账索引/TODO：由root统一更新V10及036记录。
- 核心持久多轮、前端与联合结果分别由035、037、038台账负责，本台账不代称其已完成。
