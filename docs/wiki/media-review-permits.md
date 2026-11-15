# 媒体审核共享许可

本页说明 V16-A3.2 的引擎许可边界。它与[首次审核持久分派契约](../planning/v16a32-contract.md)配合，不引入新的审核服务或另一套并发池。实际字节、策略冻结和 HTTP 协议继续见[媒体审核](media-moderation.md)，验证记录见[077 台账](../changes/2026-11-14-077-media-review-permits.md)。

## 同一份预算

每个 mediaModerationEngine 仍只有两个原有槽位。新参考输入通过 Check 临时占用一个槽，输出审核通过非阻塞 tryAcquireReviewPermit 先占一个槽，再由执行模块领取 READY、提交 STARTED。未取得许可不启动审核，核心保持 READY，不能把容量不足写成最终 check_busy 隔离决定。

输出整组按原候选顺序调用同一个 permit.Check。每件都检查完整原件并绑定冻结策略，但不重新申请第二份槽位，也不在件与件之间归还。输入与输出合计最多两个本地在途检查；它不是两个输入槽加两个输出槽。

review lane 的工作线程/PG 连接与引擎许可有不同生命周期。引擎许可保护字节检查和审核调用；执行 lane 还要保护领取、决定保存和清理。业务层必须在取得预算后才领取，确认 STARTED 后才调用 Check，保存决定的重试不能重新调用审核。许可本身不写数据库，也不授予公开候选的权限。

## 私有 Go 接口

| 接口 | 语义 |
| --- | --- |
| engine.tryAcquireReviewPermit() | 立即返回；可用为 permit,true，容量忙为 nil,false；禁用或未初始化引擎为 nil,true，让核心隔离而非一直等待 |
| engine.compatiblePolicy(policy) | 验证冻结策略摘要、provider 模式、审核模式及 adapter 身份；不以当前规则版本替换旧快照 |
| permit.Check(ctx,policy,phase,content,reader) | 使用已持有的原引擎许可；保留完整长度/hash、编码、HTTP 上传和响应身份校验 |
| permit.Close() | 幂等；立即禁止新检查并取消活动检查，但实际槽位等正在读取/请求的调用真正返回才归还 |

同一个 permit 只能顺序执行。并发复用立即返回 REVIEW/check_busy，不触达字节或 HTTP；零值、关闭后重用及跨引擎传递拒绝为 REVIEW/policy_unavailable。许可的副本共享生命周期状态，多次或并发 Close 只归还一次，不能误释放另一个任务的槽位。

每件检查继续使用原有 60 秒期限，更短的调用 context 优先。单件取消不会隐式销毁整组的持有权，调用方仍须 Close；Close 本身会取消当前检查。若底层 reader 暂未响应取消，槽仍被占用，不能把尚未退出的工作当成空闲。许可不新增等待 goroutine，不负责强制终止远端服务已开始的处理。

## 冻结策略与隔离

策略兼容沿用既有含义：相同 adapter/provider/mode 下，合法的旧冻结规则仍可执行，当前规则版本改变不偷偷重算旧策略。摘要损坏、适配器身份变更或审核关闭都不能产生 ALLOWED。核心应把不可执行的 READY 明确隔离；不能用 nil 许可假定批准，也不能为旧 STARTED、既有隔离候选或未知审核结果重复外发。

参考输入 Check 保留原先的先验证策略/内容、再非阻塞占槽顺序。容量满时继续安全拒绝，许可释放不会自动重发参考上传或生成请求。整组公开仍依赖有效任务租约、最终决定和资产/账务/Outbox 同事务提交确认。

## 已验证与限制

077 的 10 个新增普通测试与 9 个既有引擎/HTTP测试通过，包含真实 loopback HTTP 上传、PNG/MP4、共享峰值 2、整组顺序检查、重复关闭、错引擎、预先取消、读取异常，以及取消后仍阻塞的实际 Transport body reader。静态检查通过；当前机器 CGO 关闭且没有 gcc，未执行 race detector。

本模块未跑 PG，不以许可单测替代 READY/STARTED/FINISHED 的事务与恢复验收；该证据由本批集成记录提供。公平、饥饿避免与渠道退避属于 A3.3；没有生产吞吐成绩，也没有真实审核渠道验收。数据库 019 和联备要求由整批升级承担，不能仅回退引擎绕过派发记录。
