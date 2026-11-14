# V10 多轮对话契约

v0.9.0本地联合验收通过，证据见[038](../changes/2026-11-13-038-conversation-integration.md)。新增010迁移，保留旧`/runs`单轮创建/历史读取；旧记录不自动改造成对话，不隐藏。服务仍是本地、单控制写者，真实收费模型测试不自动执行。

## 数据与身份

Conversation为`{id,project_id,title,version,head_message_id:null|string,active_run_id:null|string,node_count,created_at,updated_at}`。初始version=1，head为空。workspace由真实项目/会话推导，不接受body指定。

MessageNode为`{id,conversation_id,parent_id:null|string,role:"user"|"assistant",content,run_id:null|string,status,seq,created_at,updated_at,model_id?,model_name?,error?}`。user内容/父节点不可变，status=COMPLETED、seq=0；assistant节点仅持久关联Run，返回的内容/状态/seq读取对应Run，不重复存储检查点正文。Run新增conversation_id/user_message_id/assistant_message_id/conversation_version/context_summary（旧run可省略）。

树节点身份、父关系、user原文、assistant与run关联不可修改；编辑创建兄弟节点。每会话最多256节点、每条路径最多128节点，同一会话含各分支最多一个活跃run。运行状态仍使用现有QUEUED/RUNNING/COMPLETED/FAILED/CANCELLED/INTERRUPTED，刷新读取已持久检查点；停止沿旧run cancel，重启不会重请求模型。

PG每工作区最多1000会话；memory全进程最多64会话、2048节点，另受既有run缓存容量约束。memory容量满时拒绝新受理，保留现有会话引用的历史run，不静默淘汰。对话/消息标识使用服务端固定conv_/msg_格式；非null空标识、NUL文本或非法游标在进入PG前拒绝。上游增量含NUL/无效UTF-8时保留已有输出并失败，不污染检查点。

## 创建和运行

所有写操作必须带Idempotency-Key（沿现有8—128字符规则）。同键同原请求先于当前目录/CAS/忙/限额检查返回原资源，响应保留201并带Idempotency-Replayed:true；异参409 IDEMPOTENCY_CONFLICT。无论HTTP/commit受理不明，前端必须保留原body/key，不能换键自动重发。

`POST /api/v1/conversations` body为`{project_id,title?}`，201 Conversation。title省略/空白使用“新对话”，非空最多80字符。

`POST /api/v1/conversations/{id}/runs` body为：

```json
{
  "expected_version": 1,
  "model_id": "studio-chat",
  "model_version": "当前公开模型版本",
  "operation": "reply",
  "parent_message_id": null,
  "input": "第一条消息",
  "edited_message_id": null
}
```

operation必须reply或continue；model_version可省略以兼容明确策略，但新客户端发送。parent_message_id省略/null表示根；edited_message_id仅reply可用。expected_version必需并为正安全整数。

- reply：input为非空、合法UTF-8且符合逐模型max_prompt_bytes的原文。parent为同会话已终止assistant或空根。edited_message_id指定原user时，parent必须等于该user原父节点，新user为其兄弟，原内容/分支不改。
- continue：parent必须为同会话有非空输出的终止assistant，input省略或空，edited_message_id不得提供。服务端新建user“请从上一条回复结束处继续，不要重复已有内容。”再创建新assistant/run。它是一次新的模型请求，可消耗供应商额度，不能称为恢复旧TCP或原run。

201返回Run及上述关联字段。受理事务包含会话CAS、两个节点、Run、冻结上下文、幂等、审计与Outbox；全失败全回滚。409 CONVERSATION_VERSION_CONFLICT表示读取最新会话后再决定分支；409 CONVERSATION_BUSY表示已有活跃run；422 INVALID_CHAT_PARENT/INVALID_CHAT_EDIT/CHAT_CONTINUE_UNAVAILABLE表示父节点/编辑/继续不成立。总节点/路径超限429 CONVERSATION_LIMIT。

## 读取与分页

所有路径继承现有会话认证及X-Project-ID/project_id项目过滤；无权资源404，不通过游标放大权限。

| API | 响应/范围 |
| --- | --- |
| GET /api/v1/conversations?limit=20&cursor= | `{items:Conversation[],next_cursor?}`；limit1—50；按created_at,id倒序稳定keyset |
| GET /api/v1/conversations/{id} | Conversation |
| GET /api/v1/conversations/{id}/messages?leaf_id=&limit=20&before= | `{items:MessageNode[],leaf_message_id:null|string,next_cursor?,has_more,conversation_version}`；limit1—40；省略leaf用head，空会话items=[] |
| GET /api/v1/conversations/{id}/branches?limit=20&cursor= | `{items:[{leaf_message_id,parent_message_id:null|string,run_id:null|string,status,preview,created_at}],next_cursor?}`；limit1—50；只返回叶子元数据，preview最多160字符 |

messages从选定叶子向旧祖先分页，每页items按祖先→后代显示；正文响应总预算1MiB，可能少于limit。cursor包含账户/workspace/project/conversation/leaf及祖先位置，最长1024字节；不能把另一分支游标拿来读当前分支。路径是选中分支，不把所有兄弟内容拼接成对话。head仅随新turn更新；客户端选择旧leaf只是显式读取，不修改其他标签页的树。

## 上下文与模型预算

Provider接口为`StreamMessages(ctx,upstreamModel,ChatRequest,emit)`。ChatRequest私有字段为Messages（ChatMessage{Role,Content}数组）、MaxOutputTokens、OutputLimitField；仅user/assistant交替，user起/结尾，无system/tool/图片附件。

模型公开能力增加context_window_tokens、max_output_tokens；chat默认32768/4096，范围2048—1048576及128—32768，且输出预算+最低输入/安全余量小于窗口。实际供应商窗口仍由运营者核对配置，默认不是自动探测或真实模型保证。私有chat_output_limit_field为max_tokens或max_completion_tokens，默认前者，参与模型版本；不按模型名称猜测或失败后自动切字段重提。

上下文在受理时冻结祖先内容、上游模型、输出字段和预算，最大129消息及256KiB UTF-8内容。估算器utf8_bytes_v1使用UTF-8字节数+每条16+256安全余量，不是精确tokenizer或账单。逐次删除最旧的完整user/assistant轮次，保留最新user；continue还必须保留选中assistant所属上一轮，无法容纳时422 CONTEXT_TOO_LARGE，不能剪断原文或悄悄丢掉继续来源。

已停止/失败但没有任何输出的旧assistant，其user/assistant整轮略过并计入omitted_messages，仍可纳入更早的完整轮次；continue对空答返回422 CHAT_CONTINUE_UNAVAILABLE。不支持StreamMessages的适配器拒绝新多轮受理，不能悄悄降级为最后一条prompt。

Run.context_summary为`{input_tokens_estimate,output_tokens_reserved,context_window_tokens,included_messages,omitted_messages,estimator:"utf8_bytes_v1"}`。客户端展示估算和裁剪事实，不能称实际计费。已受理run使用私有冻结上下文，目录变更及刷新不会重建原请求。原键重放返回原受理快照，先于新预算和目录检查。

## 事件与恢复

conversation.created/conversation.turn_created审计使用resource_type=conversation（010扩展闭集），通过原audit.created发脱敏通知；不得把消息正文写入Outbox。run.updated仍按run ID补读，新增run字段标识对话；每秒checkpoint只更新相应assistant，不全量重拉全部会话/分支。

新会话/轮次刷新会话摘要和必要分支；保留未提交输入、选中leaf及受理不明body/key。停止/失败/中断保留部分输出，显式continue新建run；不会自动继续原请求。没有永久删除、消息内容就地编辑、总结压缩、工具调用、图文附件或多活实例。

## 验收

验证多轮数组确实送入fixture、祖先归属/兄弟分支隔离、整轮裁剪和继续最低上下文、CAS/单活跃/同键重放、状态/节点/上下文/审计/Outbox回滚、commit不明、重启中断和原模型快照、分页正文上界与游标scope。旧单轮基线仍需通过。PG测试串行，真实供应商及收费请求不由自动验收发起。
