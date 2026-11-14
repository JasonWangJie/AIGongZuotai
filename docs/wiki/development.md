# 开发与交付

本页适用于本地开发。已增加 PostgreSQL 与账户模式，但尚未开放公网部署。依赖版本以仓库实际锁文件、`go.mod` 和 `package.json` 为准。

## 本地运行

要求 Node.js 22.12+、npm 和 `go.mod` 指定版本的 Go。在仓库根目录执行下列命令，安装前端依赖并一起启动 Go API 与前端开发服务。默认 Go 地址为 `127.0.0.1:8080`，前端为 `127.0.0.1:5173`。完整启动入口以[仓库 README](../../README.md)为准。

当前 Go 最低补丁版本为 1.26.11；较旧 Go 在自动工具链模式下会下载所需版本。首次运行需要可达的模块代理和校验服务，不要通过关闭校验或回退安全补丁解决网络问题。验证时使用的 Node 为22.18.0、PostgreSQL为18.7。

```powershell
npm run setup
npm run dev
```

`npm run dev` 使用 `/readyz` 检查就绪，`Ctrl+C` 关闭该命令启动的进程。也可分别运行 `go run ./cmd/server` 和 `npm --prefix apps/workbench run dev`；后者的详细行为见[前端说明](frontend.md)。开发代理把 `/api` 请求交给 Go 服务；浏览器不持有 New API 密钥。使用演示模式无需真实网关账号或 API Key。

可将根 `.env.example` 复制为 `.env` 配置根启动脚本；不要将密钥放入 `VITE_*` 变量。直接运行 Go 时，应在当前终端设置相应服务端环境变量。`npm run build` 将前端构建到 `apps/workbench/dist`，Go 二进制构建到 `.runtime/server.exe`（Windows）或 `.runtime/server`。

数据库账户模式：Windows 先运行 `npm run db:setup`，再运行 `npm run dev:db`；已有 PostgreSQL 配置 `DATABASE_URL` 即可。便携数据库使用独立英文路径和随机凭据，不安装系统服务。数据位置、启停与测试库说明见[本地数据库](local-database.md)。

## 环境变量

| 变量 | 默认或用途 |
| --- | --- |
| `BILLING_MODE` / `BILLING_PRICE_FILE` | 默认disabled；credits须PG+required及明确价格文件。demo/live价格隔离，详见[权益配置](billing-operations.md) |
| `MODERATION_MODE` / `MODERATION_POLICY_FILE` | 默认disabled；rules/http须PG+required及明确政策文件，rules仅允许demo；启动冻结政策，变更后重启 |
| `MODERATION_ENDPOINT` / `MODERATION_API_KEY` / `MODERATION_CREDENTIAL_ID` | http模式的私有完整HTTPS端点、独立服务端密钥与稳定凭据标签；不得放入浏览器、日志或仓库，协议见[文本审核](moderation.md) |
| `MODEL_CATALOG_FILE` | 可选JSON模型目录；最多64KiB、1—32模型，每媒体模型1—32完整规格预设；启动校验，更改后重启 |
| `METRICS_TOKEN` | 留空关闭 `/metrics`；启用须32—256位可见ASCII，与AI密钥不同，使用独立Bearer令牌 |
| `ADMISSION_WORKSPACE_RUNS` / `ADMISSION_MODEL_RUNS` | 默认64 / 32，限制当前工作区全部/单模型未终态LLM |
| `ADMISSION_WORKSPACE_TASKS` / `ADMISSION_MODEL_TASKS` | 默认128 / 64，限制当前工作区全部/单模型未终态媒体任务 |
| `ADMISSION_WORKSPACE_STREAMS` | 默认32，每工作区SSE连接；全进程另有128上限 |
| `APP_ADDR` | `127.0.0.1:8080`，Go HTTP 监听地址 |
| `PROVIDER_MODE` | 默认 `demo`；显式 `live` 才启用真实 LLM Provider |
| `NEWAPI_BASE_URL` | live 模式的实际 API 根地址，须含 `/v1` 等真实前缀；适配器仅追加 `/chat/completions`，具体约定见[后端文档](backend.md) |
| `NEWAPI_API_KEY` | 仅服务端使用的网关密钥；不得提交至代码、Wiki、台账或截图 |
| `NEWAPI_CHAT_MODEL` | live 模式的 LLM 模型标识 |
| `APP_WEB_DIR` | 默认 `apps/workbench/dist`，构建后的前端静态目录 |
| `DATABASE_URL` | PostgreSQL 连接地址；为空时仅内存演示，无跨重启保存 |
| `AUTH_MODE` | `disabled` 或 `required`；required 必须配置 PostgreSQL |
| `AUTH_MAIL_ENABLED` | 默认false；true须PG+required，配置不完整拒绝启动 |
| `AUTH_MAIL_HOST` / `AUTH_MAIL_PORT` / `AUTH_MAIL_TLS_MODE` | SMTP主机、显式1—65535端口、implicit_tls或starttls，必须校验证书 |
| `AUTH_MAIL_USERNAME` / `AUTH_MAIL_PASSWORD` / `AUTH_MAIL_FROM` | 服务端SMTP凭据和发件裸邮箱，TLS后PLAIN认证；不放VITE或日志 |
| `AUTH_MAIL_PUBLIC_ORIGIN` / `AUTH_MAIL_OUTBOX_KEY` | 可信HTTPS回站origin（本地可loopback HTTP）；独立32字节base64密钥，安全联备 |
| `AUTH_MAIL_TIMEOUT_SECONDS` / `AUTH_MAIL_WORKERS` | 默认20秒/1 worker；范围5—60秒/1—4，保持有界 |
| `TEST_DATABASE_URL` | 独立测试数据库，仅用于持久化与隔离用例 |
| `ASYNC_IMAGE_BASE_URL` / `ASYNC_IMAGE_API_KEY` | 图片与视频异步服务根地址和服务端密钥；地址不带 `/v1` |
| `ASYNC_IMAGE_CREDENTIAL_ID` | 原始凭据身份标签，恢复时用于校验 |
| `ASYNC_IMAGE_MODEL` / `ASYNC_VIDEO_MODEL` | 分别配置当前网关支持的图片/视频模型，未配对应能力不开放 |
| `ASYNC_VIDEO_SECONDS` / `ASYNC_VIDEO_RESOLUTION` | 视频模型明确支持的时长和分辨率 |
| `ASSET_DIR` / `ASSET_ALLOWED_HOSTS` | 私有原件目录与结果域名精确白名单 |
| `REFERENCE_DIR` | 私有参考原图/缩略图/精确编辑正文；与ASSET_DIR和静态目录分别独立 |
| `ASSET_STORAGE_BACKEND` / `S3_*` | local或s3私有内容后端，字段与独立凭据要求见[对象存储](object-storage.md) |

前四个准入值允许1—4096，订阅值允许1—128；环境变量显式空值、0、非整数或越界会拒绝启动。计数和不明任务保留规则见[资源准入](admission.md)，探针、指标口径和令牌见[可观测](observability.md)。

live 模式通过配置分别启用图片/视频，实际协议与恢复边界见[媒体网关](media-gateway.md)。未配置能力时明确禁用。开发与自动测试默认使用 demo 或本地 HTTP fixture，不触发真实收费请求。

无数据库的运行/任务与幂等记录在重启后丢失。PostgreSQL 模式恢复持久快照与原任务，LLM 进程中断不能原样续接供应商流；检查点存在约一秒窗口。浏览器状态不能替代后端事实，详见[持久化与账户](persistence-auth.md)。

## 验证

按实际修改执行适用检查，台账记录真实结果；不要用“全部通过”替代未运行项目。

```powershell
npm run check
npm run check:changes
npm run smoke
npm run smoke:runtime
npm run smoke:catalog
npm run test:db
npm run perf:local
```

`npm run check` 执行 TODO 源/生成一致性、治理规则测试、Go vet、双方测试、生产构建与文档结构检查。专项命令包括 `go test ./...`、`go vet ./...`、`npm --prefix apps/workbench run typecheck`、`npm test` 和 `npm run build`。`npm run smoke` 要求先完成构建，会以随机本地端口启动独立 demo 进程，验证结束后关闭；不会复用现有服务或调用真实网关。

`smoke:runtime` 用独立内存demo、小限额和临时运维令牌验证探针、指标鉴权、幂等优先、限额释放、SSE断开与脱敏，输出 `.runtime/runtime-smoke.json`。它不复用开发账户，不调用真实网关；CI同样执行。

`smoke:catalog`使用独立临时目录与内存demo进程，验证公开目录/ETag、私有映射不泄露、精确规格、版本拒绝、幂等和重启生效；自动清理自己创建的目录文件。既有smoke/runtime-smoke/perf脚本显式清空MODEL_CATALOG_FILE，避免外部目录污染回归。目录详细配置和旧任务边界见[模型目录](models.md)。

测试覆盖当前实现的具体边界，不证明全部生产安全或万级并发目标已经达到。`test:db` 要求独立测试库，`perf:local` 是无认证内存 demo 的回归基线。视觉和交互修改还应在浏览器验证账户、项目、运行、取消、窄屏和错误反馈；真实网关验收另行配置并记录供应商限制。

## 所有变更同步文档

遵守 [AGENTS.md](../../AGENTS.md)：每个逻辑变更都需要台账、README 和相关 Wiki。代码、配置、依赖、测试、文档、规则及部署脚本没有豁免项。

1. 修改前阅读 README、Wiki 导航、台账索引与相关模块文档。
2. 从[台账模板](../changes/TEMPLATE.md)复制一份 `YYYY-MM-DD-NNN-short-name.md`，写明目的、范围、实际文件、接口与数据影响、验证结果、风险与限制、回滚方式、文档同步。
3. 更新[台账索引](../changes/README.md)、README 的相关内容与最近变更，以及相关 Wiki。架构取舍另写 ADR。
4. 执行适用测试及文档检查。同一逻辑变更交付前的修正追加原记录；交付后新增变更建立新记录。
5. 按 [TODO](../../TODO.md) 选择纵向批次，更新 `docs/planning/tasks.json` 的范围、证据、验证与状态，再执行 `npm run todo:sync`。依赖未完成、检查未通过、没有台账或文件证据都不能标为完成。开工、发现外部阻碍与验收都及时同步，详细规则见[任务维护](../planning/README.md)。

## 文档检查脚本的边界

[check-docs.mjs](../../scripts/check-docs.mjs)只依赖 Node 内置模块，包含两层检查：

- 结构检查：要求指定基础文档存在；扫描 README、AGENTS、TODO、`docs/planning/`、`docs/wiki/` 和 `docs/changes/` 中 Markdown 的本地链接；检查编号台账的必要章节与索引覆盖。外部 URL、锚点内容不验证；不扫描 `deliverables/` 内部的历史报告。
- 变更集合检查：`--changed` 合并 Git 暂存、工作区及未跟踪文件，要求有变更时同时包含根 README、一份编号台账、至少一份 Wiki、任务源和生成 TODO。支持尚无首次提交的仓库。

CI 可使用已存在的 Git 基线检查已提交变更：

```powershell
node scripts/check-docs.mjs --base origin/main
```

`--base` 比较 `base...HEAD`，不检查本地未提交文件；基线或 HEAD 不存在时明确失败。它与 `--changed` 互斥。CI 必须先获取对应基线及足够的 Git 历史，不能把找不到基线当成没有变更。

脚本只检查可自动判断的结构与同步痕迹，不能证明台账解释了每个语义修改，也不能证明验证命令实际执行过。评审仍需比对 diff、接口行为和台账证据。

## 故障排查

| 现象 | 检查方向 |
| --- | --- |
| 前端无法访问 API | Go 是否启动、默认 8080 端口是否冲突、开发代理是否指向本地 Go |
| 刷新后数据消失 | 检查 bootstrap.storage；memory 重启清空，postgres 则检查账户/项目选择 |
| live 配置失败 | 变量是否存在、模型与基础路径是否匹配实际网关；不要把密钥粘贴到日志或台账 |
| 图片或视频在 live 下未配置 | 对照媒体 Wiki，检查异步根地址、模型、原凭据身份与原件目录 |
| `--base` 失败 | 基线是否已 fetch、是否存在 HEAD、浅克隆历史是否足够 |
| 文档检查报告缺少同步项 | 更新该逻辑变更的 README、Wiki、编号台账及台账索引后重跑 |

## V07 参考素材开发与验证

`REFERENCE_DIR=.runtime/references`保存私有原件、缩略图和冻结后的编辑正文，必须与静态根、Vite public及ASSET_DIR分开。数据库持久化模式需要保留该目录；演示任务仍只返回示意结果，不代表编辑效果。配置图片模型的`image_edit_supported`和`max_reference_images`后才开放编辑。

构建后运行`npm run smoke:references`，独立实例覆盖上传、幂等、格式/权限、任务编辑、审计和固定指标标签；既有smoke/runtime/catalog/perf脚本也分别使用独立临时REFERENCE_DIR，退出只清理各自所有且经路径校验的临时目录。不会清理主预览上传。完整协议导入链另见[HTTP集成测试](../../internal/studio/reference_integration_test.go)。

原始测试PNG/WebP位于[fixture说明](../../internal/studio/testdata/references/README.md)，为仓库原创几何图，不含用户文件。前端只预览服务器512px缩略图；未知上传的原File和原幂等键仅保存在本页内存，刷新前需要处理。验证实际结果及已知限制写入[025](../changes/2026-11-13-025-reference-integration.md)。

## V08 资产和离线维护

[资产库](assets.md)管理名称/标签的不可变元数据历史和回收站，不能替换原件。软删除不释放物理文件，也不影响已受理任务的原请求。新旧内容入口都执行当前active/项目检查；[对象存储](object-storage.md)与[生命周期决策](decisions/0005-private-library-lifecycle.md)说明后端和恢复边界。

应用及维护需要同时持有私有目录OS锁和PG writer锁。007 storage UUID与目录owner marker必须匹配，测试应用使用独立临时目录，不能复用开发库文件或另一实例目录。正常重启应关闭原App后使用同一配套数据库和目录；memory不能写已有PG标记的目录。

`go run ./cmd/maintenance`是默认dry-run的离线工具，不自动读.env、不启动worker、不迁移数据库。必须停应用并配置原所属DATABASE_URL及私有目录；先联合备份，检查候选，再用确切plan_digest显式apply。操作步骤、24小时保护期、扫描预算、失败报告和禁止绕过owner规则见[离线维护](storage-maintenance.md)。自动测试只在临时fixture执行删除，不对开发作品运行apply。

## V11安全链路验证

`npm run test:db`包含真实PG的一次性用途令牌、并发消费/改密/登录、旧会话升级、跨账户及事务回滚，以及真实本机TLS SMTP完整HTTP链和进程Kill后的未知投递保护。测试使用临时CA仅注入测试sender；生产没有跳过证书验证开关。六套smoke与perf显式覆盖所有AUTH_MAIL环境变量，父进程配置不能被继承外发。真实邮件另按[邮件Wiki](mail-delivery.md)受控验收，默认开发模式保持关闭。

011不能由旧程序或旧维护工具操作。先停服务，联备数据库/私有目录，并保留启用邮件时的独立队列密钥。密钥变化且存在未处理密文会拒绝启动，不能删记录掩盖；具体边界见[账户安全](account-security.md)。

## V14-A运营入口与验证

`OPERATIONS_ENABLED=false`默认关闭，环境值只接受`true/false`；启用要求PG和`AUTH_MODE=required`。有配置不代表有权限，角色须停机CLI按用户ID与工作区ID显式授予，注册不自动赋权。`GET /operations/access`实时返回当前授权范围，前端仅显示已授权入口；真正读写仍在服务端独立重核。

工单预演与执行契约见[V14-A](../planning/v14a-contract.md)；完整媒体审核、真实文本渠道、封禁和申诉仍属后续子项。隔离smoke/perf新增`isolatedOperationsEnvironment`强制关闭运营，不继承开发终端的启用设置。V14-A的013/014交付时，账本、角色与维护CLI接受精确001—014；当前V14-B1升级015后，三个CLI均只接受精确001—015，禁止移除版本登记回退。

## V14-B1文本审核开发与测试隔离

默认保持`MODERATION_MODE=disabled`。本地规则演示使用`PROVIDER_MODE=demo`、`MODERATION_MODE=rules`与`MODERATION_POLICY_FILE=config/moderation.example.json`，在独立PG账户环境启动；[文本审核](moderation.md)列出输入/输出block和review的四个fixture marker。规则仅做有限字面匹配，不提供真实语义安全保证。启用后新文本采用全文审核后交付，新媒体生成明确拒绝；历史任务和资产不追溯。

HTTP模式只接受平台自有text-v1严格协议，真实服务尚未选型。本机fixture允许demo下的loopback IP HTTP；其他端点须HTTPS。服务地址、独立密钥和凭据标签都由服务端环境提供，不能来自用户请求、前端变量或测试记录。政策启动后冻结，测试旧策略行为不能靠修改当前政策去假定历史输出已通过。

[scripts/lib/process.mjs](../../scripts/lib/process.mjs)的`isolatedModerationEnvironment`在六套smoke与perf子进程环境中覆盖父进程设置：强制`MODERATION_MODE=disabled`，清空政策路径、端点、密钥和凭据标签。普通回归不会继承开发终端的真实审核配置或被启用后的媒体门禁污染。审核专项自行使用本机HTTP fixture和明确政策；真实审核渠道必须另行受控验收，不能借用这些结果。

引擎专项可运行`go test ./internal/studio -run '^TestModeration' -count=1`；持久化、输入拒绝零受理、候选隔离、提交确认不明和重启路径由独立`TEST_DATABASE_URL`测试覆盖。不要对开发主库运行测试，也不要与其他使用同一测试库的PG套件并行。具体执行事实见[051引擎台账](../changes/2026-11-14-051-text-moderation-engine.md)及[054联合记录](../changes/2026-11-14-054-text-review-integration.md)；本地B1完成不代表完整V14、B2或真实审核渠道放行。
