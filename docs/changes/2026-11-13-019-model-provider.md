# 019 · V06 多模型网关路由与不可变媒体请求

日期：2026-11-13。关联 **V06 模型目录与能力参数**；完成状态以[任务源](../planning/tasks.json)及整批联合验收为准。

## 目的

让同一聊天或媒体网关承载多个目录模型，并在受理时固定实际执行模型、尺寸、分辨率和时长。目录更新不能改写已受理请求、改变上游幂等字节或阻断原任务查询。

## 范围

Go 聊天和异步媒体适配器的显式模型入口、共用连接池、无默认生成模型的历史查询模式、兼容旧默认入口及本地协议测试。目录解析、平台幂等和数据库事务由 V06 核心改动负责；前端使用完整预设，不由适配器推断供应商支持组合。

## 实际文件

- [internal/studio/provider.go](../../internal/studio/provider.go)：无默认模型的聊天网关工厂、显式 StreamModel、输入边界、共享 HTTP client/Transport。
- [internal/studio/media_http.go](../../internal/studio/media_http.go)：AllowNoDefaultModel、PrepareModel、size/resolution/seconds 原样快照及旧 Prepare 兼容。
- [internal/studio/provider_models_test.go](../../internal/studio/provider_models_test.go)：6 个模型路由、连接复用、原始字节和参数边界测试函数。
- [internal/studio/provider_test.go](../../internal/studio/provider_test.go)：未配置媒体模型的新请求遵守目录 `422 MODEL_DISABLED` 契约。
- [docs/wiki/media-gateway.md](../wiki/media-gateway.md) 与本台账；根 README、索引、任务源由集成人同步。

## 接口与数据影响

新增包内 `newOpenAIChatGateway(baseURL,key)`、`StreamModel(ctx,upstreamModel,prompt,emit)` 与媒体 `PrepareModel(input,upstreamModel)`。原 Stream/Prepare 保留供既有兼容调用，正常目录执行不依赖可变默认模型。TaskInput 新字段由核心定义，准备阶段读取已验证的有效参数，不自行补尺寸/分辨率/时长。

媒体提交仍是 `/v1/images/generations_async` 或 `/v1/videos/generations_async`，图片发送 n，视频发送 seconds、不发送 n。已持久 `MediaSubmission` 的结构及 ProviderIdentity 算法不变；模型不参与网关身份，原始 Key 不进入数据库。AllowNoDefaultModel 是内部配置，保留已知提交和 handle 的查询能力，不提供用户绕过目录的 API。

本模块无数据库迁移；核心在受理阶段把原始提交字节与任务、平台幂等和审计原子保存。网关的模型路由不复用资产下载客户端，结果下载的无凭据/SSRF 防护保持独立。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| 只读核验用户提供的公开 SPA 文档 | 通过 | 2026-11-13 公开资源 SHA256 与既有来源记录一致；核对图片/视频字段与 JSON 原字节幂等，没有登录或付费请求 |
| `go test ./internal/studio -run 'TestOpenAI\|TestLive\|TestAsyncMedia\|TestAsset\|TestMediaRetry' -count=1` | 通过 | 本轮首次完整专项 0.169 秒；共20个测试函数，含6个新增；均为本地合成服务器 |
| 多模型与连接复用 | 通过 | 16 路并发请求逐一核对模型/正文；顺序两种模型的服务端 RemoteAddr 相同，证明该 fixture 下复用 TCP 连接 |
| 原字节与旧任务续查 | 通过 | 图片和视频提交序列化持久后，使用无默认模型的新 Provider 重放完全相同字节/Key，并继续查询同一 handle；这是受控 fixture，不代表平台自动重提未知任务 |
| `go vet ./...` | 通过 | 新接口、测试与核心接线编译通过，未输出凭据 |
| `node scripts/check-docs.mjs` | 并行文档待齐 | 本模块路径与8个台账字段无错误；当时仅缺018和020两份其他模块台账，最终整批文档检查由集成人复跑 |
| 普通全仓库、真实 PG 和浏览器联合验收 | 集成人另行记录 | 本专项不运行数据库测试，不替代 V06 全批次验收 |

## 风险与限制

未调用真实收费网关；账户开通模型、参数组合、生成质量和供应商容量尚未实测。视频文档允许依模型省略时长，但当前平台目录要求明确 1—600 秒；示例规格不是通用支持承诺。模型目录当前启动时读取，没有热更新或多网关动态选择。API Key/服务根变更仍要求人工核实旧任务，不支持自动跨凭据迁移。MP4 仍仅容器头检查，未增加转码或可播放性保证。

## 回滚方式

保留 PostgreSQL 备份及已保存提交、句柄和私有资产，不清理历史数据。停用有问题的目录项只影响新请求；保留原媒体网关地址和 Key 以便继续查询。若回退 V06，需要同时回退目录/core/provider/frontend 的接口使用；不能只删 PrepareModel 而保留核心调用，也不能把新任务按旧环境默认值重新提交。执行记录应先核对原始快照，必要时保持待核实状态。

## 文档同步

- README：集成人更新多模型能力、MODEL_CATALOG_FILE、历史任务边界与最近变更链接。
- Wiki：[真实异步图片与视频网关](../wiki/media-gateway.md)补充精确预设、受理快照、共享连接池、无默认模型查询与实际验证范围。
- 台账索引、任务源与 TODO：集成人按 V06 联合证据同步；本适配器结果不直接勾选整个批次。
