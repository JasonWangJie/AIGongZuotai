# 2026-11-14-071：V16-A2媒体执行隔离联合集成

## 目的

媒体慢提交原本占用查询所需的共享执行资源，本次将提交与查询交付的预算、数据库辅助连接和HTTP池分别限定，保持已受理任务的恢复与审核账务语义。

## 范围

V16-A2/H03/H07/H09。核心068、Transport069、独立验收070由各归属负责；本记录维护严格配置、观测、联合验证及根文档。查询内部下载/导入/审核留在query通道，A3/B/C及完整项目尚未完成。

## 实际文件

- `cmd/server/main.go`、`media_execution_config.go`、`media_execution_config_test.go`、`.env.example`：两个严格MEDIA工作线程配置。
- `internal/studio/observability.go`、`media_lane_observability.go`、`media_lane_observability_test.go`：固定双通道指标、真实执行HTTP观察。
- `scripts/lib/process.mjs`、`scripts/load.mjs`：smoke/性能子进程的调度配置隔离与准确口径。
- `docs/planning/v16a2-contract.md`、`v16-contract.md`、`tasks.json`、`progress.md`、两份TODO；根README/AGENTS、相关Wiki/API及台账索引，版本文件在验证收口时同步。
- 收口实际版本文件：`package.json`、`apps/workbench/package.json`及`package-lock.json`、`docs/api/openapi.yaml`；没有依赖版本升级。
- Wiki实际页面：`README.md`、`architecture.md`、`development.md`、`scheduler.md`、`media-gateway.md`、`observability.md`、`performance.md`、新增`decisions/0015-media-execution-lanes.md`；068/069各自维护对应媒体Wiki。
- 新证据：`docs/wiki/assets/media-lanes-validation-v0.17-2026-11-14.json`、`performance-local-v0.17-2026-11-14.json`以及runtime/catalog/reference/library/conversation五份同版本smoke JSON。

## 接口与数据影响

没有新增公共API或SQL格式。`MEDIA_SUBMIT_WORKERS`与`MEDIA_QUERY_WORKERS`默认各2、显式十进制1—32，错误不回显原值。新增固定双lane指标，需已有独立运维令牌；不读取全PG队列或向普通用户泄漏全局数量。幂等、原handle、未知受理、租约、内容审核和账务原规则保留。

## 验证结果

本批本地联合验收完成。以下保留首次失败、修正和最终实际证据；真实渠道、生产容量与完整V16尚未验收。

### 首轮验证与修正

- 严格MEDIA环境与真实双通道指标专项通过：cmd/server 0.068s、studio 0.063s。
- 子模块068的首次PG与070同时争用数据库级控制写锁，随机schema无法隔离；后续完全串行。B2故障注入的裸App未经过New，零值lane配置导致零连接池等待；initTaskLeases补同一规范化与预算初始化，原断言不变。最终核心20函数37.077s、独立五函数及根指标总6.255s通过。
- 根首次完整`node .runtime/v16-check.mjs check`通过：Go普通28.921s、301前端/29文件、10治理、Go vet及TS/Vite/Go构建；Vite14.17s、主JS495.68kB/gzip157.13kB，155文档/71台账。
- 根首轮全PG总402.844s失败：两个旧账户安全函数TLSMailHTTPJourney、SessionsChangeAndIsolation在5秒HTTP/数据库边界超时；同轮有本项目构建和主机其他任务，并不能只凭此认定根因。没有更改超时或旧测试。无并行本项目构建的原两函数重跑通过8.060s，接着完整串行重验，最终结果另列。
- 六套smoke已全部打印PASS，五份JSON均为0.17.0且SHA与最终构建一致；父环境故意无效的七个CHAT/MEDIA配置及真实渠道配置均由脚本隔离。性能安排在本项目PG退出后，主机不独占，不能据耗时差宣称吞吐提升。

### 最终验收

| 实际命令或检查 | 结果 |
| --- | --- |
| `node .runtime/v16-check.mjs check` | 通过：等价隔离渠道的npm run check，Go普通28.921s、301前端/29文件、10治理、vet及TS/Vite/Go构建；没有新增前端源码 |
| `node .runtime/v16-check.mjs db` 最终串行 | 通过：等价独立本地PG的npm run test:db，studio318.880s；未改变账户断言/超时 |
| `node .runtime/v16a2-smokes.mjs --smokes-only` | 六项PASS，API/UI静态服务、runtime、目录、参考编辑、资产和会话；五份JSON同构建SHA |
| `node .runtime/v16a2-smokes.mjs --performance-only` | 通过；本项目PG退出后执行；默认submit2/query2，非媒体吞吐测试 |
| YAML解析/本地引用与迁移SHA复核 | 71路径/83操作/95schema/411引用，版本0.17.0；17SQL/55486字节与064摘要一致，格式017不变 |
| 浏览器视觉、Go race、远程CI | 本批未执行：没有前端布局改动；本机未发现GCC/Clang。不借用旧截图或配置当作本批通过 |

构建SHA256：`535bf0dfe9bbf94c54c7b819f5a148dd968196f08b9eedc0182cb3cbf76acdaf`。[联合证据](../wiki/assets/media-lanes-validation-v0.17-2026-11-14.json)及新增`*-v0.17-2026-11-14.json`保留本轮结果，不覆盖旧报告。前端主JS495.68kB/gzip157.13kB。bootstrap P95 23.55ms/2095.97请求秒，创建取消P95 13.42ms；demo首delta P95 1874.61ms含同workspace2/客户端4的排队，完整流P95 3525.43ms。主机非独占，没有CPU/RSS/FD或生产容量测量，不宣称相对旧版提速。

## 风险与限制

当前单控制写者，通道预算不等于跨实例共享配额。query仍包含下载导入、审核和保存，慢交付可占用同通道查询预算；后续A3须定义持久阶段移交，不可直接重领已有B2候选重审。fixture/本机结果不是生产吞吐或真实渠道验收。

## 回滚方式

本批不改变017数据库格式。停机后恢复上一代码与配置，保留任务、handle、私有候选和账务；不得在进程运行中更换worker或对受理不明任务再POST。不得删除迁移、owner或历史台账。

## 文档同步

README、Wiki、任务源与双TODO跟随真实验收状态；A2完成后只更新对应check，不提前勾完整V16。新增068—071台账保留063—067已交付事实；持续推进记录完整列出完成、当前、剩余、外部条件与下一项。

收口已将A2标passed，完整V16仍in_progress、completed_on=null；A3拆成三个明确待验check，仅记录下一步设计，不当作已实现。根README/AGENTS、版本及锁文件、Wiki导航/架构/开发/调度/网关/观测/性能、ADR0015、任务源及双TODO、071与索引同步；七份本轮JSON证据含五smoke、性能及联合汇总。原063—067历史验收不改写，069仅校正冲突helper归属为独立验收代理。

最终`todo:sync/check:todo/test:todo/check:changes`等价命令及`git diff --check`通过：10治理、155文档、71台账、279个当前变更路径（包含此前未提交工作，不冒称本批独占279文件）。本批源码冻结后仅更新文档、任务源与证据，不以重复构建替代验收内容。
