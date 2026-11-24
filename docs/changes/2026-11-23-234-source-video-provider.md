# 234 · 私有 MP4 的编辑与延长请求

## 目的

V20-C、H02/H03/H04。将用户已获授权的已交付原视频交给明确支持 Data URI 输入的编辑或延长模型，并在上游受理之前冻结可核对的原请求。

## 范围

供应商公开文档第 5.1、5.3 节规定两操作接受 JSON，原视频来源形式、时长、尺寸及延长方向取决于实际开通的模型。本次只实现服务端从原件构造 MP4 Data URI。调用者负责项目、资产修订、审核和模型能力校验。[供应商文档](https://api.aiimg.lol/async-image-api-docs) · [已核对的文档资源](https://api.aiimg.lol/static/js/async/6168.5abcbf94eb.js)

## 实际文件

- `internal/studio/media_source_video.go` 通过同一读者核对原件 MP4 标识、总长度和 SHA-256，将 Base64 和 JSON 字段流式写入私有存储。
- `internal/studio/media_http.go`、`media_edit.go` 仅按冻结操作、固定路径、凭据身份、媒体类型、原键和已验证的私有文件发送；沿用原 handle 查询与原接受不明状态。
- `internal/studio/reference_store.go` 按正文格式预留空间。视频 JSON 上限 64 MiB；原图片及首帧 multipart 继续沿用 33 MiB。
- `internal/studio/media_source_video_test.go` 与 `internal/studio/testdata/media/source-video-protocol.json` 记录原始字节、边界检查与公开协议来源。

## 接口与数据影响

准备方法接收 `video_edit` 或 `video_extend`、冻结上游模型、方向及服务端原件。请求分别发往 `/v1/videos/edits_async`、`/v1/videos/extensions_async`；只有延长可带方向。发送前在同一个已打开的正文文件上校验内容类型对应的前缀、长度和完整摘要，并回绕后发送。数据库沿用既有文件身份和请求元数据，私有原始 JSON 保存在 `REFERENCE_DIR`。维护从所有任务的 `submission.body_file_id` 收集可达文件，包含两种新正文。备份和恢复须同时覆盖数据库和私有目录。

## 验证结果

执行 `go test -p 1 -count=1 -run '^(TestSourceVideo|TestAsyncImageEdit|TestAsyncVideoFirstFrame|TestReferenceStoreSpool)' -v ./internal/studio`，13 项通过，包耗时 1.186 秒。

- 以仓库内的 5,993 字节 MP4 为源，环回服务器对两种操作的路径、模型、提示词、可选参数、凭据、原键、长度、正文摘要、解码后原件逐项核对。每个用例只开一次源文件，一次 POST 后对同一 handle 做两次 GET。
- 恢复原请求元数据和正文目录后发送；已保存的正文遭改写、丢失，或保存的身份/长度/摘要不符时，提交被拒且零外发。202 缺有效 handle 时保留受理不明。取消准备释放磁盘预留及临时文件。
- 使用有限生成的补齐字节检查编码后总请求大小。精确 67,108,864 字节 JSON 已冻结并通过同文件重读校验；增加 1 字节源数据时，在打开源前拒绝。此项单测耗时 0.88 秒。

## 风险与限制

MP4 容器标识和完整摘要用于核对已登记原件。模型最终能否接受实际编码、Data URI、方向与参数，仍需受控真实验收。环回测试覆盖协议和文件层；项目授权、权益、防重复受理以及整套数据库/原件恢复由集成专项验证。

## 回滚方式

先停止新视频编辑与延长受理，保留完成或待核实任务的原键、冻结文件和原 handle。需要跨格式回退时，在独立目录恢复配对的旧制品和全套联备；保留升级窗口内原件和收据供核对。

## 文档同步

集成人同步 README、V20-C Wiki、推进记录、台账索引及由任务源生成的 TODO。真实渠道能力与本机验收分别登记。
