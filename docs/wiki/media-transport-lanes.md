# 媒体提交与查询连接池

040 保留独立的提交与查询 HTTP 连接池。提交最多 32 个执行位；原 handle 查询在 PostgreSQL 配置短时租约连接池后可显式扩至 1,536 个执行位。下文的 1—32 为 V16-A2 交付口径。[当前配置与实测](media-original-query-capacity.md)

V16-A2 将异步媒体适配器的生成提交和原任务查询拆成两个独立 HTTP Transport。普通 JSON 生图/视频与 multipart 图片编辑共享提交池；已知 handle 的 GET 使用查询池。慢 POST 不再占用 GET 的连接预算，慢 GET 也不占用 POST 的连接预算。执行线程和数据库领取属于同批次的调度层，见 [V16 契约](../planning/v16-contract.md)；本页与 [069 台账](../changes/2026-11-14-069-media-transport-lanes.md)只记录适配器实现及其验证。

## 配置与连接归属

| 入口 | HTTP client | 连接池 | Client.Timeout |
| --- | --- | --- | --- |
| 图片/视频 JSON 生成 | `client` | 提交池 | 30 秒 |
| 图片编辑 multipart 原文件 | `editClient` | 与 JSON 生成共享提交池 | 2 分钟 |
| 原 handle 结果查询 | `queryClient` | 独立查询池 | 30 秒 |
| 成果下载、格式校验与私有导入 | 原资产客户端 | 独立且受 SSRF 限制 | 保留原有 2 分钟限制及外层 context |

`AsyncMediaConfig.SubmitConcurrency` / `QueryConcurrency` 分别控制两池的 `MaxConnsPerHost`、`MaxIdleConnsPerHost` 与 `MaxIdleConns`。Go 配置零值分别使用 2；负数或大于 32 拒绝构造。应用传入归一后的 `MediaScheduling.SubmitWorkers` / `QueryWorkers`，对应启动变量 `MEDIA_SUBMIT_WORKERS` / `MEDIA_QUERY_WORKERS`，默认各 2、范围 1—32。环境变量显式无效值的拒绝由入口负责，不能把 Go 配置零值兼容规则理解为允许环境变量填 0。

两个 Transport 各自保留 10 秒拨号、10 秒 TLS 握手、20 秒等待响应头、90 秒空闲连接期限；禁用系统代理。请求仍受原 worker context 约束，较早截止者生效。编辑使用预建客户端，不在并发执行期间修改共享 Timeout 或 Transport。

连接数上限不是 HTTP/2 stream 数量上限。实际执行并发由对应 lane 的有界 worker 保证，本地 Transport 测试使用 HTTP/1 fixture 测量连接占用，没有据此声称 HTTP/2 并发上限或生产吞吐。

## 原请求与故障语义

拆池不改变受理时冻结的正文、multipart 边界、原文件摘要、模型、路径、凭据标签与 provider identity。发送前仍校验原私有文件，同一个打开文件描述符通过校验后发送。查询仅使用原 handle 重建同源路径；新旧模型目录不重写已受理请求。

三个网关客户端都拒绝跳转。提交设置 `GetBody=nil`，不因 `Idempotency-Key` 存在而允许 Transport 重放非空 POST；适配器不增加应用级重试。已读取 POST 后连接中断仍是 `SUBMIT_UNKNOWN`，不推定失败免费、不换键再次生成。等待连接或执行中的 context 取消沿用原分类：提交保守保留受理不明，查询为可再次查询原任务的 `QUERY_TEMPORARY`。供应商不支持取消的能力声明没有改变。

`Close()` 关闭两个网关池的空闲连接，然后关闭原资产存储及其下载客户端。multipart 与 JSON 共享同一个池，关闭一次即可；请求中的连接由调用方 context 和应用退出流程停止，`CloseIdleConnections` 不是强杀所有在途请求。关闭提交池的空闲连接不会关闭或重建查询池的空闲连接。

私有 `client` 字段继续表示 JSON 提交客户端，已有 multipart 测试仍验证它与 `editClient` 共享 Transport。新增测试通过真正的客户端和本地 HTTP 服务，不用替换 RoundTripper 绕过连接池；查询明确通过新 `queryClient` 验证。

## 验证与当前边界

新增 6 个测试函数及 8 个子例，覆盖双向慢请求隔离、JSON/编辑共享提交预算、HTTP/1 同时占用 2 个提交和 3 个查询连接、等待与执行中取消、独立空闲关闭、资产 root 关闭、拒绝重定向及读取 POST 后断开不重发。连同既有媒体/编辑/模型快照测试共 20 函数通过，最终命令与时间见 [069](../changes/2026-11-14-069-media-transport-lanes.md)。

以上是A2/069的历史验收范围，当时`Query`同步导入、格式017。当前A3.1原生适配器由App调用`QueryStatus`，先提交私有加密描述再进入独立delivery；旧`Query`保留兼容包装。下载池和执行容量由MEDIA_DELIVERY_WORKERS限定，当前018格式与恢复规则见[媒体持久交付](media-delivery.md)和[适配器](media-delivery-provider.md)。独立首次审核、渠道公平、持久流与生产容量仍待后续验收。

关联：[媒体协议与故障](media-gateway.md) · [图片编辑与参考素材](references.md) · [任务领取与恢复](scheduler.md) · [私有对象存储](object-storage.md)。
