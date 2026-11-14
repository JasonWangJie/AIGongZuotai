# 030 · V08资产库与对象存储联合集成

日期：2026-11-13。版本：0.7.0。批次V08，横向H01/H02/H04/H06/H07/H08/H09。026—029分别记录核心、存储、界面和维护；本记录负责根集成及独立验收。

## 目的

交付可检索、整理、恢复和备份的私有资产链路，连接独立目录、原始内容、对象后端与前端，并防止错误数据库或并发写者误维护私有文件。

## 范围

独立资产库的本地可运行交付、服务端S3配置、固定指标路由、隔离脚本/CI、OpenAPI、旧开发库升级与浏览器验收。S3协议通过真实本地兼容服务验证；实际AI供应商、云账号权限、多实例与生产容量仍分别验收。

## 实际文件

- `cmd/server/main.go`、`cmd/server/storage_config_test.go`：读取AssetStorage配置、布尔参数严格校验；密钥只在服务端环境。
- `internal/studio/observability.go`、`internal/studio/storage_observability_test.go`：七个library固定路由类别，指标令牌禁止复用S3 secret/session token。
- `.env.example`：local/S3选项、endpoint/region/bucket/prefix、凭据与path-style配置说明。
- `scripts/lib/process.mjs`：隔离服务同时拥有独立reference/assets子目录，只清理本次创建且位于runtime内的根。
- `scripts/smoke.mjs`、`runtime-smoke.mjs`、`catalog-smoke.mjs`、`reference-smoke.mjs`、`load.mjs`：隔离资产目录与强制local后端，避免与主服务争锁或继承真实S3配置。
- `scripts/library-smoke.mjs`：独立构建服务的检索/游标/权限、CAS/原键、历史/回收恢复、既有编辑连续性、来源标记、审计及指标验收。
- `package.json`、`.github/workflows/ci.yml`：0.7.0及library冒烟命令/CI步骤；前端版本锁文件由028同步。
- `docs/api/openapi.yaml`、`docs/planning/tasks.json`、`TODO.md`、`AGENTS.md`、根README与Wiki/台账索引。
- `docs/wiki/observability.md`、`performance.md`与本记录；`docs/wiki/assets/`下v0.7的library/reference/runtime/catalog/performance报告、浏览器JSON和四张PNG截图。其他模块变更在026—029逐项列明。

## 接口与数据影响

新增library目录、条目、修订、trash/restore、content/thumbnail接口，共36路径46操作。元数据修改要求当前revision和原幂等键；历史只覆盖元数据，不替换二进制。项目归属及active状态约束新旧私有下载入口，trash不改已受理请求的引用与原始字节。

007迁移新增目录/修订/幂等表、回填与storage UUID。首次正常启动在目录OS锁和PG写锁下绑定私有目录；数据库/目录角色不匹配、不可解释的旧正式文件都拒绝。维护命令不做迁移，不操作有效引用或远端对象。S3迁移校验后保留本地文件和收据。

所有五套冒烟与性能脚本显式隔离双目录、强制demo/local，不访问主数据库或收费网关。指标路由由31扩展为38种固定值；名称、标签、ID、对象key不进入标签。

## 验证结果

### 后端、存储和工具

- 根独立 `npm run test:db` 通过：studio42.534秒，cmd/server0.032秒；129个Go测试函数及子例包含007回填、来源/目录原子性、并发CAS/原键、审计失败、版本恢复预留、归属与维护。真实S3测试须另行配置，不能用此PG运行替代。
- 根加载忽略的`.runtime/s3-test.env`后独立 `go test ./internal/studio -run TestS3Real -count=1 -v`：两个真实服务测试均通过，总0.273秒。包括App任务→实际SeaweedFS→库注册→Range/权限→回收拒绝旧URL→对象保留→恢复，及缺失对象/配置错位拒绝与迁移重入。
- 027实际CLI计划→apply→再次verify_existing通过，本地原件和缩略图保留。官方SeaweedFS发布包校验及脱敏证据见[对象存储报告](../wiki/assets/object-storage-v0.7-2026-11-13.json)。临时服务已核验进程归属并停止，未动PG或主预览。
- 五套隔离冒烟均通过：既有总链路、运行5组、目录3组、参考3组、新资产库3组。新增冒烟第一次错误预期trashed引用为422，实际按既有隐藏归属契约返回404；修正精确断言为404后通过，没有放宽为任意失败。
- 新资产库冒烟验证分页游标绑定、字面量搜索、Range字节、跨项目404/作用域冲突400、版本冲突/幂等冲突、旧键返回原快照但不恢复当前trash、已有任务原键重放、旧URL失效、恢复、生成结果独立注册和审计去重。
- OpenAPI严格重复键检查、全部本地引用、operationId唯一、JSON Schema2020-12及参数匹配通过：36路径、46操作、194引用出现/38唯一、270 schema检查、97参数出现。补齐revision≤1000、before_revision≤1001/default1001。

### 升级与页面

- 停止原v0.6工作台后生成PG自定义备份`.runtime/backups/pre-v0.7-1794569010398/database.dump`，45254字节；`pg_restore --list`成功解析。最初Node子进程流程异常退出，未完成私有目录复制；立即补齐本地文件备份。私有payload复制发生在007启动后、任何UI修改前；没有改动payload，新owner文件从旧版备份副本排除。分阶段时间写入本地manifest，未声称完成全量恢复演练。
- 正常开发服务升级001—007后，原项目5项素材自动回填，旧会话、任务、参考文件保留。名称/标签另存v2，旧元数据载入另存v3；关闭详情保留未保存值，再打开保存v4；回收v5/恢复v6。
- 实际停止并重启Go/Vite后，原会话/项目、5项资产和v6元数据仍在。后续复验回收v7与恢复v8，回收站只显示目标项，恢复后回到正确排序。
- 浏览器发现并修复回收态允许输入但无法保存，以及恢复记录错误追加到末尾。028补齐disabled/状态冲突保护、精确排序和分页范围防护；额外5项测试加入后前端118项/17文件通过。
- 1280×900桌面与390×844手机检查通过。手机页面宽390、main380/client380、dialog335/client335，无横向溢出；详情真实图片512×341。最终重载后控制台warning/error为0，临时viewport恢复，预览保留。记录见[浏览器报告](../wiki/assets/library-browser-v0.7-2026-11-13.json)。

### 构建与性能

最终`npm run check`已通过：前端118项/17文件、治理8项、Go普通回归studio8.869秒、vet、严格TS与生产构建；文档61份/台账30份的结构及链接通过。v0.7前端gzip JS116.82kB、CSS15.90kB；真实PG与S3独立运行如上。V08完成证据写入任务源并生成TODO；最终check:todo通过8/23批，check:changes通过61份文档、30份台账及265个变更路径。最终构建总冒烟再次通过；四份专项冒烟及性能报告的SHA256均与最终server二进制一致。

隔离本地基线：1000请求/32并发bootstrap P95 21.80ms、2199.16请求/秒；任务创建+取消P95 12.85ms；demo首delta P95 56.60ms。含同机客户端开销，未测生产PG、TLS或真实云服务；不据单次差异宣称提速。报告带版本、二进制摘要与环境，见[性能报告](../wiki/assets/performance-local-v0.7-2026-11-13.json)。

## 风险与限制

当前仍是单写PG及串行数据库连接；本地OS锁不是跨主机租约。参考图、编辑正文、S3收据仍依赖私有目录，无完整无状态多实例部署。列表keyset不等于已通过大库搜索压测。历史为元数据，暂无内容版本、硬删除、远端GC或保留策略自动化。

目录owner缺失不能以memory接管来“修复”；禁止删marker绕过归属。回收站/终态任务文件保留，维护只在测试临时目录执行apply，没有删除用户数据。符号链接负例受Windows权限未运行；Go race受CGO/C编译器环境限制，远程Linux CI尚未执行。

SeaweedFS是真实兼容对象服务，但不等于AWS或目标云生产IAM/TLS/临时凭据/容灾验收。V07/V15仍缺私有真实模型配置和受控调用条件，没有付费AI或公网发布。本批不能作为对外收费运营放行。

## 回滚方式

保留升级后的数据库和私有目录。停止写入后在独立位置恢复匹配程序版本的数据库、reference/assets文件及必要S3对象/收据。007绑定数据库UUID与目录角色，不能删除迁移登记或owner强制降级。S3迁移无删除原件，但已有S3收据不能只改backend为local来回滚，详见027/029操作说明。

## 文档同步

README、Wiki索引/模块、台账索引026—030、ADR0005、AGENTS规则、OpenAPI、任务源和生成TODO同步。已交付022—025保留原V07事实，不把本批验收覆盖为真实供应商完成。V08完成后共8/23批已验收，V07待外部条件，剩余批次以TODO为准。

下一步执行顺序：V09先做同事务Outbox/消费去重/断点回放，再做任务租约、epoch fencing和过期回收，最后用重复投递、宕机、失锁及受理不明故障注入验收；通过前维持单写者。并行准备V07/V15私有真实网关验收；随后V10/V11/V12补多轮对话、账户恢复、精确账本，再进入支付。

