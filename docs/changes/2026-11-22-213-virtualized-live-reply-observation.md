# 213 虚拟消息列表中的持续回复验收

## 目的

使 V17-E 与 V17-F 的浏览器试具能够验证长回复期间的原始消息、实际可见前缀和受理状态。

## 范围

涉及 H03、H07 的持续回复和短分支浏览器验证，以及仅含固定源码位置和数值的诊断。原先 035 的独立半小时数据保留。

## 实际文件

`apps/workbench/scripts/long-reply-browser.mjs`、`scripts/perf/long-reply.mjs`、`scripts/perf/long-reply.test.mjs`。短分支较早页的原游标回读调整见 `apps/workbench/scripts/conversation-history-browser.mjs` 和 [189](2026-11-22-189-persistent-conversation-history-browser.md)。根 README、Wiki、任务源与生成 TODO、进度及台账索引同步。

## 接口与数据影响

测试在原消息索引、正文驻留与实际挂载的 DOM 行三个层次分别核对。交互阶段须有唯一助手行，两条消息仍在索引与缓存中。完成后实际滚回原用户消息检查原文，再回到助手行核对整份答复。未提交草稿、持续前缀的逐段检查、真实键盘/滚动、独立服务端原库检查均保留。产品接口及持久化无变化。

## 验证结果

- 冻结 037 源的首次 30 分钟测试在 63.33 秒停止。随后用同一制品和独立本机内存诊断复现了旧试具的具体断言。用户行已滚出挂载窗口，实际 DOM 为 0 行，该断言期望 1 行。
- 本机诊断保持 1 秒一个增量，设置 180 秒以覆盖相同大小的正文，获取 181 个增量、2,534 字节；修正试具后成功检查实际滚回的原始提示与完整答复并退出。该诊断的临时源码时长与正式 30 分钟 PG 测量单列。
- `node --test scripts/perf/long-reply.test.mjs` 初次 6 项通过，1 项要求指定构建程序的进程取消测试单列运行。专项验证诊断仅返回已知代码行和数值，不输出异常正文或文件路径。

## 风险与限制

虚拟列表正常卸载视口外行。只有正文、身份和索引仍满足原状态，而且用户重新滚回时可读取正确原文，才计为通过。内存短程测试不能替代同一条真实 PostgreSQL 运行的半小时、租约续期与原库重开。

## 回滚方式

回退本条浏览器试具、定位输出与相应测试即可。保留原有验收产物；无需变更数据库或工作台设置。

## 文档同步

[前端与性能验收](../wiki/performance.md) · [035 历史长回复报告](../wiki/assets/performance-long-reply-035.json) · [批次任务源](../planning/tasks.json) · [推进记录](../planning/progress.md)。

## 集成源已有程序的取消专项

在含 205–212 的 037 冻结集成源上指定本次构建的 LONG_REPLY_CANCEL_TEST_SERVER，重新运行 node --test scripts/perf/long-reply.test.mjs，7 项全部通过、0 跳过。
