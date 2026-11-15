# 2026-11-14-067：V16-A1配置、观测与联合验收

## 目的

为新聊天公平调度提供可配置执行预算、有限安全指标及完整联合验证，防止单workspace深队列长期挤占其他用户。

## 范围

V16-A1/H03/H07/H09。核心065、独立验收066，本记录负责配置、观测、隔离验证、任务源和交付集成。A2/A3媒体隔离、B持久流、C综合容量仍待实现。

## 实际文件

- `cmd/server/main.go`、`cmd/server/execution_config_test.go`、`.env.example`：严格五项CHAT配置与启动接线。
- `internal/studio/observability.go`、`chat_execution_observability.go`、`chat_execution_observability_test.go`：固定无标签调度指标。
- `apps/workbench/src/api.ts`、`requestFeedback.ts`、`executionFeedback.test.ts`：确定队列拒绝、原未知保留、冷却与手动重试提示。
- `internal/studio/http.go`、`docs/api/openapi.yaml`：两项明确容量拒绝及Retry-After协议。
- `scripts/lib/process.mjs`及七个smoke/load脚本：独立默认执行预算，不继承开发者渠道调优。
- 根与工作台`package.json`、工作台`package-lock.json`：版本0.16.0，依赖不变。
- `docs/planning/v16-contract.md`、`tasks.json`、`progress.md`、两份生成TODO，根README/AGENTS、Wiki导航/performance/observability/roadmap、decisions/0014、台账索引。

## 接口与数据影响

无新的数据库版本或公共API路径；现有聊天接口新增workspace队列429，原全局503保持，均提供Retry-After:3。新增五项服务端CHAT配置，非法显式值拒绝启动且错误不回显值。/metrics新增11个固定调度系列，仍需要独立运维令牌，不含用户/模型/凭据标签；没有把全局调度暴露给普通workspace runtime。队列计时仅本进程累计和最大值，不是P95/P99。

## 验证结果

以下为本批最终实际验证；首轮失败和修正保留在文末。未运行的浏览器视觉重验及race单独列明。

本机旧LocalAppData PG实例缺失，原postgres.env与忽略备份仍保留；为本轮验证在独立ASCII目录初始化新PG，端口55433，仅新建test数据库，不覆盖用户开发库/配置或恢复冒充原现场。测试将通过忽略的独立envwrapper注入，不在文档保存连接凭据。

## 风险与限制

单控制写者、单chat网关凭据；持久token日志和跨实例调度没有完成。限并发的公平是按可执行机会轮转，不保证长短任务的耗时公平。真实渠道、万级/长稳态及V16整批未验收，不能据此公开收费上线。

## 回滚方式

恢复上一程序和调度配置；本批不更改017格式。停机后恢复代码接线，不在线替换worker或重复POST已受理请求。保留现有数据库/私有文件和幂等/审核/权益结果；不能删除迁移或owner回退。

## 文档同步

README与Wiki记录A1实际进展/边界、五项配置和指标；V16任务源保持in_progress，分批checks证据按真实验收更新，两份TODO同步。063/064历史和V14-C1验收保留。

### 实施与首轮检查记录

- 并行接线首轮Go编译发现观测源文件同名覆盖导致原Outbox/lease方法缺失；原文件本轮前无改动，已按HEAD完整恢复，新指标移至独立chat_execution_observability.go。之后全包编译和指标HTTP专项通过，原指标未删除。
- 首轮联合check已通过Go vet、Go普通全量8.649s、301项前端/29文件、10项治理及生产构建1.75s；最终docs阶段发现并行新065尚未加入索引而失败。补索引后继续完整联合验证；保留失败，不将首轮命令写成全通过。

### 最终联合验收

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `node .runtime/v16-check.mjs check` | 通过 | 隔离真实渠道且清除TEST_DATABASE_URL，执行与npm run check相同链路；Go vet/普通studio8.821s、301前端/29文件、10治理、严格TS/构建1.79s通过 |
| `node .runtime/v16-check.mjs db` | 通过 | 等价npm run test:db，独立本地PG全量studio238.022s；包含017迁移和全部适用故障注入，不是fixture替代PG |
| `node .runtime/v16-a1-smokes.mjs` | 通过 | 六smoke+perf；父进程五CHAT配置无效并启用无效审核/邮件/账务，子进程仍正确隔离；最终SHA一致 |
| Python YAML解析与本地ref解析 | 通过 | 71路径、83操作、95schema、411引用；0.16.0 |
| 根SQL/HTML复核 | 通过 | 17SQL/55486字节按064摘要相同，三HTML原字节及本地链接通过 |
| 浏览器视觉重验 | 未运行 | 本批只改队列拒收/原未知协议与反馈文案，没有布局改动；四项新协议回归、全部前端测试及构建覆盖，不借用旧截图当本批验收 |
| Go race | 环境限制 | 当前未发现GCC/Clang；没有运行本机race或远程CI，不把CI配置当通过 |

构建SHA256：`4f8b5002a97548bda126abccb6fcf29a5674b435f91273135f3a729367b173c5`。联合汇总存于[execution-validation](../wiki/assets/execution-validation-v0.16-2026-11-14.json)，五份smoke报告及[本地性能](../wiki/assets/performance-local-v0.16-2026-11-14.json)分别新增到`docs/wiki/assets/*-v0.16-2026-11-14.json`，不覆盖历史报告。

主JS495.68kB/gzip157.13kB；bootstrapP95 24.54ms、2097.14请求/秒，创建加取消P95 14.25ms。demo首deltaP95 1761.82ms含队列等待：12请求/客户端4并发、同workspace预算2，完整流P95 3412.92ms。旧版同workspace4活跃；配置口径改变，不能拿差值宣称性能退化/提速，更不是生产SLO。

A1通过后任务源只勾选对应check，完整V16保持in_progress、completed_on=null，A2/A3/B/C完整保留。每轮进度和下一执行的媒体隔离范围已同步；不标全产品完成。

最终文档收口后重新运行todo:sync/check:todo/test:todo/check:changes：10项治理、147份文档/67份台账、252个当前工作树路径检查通过（包含之前未提交工作，不冒称本轮独占252文件）；`git diff --check`通过。
