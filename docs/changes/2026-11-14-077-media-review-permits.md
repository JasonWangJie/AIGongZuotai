# 2026-11-14-077：媒体审核共享许可

## 目的

实现 V16-A3.2 的引擎许可部分，让首次输出审核在领取任务前取得原有共享许可，参考输入与输出整组共用两份预算。

## 范围

关联 V16-A3.2、H03、H07、H09。只改审核引擎许可和专属测试；持久分派、租约、019 与执行接线由 076 负责，联合验收由 079 负责。本模块普通验证通过，整批状态由根集成人按实际验收更新。

## 实际文件

- [internal/studio/media_moderation.go](../../internal/studio/media_moderation.go)：输入 Check 接入共享许可，完整字节/协议处理抽为持有许可的内部检查。
- [internal/studio/media_review_permit.go](../../internal/studio/media_review_permit.go)：原引擎许可、冻结兼容、顺序整组、生命周期与误用保护。
- [internal/studio/media_review_permit_test.go](../../internal/studio/media_review_permit_test.go)：10 个独立许可、真实 HTTP 与取消测试函数。
- [docs/wiki/media-review-permits.md](../wiki/media-review-permits.md)与本台账。

## 接口与数据影响

增加 tryAcquireReviewPermit、mediaModerationPermit.Check/Close 和 compatiblePolicy。许可绑定原引擎的原有两个槽，整组复用不再申请；无可用槽立即返回 nil,false，disabled 返回 nil,true，由核心明确隔离。合法旧冻结策略继续按原 adapter/provider/mode 判断兼容，不替换当前规则。

状态使用共享指针与锁，复制许可、并发/重复 Close 仅释放一次。同许可并发检查拒绝，错引擎或已关闭许可不触达字节；Close 立即禁用并取消活动检查，物理槽等读取/HTTP 真返回才释放。输入 Check 保持原有先验证、非阻塞占槽和安全拒绝。没有新增持久数据，也没有修改原 SQL、执行核心或公开审核状态。

## 验证结果

| 检查 | 实际结果 |
| --- | --- |
| 初次编译 | 并行接线尚缺 ReviewDispatch/ReviewInFlight/ReviewLimit/ReviewSteps，随后并行测试短暂缺 laneDone；接齐后通过，未修改其他代理文件。 |
| 首轮 9 个新增许可测试 | 全部通过，0.076s。 |
| 实际 HTTP body reader 取消测试首版 | 错误等待 Transport 在阻塞 reader 退出前返回，3 秒超时；Go 实际仍等待 reader。改为直接验证未退出前不释放、释放 reader 后完整退出，不放宽产品行为。 |
| 最终 10 个新增 + 9 个既有引擎/HTTP函数 | 下方命令通过，0.310s；旧 PNG/MP4、冻结策略及严格协议断言均保留。 |
| go vet ./internal/studio | 通过。 |
| race detector | 未运行；go env CGO_ENABLED 为 0，Get-Command gcc 无可执行结果。 |
| npm run check:docs | 通过：170 份文档、79 份台账，本地链接与必需字段有效。 |
| 本模块 git diff --check | 通过。 |

    go test ./internal/studio -run '^TestMediaReviewPermit|^TestMediaModerationExactBytesBothKindsAndPhases$|^TestMediaModerationFrozenPolicyAndConfiguration$|^TestMediaModerationHTTP' -count=1 -v
    go vet ./internal/studio

两个真实 input HTTP 请求占满时输出无法取得许可；释放其一后输出可开始，另一输入与输出合计峰值为 2。整组连续检查四个真实文件且两个许可都已占用，证明不二次申请、不在件间释放。并发复用拒绝零额外 HTTP，复制许可并发关闭 32 次只腾出一位，错引擎/损坏快照零字节读取。实际 HTTP 取消不重试；同步文件读取及 Transport body writer 尚未退出时都不归还槽。读取 panic 的清理仍可安全 Close。

本模块没有执行 PG 或真实审核外发，loopback 协议 fixture 不替代真实渠道。全批 PG 和联合验收由根负责人串行执行。

## 风险与限制

不新增审核商，不声称渠道公平、饥饿避免或生产容量。许可可用不代表业务已领取或审核已开始；持久 STARTED、迟到结果与重启隔离仍由执行核心保证。取消只保证本地占位与调用退出顺序，不保证远端服务撤销已经开始的处理。

## 回滚方式

本模块无独立数据库变更。完整 A3.2 交付需遵守 019 的备份和格式栅栏，不允许仅换回旧程序绕过已保存的派发记录。

## 文档同步

已新增[模块 Wiki](../wiki/media-review-permits.md)，说明许可与 lane、事务和真实渠道的边界。根负责人维护 README、Wiki/台账索引、任务源、TODO 和本轮进度；子模块不覆盖共享入口。

联合收口补记：根最终check、全PG368.160s（实际版本化S3及联备启用）、六smoke、构建和文档治理通过，详见079。本文中间失败/待集成交接事实保留，完整V16不标完成。
