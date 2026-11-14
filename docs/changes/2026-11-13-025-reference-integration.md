# 025 · V07 参考素材与图片编辑联合集成

## 目的

贯通私有参考图上传、图片编辑、异步查询、结果导入和授权下载，保留原始请求与幂等身份；完成v0.6.0本地联合验收并如实保留真实供应商条件。

## 范围

V07/H01/H03/H04/H07/H08/H09。022记录应用/HTTP/持久化，023记录网关编辑适配，024记录工作台，025记录底层私有文件存储、联合集成、接口、运行证据与文档治理。V07两项本地检查通过，真实供应商验收未具备条件，整批标blocked；已验收批次仍为7/23，没有缩减原验收条件以提前勾选。

## 实际文件

- `internal/studio/reference_store.go`、`reference_store_test.go`：os.Root私有存储、原字节SHA256、真实编码与尺寸检查、512px缩略图、磁盘流式正文、容量预留/去重/孤立文件计数、并发及篡改负例。
- `internal/studio/testdata/references/{source.png,source.webp,README.md}`：原创960×640几何风景fixture，供格式/预览和浏览器验收，不含用户文件。
- `internal/studio/reference_integration_test.go`：真实HTTP应用上传→实际multipart适配器→本地网关查询→安全导入→授权结果下载；供应商字段、顺序、幂等与跨项目权限均断言。
- `internal/studio/observability.go`：31类固定路由标签，新增参考上传/原图/缩略图，507归为unavailable；不放入文件名、哈希或ID。
- `scripts/reference-smoke.mjs`：独立临时实例的3组参考素材验收；`scripts/lib/process.mjs`、`smoke.mjs`、`runtime-smoke.mjs`、`catalog-smoke.mjs`、`load.mjs`：隔离REFERENCE_DIR并只清理各自目录。
- `package.json`、`.github/workflows/ci.yml`：v0.6.0、smoke:references命令及CI步骤；前端版本/锁文件由024记录。
- `docs/api/openapi.yaml`：v0.6契约、29路径/36操作；补充标准文件304/412/416与多Range响应，HEAD不声明正文。
- `AGENTS.md`、`README.md`、`TODO.md`、`docs/planning/tasks.json`、`docs/changes/README.md`、本台账：强制文档规则、实际状态、证据与导航。
- `docs/wiki/{README,architecture,backend,persistence-auth,observability,performance,development,roadmap,audit}.md`、`docs/wiki/decisions/0004-private-reference-bodies.md`：边界、恢复、配额、性能与后续路线。022/023/024维护references/models/media-gateway/frontend Wiki；022风险表述同步为V07/V15真实待验收。
- `docs/wiki/assets/*-v0.6-2026-11-13.json`（reference-smoke/runtime-smoke/catalog-smoke/performance-local/references-browser五份）、`references-phase6.jpg`、`mobile-references-phase6.jpg`：独立版本证据，不覆盖旧版本报告。

## 接口与数据影响

新增GET/POST `/api/v1/references`和鉴权content/thumbnail的GET/HEAD。上传只能单个file部分；相同项目/规范化名/原字节的原键重放返回同201，不依赖浏览器随机boundary。公开ReferenceAsset隐藏文件身份/哈希/路径；参考URL包含project_id，其他项目/账户仍404。

TaskInput支持`operation: edit`与有序`inputs.reference_asset_ids`，1—4张且符合模型能力；普通生成不允许引用。服务端模型能力和bootstrap限额供前端渲染。原文件与multipart原始正文持久在REFERENCE_DIR，PG只保存身份、摘要及有序关系，不用Base64扩张JSONB。磁盘准备在业务锁外进行，重新加锁后复核幂等、权限、取消和准入。

006新增参考、上传幂等、复合外键约束的有序关联与审计动作；也是旧程序拒绝新格式的栅栏。原件先写盘，PG失败可能留下孤立文件，这些文件仍占用目录硬限，不宣称跨磁盘/数据库原子事务。全局2GiB/4096文件、工作区100条/128MiB逻辑字节、上传4槽/准备2槽均有界。

## 验证结果

2026-11-13，Windows/Go1.26.11/Node22.18.0/PostgreSQL18.7，使用本机回环开发环境。

| 验证 | 实际结果 |
| --- | --- |
| `npm run check` | 通过：Go vet、106个Go测试函数及子例（普通运行跳过PG）、前端100项/15文件、治理8项、严格TS、62模块生产构建、51文档/25台账检查 |
| `npm run test:db` | 最终通过，studio25.156秒；真实PG权限/审计回滚/006升级/旧loader拒绝/原正文与handle恢复，不把普通Skip算通过 |
| 四套隔离冒烟 | smoke、runtime 5组、catalog 3组、references 3组全部通过；根实际执行，测试目录独立且不调用真实网关 |
| 格式与文件存储 | PNG/JPEG/WebP、完整解码、截断/伪装/GIF/SVG/像素炸弹/8MiB上限、哈希篡改、大小/正文上限、取消、并发去重及重启孤立计数通过 |
| 协议HTTP全链 | 实际上传PNG/JPEG后逆序编辑，multipart文件与模型/参数/键准确；查询→本地签名URL下载不带网关凭据→原件导入→授权下载，其他项目404；重复平台提交不重复调用 |
| 生产构建体积 | JS345.86kB/gzip109.87kB，CSS66.27kB/gzip13.97kB；不是图片/网络总量或实机LCP成绩 |
| OpenAPI严格检查 | 29路径/36唯一operationId/139有效本地引用，无重复YAML键；独立审阅后补齐文件响应 |
| 浏览器 | 1280×900桌面及390×844窄屏；真实上传PNG/WebP、512×341缩略图、排序/复用、项目隔离、无效PNG拒绝和原文件重试提示；编辑demo任务完成，最终控制台无warn/error，viewport已重置 |

根首轮PG复测触发既有恢复用例失败：停机取消Query被写成FAILED。新增确定性阻塞查询在修复前0.42秒复现；修复后原用例和新回归各10次通过，随后根重新运行全量PG、check、最终构建与四套冒烟。保护只处理应用关闭/失锁引起的取消，正常供应商失败/超时仍按既有分类。完整故障与复现记录见[022](2026-11-13-022-reference-assets.md)。

主开发库升级前完成`.runtime/backups/pre-v0.6-1794562314528.dump`（35524字节）；升级时尚无参考文件。001—006已确认，旧项目/任务/草稿v5保留。浏览器创建的2条参考逻辑占用40058字节，编辑关联位置0为source.webp、1为source.png，数据库读回吻合。随后实际重启Go/Vite并刷新页面，2条素材、预览及已完成编辑任务仍在。此次为本地增量升级，不是生产整库/文件灾备恢复演练。

最终构建SHA256：`fe4b95681a95da5a5533d837776261aa6abfc1ecfda266042971a18d8440fc76`。运行、目录、参考冒烟与性能报告均对应同一二进制。浏览器使用相同源代码的dev构建，报告中的摘要用于标识本轮生产构建，不能误称二进制完全相同。

`npm run perf:local`：32并发1000个bootstrap请求P95为23.10ms、2070.78请求/秒；任务创建+取消P95为14.41ms；demo首delta P95为56.85ms。仅同机memory/demo HTTP基线，与上一版本有波动，不证明生产提速。8MiB正文写盘单次基准45.11ms、112672B/op（含初始化），不包含完整解码/PG/网络；详见[性能页](../wiki/performance.md)。

证据：[参考冒烟](../wiki/assets/reference-smoke-v0.6-2026-11-13.json)、[运行冒烟](../wiki/assets/runtime-smoke-v0.6-2026-11-13.json)、[目录冒烟](../wiki/assets/catalog-smoke-v0.6-2026-11-13.json)、[性能原始报告](../wiki/assets/performance-local-v0.6-2026-11-13.json)、[浏览器记录](../wiki/assets/references-browser-v0.6-2026-11-13.json)、[桌面](../wiki/assets/references-phase6.jpg)、[手机](../wiki/assets/mobile-references-phase6.jpg)。

## 风险与限制

当前为单写者、本地私有存储和demo/fixture验证；没有真实服务端模型凭据和受控生成验收，未发送收费请求。V07真实环节与V15仍待条件，不能宣称效果/费用/实际供应商兼容已验收。

没有mask、视频编辑、删除/GC、对象存储共享、完整资产管理或万级容量成绩。大图解码和缩略图仍消耗有界内存；超限后需处理生命周期，不能靠无限等待恢复。memory重启丢引用关系但磁盘文件继续计入容量。页面未知上传的原File仅内存保留，刷新/关闭会丢本页重试信息，UI已明确告知。

本机无CGO/C编译器，Go race未执行；Linux CI已配置但尚未远程运行。Windows创建符号链接的存储测试因权限不足跳过该子路径，路径格式/静态树/独立目录负例已验证，不能声称本机符号链接攻击测试全部执行。

## 回滚方式

在目录中停用image编辑能力并重启可暂停新编辑，原handle仍可查询。006之后不能直接启动旧程序或删除迁移登记；如需版本回退，停止写入后联合保留当前PG/REFERENCE_DIR/ASSET_DIR，再在独立库和独立目录恢复匹配旧版本备份。升级前备份不能保留升级后新增素材，须保留当前完整副本，不清理用户历史或待核实正文。

## 文档同步

README、AGENTS规则、Wiki导航与相关模块、ADR0004、022—025台账索引、任务源及生成TODO已同步；V07本地检查passed、真实检查blocked、整批blocked，completed_on为空。继续遵守每批“实现/验证/文档/证据”门槛，后续可独立推进V08。既有已验收历史台账和版本报告保持原事实。
