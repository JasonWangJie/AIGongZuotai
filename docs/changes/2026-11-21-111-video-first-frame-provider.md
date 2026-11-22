# 111 · 视频生成接收已选图片首帧

## 目的

V19-B 在原异步网关上增加显式的首帧输入能力。用户确认工作流中的原图片后，为按原图生成视频另行报价和受理。协议范围为支持单文件输入的已配置视频模型，涉及 H03、H05、H10。

## 协议来源

2026-11-21 匿名读取网关公开文档，沿同站公开页面的脚本定位到 11 月 20 日更新的 §5.1.1。文档允许有相应能力的模型在视频生成端点接收 `input_reference` 文件，整个视频请求最大 64 MiB。已记录当日资源长度及 SHA256。工作台首帧原件沿用私有参考图的 8 MiB 上限，允许 PNG、JPEG 和 WebP，冻结的请求正文沿用 33 MiB 本地存储上限。[公开文档](https://api.aiimg.lol/async-image-api-docs) · [本次来源记录](../../internal/studio/testdata/media/video-first-frame-source.json)

## 范围

V19-B 的模型声明、首帧原件准备、持久提交、视频报价与冻结。

## 实际文件

- `internal/studio/catalog.go`、`model.go` 增加可选视频首帧能力和具体任务来源身份。目录缺省关闭，旧模型与请求保留原版本计算方式。
- `internal/studio/video_first_frame.go`、`app.go`、`persistence.go` 接入已确认项目资产的只读核验、锁外原件读取、提交前的事务复核，以及与父流程相同事务的子任务链接。
- `internal/studio/media_edit.go`、`media_http.go` 将选中原图写入私有 multipart 请求文件；运行期沿原文件提交，原 handle 沿统一端点查询。
- `internal/studio/media_moderation_execution.go`、`billing_http.go`、`billing_pricing.go` 核验首帧的实际审核输入、工作流冻结输入、原报价与独立视频费率。
- `internal/studio/video_first_frame_test.go` 和 `internal/studio/testdata/media/video-first-frame-source.json` 记录环回字节、原件存储、故障与报价回归。

## 接口与数据影响

启用适配时在特定模型目录声明 `capabilities.video_first_frame_supported=true`。启用权益时需给对应模型及预设配置 `operation=first_frame` 费率。运行节点输入含项目、模型版本、完整规格、提示词、`inputs.workflow_id` 和原 `inputs.first_frame` 的资产、产物、来源任务、修订与内容版本。项目工作流受理前复核已选原件及内容；启用媒体审核时依当前策略读取整张图作输入审核。

服务端流式准备 `POST /v1/videos/generations_async` 的请求正文，发送模型、指令、秒数、冻结规格及一个 `input_reference` 文件。PG 保存私有正文身份和摘要，正式提交时核对同一文件描述符与冻结内容，保留原幂等键。收到 202 后仅依据原任务号查询同源网关，沿用既有交付、审核、权益和 Outbox 链路。已提交受理不明仍保守停驻。[工作流契约](../planning/v19-contract.md)

## 验证结果

`go test ./internal/studio -run '^(TestAsync(VideoFirstFrame|ImageEdit)|TestVideoFirstFrame)' -count=1` 通过，用时 0.451 秒。环回网关按 multipart 字段读取两张不同 PNG 并逐字节比较，每张一次 POST、一次原 handle GET。丢失或篡改的冻结文件、错误路径或数据类型均在网络前拒绝。S3 兼容夹具只读原 VersionId，篡改时拒绝，入参读取后清理私有临时文件。已有五项图片编辑测试继续通过。

集成人串行实测 `TestPostgresWorkflowVideoFirstFrameQuoteAndOriginalReceipt` 通过，用时 8.97 秒。没有报价、借用普通视频报价都无法建视频任务；按准确冻结输入报价后受理、重放、读回原收据均指向同一子任务，视频只外发一次并结算 300000 微额 CREDIT。本机两条完整工作流实际传输不同选图原件通过，用时 21.42 秒；浏览器验收通过，用时 16.90 秒。实际渠道的账号开通、模型效果和真实费用尚待用户授权。

## 风险与限制

网关支持某种输入方式不代表所有映射均已开通。生产目录按实际模型显式开启。缺少原件、项目权限、冻结来源或费用匹配时返回错误。

## 回滚方式

停用首帧模型可停止新请求；原任务及提交正文须保留。需要恢复 023 时，停写并恢复同一点的数据库、私有正文和资产目录、相关密钥及已登记对象版本。

## 文档同步

集成人统一同步 README、Wiki、任务源、生成的 TODO、进度与台账索引。本记录从属于 V19-B 的模型适配部分。
