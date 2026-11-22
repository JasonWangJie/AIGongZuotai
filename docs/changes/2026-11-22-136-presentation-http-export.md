# 136 · 项目文稿 HTTP 接入与冻结原图导出

## 目的

创作者可以读取指定版本的文字、演讲备注与原图，导出可继续编辑的文稿。历史版本保留当时的排版和内容身份，下载资格随当前项目、账号、素材状态和内容审核决定重新核验。

## 范围

V21-C，H01、H02、H08、H10。使用 PostgreSQL、现有登录会话、项目和资产库。可选择本项目私有参考图或已公开且有私有原件的生成作品。用户在工作台编辑的文稿文字随版本冻结。

## 实际文件

- `internal/studio/http.go` 注册项目路由。
- `internal/studio/presentation_http.go` 提供保存、列表、原键回执、版本历史、按版原文、导出清单及按页取原图。
- `internal/studio/presentation_http_test.go` 包含请求边界、真实 PostgreSQL/HTTP、联备恢复、提交确认丢失和本机 Chromium 引导用例。
- 029 表与持久事务见 135；工作台文件生成见 137；独立文件读取、编辑及渲染验收见 138。

## 接口与数据影响

基址为 `/api/v1/projects/{project}/presentations`。保存用 POST、`Idempotency-Key` 和 `expected_version`；新文稿从 0 起。GET 基址返回项目列表，`/receipt` 从请求头原键查回该次保存。GET `/{id}/versions` 支持按 `before_version` 翻页，`/{id}/versions/{version}` 返回指定版本。文稿可在回收图片后打开，图片标记为当前不可用。

GET `/{id}/versions/{version}/export` 返回版本及按稳定页 ID 索引的同源 `image_urls`。每张图经 `/slides/{slide}/image` 获取；读取前后均核实会话、项目、版次、已批准来源、可用性与内容版本。私有读取器按原件或原 S3 版本打开，HTTP 再比对待发送字节的 SHA-256、格式与实际尺寸。二进制响应禁缓存，并提供 `X-Image-Width`、`X-Image-Height`。

请求 JSON 最大 2 MiB，持久文稿采用 135 的规范化大小与容量约束。单稿最多 4 个图片页，重复图片按页计数；单图不超过 8 MiB、1600 万像素，总计不超过 32 MiB、6400 万像素。导出清单只读元数据。图片单页按需取，4 个传输槽限制同进程并发；每个响应有独立的两分钟传输期限。

## 验证结果

- `go test ./internal/studio -count=1 -run '^TestPresentationHTTPWriteFieldsAndInputLimit$'` 通过，0.249 秒。验证损坏 UTF-8、超限、多个 JSON、伪造私有及只读字段、memory 模式门禁。`go vet ./internal/studio` 通过。
- 真实 PostgreSQL 由集成人按库串行执行。用例为 `TestPostgresPresentationHTTPVersionAndPrivateImage`、`TestPostgresPresentationGeneratedApprovalAndRestrictedAccount`、`TestPostgresPresentationLostSaveReceipt`、`TestPostgresPresentationBackupOldVersion`。联备用例另需 `TEST_POSTGRES_BIN_DIR`。
- 浏览器专项设置 `PRESENTATION_BROWSER_TEST=1`、绝对目录 `PRESENTATION_BROWSER_OUTPUT`，执行 `go test ./internal/studio -count=1 -run '^TestPostgresPresentationBrowserExport$' -v`。引导在独立项目中经正常 HTTP 上传真实 PNG 并获取 ALLOWED，同时向测试脚本提供输入字节的独立摘要，用于与下载的 PPTX 内嵌图片比较。真实执行和文件检查结果由集成人补录。

集成人串行执行的 029 专项共 46.647 秒通过。其中原版文稿和私图 3.13 秒、真实联备恢复 7.67 秒、生成图审核及受限账户 8.87 秒、保存确认丢失后只读原收据 2.87 秒、真实 Chromium 下载 10.28 秒。最终 029 全量 PostgreSQL 套件的 `internal/studio` 包在 1326.832 秒通过。文件结构、渲染及编辑证据见 138。

## 风险与限制

导出只接受可验证的 PNG/JPEG 原件。一个文件已被接收并保存在用户设备后，后续回收或账户停用仍能阻止服务端的新下载；本地已交付文件由用户管理。读取完成且最终事务核准后开始发送的响应属于已经受理的下载。历史文本和备注按当时版本读取，未对旧资料引入追溯审核。团队协作、真实外部审核商和生产对象存储的证据按各自批次跟踪。

## 回滚方式

停写后联备 PostgreSQL、参考和资产原件、目录 owner、已使用的私有对象版本及独立密钥。恢复 029 后按原账号和同一项目读取已保存文稿、旧版及私图原件；降至 028 须恢复该时间点的完整配套备份，先核对升级期间新增的版本。

## 文档同步

集成人同步 README、Wiki、OpenAPI、台账索引、任务源及生成的 TODO。136 与 135、137、138 共同记录 V21-C，其他 V21 子项独立验收。
