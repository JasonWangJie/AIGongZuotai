# 058 · V14-B2 媒体审核联合集成

## 目的

完成V14-B2图片、视频与参考原件的本地审核闭环，发布v0.14.0；真实审核商按用户安排后续选型，完整V14继续进行中。

## 范围

横切H01身份/权限、H03执行、H04资产、H05权益、H06运营审核、H07运行、H08前端、H09交付；纵切上传→输入检查→受理→私有候选→整组审核→交付/隔离→下载及账务→恢复与维护。055记录引擎和016，056记录执行事务，057记录界面。本记录负责跨模块集成与验收。

## 实际文件

- `internal/studio/references.go`：同步原件审核、同事务批准登记、列表摘要和引用/旧下载门禁；审核期间不占全局业务锁。
- `internal/studio/asset_library.go`、`asset_library_mutations.go`、`repository_queries.go`：独立安全摘要投影、当前状态门禁，严格ID/游标/原始UTF-8筛选校验。
- `internal/studio/storage_maintenance.go`、`ledger_admin.go`、`operations_admin.go`：016精确格式栅栏，所有已登记私有候选纳入可达性，禁止按终态删文件。
- `internal/studio/media_moderation_demo.go`、`media_moderation_access_test.go`：真实私有样本演示、上传/内容入口/数据库/维护及输入故障专项。
- `internal/studio/testdata/moderation/README.md`、`manifest.json`、`allow.png`、`block.png`、`review.png`、`allow.mp4`、`block.mp4`、`review.mp4`：自行绘制的固定几何PNG与一秒H264 MP4，无外部图像来源、非AI产物。
- `cmd/server/main.go`、`main_test.go`、`.env.example`、`scripts/lib/process.mjs`：严格媒体环境配置及现有隔离脚本的MEDIA_MODERATION_*覆盖。原六smoke不继承真实渠道配置。
- `package.json`、`apps/workbench/package.json`、`apps/workbench/package-lock.json`：0.14.0版本；无新产品依赖。
- `docs/api/openapi.yaml`、`docs/planning/v14b2-contract.md`、`docs/wiki/decisions/0012-media-review-gate.md`：接口、范围和媒体交付决策。
- `README.md`、`AGENTS.md`、`docs/wiki/README.md`、`docs/changes/README.md`、`docs/planning/tasks.json`、`TODO.md`：用户要求的规则/状态/台账同步。
- `docs/wiki/backend.md`、`frontend.md`、`development.md`、`references.md`、`roadmap.md`、`storage-maintenance.md`、`object-storage.md`、`billing-operations.md`、`operations-access.md`、`operations-cases.md`、`performance.md`：当前范围及016边界，保留历史验收事实。
- `docs/wiki/assets/media-moderation-validation-v0.14-2026-11-14.json`、`media-moderation-browser-v0.14-2026-11-14.json`、`media-moderation-upgrade-v0.14-2026-11-14.json`、五份`*-smoke-v0.14-2026-11-14.json`、`performance-local-v0.14-2026-11-14.json`及七张phase14截图：公开可提交的脱敏证据。

`.runtime/v14b2-*`为本次忽略目录内的隔离运行器、日志和联备辅助文件，不提交运行日志、数据库、凭据、工具安装目录或构建产物。fixture生成临时使用Pillow/FFmpeg，产品运行不依赖它们。所有非根执行改动及新增测试另见055—057，文件共享点由集成人审阅。

## 接口与数据影响

独立media-v1策略与文本策略冻结；016新增任务政策、私有候选、不可变决定和参考批准记录。Task新增MODERATING及可选moderation，ReferenceAsset和LibraryEntry增加可选摘要，Bootstrap增加media_mode/政策标识。共61路径、73操作、84 schemas、347本地引用，未增加公共路由。

只有全组ALLOW且SUCCEEDED/PARTIAL提交成功才能公开产物、注册目录和按实际交付结算；有任一受限项整组隔离，已执行额度REVIEW。事务失败或COMMIT确认丢失不提前放行缓存/事件；重启读取已提交决定，旧键不重新POST。已登记候选不自动重审；无候选原handle且明确can_reconcile可继续原Query。财务处理不能改变内容判定。

新参考只在完整原件ALLOW后登记；上传受理不明保留原File/key。当前策略不批量追审旧资源，但新编辑会核对旧参考的实际字节。关闭审核不批准旧隔离内容。默认两类审核均关闭，启用媒体须PG、required鉴权及文本审核；fixtures仅demo。

## 验证结果

| 实际命令／检查 | 结果与范围 |
| --- | --- |
| `node .runtime/v14b2-all-checks.mjs check`（隔离环境调用`npm run check`相同入口） | 最终通过：Go vet、普通Go测试studio8.637s、270项前端/26文件1.68s、8项治理、TS和构建；Vite1.60s。主JS505.19kB/gzip159.37kB，CSS116.27kB/gzip23.25kB；保留500kB分包警告 |
| `node .runtime/v14b2-all-checks.mjs db`（隔离TEST_DATABASE_URL和外部配置调用test:db入口） | 全量真实PostgreSQL通过，studio192.482s；包括8个媒体PG函数、candidate/ALLOWED/BLOCKED三类真实COMMIT ACK丢失、混合整组、故障回滚、取消/租约/恢复、全部下载门禁、参考登记及GC |
| `node .runtime/v14b-test.mjs '^TestPostgresMediaModerationWholeBatchReleaseAndBilling$'` | 新增PARTIAL后根再次执行，7子例通过，1.675s；请求2份交付1份，仅结算1份并释放余量，旧键零额外Provider调用。仅新增测试，不改变已验产品核心 |
| `node .runtime/v14b2-smokes.mjs` | 六套现有smoke和perf全部通过；父环境故意开启无效媒体/文本审核、邮件、权益、运营，子进程仍显式隔离；最终UI修复重构建后再跑`node .runtime/v14b2-final-smoke.mjs`通过 |
| 五份smoke JSON、perf与最终server.exe SHA256 | 全部一致：`3e08a07c354d2099a9f46386ea68869dfdd82eabb377457f79fce3e2b1f5c6a5` |
| OpenAPI解析／引用／operationId | 61路径/73操作/84 schemas/347引用全部可解析，无重复operationId |
| 固定MP4全片解码 | 三个样本执行FFmpeg `-v error -i <fixture.mp4> -f null -`均exit0、无解码错误；浏览器ALLOW视频实际播放至1秒，readyState4，256×144，error=null |
| CUA桌面与390×844手机 | 图片批准/隔离、参考拒绝→更换→批准编辑、0.2/2/0.3演示点交付结算、受限0.2待核实、资产详情与鉴权缩略图、刷新保持、无横向溢出。修复后没有新增console warning/error；早先白屏错误如实保留在证据 |
| 015→016联备升级 | 停旧preview，pg_dump200798字节并验证restore-list，复制5个参考文件/2个产物文件；升级前后资产10、最大修订10、草稿v5、会话节点[2,4,12]、Run12、journal14、reservation6等一致；新媒体记录0，不自动审批旧资源 |
| `npm run todo:sync`、`check:todo`、`check:docs`、`check:changes`、`git diff --check` | 通过：10模块/23批/11整批、122文档/58台账；变更同步及diff空白检查通过。V14-B2本地子项已完成，完整V14仍进行中 |

本机memory/demo bootstrap P95 24.08ms、2255.5请求/秒；不是审核、PG或生产容量。实测时同机仍有隔离PG验证负载，不据单次波动宣称优化。未运行本机Go race：CGO_ENABLED=0，未找到gcc/clang；Linux CI未远程执行。

修复事实：首次参考ALLOW错误进入只返回错误的helper，已改为仅非ALLOW调用并通过PG；只读复核发现恶意ID/游标/NUL/非法UTF-8能触发PG熔断，新增前置校验并断言readyz仍200；浏览器发现空/受限参考在render构造报价输入导致白屏，057修复统一ready门槛并补6项实际App渲染回归，CUA重新验证通过。最初PG测试将故意审计熔断置于后续操作之前，已重排；ACK测试代理原只接受单连接，已将独立lease连接直连同一测试库，控制COMMIT仍经故障代理。保留这些失败原因，不把修前尝试称为通过。

## 风险与限制

真实审核商未选型；fixture是完整字节闭集，HTTP模拟器不代表语义准确率、真实供应商或生产S3。运行时MP4预检不是逐帧/音轨审核，真实供应商必须承担完整内容检查。固定PNG/一秒视频不按模型所选规格生成，界面已明示。未提供人工申诉、重新审核、放行、封禁和完整风控；V14-C后续独立设计。

候选持续占用私有存储，所有已登记候选均保留可达性，尚无政策保留期限/删除流程。当前单控制写者和私有目录锁不变；没有公网部署、真实AI/邮件/支付调用或多实例容量声明。完整V14仍in_progress，整批完成数量仍11/23。

## 回滚方式

016前联备位置记录在升级证据，数据库与REFERENCE_DIR、ASSET_DIR及owner必须匹配；启用S3还需对象版本，启用邮件还需独立队列密钥。本轮邮件关闭，无邮件密钥备份需求。若退版，先停服务并核对升级后新任务/资产/账务，再恢复完整联备；不得删候选、决定、迁移登记或owner绕过旧写者栅栏，关闭媒体审核也不能批准隔离内容。

## 文档同步

README和Wiki记录v0.14、配置、验收、限制与下一步；AGENTS加入媒体完整字节、整组同事务交付、候选可达性和配置隔离规则。任务源V14-B2本地check完成，真实文本/多模态审核继续blocked，V14-C保持pending，完整V14不勾选。TODO由唯一任务源生成；055—058均入索引。下一步建议推进V14-C本地申诉/处置状态机，另列前端按路由拆包及V16/V17容量验收。
