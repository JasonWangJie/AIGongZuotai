# V16-A2 · 媒体提交与查询交付隔离

状态：本地已验收，实际命令与失败修正见[071](../changes/2026-11-14-071-media-lane-integration.md)。依赖V16-A1、V05、V09以及既有B2审核边界；完整V16的A3/B/C继续保留。上一轮已完成A1，是有代码、真实验证及文档的实质进展，本轮继续下一条执行链。

## 交付范围

图片、图片编辑和视频共用两条有限执行通道：`submit`处理无handle任务，`query`处理已有handle任务及本次查询的下载、导入、审核和最终保存。慢POST不能占用全部查询资源，慢查询交付也不能占用提交资源。

工作线程、执行预算、PostgreSQL辅助连接和HTTP连接池均按通道隔离；默认各2，服务端`MEDIA_SUBMIT_WORKERS`与`MEDIA_QUERY_WORKERS`仅允许十进制1—32，未设置才默认，显式空/零/越界拒绝。配置启动生效；没有新增凭据路由或多实例共享配额。

Go内旧`TaskWorkers`作为未显式配置时的兼容默认：submit为max(1,ceil(N/2))，query为max(1,floor(N/2))。旧N=4为2+2，旧N=1为两通道最小1+1，不宣称仍总并发1；该旧字段不是服务端环境开关。

## 执行与恢复约束

1. 必须先有通道执行预算，再获取对应PG连接并领取租约。禁止先写SUBMITTING再等待执行名额。
2. 持久队列继续来自PG到期任务。SQL按冻结handle是否存在选通道，不从当前模型目录推断原请求；无handle SUBMITTING失租仍须进入未知隔离，不可永久遗漏或再次POST。
3. memory模式由固定worker短锁选择匹配通道的到期记录；不另造每任务goroutine或无界待发队列。槽位满时任务继续保留，不能把已经受理任务拒绝或丢掉。
4. 获取预算后仍保留原租约owner/epoch/generation、预期业务版本和事务提交保护。取消/运营核对/财务处置使旧领取失效，迟到结果不能公开；实际步骤返回前仍占原通道预算。
5. 终态Outbox背压只重试状态事务，不重新Submit/Query。数据库确认不明、失租和停机延续原隔离规则。
6. B2已有私有候选不能由下一轮领取自动重审；全部通过并确认提交后才交付/结算。媒体通道拆分不能弱化参考/产物门禁、REVIEW冻结或已受理原键优先。
7. JSON提交与multipart编辑共享submit Transport，查询GET使用独立Transport。保留重定向禁用、凭据冻结、原字节发送、超时与未知受理语义；连接池隔离不授权重复POST。
8. 运维仅固定`lane=submit/query`聚合：已保留预算、配置上限和已领取步骤计数。已保留预算包括领取/连接等待/下载审核/保存，不等于网络并发；计数不冒充供应请求或实际交付量。普通workspace API不泄漏全局用量。

## 文件归属

| 归属 | 文件与职责 |
| --- | --- |
| architecture / 068 | app.go、task_leases.go、task_leases_worker.go、media_execution_lanes.go及对应核心测试；执行/PG预算、memory/PG接线 |
| go_streaming / 069 | media_http.go、独立媒体Transport测试；submit/query/edit连接池及关闭 |
| media_lane_validation / 070 | 新media_lane_integration_test.go；独立真实App/PG并发和故障验收 |
| root / 071 | cmd/env、固定指标、隔离脚本、版本/API说明、任务源、README/Wiki与联合验证 |

## 验收

- 真实HTTP堵满POST时GET仍进入，堵满GET时新POST仍进入；编辑与JSON提交受同一submit池限制。并发峰值不超过配置，取消与关闭不泄漏连接。
- 真正App受理任务，memory及PG分别验证两通道双向独立、预算占用到真实返回、原幂等不重复执行。
- handleless SUBMITTING租约过期不重POST，旧lease/版本迟到不发布、取消不复活；真实PG回滚及COMMIT确认不明保持原语义。
- 慢导入/审核与B2整组隔离、终态背压、权益/运营核对的既有测试继续通过，不删除断言或仅测试配置字段。
- 严格环境输入、安全指标、全量Go/PG/前端构建及隔离smoke通过后归档证据；更新任务源A2 check，完整V16仍in_progress。

## 后续范围

A3需独立设计查询与导入/审核之间的持久移交、恢复和公平供应限流；本次不能宣称这些阶段已拆分。B持久token日志与跨实例fencing、C综合测量以及V17生产长稳态不被本地受控Provider替代。当前不调用真实收费渠道、不发布公网。
