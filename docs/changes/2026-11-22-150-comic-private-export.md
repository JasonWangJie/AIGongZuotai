# 150 · 作者确认的漫剧分镜与私图导出

## 目的

创作者在原项目中保存、查看和确认不可变的分镜版本，并把已确认版的标题、场景、角色、动作、对白及可选原图交给可编辑的 PPTX 导出器。

## 范围

V21-B1 涉及 H01、H02、H08、H10，私有原图复用 H04。149 提供版本、资产身份和确认收据，150 提供项目 HTTP 接口、授权清单与私图读取。工作台负责文字排版、可编辑续页和最终文件下载。

## 实际文件

- `internal/studio/comic_http.go` 注册漫剧路由，校验写入字段，读取当前或历史版本，核对操作收据，转换已确认分镜并按镜头交付获准原图。
- `internal/studio/comic_http_test.go` 包含输入限制、长文本完整性及真实项目、私图、原键和历史版导出的联合 HTTP 测试。复用 029 的受限图片读取和原有账号/项目入口。

## 接口与数据影响

项目基址为 `/api/v1/projects/{project}/comic`。GET 基址读取当前版，POST 使用 `expected_version`、可编辑文档和 `Idempotency-Key` 保存。GET `/versions` 支持 `before_version` 与 `limit`，GET `/versions/{version}` 读取指定版本。作者以空正文和原键 POST `/versions/{version}/confirm` 确认当前版。保存或确认响应不明时，GET `/receipt?operation=save|confirm` 携带对应操作的原键，只读回该版本及操作类型。确认只记录创作者认可这一分镜修订。

GET `/versions/{version}/export` 返回以 `source=comic`、项目、原版次和创建时间标识的导出清单。生成前重核当前会话、项目及该修订全部私图的冻结来源与现有可见性。每镜提供一份原始语义页；正文含场景名、按选角顺序的角色、完整动作与非空对白，备注含故事及对应卡片说明。前端按实际排版处理长文续页，通过最终原版清单复核后交付。导出读流程不创建 029 文稿或改变任何内容审核记录。

GET `/versions/{version}/panels/{panel}/image` 仅在原版已确认、该镜冻结图片当前仍可读时提供 PNG/JPEG。它与 029 共用最多四个读取占位，校验原始私有文件或对象版本、SHA-256、字节数、编码及像素后，再次核对会话与来源。每图限 8 MiB、1600 万像素，每稿最多四图、32 MiB；API 响应禁用共享缓存。稿件单次保存请求上限 2 MiB。

## 验证结果

`go test -count=1 -v ./internal/studio -run '^TestComicHTTPAcceptsOnlyEditableFieldsAndPreservesLongText$'` 通过，0.301 秒。验证只读字段及私有路径注入、无效 UTF-8、超限、尾随 JSON、长动作、对白与角色场景说明完整保留。真实 PostgreSQL/HTTP 用例 `TestPostgresComicPrivateExportConfirmationAndRevokedImage` 由集成人串行执行，2.39 秒通过，包含同一原键重放、未确认拒绝、原版确认后可重复导出、新版不继承确认、匿名与跨账户拒绝、回收/还原、完整原图及 029 文稿数保持为零。

## 风险与限制

已下载到本机的文件仍由下载者控制。服务器在每次新导出和镜图下载时检查素材当前许可。客户端本地排版可增加续页，镜头顺序和完整文字由来源版本确定。此项没有生成新图、绕过平台审核或向外部供应商发送内容。

## 回滚方式

停写并联备数据库、配置与所有私有素材后，恢复对应软件、版本和素材。在原项目读回作者已确认的修订，重新验证原图和原键。降至 030 时应使用升级前的完整配套备份。

## 文档同步

集成人同步 README、Wiki、OpenAPI、台账索引、任务源与生成 TODO。用户实际下载和独立 PPTX 验收另在工作台及联合记录登记。
