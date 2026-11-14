# 035 · V10 多轮对话与持久消息树

日期：2026-11-13。关联V10。本记录覆盖后端领域/HTTP/持久化，Provider和前端及根最终联合验收分别记录；不将本模块专项当作整批已验收。

## 目的

将现有单轮run扩展为可刷新、分支、停止后显式继续的多轮会话，保证选中路径真实传入模型、原请求受理可核对，保留旧单轮历史与执行恢复边界。

## 范围

会话/不可变消息树、结构CAS、单会话单活跃run、上下文整轮裁剪/冻结、分页/权限/容量、同事务审计/Outbox、非法输入与慢客户端保护。Provider wire字段/估算器及目录能力由036提供；原媒体租约/资产内容不改。

## 实际文件

- `internal/studio/conversation.go`：类型、受理、分支/继续、上下文选择和内存界限。
- `internal/studio/conversation_sql.go`、`conversation_http.go`：有界批量读、项目scope、列表/路径/叶子分页及HTTP契约。
- `internal/studio/conversation_test.go`：上下文、容量历史、空答、转义后响应、慢写、PG归属/不可变/事务/确认丢失等测试。
- `internal/studio/migrations/010_conversations.sql`：会话、节点、私有执行上下文、复合外键、单活跃索引、不可变触发器及闭集通知/审计扩展。
- `internal/studio/app.go`、`model.go`、`persistence.go`、`http.go`：run关联、执行快照、同事务受理与结构化Provider接线，非法增量保护及旧单轮兼容。
- `docs/planning/v10-contract.md`、`docs/wiki/conversation.md`及本记录。根负责OpenAPI/指标/010维护栅栏、README/索引/任务源；Provider代理拥有catalog和provider。

## 接口与数据影响

新增conversations列表/创建、详情、runs创建、messages路径及branches叶子读取。Conversation version为结构CAS；assistant只关联Run，状态/输出仍由原run检查点与终态保存。新run有conversation/message IDs、受理version、context_summary；旧run字段省略保持兼容。

受理冻结上下文和上游参数，节点/Run/CAS/幂等/审计/Outbox同事务；同键重放先于新模型/预算/CAS规则。010单活跃索引禁止同会话并发生成；消息关系及上下文触发器阻止就地改写。continue固定新user和新run，不重连旧请求。消息正文、上游配置和私有快照不进入事件。

PG批量读取祖先运行，路径<=128节点、会话<=256；memory保留历史并在容量满时拒收。消息页最多40、真实JSON转义后<=1MiB，HTTP写入前释放状态锁。非法NUL/ID/cursor进入DB前拒绝；上游非法增量保留有效输出后失败。

## 验证结果

- 普通专项 `go test ./internal/studio -run '^TestConversation' -count=1` 最终通过0.154秒（4函数）：整轮裁剪/继续来源保留、空答后普通回复、编辑兄弟分支/原键优先/内存满不丢历史、1MiB转义后分页和阻塞ResponseWriter仍可取得状态锁。
- 真实PG `go test ./internal/studio -run '^TestPostgresConversation' -count=1 -v -timeout=90s` 通过5.978秒，包含3个本模块函数和正则匹配到的根ActualCrash函数：多轮私有快照、DB不可变触发器、跨账户/游标scope、NUL/恶意ID保持DB健康、非法delta保留前缀、CAS并发和通知失败全事务回滚；根实际Kill用例同次也通过2.30秒。
- 真实PG `go test ./internal/studio -run '^TestPostgresConversationUnknownCommitReplaysWholeTurn$' -count=1 -v -timeout=60s` 通过0.971秒。代理在数据库完成COMMIT后丢弃确认，重启以原键返回同一INTERRUPTED run；仅两个节点、一份上下文、会话version=2，未重发模型。
- `go vet ./...`通过。根报告独立built binary多轮HTTP fixture冒烟5组已通过；根最终全仓、构建和浏览器验收另记，不在本记录提前宣称。
- 没有使用真实供应商密钥或执行收费请求；估算不是实际计费，未做生产高并发容量成绩。

## 风险与限制

当前窗口来自配置，默认值不等于实际模型声明；UTF-8估算可能比供应商更保守，裁剪结果必须显示。continue来源整轮过大则拒绝，不裁断原文。源历史与冻结快照会增加存储，当前有会话/节点/路径/消息/内存上界，没有完整保留或永久删除策略。

单控制写者和状态锁仍在，PG祖先批量读有界但不是分布式运行。LLM token delta仍不持久，崩溃只保留最近检查点；显式新continue可再次收费。会话默认head随新轮次变化，浏览器旧leaf选择不隐式写库。

## 回滚方式

010前停写并联合备份PG、两个私有目录及对象/收据。新程序写过010后旧二进制拒绝，使用匹配旧版本的独立备份库和目录回退，保留新会话和升级库；不能删迁移登记或owner marker绕过。原单轮数据不做破坏性导入，回退不等于自动转换新会话结构。

## 文档同步

已同步[多轮对话Wiki](../wiki/conversation.md)及[V10契约](../planning/v10-contract.md)。Provider目录能力、前端工作区由对应代理更新；根维护README/Wiki/台账索引、OpenAPI、任务源/TODO及最终联合验收。当前实际边界与整体方案目标分开记录。
