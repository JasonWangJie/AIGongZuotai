# 多轮对话、分支与上下文

V10新增项目内对话树和真实结构化历史传递。精确接口见[V10契约](../planning/v10-contract.md)，实现/验证见[035](../changes/2026-11-13-035-conversation-core.md)，Provider预算与wire字段由036记录。旧`/runs`单轮创建及历史读取继续保留，不自动迁移成会话。

## 对话与执行分开

Conversation保存项目归属、标题、结构version、head及节点数量；active_run_id从关联run状态推导。user节点持久原文，assistant节点只引用Run，读取时取得该run的输出/状态/seq，不在每秒检查点复制第二份完整正文。

010用复合外键约束conversation/message/parent/run的工作区和项目。数据库触发器拒绝消息节点与执行上下文更新或删除；生成过程只更新原Run。编辑旧user会创建同父节点的兄弟user及新assistant，原文和原分支保留。一次受理增加两个节点和一个会话version；每秒输出不增加会话结构version。

每会话最多一个活跃run，由服务端检查和PG部分唯一索引共同约束。expected_version防止旧标签页按过期结构提交。选择旧leaf用于显式读取，只有受理新轮次才改变head，不把浏览器切换分支当隐式修改。

## 原子受理与重试

会话创建以及新轮次均要求幂等键。新轮次把Run、两个节点、私有冻结上下文、会话CAS、幂等、审计及Outbox放在同一事务；通知失败也不会留下半轮消息。原键重放先于模型启停/版本、预算、CAS和忙状态，不复制消息、不重发模型。

Commit确认丢失时保持受理不明。客户端保留原body/key，通过原键确认结果；不能自动换键。新会话在PG模式最多每工作区1000个；memory最多64会话/2048节点，另受既有run容量限制。内存满时明确拒收，原会话引用的历史仍可读。

## 上下文选择

只读取选定父assistant的祖先路径，兄弟分支不进入模型。PG一次读取有界祖先元数据，并用ANY批量读取对应运行，避免每个节点一次数据库往返；messages同样批量，branches仅JOIN读取最多160字符预览。

上下文在受理时固定为user/assistant交替数组，冻结实际上游模型、输出字段与预算。新Provider接口接收该数组；不支持结构化上下文的适配器在受理时拒绝，不能丢弃历史降级为单prompt。

模型声明context_window_tokens与max_output_tokens。估算采用UTF-8字节+每消息16+256安全余量，名称utf8_bytes_v1；这是保守准入估算，不是供应商精确tokenizer或费用。上下文最大129条/256KiB内容，按最旧完整轮次裁剪，不能切掉半个user/assistant对。

Run.context_summary说明输入估算、输出预留、模型声明窗口、纳入/遗漏消息数与估算器。默认窗口不是自动探测，真实模型能力需要运营者核对。目录调整只作用于新请求，原键与已受理请求保持原快照。

## 停止、继续与恢复

停止沿已有run cancel。失败/取消/中断保留已生成部分；空答可继续提新问题，其原user/空assistant整轮从上下文略过并计入遗漏数。它不能作为显式continue来源。

continue要求终态assistant有输出，服务端新建“请从上一条回复结束处继续，不要重复已有内容。”用户节点和一个新run；它是新的模型请求，可能产生新费用，不是恢复旧连接或改写原回复。必须保留来源assistant所属上一轮，预算不够则拒绝，不能静默删除续写来源。

进程重启仍将未完成run标为INTERRUPTED，保留最后检查点；消息树通过原run关联显示部分输出，不自动重新请求。真实进程Kill、原键重放和显式继续已通过根联合验收，证据见[038](../changes/2026-11-13-038-conversation-integration.md)。

## 读取与客户端

Conversation列表按created_at/id稳定keyset。messages以指定leaf为终点从新到旧分页，每页items以祖先到后代显示；默认20、最大40节点，JSON转义后的正文响应限制1MiB，可能少于limit。branches只返回叶子元数据与160字符预览，默认20/最大50，不一次取整棵树正文。

游标绑定账户、workspace、project、conversation与leaf，最多1024字节；标识有固定格式校验，不能把任意含NUL的URL或自制游标交给PG编码。消息输入和上游delta也拒绝NUL/无效UTF-8；上游异常保留有效前缀并结束，不毒化数据库。

HTTP响应写入在全局状态锁释放后进行，慢客户端不能通过大消息页占住业务锁。原run SSE继续负责实时文本；run.updated仅补读对应run，新增conversation_id定位时间线。conversation.created/turn_created以脱敏audit.created通知刷新必要摘要，消息内容不进入Outbox。

## 当前边界

仍是单控制写者与有限本地内存/PG服务，没有多活对话执行、永久删除、摘要压缩、工具调用、系统提示词编辑或图文附件。节点版本不等于内容可变历史；保留上游失败与未确认操作，不能把受理不明当成生成失败重新发送。

升级010后旧程序拒绝写库；按[持久化](persistence-auth.md)、[事件](events.md)及[私有存储维护](storage-maintenance.md)联合备份与恢复，不能删除schema或owner记录强行降级。真实供应商收费验收和生产容量另行推进。
