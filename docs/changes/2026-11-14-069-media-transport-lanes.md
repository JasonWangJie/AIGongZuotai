# 2026-11-14-069：媒体提交与查询 Transport 隔离

## 目的

为 V16-A2 媒体执行分道提供独立网络连接预算，让慢生成提交与已知任务查询不会互相占满网关连接；保留受理不明、不可变提交、凭据和取消边界。

## 范围

关联 V16-A2、H03 模型与执行、H07 可靠性与性能、H09 工程与交付治理。只修改异步媒体 HTTP 适配器并增加真实 HTTP 专项；worker/租约接线由并行调度实现负责，入口配置、指标、任务状态和联合验收由根集成。完整 V16、实际供应商和生产容量仍另验。

## 实际文件

- [internal/studio/media_http.go](../../internal/studio/media_http.go)：新增两类连接预算、独立查询客户端与池、关闭两个池；保留编辑共享提交池。
- [internal/studio/media_transport_lanes_test.go](../../internal/studio/media_transport_lanes_test.go)：新增 6 个测试函数，含 8 个子例，使用真实本地 HTTP 客户端/服务端。
- [docs/wiki/media-transport-lanes.md](../wiki/media-transport-lanes.md)：当前结构、超时、取消和验证边界。
- 本台账；根负责人统一更新 README、Wiki/台账索引、任务源及旧媒体页的当前连接池描述。

## 接口与数据影响

`AsyncMediaConfig` 新增 `SubmitConcurrency` / `QueryConcurrency`，Go 零值各默认 2，有效值为 1—32。应用将它们与实际提交/查询 worker 数量绑定。两个池的总空闲、每 host 空闲及每 host 总连接上限分别等于各自预算，替换旧单池固定 16 条每 host 连接配置。

JSON 提交保留 `client`，multipart 使用与它共享 Transport 的 `editClient`，查询使用独立 `queryClient`。原 30 秒 JSON/查询、2 分钟编辑、20 秒响应头及 worker context 语义不变。Close 关闭两池空闲连接并保留原资产关闭。没有修改公开 HTTP API、幂等 hash、数据库 JSON 快照、SQL 迁移或 schema 17。

提交仍禁止重定向、保留原文件摘要校验与 `GetBody=nil`；POST 响应丢失不重提。已有 `client` 测试仅检查客户端身份与 multipart 共池，没有被新查询路径悄悄绕过的注入式测试；旧测试无需重命名或削弱。新专项直接观察真实 HTTP 到达、连接复用和关闭。

## 验证结果

| 命令或检查 | 实际结果 |
| --- | --- |
| 首次定向编译 | 并行调度文件尚未接齐 App 的 lane 方法/字段而暂时失败；没有据此修改其他代理文件，接齐后复验。 |
| 新 Transport 测试首次编译 | 本测试 helper 与独立验收代理新集成测试的 `mediaLaneProvider` 同名；只重命名本文件 helper 为 `mediaTransportProvider`，不改产品。 |
| `go test ./internal/studio -run '^TestMediaTransport' -count=1 -v` | 首轮 5 函数及 5 子例通过，0.682s。其后补充正在执行的请求取消释放测试。 |
| 下方最终媒体正则命令 | 最终 20 函数通过，0.852s；包含新增 6 函数和 8 子例。 |
| `go vet ./internal/studio` | 静态检查通过；最终追加正在执行的请求取消测试后再次执行，通过。 |
| `npm run check:docs` | 本模块交接时仅 1 项待集成：台账索引尚未覆盖 069；无本模块断链。索引归根负责人，联合阶段补齐后复验。 |
| `git diff --check`（本模块文件） | 通过，无空白错误；Git 提示 Windows 后续检出可能将 LF 转为 CRLF，不影响当前内容。 |

```powershell
go test ./internal/studio -run '^TestMediaTransport|^TestAsyncMedia|^TestAsyncImageEdit|^TestMediaRetryAfter' -count=1
```

真实 HTTP 证据包括：POST 卡住时 GET 能结束，GET 卡住时 POST 能结束；JSON 与 multipart 相互共享提交上限；两池实际峰值分别为 2/3；等待连接的取消不额外发送请求，已经到达服务端的取消释放连接后两侧仍可用；关闭提交池后查询复用原 TCP 连接；最终 Close 关闭两池和私有 asset root；307 不跟随，复用连接上 POST 已被读取后断开仍只发送一次。

仅使用 `httptest`、临时目录与现有合成图片，不使用 PG、真实供应商、SMTP、支付或审核渠道。本台账不借用联合全量结果；根完成全仓与真实 App/PG 检查后在联合台账记录。

## 风险与限制

Transport 的连接上限不等于 HTTP/2 stream 上限，必须配合有界 worker；本次测量是本机 HTTP/1 确定性实验，不是吞吐或 p95/p99 成绩。Query 内部仍可能同步等待私有导入，慢下载不能仅靠拆池解决，V16-A3 独立导入预算保留待办。

CloseIdleConnections 不取消正在执行的请求，应用仍须先取消 context 并等待 worker 清理。Go 配置零值默认与环境变量严格校验是两个层次；实际入口配置和 worker 接线依赖同批集成，单独复用适配器仍需遵守调用方并发预算。

## 回滚方式

本次没有数据库变化；需要回退时先停止进程并保留全部原请求、handle、候选及私有文件，与同批 worker/配置一起回退构建。不能为了回退自动重发 `SUBMIT_UNKNOWN`，不能删 schema 登记、owner 或原任务文件。保留本台账的失败和修正证据。

## 文档同步

新增 [媒体连接池 Wiki](../wiki/media-transport-lanes.md)，关联 [V16 契约](../planning/v16-contract.md)和 [媒体协议 Wiki](../wiki/media-gateway.md)。根负责人同步顶层 README、旧媒体页的共池描述、Wiki/台账索引与任务源；V16-A2 完成状态以联合验收为准，本适配器专项不单独将整批标为 done。
