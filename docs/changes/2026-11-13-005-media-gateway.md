# 005：真实异步图片与文生视频适配

日期：2026-11-13。范围为真实协议实现及本地 fixture 验证；没有发起真实收费生成。

## 目的

根据用户提供的实际网关文档，接通图片和文生视频的异步提交、状态查询及本地结果导入，同时避免受理不明、查询故障或存储问题导致重复生成和费用。

## 范围

新增 MediaProvider HTTP 实现、不可变提交记录、供应身份校验、状态/错误分类、私有本地资产导入与读取 helper，以及合成协议 fixtures 和专项测试。PG 持久化、账户授权、应用配置和 HTTP 路由接入由 [004](2026-11-13-004-persistence-auth.md)负责，前端能力与状态由 [006](2026-11-13-006-accounts-projects.md)负责，根集成由 [007](2026-11-13-007-integration-database.md)负责。

## 实际文件

- `internal/studio/media_http.go`：Prepare/SubmitPrepared、图片/视频 JSON、查询、幂等字节、凭据指纹、分类错误和取消边界。
- `internal/studio/assets_local.go`：域名/IP/跳转限制、私有流式写入、图片解码和资源预算、MP4 头检查、稳定 ID；独立只读 reader 让已保存资产不依赖当前网关或 Key 仍可授权下载，明确拒绝导入与创建目录。
- `internal/studio/media_http_test.go`：本地协议、状态、下载、SSRF、超限和恢复边界测试。
- `internal/studio/testdata/media/`：人工合成 JSON、README 与公开文档资源哈希，不含用户数据或真实密钥。
- `docs/wiki/media-gateway.md`、本记录：取证、接口契约、配置、已实现能力与限制。
- `provider.go` 的共享类型、`go.mod/go.sum` 的 `golang.org/x/image`、应用/HTTP 配置由后端负责人协作修改并记录在 004；本记录说明其与适配器的契约关系。

## 接口与数据影响

新增 `MediaSubmission`，要求网络提交前持久化原始 Body、IdempotencyKey、Endpoint、BaseURL、CredentialID、ProviderIdentity、数量、类型、时间和 SHA256。Handle 记录上游 task ID、供应身份与 RetryAfter；原始 API Key 不存数据库。网关或 Key 变化时拒绝静默继续旧任务。

请求使用 `/v1/images/generations_async`、`/v1/videos/generations_async` 和 `/v1/media/tasks_async/{task_id}`。本轮只开放 JSON 文生图与文生视频，不开放编辑、延长或参考素材上传。真实取消明确不支持。

错误结构区分 SubmissionUnknown、Retryable 和 ReconcileRequired；图片 608/609、结果过期、身份问题不得当作普通可重新生成失败。有效但数量不足的结果为 PARTIAL。资产 URL 返回平台受鉴权路由，供应商完整结果地址和 Key 不下发前端。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| 公开文档只读取证 | 通过 | HTTP 200；SPA 正文资源 31988 字节及 SHA256 记录在 fixtures；没有登录和调用生成接口 |
| `gofmt` | 完成 | 限定本记录的 Go 文件 |
| `go test ./internal/studio -run 'TestAsyncMedia\|TestAsset\|TestMediaRetry' -count=1` | 通过 | 10 个测试函数、25 个子用例；覆盖图片/视频、原请求重放、未知受理、临时查询、身份、SSRF、内容限制，以及供应关闭后离线读取、只读拒绝 Import、不创建目录 |
| `go vet ./internal/studio` | 通过 | 本地静态检查，没有真实模型调用 |
| 工具链切换后的专项重跑 | 首次受环境阻挡，重跑通过 | Go 1.26.11 自动下载在默认代理 IPv6 超时；仅本次命令设置 `GOPROXY=https://goproxy.cn,direct` 后专项通过，没有修改全局 Go 配置或回退版本 |
| 真实模型与实际视频播放 | 未运行 | 未提供并启用实际供应 Key/模型；没有发起收费请求，视频测试只验证合成 MP4 头与字节交付 |
| 全仓构建、账户隔离与 PG 恢复 | 由 004/007 集成记录 | 专项通过不替代应用授权、持久化恢复和生产容量验收 |

## 风险与限制

文档没有给幂等记录保留期、确定的素材 TTL、独立按客户端请求 ID 查回或取消 API。未知受理保留并暂停，不自动新提交；当前操作员恢复控制台尚未实现。真实模型对尺寸、时长和数量的限制仍需账户级联调。

私有素材是本机文件系统，不具备对象存储冗余、磁盘配额和孤立文件回收。图片受格式、帧和像素预算限制，SVG 暂不接收。MP4 只验证容器头，不保证完整可播放、扫描或转码安全。文件与数据库不是跨资源原子事务，依赖稳定 ID 和恢复重查收敛。

交付前审阅修复了资产读取与生成 Provider 耦合的问题：读取已保存作品现在使用独立只读 reader，停用供应配置不删除或隐藏原件。应用层仍须先验证任务与工作区归属，不能直接把 reader 当成公开文件服务器。

## 回滚方式

先关闭新真实媒体受理，保留原始 Submission、Handle 和已保存资产供核实与导出；不要将旧任务切换到 demo，也不要换 Key 后重提。回退代码前确认旧版本能读取新增状态与记录；若不能，保持查询/下载服务并停止新受理。不得删除资产目录、上游任务或数据库记录来隐藏不确定状态。

## 文档同步

- 已新增媒体网关 Wiki，包含来源、协议、配置、状态、安全导入、验证和未完成项。
- 根 README、Wiki 导航与台账索引由汇总负责人同步 005 及真实能力；模块引用 004/006/007 明确集成归属。
- 后续真实联调、编辑/延长、取消能力或媒体处理升级应建立新台账；本轮交付前修正继续追加本记录。
