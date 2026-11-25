# 星图 AI 工作台 · AIGongZuotai

面向个人创作者的 AI 工作台。React + TypeScript 前端，Go 核心后端，按实时 LLM 与异步媒体任务分别组织执行链路。总体设计见 [落地开发方案](docs/deliverables/Go与NewAPI高性能AI工作台开发方案.html)。

工作台支持简体中文、繁體中文、English 和日本語。首次打开时按访问 IP 选择语言：中国大陆使用简体，香港、澳门和台湾使用繁体，日本使用日语，其他地区使用英语。登录页和工作台顶栏均可手动切换，选择会保存在当前浏览器；选择「自动」可恢复按 IP 判断。[地区识别与代理配置](docs/wiki/frontend.md#工作台语言与地区识别)

「本人订单」的详情分别展示收款、退款和 CREDIT 状态。原意向没有绑定商户时显示未核实，测试记录标明测试环境。正式商户仍待选定、接入和受控验收。[035 状态与范围](docs/wiki/decisions/0035-order-payments.md)

登录账户可提交站内客服工单，追问、查看答复并关闭工单。客服须具备该工作区单独授予的客服角色，回复时核验当前密码。[工单范围与配置](docs/wiki/decisions/0034-support-tickets.md)

在「账户安全 → 本人资料下载」选择账户资料、安全事件或本人账户申诉。每页用当前密码确认，下载文件含本页读取时间和创建截止时间。受限账户也可使用此入口。[导出范围与分页](docs/wiki/account-data-export.md)

**「本人订单」可以核对目录条款、登记和取消购买意向。** 当前支付渠道尚未接入，意向不会扣款或发放权益。目录改变或关闭后可核对当时价格、币种、拟购 CREDIT 和原收据。[配置和状态](docs/wiki/decisions/0033-order-intents.md) · [验收记录](docs/changes/2026-11-22-160-order-intent-integration.md)

**侧栏「项目协作」支持定向邀请已有账户。** 对方接受后，可按只读或编辑角色访问当前共享项目的节点草稿。两人编辑相同修订时保留冲突输入，权限变化在下次操作时重新核对。[邀请与协作](docs/wiki/decisions/0032-project-collaboration.md) · [验收记录](docs/changes/2026-11-22-156-project-collaboration-integration.md)

**侧栏「漫剧分镜」支持把故事、角色、场景和项目私图编成分镜本。** 保存后由该版作者确认，可下载可编辑的图文 PPTX。改名后的角色随新稿保存，旧版保留原设定。[操作与限制](docs/wiki/decisions/0031-project-comics.md) · [验收记录](docs/changes/2026-11-22-152-comic-integration.md)

**侧栏「视频剪辑」可以用项目已有视频裁切、重排并导出 MP4。** 登录 PostgreSQL 项目后可保存固定版本并回读历史。Windows 服务器启用受信的 FFmpeg、FFprobe 与独立私有临时目录后开放成片下载。浏览器实测下载了两版，对 125 帧和两个原声音调完成独立核对。030 累计 PostgreSQL 回归 1461.356 秒通过。[配置与剪辑限制](docs/wiki/decisions/0030-video-timelines.md) · [030 验收](docs/changes/2026-11-22-148-video-edit-integration.md)

**本机双制品切换已验证故障回退。** 候选检查失败时继续封流，旧制品重新取得数据库和两个目录的写锁，读回原私图和原键收据后开放。三次交接检查分别用时 2.061、3.478 和 3.949 秒。[脱敏实测](docs/wiki/assets/release-handoff-2026-11-22.json)

**项目演示文稿可从侧栏「演示文稿」进入。** 支持逐页编辑、固定版本、项目私图、放映、离线 HTML、PDF 打印与可编辑 PPTX。实际下载的文件已经在独立软件中改写标题、重新打开并逐页核对。[文稿与导出](docs/wiki/decisions/0029-project-presentations.md) · [下载验收](docs/changes/2026-11-22-138-presentation-integration.md)

**V18-A 已两次从本机独立备份恢复工作台。** 移离原数据库及原文件目录后，原用户在 23.910 秒内重新登录并读回指定草稿、私图和结算记录。多可用区切换仍需在指定环境验收。[恢复报告](docs/wiki/assets/recovery-drill-2026-11-22.json) · [演练命令](docs/wiki/recovery-drill.md)

**V19 工作流已完成本地验收。** 在“创作草稿 → 工作流画布”可从模板生成文字、图片和视频，跨登录选图，预览局部重跑范围或停止后续步骤。最终 025 全量 PostgreSQL 959.929 秒，含三套实际工作流浏览器。[验收台账](docs/changes/2026-11-21-118-workflow-attempt-integration.md)

**当前版本：v0.20.0，V16 已完成本地验收，包括持久聊天回放、执行租约与两个工作区的混合负载测试。V16-A4 分页恢复、有界缓存与 20 万条记录完整启动已本地验收，V16-A3.3媒体公平与持久退避本地已验收，保留A3.2/A3.1/A1/A2能力。** V14-C1工作区新生成限制与复核本地闭环保留。 图片/视频产物先保存为私有候选，整组通过且事务提交成功才开放预览、下载和结算；新参考图先检查原件再登记。V14-B1文本审核与V14-A运营核实继续保留。真实审核商按用户安排后续选型，C1提供工作区新生成限制、复核申请和答复，C2a 内容申诉和原冻结核实已本地验收，C2b 账户限制与 C3 自动额度风控均已本地验收，029 全量 PostgreSQL 1326.832 秒通过，完整V14仍进行中。V00—V06、V08—V10、V12、V16 和 V19 共 13/23 整批已验收，见[TODO](TODO.md)。V11真实邮件与V07真实供应商另验。当前仍是单控制写者、本地开发版本，尚不能作为公众收费站点上线；demo与fixture不代表真实模型或审核能力。

![生成权限与复核答复](docs/wiki/assets/generation-reply-phase15.jpg)

## 当前能力

| 模块 | 已实现 |
| --- | --- |
| 工作台 | 聊天、图像、视频、作品素材、任务中心；模型/完整规格/数量选择、UTF-8字节限制、灵感模板、桌面和移动布局 |
| 模型目录 | 启动加载有限JSON配置、私有映射、逐模型能力版本与ETag；启停/版本仅约束新请求，历史任务保留原快照 |
| 账户与项目 | PostgreSQL 注册/登录/退出、HttpOnly cookie、项目/工作区权限；邮箱验证、密码找回/修改、会话撤销及账户安全记录 |
| 安全邮件 | 独立TLS SMTP、AES-GCM加密持久队列、有限重试；确认不明停止重投，默认关闭，真实渠道待配置 |
| 文本审核 | 默认关闭；实际输入锁外审核、冻结策略、全文通过且事务成功才展示；隔离正文不进历史/上下文，正常新提问略过受限整轮；本地规则与HTTP模拟器已验，真实渠道待选 |
| 媒体审核 | 独立策略、真实原件字节、私有候选与整组交付；租约/取消/恢复隔离、访问门禁和已执行额度待核实，默认关闭，真实渠道待选 |
| 运营核实 | 独立工作区observer/operator/finance角色、异常工单、安全元信息、真实预演与密码确认、原handle核对/REVIEW结算释放、收据和审计；默认关闭，角色仅停机CLI管理 |
| 本机恢复演练 | 独立 PostgreSQL 基础备份与 WAL、版本化对象服务冷备、私有目录和工作台联合复验。[运行方法](docs/wiki/recovery-drill.md) |
| 工作区额度风控 | 默认关闭，可按未结冻结额和每分钟有价受理数设置阈值，误伤复核须绑定原报价。028 联合验证进行中。[配置与范围](docs/wiki/decisions/0028-credit-risk.md) |
| 账户限制与申诉 | 独立授权到目标账户。受限用户重新登录后可查看限制原因、申请复核与读取答复。027 联合验证进行中。[账户治理](docs/wiki/decisions/0027-account-governance.md) |
| 内容申诉 | 已执行的受限文本或媒体可由本人申诉；授权审核员独立审议，有原冻结时由财务另行核实。本人可查原决定及实际释放的内部额度。[内容申诉](docs/wiki/decisions/0026-content-appeal.md) |
| 生成权限与复核 | 显式moderator、限制未来生成、原幂等优先、用户复核、真实预演及当前密码确认、公开答复和已提交回执；解除不放行隔离内容 |
| 创作权益 | 六位整数精度、生成前报价与确认、工作区钱包/流水、原子冻结/结算/释放；未知待核实、停机预演及追加冲正，默认关闭，无真实支付 |
| 聊天 | 持久多会话、不可变分支、真正的多条messages、上下文预算/整轮裁剪、SSE/停止/显式继续；未启用审核时崩溃保留检查点，开启时未审候选仅内存且恢复保持隔离；旧单轮历史保留 |
| 媒体任务 | 集中查询、版本与恢复；普通demo为SVG/视频海报；开启媒体fixture后为固定PNG/一秒MP4，明确非AI生成；live 文生图、图片编辑与文生视频通过服务端适配器接入 |
| 参考素材 | PNG/JPEG/WebP真实校验、私有原件与512px缩略图、项目内复用、有序1—4张引用、上传幂等与原文件重试 |
| 提交正确性 | 创建运行/任务使用幂等键；同键异参 409；网络受理不明时保留原键显式重试 |
| 工作流 | 在画布的工作流入口保存不可变版本，逐步授权文本和图片生成，重新登录可从原项目候选中选图。四步模板可用已批准原件生成视频，支持预览局部重跑范围、独立报价、取消及原键核对。[工作流说明](docs/wiki/decisions/0023-project-workflows.md) |
| 图片编辑配置 | 从侧栏“图片编辑配置”或图像工作室“配置编辑模型”下载非密钥配置，在服务器合并后可刷新并核对实际公开能力。[配置步骤](docs/wiki/references.md#图片编辑配置页) |
| 创作草稿 | PG项目私有云端草稿、不可变版本、并发冲突、原键重试、恢复旧稿为新版；memory保留本地草稿；原文字草稿保留独立版本 |
| 编辑保护 | 未保存切项目/退出提示，同账号同项目重登录恢复本页内存编辑；刷新/关闭前仍须保存或导出 |
| 活动记录 | 项目/工作区范围、游标分页、业务变更与审计同事务、幂等不重复；不含提示词/正文/凭据，非账单 |
| 网关边界 | LLM 实时流、图片/视频异步协议、精确请求体幂等、未知受理状态；密钥仅在服务端 |
| 资产库 | 独立目录与游标搜索、标签、元数据历史、CAS幂等、回收站/恢复；来源区分上传、生成与demo |
| 私有存储 | 本地产物或S3兼容私有对象、512px图片缩略图、鉴权Range下载、copy+verify迁移、默认只读的离线孤立文件检查 |
| 持久通知 | 业务/审计与Outbox同事务、幂等消费、范围游标和有限窗口回放；前端有界补读，不覆盖未保存/未知操作 |
| 任务租约 | PG到期领取、周期续约、owner/epoch/generation/业务版本隔离；无handle的未知提交不自动重发 |
| 媒体执行隔离 | submit/query/delivery/review四通道固定预算及独立PG连接；019首次审核证据、共享审核许可；取消实际退出才释放预算 |
| 恢复 | 无交付清单的已知任务只查询原handle；有清单只补原集合，有完整收据不重下；已完成资产可独立于供应商启用状态下载，编辑保持原引用及正文 |
| 聊天执行调度 | 单写者workspace轮转；全局/工作区/冻结上游模型执行预算、等待队列容量、取消释放和固定指标；媒体独立首次审核已在A3.2验收，持久 token 日志与执行租约已在 V16-B 验收 |
| 资源准入 | 工作区/模型未终态任务与SSE上限；未知任务继续占用，已有幂等重放优先 |
| 运行可观测 | 存活/就绪探针、独立运维令牌指标、请求编号；用户工作区容量与限流冷却反馈 |
| 工程治理 | Wiki、逐次变更台账、README/TODO同步检查、10模块/23批次验收、PR检查及OpenAPI |

## 开发规则

所有贡献者必须先阅读 [AGENTS.md](AGENTS.md)。**任何代码、配置、依赖、测试、文档或规则变更，都必须登记台账，同步更新 README 与相关 Wiki。**

后续工作按 10 个横向模块、23 个纵向批次推进，以 [TODO](TODO.md) 查看当前状态。每批有范围、依赖、验收条件和证据，完成一批更新一批；[任务维护说明](docs/planning/README.md)解释如何更新唯一任务源并生成勾选状态。已验收批次数量不代表整体工期百分比。

- [统一文档入口](docs/README.md) · [完整 TODO](docs/planning/TODO.md) · [持续推进记录](docs/planning/progress.md)
- [Wiki 入口](docs/wiki/README.md)
- [变更台账](docs/changes/README.md)
- [台账模板](docs/changes/TEMPLATE.md)
- [阶段架构决策](docs/wiki/decisions/0001-phase-one.md)
- [当前 OpenAPI 契约](docs/api/openapi.yaml)

## 本地启动

要求 Node.js 22.12+、npm、Go（版本以 `go.mod` 为准）。在仓库根目录执行：

```sh
npm run setup
npm run dev
```

工作台：<http://127.0.0.1:5173>。Go API：<http://127.0.0.1:8080>。`Ctrl+C` 关闭本命令启动的进程。默认配置无需密钥；可将 `.env.example` 复制为 `.env` 调整服务端配置。不得把密钥写入 `VITE_*` 环境变量。

无数据库时默认内存演示模式，服务重启清空运行记录。Windows 可启用仓库独立的便携 PostgreSQL：

```sh
npm run db:setup
npm run dev:db
```

数据库工具和数据位于 LocalAppData 下按工作区路径哈希区分的英文目录，凭据保存在忽略的 `.runtime/postgres.env`，不打印密码；数据库不会随工作台关闭，另用 `npm run db:stop`。已有 PostgreSQL / Linux / macOS 配置 `DATABASE_URL` 后运行 `dev:db`。详见[本地数据库说明](docs/wiki/local-database.md)。

`npm run dev` 读取根 `.env`；`dev:db` 额外补充本地数据库配置并强制启用账户。真实 LLM 要求 `PROVIDER_MODE=live`、`NEWAPI_BASE_URL`（API 根，含 `/v1`）和 `NEWAPI_API_KEY`；不使用目录文件时另需 `NEWAPI_CHAT_MODEL`，使用目录时由对应私有 `upstream_model` 提供映射。异步媒体按[网关说明](docs/wiki/media-gateway.md)单独配置模型、服务根地址、服务端密钥与素材域名白名单。前端按逐模型 capabilities 开放功能；上游无取消接口时不显示虚假的取消成功。

参考素材默认保存至 `REFERENCE_DIR=.runtime/references`，须与静态目录及 `ASSET_DIR` 分离。持久模式应同时备份数据库和两个私有目录；升级016后旧程序不能直接写新库，启用邮件还须联备队列密钥。单图8MiB/16M像素，工作区100条/128MiB，整个参考目录2GiB/4096文件；回收站不释放配额；受引用文件保留，孤立文件只能通过停机维护计划回收。live编辑需要在模型配置中明确启用 `image_edit_supported` 和 `max_reference_images`。详见[参考素材Wiki](docs/wiki/references.md)。

资产库使用独立PG表与项目权限，历史版本只覆盖名称/标签/回收状态，不代表图片或视频内容版本。`ASSET_STORAGE_BACKEND=s3`启用私有对象存储，配置详见[对象存储Wiki](docs/wiki/object-storage.md)。当前参考图、编辑请求文件及S3收据仍依赖私有本地目录，尚不支持无状态多实例。

每个私有目录使用操作系统排他锁，PG数据库UUID与目录角色绑定到owner文件。绑定后的目录禁止用memory模式打开，不能删除owner文件绕过失败；测试/内存演示请使用独立的`REFERENCE_DIR`与`ASSET_DIR`。离线维护默认只生成计划，详见[维护与恢复](docs/wiki/storage-maintenance.md)；升级与回滚必须匹配数据库和私有文件备份。

`TASK_LEASE_SECONDS`默认30、范围5—300；PG worker使用数据库时钟领取/续约。业务控制面和目录锁保持，不能据此同时启动多个工作台服务。持久事件窗口过期时补读资源状态，LLM逐token流仍不是持久日志。详见[事件说明](docs/wiki/events.md)及[任务租约](docs/wiki/scheduler.md)。

模型目录可用 `MODEL_CATALOG_FILE=config/models.example.json` 启用[示例配置](config/models.example.json)，不设置时兼容原三个默认模型；live模式需将私有 `upstream_model` 替换为网关实际模型。目录只在启动时读取，修改后重启；尺寸、比例、分辨率和时长按整组预设提交。配置限制、版本错误、ETag与历史任务规则见[模型 Wiki](docs/wiki/models.md)。当前本地预览启用示例目录，仍使用 demo Provider，不发真实收费请求。

多轮对话由服务端根据选中分支构造上下文，浏览器不上传任意历史。模型配置`context_window_tokens`和`max_output_tokens`；私有`chat_output_limit_field`明确选择上游输出限制字段。估算不等于真实token账单，继续生成是新的模型请求。会话选择ID可在刷新后恢复，未发送输入和受理不明请求仅当前页面内存保护。详见[会话Wiki](docs/wiki/conversation.md)和[Provider契约](docs/wiki/chat-provider.md)。

账户安全入口位于侧栏和账户菜单。未配置邮件时，验证与找回显示明确不可用；改密和撤销当前/全部会话会要求重新登录。邮件启用前须设置服务端`AUTH_MAIL_*`、可信回站地址及独立32字节加密密钥，密钥需随数据库安全备份，配置见[账户安全](docs/wiki/account-security.md)和[邮件投递](docs/wiki/mail-delivery.md)。本地自动测试仅使用独立TLS模拟器，不会继承真实邮件配置外发。

构建后可在根目录直接运行 `.runtime/server.exe`（Windows）或 `./.runtime/server`（Linux/macOS），由 Go 同源提供 `apps/workbench/dist` 和 API，访问 <http://127.0.0.1:8080>。这个入口仍是本地开发模式，不能直接部署成公众收费站点。

V12权益配置已接入，默认关闭；`BILLING_MODE=credits`须数据库、账户模式和`BILLING_PRICE_FILE`。本地示例价格只用于demo，新钱包不自动赠送额度。报价确认后冻结最大值，可信用量/交付结果结算，未知保留待核实；离线预演/核账/一次全额冲正见[配置与操作](docs/wiki/billing-operations.md)。真实支付仍属V13。

运营配置`OPERATIONS_ENABLED=true`要求PG和`AUTH_MODE=required`；注册及工作区owner不自动获权。先停服务，通过`go run ./cmd/operations`显式授予目标用户/工作区角色，默认只预演，执行须精确摘要和稳定操作键，详见[角色管理](docs/wiki/operations-access.md)。授权后侧栏出现[运营核实](docs/wiki/operations-workbench.md)；此入口不提供在线赠额、支付退款、内容审核或封禁。016升级继续联备数据库、私有目录和已启用邮件时的独立密钥。

文本审核默认 `MODERATION_MODE=disabled`；本地闭环使用PG账户模式、demo Provider、`MODERATION_MODE=rules` 与 `MODERATION_POLICY_FILE=config/moderation.example.json`。真实接入使用 `http` 及服务端适配配置，协议见[审核引擎](docs/wiki/moderation.md)；它不是New API通用接口，尚无真实供应商验收。启用后完整回复通过检查才展示，未审候选不保存；输入拒绝不冻结，已执行但隔离的输出保留待核实。只开文本审核时拒绝新图片/视频生成；同时开启下述媒体审核后才开放。历史内容不批量追审，但新编辑请求须重新检查实际引用原件。

媒体审核默认 `MEDIA_MODERATION_MODE=disabled`。本地需同时启用文本规则，并设置 `MEDIA_MODERATION_MODE=fixtures`、`MEDIA_MODERATION_POLICY_FILE=config/media-moderation.example.json`；必须使用PG账户模式。fixture只识别仓库登记的完整PNG/MP4字节；未知文件不自动批准。真实服务使用私有流式multipart `media-v1` 适配协议，见[引擎](docs/wiki/media-moderation.md)、[执行链](docs/wiki/media-moderation-execution.md)、[工作台](docs/wiki/media-moderation-workbench.md)和[ADR0012](docs/wiki/decisions/0012-media-review-gate.md)。已登记候选纳入所有终态的存储可达性；没有人工重审/自动放行入口。

v0.14历史验证：270项前端/26文件、8项治理、Go vet/测试/构建、真实PG全量192.482秒和六套隔离冒烟通过；新增PARTIAL定向PG1.675秒。已完成016联备升级、桌面/390手机验收、私有视频实际播放和空参考报价白屏修复。详见[058联合台账](docs/changes/2026-11-14-058-media-review-integration.md)与[联合证据](docs/wiki/assets/media-moderation-validation-v0.14-2026-11-14.json)。主JS505.19kB触发分包警告，后续按路由拆包；[本机demo回归](docs/wiki/performance.md)不代表审核吞吐或生产容量。

## 工程命令

媒体通过`MEDIA_SUBMIT_WORKERS`、`MEDIA_QUERY_WORKERS`、`MEDIA_DELIVERY_WORKERS`配置三通道，v0.19通过`MEDIA_REVIEW_WORKERS`配置独立审核通道。默认各2；提交/查询显式十进制1—32，交付1—16，审核1—2，重启生效。审核输出与参考输入共用原两个引擎许可；新增worker不扩大总审核并发。原生异步适配器先持久保存加密清单，再下载并交接首次审核；普通SVG demo没有真实下载。PG接入原生异步媒体须先设置独立32字节标准base64的`MEDIA_DELIVERY_KEY`，不得复用邮件或网关密钥，必须与PG、私有目录及对应对象版本联备。已有交付即使终态仍需原密钥，不能关闭provider绕过。边界见[媒体交付Wiki](docs/wiki/media-delivery.md)及[A3.2契约](docs/planning/v16a32-contract.md)，此设置不代表供应商QPS或多实例共享配额。v0.20新增MEDIA_WORKSPACE_CONCURRENCY（默认1）和MEDIA_CHANNEL_CONCURRENCY（默认2），每lane按工作区/冻结渠道约束，显式1—32；本地公平、持久退避及积压预选已验收，不能推定生产吞吐。

| 命令 | 用途 |
| --- | --- |
| `npm run setup` | 按前端锁文件安装依赖 |
| `npm run dev` | 启动 Go API 与 Vite，包含服务就绪检查 |
| `npm run dev:db` | 启动 PostgreSQL 账户模式工作台 |
| `npm run db:setup` / `db:start` / `db:stop` / `db:status` | Windows 便携 PostgreSQL 初始化与启停 |
| `npm run build` | 构建前端与 Go 二进制到 `.runtime/` |
| `npm test` | 任务治理、Go 与前端关键逻辑测试 |
| `npm run test:db` | 使用独立 TEST_DATABASE_URL 运行持久化、身份与恢复测试 |
| `npm run check` | TODO一致性、Go vet、测试、生产构建、文档结构与链接检查 |
| `npm run check:changes` | 检查当前变更是否同步 README、Wiki、台账、任务源及TODO |
| `npm run todo:sync` | 从任务源生成 TODO，完成状态必须具备验收证据 |
| `npm run check:todo` / `test:todo` | 检查批次依赖、完成门槛、生成一致性及治理规则测试 |
| `npm run smoke` | 启动独立临时 demo 服务验证完整链路，结束后关闭；先运行 build，不接触现有网关 |
| `npm run smoke:runtime` | 独立小限额实例验证探针、指标权限、准入/幂等与SSE名额释放 |
| `npm run smoke:catalog` | 独立目录实例验证模型映射、版本/ETag、精确规格、重启启停与幂等 |
| `npm run smoke:references` | 独立参考素材实例验证上传/幂等/权限、编辑链路、审计及指标；不发真实请求 |
| `npm run smoke:library` | 独立资产库实例验证检索、权限、版本、回收恢复、旧URL拒绝及审计 |
| `npm run smoke:conversations` | 独立Go+HTTP网关fixture验证真实多轮messages、编辑分支、继续、预算裁剪、原键与权限；不发真实收费请求 |
| `go run ./cmd/storage` | 离线S3迁移计划；显式apply后复制与完整校验，本地副本保留 |
| `go run ./cmd/maintenance` | 停机检查孤立文件；apply必须携带当前计划摘要 |
| `go run ./cmd/ledger` | 停机核账、赠额、一次全额权益冲正及待核实处置；默认只预演 |
| `go run ./cmd/operations` | 停机inspect/grant/revoke运营角色；默认预演，001—015格式栅栏，稳定操作键与精确摘要 |
| `npm run perf:local` | 隔离 demo 性能基线，输出分位数及机器环境；不代表生产容量 |

## 目录

```text
apps/workbench/          React 工作台
cmd/server/             本地组合部署 Go 入口
cmd/storage/            私有产物到S3的离线迁移
cmd/maintenance/        绑定数据库身份的离线孤立文件维护
cmd/ledger/             权益账本离线预演、核账与处置
cmd/operations/         工作区运营角色停机预演与授予/撤销
internal/               Go 领域、执行、适配与 HTTP 服务
scripts/                启动、检查与开发工具
docs/wiki/              架构、开发、模块说明、路线及 ADR
docs/changes/           逐次变更台账
docs/api/               当前已实现 API 的 OpenAPI 契约
docs/planning/          模块/批次任务源与维护规则，生成根 TODO.md
docs/deliverables/      原始方案与离线交互蓝图
docs/planning/          完整TODO、任务源与持续推进记录
docs/testing/           测试样本来源和用途说明
db/migrations/         内嵌数据库SQL脚本
```

本机 035 已完成收退款权益、作品库万条索引与剪辑回执的累计验证。工作台 553 项测试和六套冒烟通过。[验收记录](docs/planning/progress.md) · [作品库数据](docs/wiki/assets/performance-library-035.json) · [当前制品依赖](docs/wiki/assets/workbench-035.spdx.json)。

## 最近变更

040 支持为原任务查询单独配置执行位、工作区上限、渠道上限和 PostgreSQL 连接数。修改后需重启。大于 32 的配置须显式启用查询连接池。[配置及实测范围](docs/wiki/media-original-query-capacity.md)

视频工作室支持从项目素材库选择已交付 MP4，分别报价编辑或延长。工作台按原键查询受理不明的任务。本机 Chromium 已验证响应正文截断与无响应头重送两种情况。[配置与验收范围](docs/wiki/decisions/0039-source-video.md)

- [262 · 原 GET 进度保存的 Outbox 分段耗时与事务归属](docs/changes/2026-11-25-262-outbox-progress-save-timing.md)
- [260 · 原查询保存时的工作区计数成本](docs/changes/2026-11-25-260-media-workspace-count-experiment.md)
- [259 · 定速媒体受理与完整生命周期](docs/changes/2026-11-24-259-openloop-media-lifecycle.md)
- [258 · 受管聊天 1,024 路并发对照](docs/changes/2026-11-24-258-managed-chat-1024.md)
- [257 · 正常受理任务的多轮原查询](docs/changes/2026-11-24-257-recurring-original-query.md)
- [256 · 同一服务的混合运行与重启核验](docs/changes/2026-11-24-256-mixed-service-soak.md)
- [255 · 原查询预选的公平页对照](docs/changes/2026-11-24-255-original-query-page.md)
- [254 · 六万项正常媒体受理及原库重开](docs/changes/2026-11-24-254-normal-admission-60000.md)（本机通过，重开后零新增生成提交）
- [253 · 受管模型全执行位联合验收](docs/changes/2026-11-23-253-managed-chat-512.md)
- [252 · 准入配额与原库恢复扫描](docs/changes/2026-11-23-252-admission-thresholds-and-recovery.md)
- [251 · 040 同源程序累计回归](docs/changes/2026-11-23-251-format-040-regression.md)（本机 040 完整实库 789 项通过、31 项跳过、0 项失败，六组同制品冒烟通过）
- [250 · 原查询结果合批的实库对照](docs/changes/2026-11-23-250-known-query-save-batch.md)
- [249 · 原任务查询的并发登记](docs/changes/2026-11-23-249-large-query-entry-pin.md)
- [248 · 原任务查询的分段耗时](docs/changes/2026-11-23-248-query-stage-timing.md)
- [247 · 窄屏报价的完整原片编号](docs/changes/2026-11-23-247-source-quote-narrow.md)
- [246 · 经正式接口受理的媒体在途任务](docs/changes/2026-11-23-246-normal-media-admission-capacity.md)
- [245 · 039 至 040 的原任务恢复与渠道探测](docs/changes/2026-11-23-245-media-query-format-restore.md)
- [244 · 大档原任务查询与有界派发](docs/changes/2026-11-23-244-large-original-query.md)
- [243 · 039 本机累计回归](docs/changes/2026-11-23-243-format-039-regression.md)
- [242 · 媒体原任务查询的独立 PostgreSQL 连接预算](docs/changes/2026-11-23-242-query-lease-pool.md)
- [241 · 媒体原 handle 查询的到期与发起延迟](docs/changes/2026-11-23-241-media-original-query-capacity.md)
- [240 · 受管模型执行容量与 264 路联合入口](docs/changes/2026-11-23-240-managed-chat-capacity.md)
- [239 · 真实浏览器的原视频报价与受理恢复](docs/changes/2026-11-23-239-source-video-browser.md)
- [238 · 原视频操作与停写联备验收](docs/changes/2026-11-23-238-source-video-integration-restore.md)
- [237 · 项目事件按游标共享已提交的通知页](docs/changes/2026-11-23-237-shared-project-feed.md)

- [236 · 视频工作室选择原片、报价和查询原收据](docs/changes/2026-11-23-236-source-video-workbench.md)
- [234 · 冻结私有原视频的网关请求](docs/changes/2026-11-23-234-source-video-provider.md)
- [233 · 真实项目 SSE 广播与断线恢复](docs/changes/2026-11-23-233-project-sse-fanout.md)
- [232 · 授权订阅的分页会话复核](docs/changes/2026-11-23-232-paged-stream-session-admission.md)

- [235 · 私有原视频编辑与延长受理](docs/changes/2026-11-22-235-video-source-admission.md)

- [231 · 按用户选择暂缓正式支付接入](docs/changes/2026-11-22-231-payment-channel-deferred.md)

- [230 · 根任务导航显示已验证子项与最新台账](docs/changes/2026-11-22-230-todo-entry-acceptance.md)

- [229 · 单镜视频窄屏表单标签对比度](docs/changes/2026-11-22-229-panel-video-label-contrast.md)

- [228 · 共享会话批量复核的返回位置](docs/changes/2026-11-22-228-stream-session-batch-positions.md)

- [227 · 037 与 038 本地制品复验](docs/changes/2026-11-22-227-local-acceptance.md) · [037 半小时实测](docs/wiki/assets/performance-long-reply-037.json)

- [222 · 已确认分镜的视频报价与受理](docs/changes/2026-11-22-222-comic-panel-video.md) · [223 · 分镜产物替换指定剪辑镜头](docs/changes/2026-11-22-223-panel-video-timeline-replacement.md) · [224 · 工作台单镜生成与原键核对](docs/changes/2026-11-22-224-panel-video-workbench.md)

- [226 · 四语界面与首次访问 IP 地区识别](docs/changes/2026-11-22-226-workbench-languages.md)

- [221 · 账户切换后的首个草稿输入](docs/changes/2026-11-22-221-account-composer-scope.md)

- [225 · 固定分镜生成与局部重做集成](docs/changes/2026-11-22-225-panel-video-integration.md)

- [220 · 80 个模型流、241 个订阅与批量持久化联合验收](docs/changes/2026-11-22-220-integrated-chat-sse-acceptance.md)

- [219 · 192 个授权订阅、撤销与重连](docs/changes/2026-11-22-219-authenticated-sse-sessions.md)

- [216 · 036 到 037 的跨格式回退演练](docs/changes/2026-11-22-216-cross-format-recovery.md) · [217 · 同步备机切换与原业务收据](docs/changes/2026-11-22-217-postgresql-sync-standby.md)

- [218 · 共享会话核验与全局订阅容量](docs/changes/2026-11-22-218-stream-session-batches.md)

- [215 · 037 制品与 SPDX 复核](docs/changes/2026-11-22-215-release-evidence-037.md)

- [214 · 真实慢 TCP 读者与订阅上限](docs/changes/2026-11-22-214-sse-socket-acceptance.md)

**托管聊天已完成 80 路本机验收。** 两个子进程各持有 40 条模型连接，80 份结算与 960 条授权增量核对通过。[正式报告](docs/wiki/assets/performance-managed-chat-037.json) · [208 验收](docs/changes/2026-11-22-208-managed-chat-integration.md)。

**64 路会话增量的批写已在本机验证。** 默认关闭。两组各 768 条增量逐条可回放；启用后写入这些增量使用 24 次物理提交。[性能报告](docs/wiki/assets/performance-batched-deltas-037.json) · [212 验收](docs/changes/2026-11-22-212-business-delta-batch-integration.md)。

- [213 · 长回复虚拟行与原提示滚回核对](docs/changes/2026-11-22-213-virtualized-live-reply-observation.md)

- [212 · 真实工作台增量批量提交集成](docs/changes/2026-11-22-212-business-delta-batch-integration.md)

- [209 · 原增量表的跨运行 PostgreSQL 提交](docs/changes/2026-11-22-209-batched-run-delta-store.md) · [210 · 跨运行增量的有界协调与开关](docs/changes/2026-11-22-210-run-delta-coordinator.md) · [211 · 真实聊天流的批量提交前后对照](docs/changes/2026-11-22-211-batched-delta-acceptance.md)

- [208 · 受管聊天执行的集成与故障验收](docs/changes/2026-11-22-208-managed-chat-integration.md)

- [207 · 受管会话的授权 SSE 与真实进程归属验收](docs/changes/2026-11-22-207-managed-chat-acceptance.md)

- [205 · 私管道的受管聊天执行进程](docs/changes/2026-11-22-205-chat-execution-child-transport.md) · [206 · 受管与本地执行共用调度预算](docs/changes/2026-11-22-206-managed-chat-scheduling.md)

- [204 · 运行 SSE 断线回放的只读连接隔离](docs/changes/2026-11-22-204-run-replay-read-pool.md)

- [203 · PR 启动资源预算与制品差异](docs/changes/2026-11-22-203-pr-bundle-diff.md)

- [202 · 分片批量持久日志与并发回放试验](docs/changes/2026-11-22-202-sharded-stream-journal-poc.md)

- [201 · 64 个同时活跃的模型流和授权 SSE](docs/changes/2026-11-22-201-active-stream-capacity.md)

- [199 · 启动 JavaScript 实际 gzip 传输](docs/changes/2026-11-22-199-startup-javascript-compression.md)

- [200 · 受限网络下的冷暖工作台与深历史](docs/changes/2026-11-22-200-workbench-network-browser.md)

登录 PostgreSQL 工作台，在「创作草稿 → 自由画布」可编辑节点、连接和分组，保存修订，并查看本项目的私有图片和运行状态。[数据与使用范围](docs/wiki/decisions/0037-project-canvas.md)。

- [195 · 项目画布保存与私有来源](docs/changes/2026-11-22-195-project-canvas-versions.md) · [196 · 自由画布的编辑与无障碍操作](docs/changes/2026-11-22-196-free-canvas-workbench.md) · [197 · 两档真实图的平移测量](docs/changes/2026-11-22-197-canvas-pan-browser.md) · [198 · 画布集成、精确格式与恢复](docs/changes/2026-11-22-198-project-canvas-integration.md)

使用 PostgreSQL 时可显式开启长会话。本机已验证一条分支 5,000 轮、500 页及淘汰后的原文重读。默认配置仍按 128 个分支节点受理新写入。[配置与恢复](docs/wiki/decisions/0036-long-conversations.md)。

- [191 · 可回读的消息正文缓存](docs/changes/2026-11-22-191-chat-message-body-cache.md) · [192 · 持久会话祖先与上下文](docs/changes/2026-11-22-192-durable-conversation-ancestry.md) · [193 · 长会话配置、格式与恢复](docs/changes/2026-11-22-193-long-history-format-and-restore.md) · [194 · 真实万条分支验收](docs/changes/2026-11-22-194-deep-conversation-acceptance.md)

本机已验收单分支 128 条消息的连续翻页、键盘导航和窄屏阅读；前插或改变宽度时，原消息锚点最大偏移约 0.14 CSS 像素。[浏览器与数据库实测](docs/wiki/assets/performance-conversation-history-035.json)。

本机 PostgreSQL 和 Chromium 已完成一次连续 30 分钟的聊天。30 次检查点均见正文增长，重开原库能读回全部 25,214 字节。[实际样本](docs/wiki/assets/performance-long-reply-035.json)。

- [187 · 可变行高的消息列表](docs/changes/2026-11-22-187-variable-height-chat-timeline.md) · [188 · 按页读取持久会话正文](docs/changes/2026-11-22-188-conversation-message-pages.md) · [189 · 128条分支消息的浏览器验收](docs/changes/2026-11-22-189-persistent-conversation-history-browser.md)

- [190 · 前端放行指标与待验范围](docs/changes/2026-11-22-190-frontend-acceptance-coverage.md)

- [183 · 持久会话 30 分钟回复验收](docs/changes/2026-11-22-183-durable-long-reply.md) · [184 · 标准服务聊天执行期限](docs/changes/2026-11-22-184-chat-run-timeout.md) · [185 · 可核对的本机长回复流](docs/changes/2026-11-22-185-long-reply-gateway-and-observation.md) · [186 · 活跃回复中的可信输入和滚动](docs/changes/2026-11-22-186-long-reply-browser.md)

- [180 · 万条作品索引及项目切换实测](docs/changes/2026-11-22-180-private-library-browser-resources.md)

- [182 · 剪辑稿保存回执与选择框同步](docs/changes/2026-11-22-182-video-timeline-selection.md)

- [181 · 作品库可见卡片与退出清理](docs/changes/2026-11-22-181-library-virtual-grid.md)

- [179 · 发布前本地安全证据](docs/changes/2026-11-22-179-local-release-security-evidence.md) · [核对用例和渠道条件](docs/wiki/release-security-evidence.md)

- [177 · 订单资金与权益集成](docs/changes/2026-11-22-177-order-payment-integration.md) · [176 · 详情状态](docs/changes/2026-11-22-176-order-payment-workbench.md) · [175 · 本人只读接口](docs/changes/2026-11-22-175-order-payment-status-http.md) · [174 · 资金事务核心](docs/changes/2026-11-22-174-order-payment-store.md)

- [178 · 制品依赖与许可核对](docs/changes/2026-11-22-178-release-sbom.md) · [固定 034 清单](docs/wiki/assets/workbench-034.spdx.json)

- [173 · 订单资料只读对账](docs/changes/2026-11-22-173-offline-order-reconciliation.md)。可在工作台运行时用只读数据库账号，将归一的商户资料与指定工作区的原订单条款作比对。[用法与报告范围](docs/wiki/decisions/0033-order-intents.md#商户资料只读比对)

- [172 · GC、数据库等待及慢订阅者](docs/changes/2026-11-22-172-mixed-resource-observation.md) · [171 · 工作台浏览器基准](docs/changes/2026-11-22-171-workbench-frontend-benchmark.md)。测量配置及报告见[性能 Wiki](docs/wiki/performance.md)。

项目协作与漫剧分镜均已完成本机累计验收。032 全量 PostgreSQL 1639.820 秒通过，覆盖已修正的内容申诉准备和 MP4 下载清理两项。[032 验收](docs/changes/2026-11-22-156-project-collaboration-integration.md)

- [170 · 客服工单联验](docs/changes/2026-11-22-170-support-ticket-integration.md) · [169 · 工单工作台](docs/changes/2026-11-22-169-support-ticket-workbench.md) · [168 · 权限与接口](docs/changes/2026-11-22-168-support-ticket-access-http.md) · [167 · 工单与收据](docs/changes/2026-11-22-167-support-ticket-store.md)

- [164 · 本人资料下载联验](docs/changes/2026-11-22-164-personal-data-export-integration.md) · [163 · 下载工作台](docs/changes/2026-11-22-163-account-data-export-workbench.md) · [162 · 后端导出](docs/changes/2026-11-22-162-account-data-export.md)

- [160 · 本人订单与联备](docs/changes/2026-11-22-160-order-intent-integration.md) · [159 · 订单工作台](docs/changes/2026-11-22-159-order-intent-workbench.md) · [158 · 受信目录](docs/changes/2026-11-22-158-order-intent-catalog-http.md) · [157 · 意向存储](docs/changes/2026-11-22-157-order-intent-store.md)

- [156 · 站内协作与恢复](docs/changes/2026-11-22-156-project-collaboration-integration.md) · [155 · 协作工作台](docs/changes/2026-11-22-155-project-collaboration-workbench.md) · [154 · 同稿编辑](docs/changes/2026-11-22-154-shared-project-draft.md) · [153 · 邀请与角色](docs/changes/2026-11-22-153-project-collaboration-members.md)

- [166 · 下载清理完成边界](docs/changes/2026-11-22-166-video-export-cleanup-verification.md) · [165 · 原审核任务的已提交终态](docs/changes/2026-11-22-165-content-appeal-fixture-completion.md) · [031 本机资源采样](docs/wiki/assets/performance-owned-031-2026-11-22.json)

- [161 · 负载阶段的实际 CPU、驻留内存与句柄取样](docs/changes/2026-11-22-161-owned-resource-sampling.md)

- [152 · 漫剧分镜实测](docs/changes/2026-11-22-152-comic-integration.md) · [151 · 工作台](docs/changes/2026-11-22-151-comic-workbench.md) · [150 · 确认与私图](docs/changes/2026-11-22-150-comic-private-export.md) · [149 · 角色、场景与分镜修订](docs/changes/2026-11-22-149-comic-store.md)

- [148 · 双版本成片与恢复验收](docs/changes/2026-11-22-148-video-edit-integration.md) · [147 · 私有导出](docs/changes/2026-11-22-147-timeline-private-export.md) · [146 · 不可变剪辑版本](docs/changes/2026-11-22-146-video-timeline-store.md) · [145 · 剪辑工作台](docs/changes/2026-11-22-145-video-edit-workbench.md)
- [144 · 有界真实视频裁切和拼接](docs/changes/2026-11-22-144-bounded-video-export.md)
- [143 · 单写者值班与故障交接](docs/changes/2026-11-22-143-release-operations.md)
- [141 · 分环境可重现制品与文件校验](docs/changes/2026-11-22-141-local-release-artifacts.md)
- [140 · 工作台进程退出与三锁归属](docs/changes/2026-11-22-140-release-process.md)
- [142 · 单写者故障回退与联合验收](docs/changes/2026-11-22-142-release-handoff-integration.md)
- [139 · 本机入口排空与候选核验](docs/changes/2026-11-22-139-release-ingress.md)
- [138 · 项目文稿版本与独立导出验收](docs/changes/2026-11-22-138-presentation-integration.md)
- [137 · 演示文稿编辑、放映与下载](docs/changes/2026-11-22-137-presentation-workbench.md)
- [136 · 私图鉴权、导出清单和联备](docs/changes/2026-11-22-136-presentation-http-export.md)
- [135 · 文稿与不可变版本](docs/changes/2026-11-22-135-presentation-store.md)

- [134 · 观察真实行锁后再中断恢复](docs/changes/2026-11-22-134-recovery-barrier-observation.md)
- [133 · 工作台恢复点、私有资产与结算联合演练](docs/changes/2026-11-22-133-recovery-platform-integration.md)
- [132 · 版本化对象存储冷备与独立还原](docs/changes/2026-11-22-132-object-store-cold-backup.md)
- [131 · PostgreSQL 命名恢复点与物理备份](docs/changes/2026-11-22-131-postgresql-pitr-drill.md)
- [130 · 工作区风控配置与联合验收](docs/changes/2026-11-22-130-credit-risk-integration.md)
- [129 · 额度风控与原报价复核工作台](docs/changes/2026-11-22-129-credit-risk-workbench.md)
- [128 · 原报价复核与独立运营审批](docs/changes/2026-11-22-128-credit-risk-review.md)
- [127 · 冻结上限、频率与单次许可](docs/changes/2026-11-22-127-credit-risk-store.md)
- [126 · 账户限制、本人复核与恢复的联合验证](docs/changes/2026-11-22-126-account-governance-integration.md)
- [125 · 受限身份与账户治理工作台](docs/changes/2026-11-22-125-account-governance-workbench.md)
- [124 · 账户准入、事件流与离线授权](docs/changes/2026-11-22-124-account-governance-auth.md)
- [123 · 指定账户授权、限制轮次与原申诉](docs/changes/2026-11-22-123-account-restriction-store.md)
- [122 · 三账户申诉、备份及释放验收](docs/changes/2026-11-21-122-content-appeal-integration.md)
- [121 · 申请人、审核员和财务工作台](docs/changes/2026-11-21-121-content-appeal-workbench.md)
- [120 · 申诉转交及原冻结的财务收据](docs/changes/2026-11-21-120-content-appeal-finance.md)
- [119 · 已执行输出的原证据与独立审议](docs/changes/2026-11-21-119-content-appeal-store.md)

- [118 · 进程强杀、项目补读及新尝试验收](docs/changes/2026-11-21-118-workflow-attempt-integration.md)
- [117 · 重跑范围、原图与取消收据工作台](docs/changes/2026-11-21-117-workflow-attempt-workbench.md)
- [116 · 停止后续步骤与原子资源核对](docs/changes/2026-11-21-116-workflow-cancellation.md)
- [115 · 局部派生、独立报价和原范围继承](docs/changes/2026-11-21-115-workflow-rerun.md)

- [114 · 双原件到私有视频的联合验收](docs/changes/2026-11-21-114-workflow-video-integration.md)
- [113 · 四步模板、首帧核对及私有播放器](docs/changes/2026-11-21-113-workflow-video-workbench.md)
- [112 · 四步图、项目选图与视频交付](docs/changes/2026-11-21-112-workflow-video.md)
- [111 · 私有视频首帧协议、实际文件与独立报价](docs/changes/2026-11-21-111-video-first-frame-provider.md)

- [110 · 图片编辑配置准备页](docs/changes/2026-11-21-110-image-edit-setup.md)
- [109 · 工作流升级、故障与联备验收](docs/changes/2026-11-21-109-workflow-integration.md)
- [108 · 工作流画布与跨会话操作](docs/changes/2026-11-21-108-workflow-workbench.md)
- [107 · 步骤授权、模型快照和权益](docs/changes/2026-11-21-107-workflow-execution.md)
- [106 · 项目内流程版本、原候选和人工选择](docs/changes/2026-11-21-106-workflow-store.md)

- [105 · 工作流模板、选图和视频输入的分批验收](docs/changes/2026-11-21-105-workflow-execution-plan.md)
- [104 · 双工作区聊天与媒体混合负载](docs/changes/2026-11-21-104-mixed-workload.md)
- [103 · 聊天执行失权、升级与联备集成](docs/changes/2026-11-21-103-run-lease-integration.md)
- [102 · 独立连接竞争与迟到审核](docs/changes/2026-11-21-102-run-lease-contention.md)
- [101 · 执行续租、取消与真实退出](docs/changes/2026-11-21-101-run-lease-execution.md)
- [100 · 执行租约与数据库条件写入](docs/changes/2026-11-21-100-run-lease-store.md)

- [099 · 持久聊天流升级、故障与联备集成](docs/changes/2026-11-21-099-run-stream-integration.md)
- [098 · 原游标回放与活动 SSE 交接](docs/changes/2026-11-21-098-run-stream-replay.md)
- [097 · 聊天执行、取消与审核门禁](docs/changes/2026-11-21-097-run-stream-execution.md)
- [096 · 021 聊天增量与快照事务](docs/changes/2026-11-21-096-run-stream-store.md)
- [095 · 聊天增量持久化与断线回放的实施契约](docs/changes/2026-11-21-095-durable-run-stream-plan.md)
- [094 · 有界缓存与完整启动联合验证](docs/changes/2026-11-21-094-bounded-startup.md)
- [093 · PG 工作区项目查询与创建上限](docs/changes/2026-11-21-093-workspace-project-queries.md)
- [092 · 媒体领取与在途缓存占位](docs/changes/2026-11-21-092-media-cache-pins.md)
- [091 · 聊天执行与活动 SSE 的缓存占位](docs/changes/2026-11-21-091-run-cache-pins.md)

- [090 · PG 媒体受理与缓存容量分离](docs/changes/2026-11-21-090-pg-media-cache-admission.md)

- [089 · A4.1 恢复故障与完整启动联合验证](docs/changes/2026-11-21-089-recovery-acceptance.md)

- [086 · 分页恢复与先行generation栅栏（实施/待验）](docs/changes/2026-11-15-086-paged-recovery.md)
- [087 · 完整启动分页恢复独立验收（实施/待验）](docs/changes/2026-11-15-087-recovery-validation.md)
- [088 · 完整启动恢复故障验收（实施/待验）](docs/changes/2026-11-15-088-recovery-faults.md)
- [085 · 恢复私有阶段有界批读](docs/changes/2026-11-15-085-recovery-private-phases.md) · [实现与限制](docs/wiki/recovery-private-phases.md)
- [084 · 分页恢复与有界缓存设计启动](docs/changes/2026-11-15-084-recovery-plan.md) · [A4契约](docs/planning/v16a4-contract.md)
- [083 · 媒体公平调度与联合验收](docs/changes/2026-11-15-083-media-scheduler-integration.md) · [A3.3契约](docs/planning/v16a33-contract.md)

- [079 · V16-A3.2首次审核分派与联合验收](docs/changes/2026-11-14-079-media-review-dispatch-integration.md)
- [075 · V16-A3.1持久私有交付与联合验收](docs/changes/2026-11-14-075-media-delivery-integration.md)


- [071 · V16-A2媒体执行隔离与联合验收](docs/changes/2026-11-14-071-media-lane-integration.md)

- [065 · 聊天公平调度核心](docs/changes/2026-11-14-065-execution-scheduler.md)
- [066 · 聊天执行独立验收](docs/changes/2026-11-14-066-execution-validation.md)
- [067 · V16-A1配置、观测与集成](docs/changes/2026-11-14-067-execution-integration.md)

- [063 · 文档归档与TODO双入口治理](docs/changes/2026-11-14-063-document-layout.md)
- [064 · SQL统一归档与嵌入](docs/changes/2026-11-14-064-sql-layout.md)

- [059：生成权限与复核核心](docs/changes/2026-11-14-059-generation-controls.md)
- [060：新生成准入与角色](docs/changes/2026-11-14-060-generation-admission.md)
- [061：复核工作台与恢复保护](docs/changes/2026-11-14-061-generation-controls-workbench.md)
- [062：V14-C1联合验收](docs/changes/2026-11-14-062-generation-controls-integration.md)
- [055：媒体策略与私有字节](docs/changes/2026-11-14-055-media-moderation-engine.md)
- [056：媒体执行与交付事务](docs/changes/2026-11-14-056-media-moderation-execution.md)
- [057：媒体审核工作台](docs/changes/2026-11-14-057-media-moderation-workbench.md)
- [058：V14-B2联合验收](docs/changes/2026-11-14-058-media-review-integration.md)

- [051 · 文本审核引擎与策略](docs/changes/2026-11-14-051-text-moderation-engine.md)
- [052 · 文本准入与全文交付隔离](docs/changes/2026-11-14-052-moderation-execution.md)
- [053 · 文本审核工作台](docs/changes/2026-11-14-053-moderation-workbench.md)

- [054 · V14-B1文本审核联合验收](docs/changes/2026-11-14-054-text-review-integration.md)

- [047 · 运营权限与角色治理](docs/changes/2026-11-14-047-operations-access.md)
- [048 · 异常工单与预演处置](docs/changes/2026-11-14-048-operations-cases.md)
- [049 · 运营异常核实工作台](docs/changes/2026-11-14-049-operations-workbench.md)

- [050 · V14-A运营异常核实联合验收](docs/changes/2026-11-14-050-operations-integration.md)

- [043 · 权益账本核心](docs/changes/2026-11-13-043-credit-ledger.md)
- [044 · LLM结构化用量](docs/changes/2026-11-13-044-provider-usage.md)
- [045 · 工作区权益与报价确认](docs/changes/2026-11-13-045-billing-workbench.md)
- [046 · V12权益账本联合验收](docs/changes/2026-11-13-046-billing-integration.md)

- [039 · 账户安全与自助恢复](docs/changes/2026-11-13-039-account-security.md)
- [040 · 加密安全邮件与SMTP](docs/changes/2026-11-13-040-security-mail.md)
- [041 · 账户安全工作台](docs/changes/2026-11-13-041-account-security-workbench.md)
- [042 · V11账户安全本地联合验收](docs/changes/2026-11-13-042-account-security-integration.md)

- [035 · 持久会话与不可变消息树](docs/changes/2026-11-13-035-conversation-core.md)
- [036 · 多消息Provider与上下文预算](docs/changes/2026-11-13-036-chat-provider.md)
- [037 · 多轮对话工作台](docs/changes/2026-11-13-037-conversations-workbench.md)
- [038 · V10联合验收](docs/changes/2026-11-13-038-conversation-integration.md)

- [031 · 持久事件与断点回放](docs/changes/2026-11-13-031-durable-events.md)
- [032 · 任务租约与隔离](docs/changes/2026-11-13-032-task-leases.md)
- [033 · 事件补齐工作台](docs/changes/2026-11-13-033-durable-events-client.md)
- [034 · V09联合验收](docs/changes/2026-11-13-034-durable-execution-integration.md)

- [026 · 独立资产目录与元数据版本](docs/changes/2026-11-13-026-asset-library.md)
- [027 · 私有对象存储与迁移](docs/changes/2026-11-13-027-object-storage.md)
- [028 · 资产库工作台](docs/changes/2026-11-13-028-library-workbench.md)
- [029 · 离线维护与目录归属](docs/changes/2026-11-13-029-storage-maintenance.md)
- [030 · V08联合验收](docs/changes/2026-11-13-030-asset-library-integration.md)

- [022 · 私有参考素材与核心受理](docs/changes/2026-11-13-022-reference-assets.md)
- [023 · 不可变图片编辑请求](docs/changes/2026-11-13-023-media-edit-provider.md)
- [024 · 参考图与图片编辑工作台](docs/changes/2026-11-13-024-reference-workbench.md)
- [025 · V07本地联合验收](docs/changes/2026-11-13-025-reference-integration.md)

- [018 · 版本化目录与执行快照](docs/changes/2026-11-13-018-model-catalog.md)
- [019 · 模型映射与精确上游参数](docs/changes/2026-11-13-019-model-provider.md)
- [020 · 模型与规格工作台](docs/changes/2026-11-13-020-model-workbench.md)
- [021 · V06联合验收](docs/changes/2026-11-13-021-model-integration.md)

- [014 · 工作区与模型准入](docs/changes/2026-11-13-014-resource-admission.md)
- [015 · 健康与运行指标](docs/changes/2026-11-13-015-observability.md)
- [016 · 容量与请求反馈](docs/changes/2026-11-13-016-runtime-workbench.md)
- [017 · V05联合验收](docs/changes/2026-11-13-017-runtime-integration.md)
- [009 · 模块、批次与 TODO 治理](docs/changes/2026-11-13-009-batch-tracking.md)
- [010 · 云端草稿与不可变版本](docs/changes/2026-11-13-010-project-drafts.md)
- [011 · 云端草稿与活动工作台](docs/changes/2026-11-13-011-drafts-activity-workbench.md)
- [012 · 事务审计与活动记录](docs/changes/2026-11-13-012-transactional-audit.md)
- [013 · v0.3 批次联合验收](docs/changes/2026-11-13-013-batch-integration.md)
- [004 · 持久化与账户](docs/changes/2026-11-13-004-persistence-auth.md)
- [005 · 实际异步媒体协议](docs/changes/2026-11-13-005-media-gateway.md)
- [006 · 账户与项目工作台](docs/changes/2026-11-13-006-accounts-projects.md)
- [007 · 数据库开发环境与集成](docs/changes/2026-11-13-007-integration-database.md)
- [008 · 本地性能基线](docs/changes/2026-11-13-008-performance-baseline.md)
- [001 · 工程规则、Wiki 与交付治理](docs/changes/2026-11-13-001-foundation.md)
- [002 · Go 核心执行链路](docs/changes/2026-11-13-002-go-core.md)
- [003 · React 工作台](docs/changes/2026-11-13-003-workbench.md)

## 验证状态

2026-11-14，V14-A的`npm run check`和真实PG全量`npm run test:db`通过（studio169.657秒）；217项前端/23文件、8项治理、Go vet、严格TS、生产构建、六套隔离冒烟通过。OpenAPI为61路径/73操作/82 schemas/340引用。PG验证授权/撤权、密码重核、预演过期/CAS、原任务只查询、旧租约拒写、审计回滚及COMMIT确认丢失后重启只读取唯一收据。详见[050台账](docs/changes/2026-11-14-050-operations-integration.md)与[验证汇总](docs/wiki/assets/operations-validation-v0.12-2026-11-14.json)。

真实Go+PG/demo浏览器完成开单→修改证据使旧预演失效→错误密码拒绝→正确密码确认→释放0.045056演示创作点→收据与5条审计补读。普通账户无运营入口，observer范围只读，390×844及1440×1000无横向溢出；近期console warning/error为0。见[浏览器证据](docs/wiki/assets/operations-browser-v0.12-2026-11-14.json)。012→014停写联备后保留旧资产/修订、草稿、会话、运行和账本；没有自动运营授权，[升级比对](docs/wiki/assets/operations-upgrade-v0.12-2026-11-14.json)可查。

本地memory/demo基线bootstrap P95 21.97ms、2350.71请求/秒；任务创建+取消P95 12.39ms，demo首delta P95 56.95ms。[原始报告](docs/wiki/assets/performance-local-v0.12-2026-11-14.json)仅用于同机回归，关闭PG/鉴权/权益/邮件/运营，不代表运营事务或生产容量。本轮未调用真实收费AI、邮件或支付。

### v0.11 历史验收

2026-11-14，V12最终`npm run check`、真实PG全量`npm run test:db`通过（studio148.283秒）；202项前端/22文件、8项治理、Go vet、严格TS和生产构建通过。六套隔离冒烟及本地性能回归通过；OpenAPI为54路径/65操作/67 schemas/311引用。PG覆盖并发超扣、原键、回滚、提交确认丢失、可信usage、实际进程Kill恢复及离线对账。详见[046联合验收](docs/changes/2026-11-13-046-billing-integration.md)。

浏览器在PG/demo下完成聊天、图片、视频海报的报价→确认→冻结→结算；100演示点结算后可用97.799616、累计消费2.200384，冻结/待核实为0。验证输入变化撤销旧报价、流水/冻结单筛选、390×844界面无横向溢出，console warning/error为0。证据见[浏览器记录](docs/wiki/assets/billing-browser-v0.11-2026-11-14.json)及[012升级比对](docs/wiki/assets/billing-upgrade-v0.11-2026-11-13.json)。升级前完成数据库与私有目录联备；旧资产、草稿和会话保留，未自动赠额或追扣旧任务。

本地memory/demo基线：bootstrap／32并发P95 26ms、2077.83请求/秒，任务创建+取消P95 14.7ms，demo首delta P95 56.96ms；关闭数据库、鉴权、邮件与权益，不代表PG账本或生产容量。见[性能报告](docs/wiki/assets/performance-local-v0.11-2026-11-14.json)。本轮未调用真实收费AI、邮件或支付。

### v0.10 历史验收

2026-11-13：`npm run check`、173项前端测试、8项治理、Go vet/严格TS/构建、六套隔离冒烟及OpenAPI 50路径/61操作/293引用通过。根真实PostgreSQL全量studio115.317秒，覆盖既有业务、账户令牌/会话/改密竞争/事务回滚、TLS SMTP完整HTTP链和实际进程Kill。SMTP确认不明及崩溃恢复不会重复投递。

浏览器验收账户安全页、测试会话真实撤销与401、安全事件、验证fragment清除和显式确认、失效链接不重试、匿名找回未配邮件禁用；390×844安全/恢复页面无横向溢出，近期console warning/error为0。升级011前停写联备；对照旧dump确认草稿v5、资产修订v10、8个资产和12个会话节点保留，旧Cookie继续有效。详见[042联合验收](docs/changes/2026-11-13-042-account-security-integration.md)、[浏览器证据](docs/wiki/assets/account-security-browser-v0.10-2026-11-13.json)和[升级比对](docs/wiki/assets/account-security-upgrade-v0.10-2026-11-13.json)。

本地memory/demo基线1000次bootstrap／32并发P95 22.08ms、约2262请求/秒；不覆盖PG账户、bcrypt、TLS或真实网关，不能作为生产容量成绩。见[性能报告](docs/wiki/assets/performance-local-v0.10-2026-11-13.json)。未外发真实邮件、未提交真实付费生成；浏览器未改主QA密码，密码写操作通过独立Go HTTP/PG测试覆盖。

### 后续放行

当前仍是单控制写者；逐token持久日志、跨实例运行和公开收费闸门尚未完成。本机Go race所需CGO/C编译器不可用，Linux CI已配置但未远程执行。S3真实兼容服务验收保留V08证据，本轮未重复未改变的专项。

**V19-A 模板运行与人工选图已在本地验收**。023 全量 PG 881.522 秒通过，V19-B 已将选定原件送入实际视频 HTTP 请求并在本地验收，V19-C 完成 025 全量回归，整批通过本地验收，见[工作流契约](docs/planning/v19-contract.md)。V14-C2账户及内容申诉证据与处置、C3额度与自动风控继续保留完整范围；真实文本与多模态审核渠道继续单列待验，依赖的V11真实邮件尚未放行，V14不能提前标整批完成。V13可先准备订单/支付适配与回调fixture，真实验收需V11及商户配置。V07/V15保留真实模型联调条件；V16的已验收依赖已齐备，可独立推进分布式流与公平调度，V17另做生产容量验收。见[TODO](TODO.md)与[路线](docs/wiki/roadmap.md)，批次数量不代表整体工期百分比。

## v0.15 / V14-C1 本地验收

本地完整链已完成：显式moderator限制新生成→用户申请复核→真实预演/当前密码确认→公开答复/解除→用户手动新生成。原键、旧任务、历史内容和账务按原规则保留；解除不会放行隔离内容。

297项前端/28文件、8项治理、Go vet/构建、PG全量282.020s、六套隔离冒烟与71路径/83操作/411引用通过。016→017联备升级保留旧数据；桌面/390手机完整验收，独立运营和普通账户权限分离。首页JS494.93kB、gzip156.88kB，用户/运营页面按需加载，移除500kB警告；不等于生产容量或LCP/INP验收。

[062联合台账](docs/changes/2026-11-14-062-generation-controls-integration.md) · [验证证据](docs/wiki/assets/generation-controls-validation-v0.15-2026-11-14.json) · [浏览器](docs/wiki/assets/generation-controls-browser-v0.15-2026-11-14.json) · [017升级](docs/wiki/assets/generation-controls-upgrade-v0.15-2026-11-14.json) · [C1契约](docs/planning/v14c1-contract.md)。两次PG失败的夹具原因、修复及最终复跑均保留在台账；没有放宽生产门禁。C2/C3、真实渠道和V16/V17继续按TODO推进。

## v0.16 / V16-A1 本地验收

聊天按工作区轮转，冻结上游模型别名共享执行预算；总队列/工作区队列有界。原幂等优先、事务提交后入队，取消运行需等实际调用退出才释放预算。新增五项CHAT配置与11项受保护固定指标；媒体通道、持久token日志和跨实例配额仍待办。

301项前端/29文件、10项治理、Go vet/构建、PG全量238.022s、六套隔离smoke及OpenAPI引用通过。主JS495.68kB/gzip157.13kB；新配置同workspace同时执行2、客户端4并发，因此首delta包含排队，不能与旧四活跃流直接比较或宣称生产提速。详细结果见[067](docs/changes/2026-11-14-067-execution-integration.md)、[验证汇总](docs/wiki/assets/execution-validation-v0.16-2026-11-14.json)、[调度Wiki](docs/wiki/execution-scheduler.md)。

本轮文档归docs、17SQL归db且字节不变。当前环境旧LocalAppData开发PG不可用，验收另用独立测试实例；原配置、私有文件和备份保留，未声称恢复原开发库。本批只改排队反馈协议，无布局调整；未重复旧浏览器视觉验收。下一批自动推进A2，V16仍进行中，全产品未完成。

## v0.17 / V16-A2 本地验收

媒体提交与查询交付在worker、PG辅助连接及HTTP池上隔离；先预留预算再领取，原handle、未知提交、租约、B2隔离与权益规则保持。新增严格MEDIA配置和六个固定运维指标；旧内部TaskWorkers=1变为双通道最小1+1。

全量Go/PG、301项前端、10项治理、生产构建和六smoke通过；最终PG318.880s。首轮两个账户用例超时及不改断言的复验均记入[071台账](docs/changes/2026-11-14-071-media-lane-integration.md)，完整[验证汇总](docs/wiki/assets/media-lanes-validation-v0.17-2026-11-14.json)与[HTTP通道说明](docs/wiki/media-transport-lanes.md)可核对。

query仍包括下载导入/审核，混合到期索引尚未证明扫描成本隔离；本机性能是memory/demo口径，创建加取消不等于媒体吞吐。下一步自动推进A3.1持久私有交付，A3.2首次审核分派及A3.3渠道公平退避已分入TODO，完整V16和公众收费上线仍未完成。

## v0.18 / V16-A3.1 本地验收

原生异步媒体现在分为submit、QueryStatus及delivery三条执行通道。查询不再下载；018事务先冻结加密清单和计划ID，再逐件导入并保存完整收据。已提交清单在重启后不换批、不重POST，已完成收据缺失或篡改则待核实；整组审核、资产、权益及Outbox仍以事务确认后公开。

完整check、301前端/29文件、10治理、Go vet/构建、六smoke、最终全PG301.345s通过；包含真实本机S3、017→018升级、COMMIT故障及两种pg_dump/pg_restore。首轮旧测试缺密钥与S3两步假设的失败及修正保留在[075台账](docs/changes/2026-11-14-075-media-delivery-integration.md)，可核对[验证汇总](docs/wiki/assets/media-delivery-validation-v0.18-2026-11-14.json)、[交付Wiki](docs/wiki/media-delivery.md)及[性能口径](docs/wiki/performance.md)。

S3恢复用例保留原远端对象，没有恢复S3服务，本次没有VersionId；不是云灾备。PG与私有目录仍单控制写者，首次审核仍占delivery；A3.2将增加持久首次审核交接，A3.3公平退避、持久token流及完整公众收费产品继续按TODO推进。

## v0.19 / V16-A3.2 本地验收

首次审核现为独立review通道：候选与019 READY同事务，STARTED确认提交后才外发。READY恢复可以首次审核，旧候选和STARTED恢复保持隔离；取消、失租和确认不明不晚放行。输入与输出共用原两个审核许可，完整收据及原件/缩略图在审核前校验。新派发不改变原请求、原handle、价格或幂等。

完整check、301前端/29文件、10治理、Go vet/构建、六smoke及最终全PG368.160s通过。真实本机版本化S3、018→019升级、READY/STARTED提交故障及pg_dump恢复均通过，审核恢复保留4/4真实对象版本。结果及首次失败修正见[079台账](docs/changes/2026-11-14-079-media-review-dispatch-integration.md)、[联合证据](docs/wiki/assets/media-review-dispatch-validation-v0.19-2026-11-15.json)、[审核分派Wiki](docs/wiki/media-review-dispatch.md)。

S3远端服务仍是保留原对象，没有灾备恢复；全站多实例和生产容量未验收。当前许可保守保持到最终保存退出，公平和饥饿控制留待A3.3；下一步按冻结渠道预算、持久冷却/退避与大积压SQL测量推进，持久token流及完整公众产品范围继续保留。

## v0.20 / V16-A3.3 本地验收

媒体四通道现在按工作区轮转，并按冻结渠道限制并发；Retry-After、冷却和GET探测状态持久保存，重启不清退避。输入/输出审核共享原两个许可，有界等待并公平分配；原幂等、未知提交和STARTED审核不因调度重发。

完整check、301前端/29文件、10治理、最终全PG 443.228s、六smoke、实际版本化S3及冷却/候选联备恢复通过。180条真实SQL计划与48次完整预选独立留存；10万活跃+10万历史下完整预选24样本P95 16.302ms（不含claim/HTTP）。20万旧任务迁移11.229秒、59原表保持，但未证明完整App启动容量。证据见[083](docs/changes/2026-11-15-083-media-scheduler-integration.md)、[联合JSON](docs/wiki/assets/media-scheduler-validation-v0.20-2026-11-15.json)及[调度Wiki](docs/wiki/media-scheduler.md)。

A3.3 验收采用单控制写者与单个已配置的异步网关，测量范围为迁移与任务预选。

## V16-A4 本地验收

A4.1 覆盖分页恢复、启动背压和提交回执丢失后的重启。A4.2 解除了其他工作区填满缓存对新请求的影响，并保留排队、执行和活动订阅所需的记录。PG 项目按工作区查询，第 100 个创建名额在事务内核验。[A4.1 验收](docs/changes/2026-11-21-089-recovery-acceptance.md) · [缓存决策](docs/wiki/decisions/0020-bounded-pg-caches.md)

本地 PostgreSQL 中 10 万活跃加 10 万终态记录的完整应用启动用时 7.913 秒，Run 与媒体任务的启动缓存峰值各 512 条，项目预载为 0。另一次启动处理 12,002 个中断资源与 9,999 条原有事件，22,001 条通知全部可补读。[A4.2 验收](docs/changes/2026-11-21-094-bounded-startup.md)

## V16-B1 本地验收

在 PostgreSQL 模式下，普通聊天和多轮会话现在先提交可公开的文字增量，再通过 SSE 发送。浏览器断线后按原游标补读，窗口过期时用当前快照恢复。进程中断后保留已提交的回答，原模型请求只执行一次。开启输出审核时，待审正文继续保存在私有内存，整段通过并完成提交后才可读。[099 验收](docs/changes/2026-11-21-099-run-stream-integration.md)

升级前需联备数据库、私有目录、已启用的独立密钥及相关对象版本。B1 交付时的格式为 021。当前维护工具要求完整 001—029；旧 020 的 Run 保留原快照与游标规则。单控制写者及目录锁继续生效。[数据库迁移](docs/wiki/database-migrations.md) · [B1/B2 契约](docs/planning/v16b-contract.md)

## 聊天租约与混合负载

022 为新聊天登记执行租约。取消、到期或服务重启后，浏览器可继续读取原请求已提交的回答。旧模型或审核返回不能追加内容、结算或触发第二次生成。升级前联备数据库、私有目录、目录归属及已启用密钥和相应对象版本。[升级与故障记录](docs/changes/2026-11-21-103-run-lease-integration.md)

在单控制写者的双工作区环回测量中，A 积压期间 B 的聊天能独立执行。三轮均保留原 handle，限流只延后媒体 GET；每轮 6 个媒体任务只发出 6 次生成 POST。本机运行条件、样本和命令见[混合负载台账](docs/changes/2026-11-21-104-mixed-workload.md)。

## 037 已登录工作台的实际启动传输

单分支 10,000 条消息、20 次冷启动和 20 次暖启动已在本机实测。冷启动 JS 每次实际传输 166,103 字节，暖缓存命中后新增 JS 传输为零；首个可交互点 P95 分别为 880 和 566 毫秒。网络配置双向 20 Mbps / 80 毫秒；校准实测中位 RTT 105–108 毫秒。[逐样本报告](docs/wiki/assets/performance-network-037.json) · [验证台账](docs/changes/2026-11-22-200-workbench-network-browser.md)。

## 运行增量回放的连接数

为同一主库的运行 SSE 回放设置 RUN_REPLAY_READ_CONNECTIONS=1 到 4，可隔离事件回放与控制写连接。默认 0 沿用原行为。开启后按数量预留额外 PG 连接，应用启动即核对库与写者身份。[配置与恢复语义](docs/wiki/decisions/0021-durable-run-stream.md) · [204 验收](docs/changes/2026-11-22-204-run-replay-read-pool.md)。

## 可选的本机聊天执行子进程

在已启用鉴权、PostgreSQL 和 live 聊天网关的服务端，可以成对设置 CHAT_CHILD_PROCESSES（1–16）与 CHAT_CHILD_SLOTS（每进程 1–64），使新会话的模型 HTTP 请求分到受管子进程。控制服务继续决定接纳、审核、权益和 SSE 交付。默认省略这两项；配置例见 .env.example。[设计、边界及回退](docs/wiki/decisions/v17j1-managed-chat-execution.md) · [验收记录](docs/changes/2026-11-22-208-managed-chat-integration.md)。

## 可选的聊天增量批量提交

配置 PostgreSQL 时可显式开启 RUN_DELTA_BATCHING_ENABLED，使不同运行的公开增量共享短事务。默认关闭。原序号、租约和提交后的 SSE 发布语义保持一致；比较依据包括原事件表的物理提交及真实授权回放。[增量日志决策](docs/wiki/decisions/0021-durable-run-stream.md) · [验收台账](docs/changes/2026-11-22-212-business-delta-batch-integration.md)。

## 持久订阅的容量配置

全局订阅默认 128 个，单工作区默认 32 个。用 ADMISSION_GLOBAL_STREAMS 和 ADMISSION_WORKSPACE_STREAMS 配置两级上限。全局取值 129–16,384 时，需要 PostgreSQL、AUTH_MODE=required 和至少一个 RUN_REPLAY_READ_CONNECTIONS；服务另外使用一个专用只读连接，每页最多核验 256 个会话，共用一次复核期限。PG 项目通知另用两条只读连接共享同项目、同游标的已提交事件页。原会话失效后关闭对应订阅，可按当前账户和已有游标重新连接。[218 配置与指标](docs/changes/2026-11-22-218-stream-session-batches.md) · [219 授权读者验收](docs/changes/2026-11-22-219-authenticated-sse-sessions.md)。
