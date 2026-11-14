# 031 · V09 事务Outbox与项目持久通知

日期：2026-11-13。批次：V09。Outbox专项及V09联合验收已通过，整批最终证据见[034](2026-11-13-034-durable-execution-integration.md)。

## 目的

消除数据库已提交、进程未广播即退出造成的通知丢失；让项目通知在有限保留窗口内按已授权范围恢复，同时保留原LLM增量流的实际耐久边界。

## 范围

状态/审计同事务Outbox、项目提交序号、持久feed消费去重与checkpoint、容量背压/裁剪、PG workspace SSE、opaque范围游标及故障测试。LLM token delta、外部broker、公众部署、多实例流和浏览器持久ACK不在本记录范围。任务lease由032负责，核心saveTask同事务调用其fence helper。

## 实际文件

- `internal/studio/outbox.go`、`outbox_http.go`、`outbox_test.go`：元数据事件、事务投影、界限、游标/SSE与测试。
- `internal/studio/migrations/008_outbox.sql`：项目head、事件、feed、checkpoint和有界计数。
- `internal/studio/persistence.go`：创建/保存及启动恢复同事务事件，接租约UPDATE helper。
- `internal/studio/audit.go`：既有闭集审计同事务附带通知。
- `internal/studio/http.go`、`model.go`：PG SSE分支及opaque cursor，原run数字Seq兼容。
- `docs/planning/v09-contract.md`、`docs/wiki/events.md`及本台账：接口、实现边界、故障证据。
- App生命周期由任务租约代理接入prepareOutbox/dispatchOutbox；根负责指标helper、维护schema栅栏、OpenAPI、README/索引和任务源。

## 接口与数据影响

新增008迁移，旧程序不能忽略新数据格式。业务、幂等、审计与Outbox同事务；run checkpoint/恢复也进入事务。每项目seq与Task.Version/Run.Seq区分，SQL行锁保证同项目提交顺序，不用全局identity分配顺序推断commit。

消费者投影、event唯一去重、checkpoint和pending释放同事务。全球未消费最多10000，满时503 EVENT_BACKLOG_FULL全部回滚但数据库仍健康；每批100，每项目保留2048、全球100000已消费，修剪维护floor。未知commit不伪称回滚，重启按持久记录恢复。

PG workspace SSE新增resource.changed和stream.reset；task.updated兼容快照用自身资源版本。游标绑定数据库/账户/workspace/project，长度和数字范围有限制；Last-Event-ID优先after。首次/过期reset使用已投影水位，pending通知不得跳过。memory沿原语义；LLM token delta不进入Outbox，原run SSE仍数字Seq。

## 验证结果

- 首轮真实PG `go test ./internal/studio -run '^TestPostgresOutbox|^TestDurableCursor' -count=1 -v -timeout=120s` 通过10.448秒：6个PG函数及1个普通函数/6个游标子项。
- 已实际覆盖producer/consumer故障整事务回滚、两独立连接序号阻塞与回滚、10000条真实pending拒绝/排空恢复、2048保留及100条分页、重启去重、实际COMMIT确认丢失、SSE重连与账户/项目拒绝。元数据无提示词/输出/token delta检查通过。
- 新增真实PG `go test ./internal/studio -run '^TestPostgresOutboxConsumerUnknown|^TestPostgresOutboxInitialReset' -count=1 -v -timeout=60s`通过1.152秒：消费者COMMIT确认丢失后恢复不重复；确定性先提交pending再连接，reset cursor保持0，消费后仍收到原事件。至此本模块8个PG函数及1个普通函数/6个游标子项均已实际运行。
- 普通游标专项及`go vet ./internal/studio`通过。租约代理报告全量PG65.615秒仅根新增子进程fixture配置缺失失败、原业务及本模块均通过；根已修fixture并接管独立全量，本记录不提前宣称最终全量完成。
- 未执行真实供应商收费请求、外部broker测试、100000全局保留容量压测或生产多机故障演练。

## 风险与限制

当前仍单控制写者和单PG事务连接，全球counter及项目head有串行成本；不是已达到总体极致性能目标。通知仅闭集元数据和有限保留，不是完整历史事件源。已消费checkpoint不表示浏览器收到，两帧同cursor中断需要客户端按resource.changed补拉；慢连接有写期限但不承诺恰好一次显示。

事务背压不能丢弃已完成上游结果或重发请求；媒体原handle/result保存重试及LLM终态保存重试由App所有者协作处理。数据库commit异常仍停止写者，可能已受理，客户端继续保留原body/key。

## 回滚方式

升级前停写并联合备份PG、REFERENCE_DIR、ASSET_DIR与所用对象/收据。008/009后旧二进制必须拒绝；回退使用匹配旧版本的独立数据库/私有目录备份，保留新库及作品，不能删迁移版本或owner marker。事件裁剪不能恢复旧通知，但资源快照及业务记录仍是事实来源。

## 文档同步

同步[持久通知Wiki](../wiki/events.md)及[V09契约](../planning/v09-contract.md)。根集成人维护README/Wiki/台账索引、OpenAPI、任务源/TODO及最终验收；本记录不代替V09完整租约与故障放行。

根最终联合`npm run test:db`通过studio66.419秒，包含本模块全部故障测试。原fixture失败和专项时间保留如上；最终实际进程Kill、浏览器、构建及限制以034为准。
