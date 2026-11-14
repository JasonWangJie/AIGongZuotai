# 021 · V06 模型目录与参数联合验收

## 目的

在v0.4基线上完成模型目录、版本化能力与规格组合，保障前后端一致，并保持已受理任务身份及原请求幂等。V06验收完成，版本v0.5.0，累计V00—V06共7/23批；余16批保持未完成。

## 范围

有限启动配置、公开目录ETag、精确参数组合、历史任务快照、上游路由冻结与前端模型/规格选择。模块变更分别记录018、019、020，本记录汇总H03/H07/H08/H09集成、API、指标和独立验收，不把fixture通过作为V15真实供应商验收。

## 实际文件

- 根 `package.json`：0.5.0和smoke:catalog；`.github/workflows/ci.yml` 增加目录独立冒烟。
- `scripts/catalog-smoke.mjs`：临时有限目录、独立进程、重启配置/ETag/参数/幂等验收；`scripts/smoke.mjs`、`runtime-smoke.mjs`、`load.mjs` 隔离外部 MODEL_CATALOG_FILE。
- `internal/studio/observability.go`、`catalog_observability_test.go`：models固定路由、自定义与已移除模型的固定kind+other指标桶及回归验证。
- `docs/api/openapi.yaml`：0.5.0、目录/ETag、逐模型能力、请求版本、输出身份/参数快照和409/422。
- `AGENTS.md`、`README.md`、`docs/planning/tasks.json`、`TODO.md`、台账索引；Wiki索引、architecture、development、backend、persistence-auth、performance、observability、roadmap；新增 `decisions/0003-versioned-model-catalog.md`。
- `docs/wiki/assets/`：models-phase5、mobile-models-phase5、video-presets-phase5、catalog-update-phase5、workbench-phase5截图；catalog-smoke/runtime-smoke/performance-local-v0.5-2026-11-13 JSON及browser-models-v0.5-2026-11-13 JSON。
- 目录核心、005迁移、provider、前端实际文件分别由018/019/020登记。`.runtime/`里的本地数据库备份、隔离验收助手及运行日志保持忽略，不包含发布资产或密钥文件。

## 接口与数据影响

增加GET /api/v1/models与目录ETag，公开模型只提供展示及能力，私有上游映射不进入响应。新工作台携带model_version；规格必须匹配完整parameter_presets，不能任意混搭。已存在幂等重放先于当前目录启停/版本校验；MODEL_VERSION_CHANGED为409，MODEL_DISABLED和参数不匹配为422。新版任务存受理时名称/能力/有效参数，live媒体无网络Prepare后同事务冻结原始提交字节。

005为数据格式版本登记，无表重写。旧v0.4遇到新版本明确拒绝启动，防止写回JSON丢失模型快照；不得删除005登记降级。历史live无handle且无submission的旧排队任务保守待核实，不按新映射重发。已知handle/冻结请求仍按原供应商身份处理。

指标新增固定models路由，合计28路由类别；模型缓存指标固定为3个历史系列和3个kind+other系列，自定义ID不增加标签基数，也不遗漏其占用。目录变更要重启，公开GET是客户端缓存校验，不是服务端热更新。

## 验证结果

2026-11-13，本机Windows/Go1.26.11、Node22.18.0、PostgreSQL18.7：

- 最终 `npm run check` 通过：81个Go测试函数及子例、85项前端测试（13文件）、8项治理测试、Go vet、严格TypeScript、前端生产构建和文档结构。最终 `npm run test:db` 全量通过，studio14.699秒，包含005增量升级与旧迁移加载器拒绝新库。无数据库的普通测试跳过不计真实PG通过。
- `npm run build` 通过。最终产物JS327.45kB/gzip103.69kB、CSS58.45kB/gzip12.72kB。
- `npm run smoke` 完整旧链路通过；`smoke:runtime` 5组通过；`smoke:catalog` 3组通过，包括公开映射隔离、ETag304、错误规格/数量拒绝、停用422/旧版本409、custom模型受理身份/有效参数/幂等、动态容量、文件变化不影响当前进程及重启生效。目录/运行冒烟报告与性能报告使用同一最终二进制SHA256，报告不含凭据。
- `npm run perf:local` 通过：1000次bootstrap/32并发P95 20.80ms、2316请求/秒；100组任务创建+取消/8并发P95 12.09ms；demo首delta P95 56.22ms。仅同机loopback、memory/demo回归，没有生产容量结论。
- 严格PyYAML读取无重复键、全部本地引用可解析、operationId唯一：26路径、30操作、109引用。这是结构核对，不宣称完整OpenAPI验证器认证。
- 开发库升级前备份 `.runtime/backups/pre-v0.5-1794557959045.dump`（34873字节）；重启自有开发进程，启用示例目录/demo，确认001—005均已登记。辅助psql初次使用默认连接参数未连到本地隔离端口；改为从本地凭据文件分别注入PGHOST/PGPORT等环境后查询通过，未修改数据库配置。
- 浏览器确认原会话/项目“月光品牌计划”、旧任务和云端草稿v5保留。新任务受理并完成；直接PG查询确认model_name、model_version、model_capabilities与1024x1024有效参数已持久。
- 独立本地demo目录浏览器验证：多图16:9/1536x864/1K/count3提交准确；视频9:16/1080p/10秒提交准确（demo结果仍是静态海报）；切模型时数量/规格调整有提示；216字节中文超过128字节保留全文并禁提交；停用模型不可选，重启目录再刷新保留提示词并提示核对；历史卡使用原名称/版本。
- 1280×720和390×844视觉验收通过，窄屏document.scrollWidth=innerWidth=390，截图已存；临时viewport已reset、隔离QA服务已关闭，PG开发预览保留在5173。最终预览控制台无warn/error。
- `todo:sync`、`check:todo`、`check:docs`、`check:changes`在证据齐备后执行并通过：10模块/23批/7已验收、45份文档/21台账、187个变更路径同步。模块中的交付前修正和首轮失败见018/019/020；没有隐去未完成真实联调。

## 风险与限制

目录声明是操作者对网关能力的配置，不是供应商实时发现或真实付费生成证明。更改文件需重启生效，无管理后台热更新；重启可能中断当前LLM。模型版本/启停不证明上游已取消旧任务，原未知仍需核实。未配置真实服务密钥、未调用付费AI、未公网部署。

仍是单写者本地实现；分布式调度/配额、对象存储生命周期、完整账本支付退款、审核/运营、账户恢复和生产性能/灾备验收待后续批次。本机race仍缺CGO/C编译环境，Linux CI虽配置尚未远程执行。验收批数不是工期百分比。

## 回滚方式

保留升级前备份；005库不能直接由旧二进制运行，禁止删除版本登记绕过。回退须在独立库恢复匹配旧程序的备份并验证，保留升级后原库中的新数据。目录配置可改回并重启，但不得改已受理的快照、原提交字节与幂等键，也不得将旧live未知记录按新目录重新生成。

## 文档同步

README、AGENTS、模块Wiki、ADR0003、018—021索引、任务源及TODO同步，V06填入实际checks/evidence/ledgers后标done；历史001—017保留。下一批V07参考素材上传与图片编辑依赖V06，真实供应商验收仍须具备相应外部条件。
