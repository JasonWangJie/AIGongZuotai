# 2026-11-13-044：LLM 结构化用量与媒体计量边界

## 目的

V12 / H03、H05、H06：为权益账本提供来源明确、完整性可判断的结构化用量；不以字符/上下文估算代替真实token账单，不把取消、断流、缺失或矛盾用量当作0费用。

## 范围

可选计量Provider方法、usage解析与校验、明确synthetic演示计量、只读媒体费用契约核对、本地HTTP fixture与说明。账本价格、冻结/结算事务及App持久接线由核心批次负责；没有修改app/model/persistence、未增加依赖、未调用真实收费上游。

## 实际文件

- `internal/studio/chat_usage.go`：ChatUsage/ChatUsageProvider、严格整数验证、有界汇总与demo固定计量。
- `internal/studio/provider.go`：共用Transport的新计量请求路径、include_usage与可选SSE usage观察；旧请求兼容。
- `internal/studio/chat_usage_test.go`：6个新增函数及边界子例，仅本地HTTP。
- `docs/wiki/chat-usage.md`：协议、字段、结束语义、媒体SKU与供应商成本区分；本台账。

## 接口与数据影响

新增 `StreamMessagesUsage(ctx,model,ChatRequest,emit)(ChatUsage,error)`，返回status/source/complete、三个int64计数及固定reason。只有新方法发送include_usage，旧Stream/StreamModel/StreamMessages不改变线协议；拒绝后不变更参数重新生成。

计数均为JSON整数0—1e9且total=prompt+completion；相同重复不累加，非法和冲突保持invalid。正常流缺usage为missing，断流即使见过数字也missing/complete=false；核心只能对COMPLETED+err=nil+complete+reported/demo候选结算。演示固定128+64=192，与文本长度无关，明确source=demo。

媒体官方文档确有quota/cost但缺完整单位/结算更正契约，未导入为平台费用。平台按冻结SKU与已安全导入并同事务注册的交付证据计价，不能把请求数量/时长或HTTP失败当收费事实；明确PARTIAL按已交付数量结算并释放差额，缺失/未知保留核实。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./internal/studio -run '^(TestChat\|TestOpenAI\|TestLive)' -count=1` | 通过 | 新实现初轮兼容回归0.120秒，未包含随后新增usage fixture |
| `go test ./internal/studio -run '^TestChatUsage' -count=1` / `go vet ./internal/studio` | 通过 | 6个新增函数及边界子例0.854秒；vet通过。初轮同期核心类型未落盘导致的编译阻断已在接线后重跑解决 |
| `go test ./internal/studio -run '^(TestChat\|TestOpenAI\|TestLive\|TestCatalog)' -count=1` | 通过 | 23个Provider/目录相关函数1.224秒，仅本地fixture |
| 官方媒体文档取证 | 通过 | 2026-11-13公开页面HTTP200及文档chunk6168.9dd1da9042.js，仅GET，无凭据/生成 |
| 真实New API计量/扣费对账 | 未运行 | 未提供受控真实计量验收条件，fixture不能替代供应商真实账单 |

## 风险与限制

reported表示配置网关报告且结构通过，不证明供应商发票真实或成本一致。未接入缓存/推理细项差异费率、供应商用量查回、媒体quota/cost换算、真实视频时长计量或自动纠错。网关不支持include_usage时不fallback重复调用；必须调整实际服务契约并受控验收。账务数值与报价溢出/结算策略由核心独立验证。

## 回滚方式

可以停用账务启用开关并保留所有持久报价/预留/证据，不把待核实改成已退款。旧Provider方法保持兼容，但已经升级的账本数据库不能用旧程序直接写回；遵循核心迁移格式栅栏与联合备份恢复方案，不删迁移登记或凭空重算已收费历史。

## 文档同步

- README：根集成记录V12计量能力与真实验收差距、最近变更。
- Wiki：[Provider用量证据](../wiki/chat-usage.md)新增；根维护导航及账本关联。
- 台账索引：根增加044并与核心/前端/集成记录关联。
- 任务状态：根依据实际验收更新tasks.json/TODO，不把真实渠道和计费验收标成已完成。
