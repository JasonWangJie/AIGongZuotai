# 235　以已有私有 MP4 编辑或延长视频

## 目的

V20-C 从已交付、仍可读取的项目 MP4 发起独立的视频编辑或延长请求。公开供应商文档明确允许特定模型接收 Data URI。

## 范围

H02、H03、H04、H06、H08、H10。依赖已有资产修订、媒体审核、不可变请求、权益报价与交付。需要验证同项目来源核验、显式模型能力、原键回放、真实字节、两种出站 JSON 和停写恢复。

## 实际文件

`db/migrations/039_task_video_sources.sql`、`internal/studio/{source_video,source_video_mp4,source_video_test,app,billing_http,billing_pricing,catalog,http,media_moderation_execution,model,persistence,storage_maintenance,video_first_frame,video_timeline_store_assets}.go`。同步 `AGENTS.md`、OpenAPI、数据库迁移及网关 Wiki、039 决策、README、台账索引、任务源、两份生成 TODO 与推进记录。

## 接口与数据影响

用户选择资产及元数据修订，服务端读取原件并构造 Data URI。只有目录显式声明的模型和规格才可用于相应操作。报价和幂等摘要分别绑定源身份、操作、参数与方向。

## 验证结果

已匿名读取公众文档核实两种 JSON 接口以及源形式。文档制品 SHA-256 为 3f2a55146093aea1778c6d2985470ca0bcb1dfd6c225a5ebc9f8ab198b103c85。本机 Go 专项按 `^TestSourceVideo` 通过，包耗时 1.726 秒，包含供应商两个端点、完整 64 MiB 正文、源容器与自包含引用、模型声明与请求域。业务、浏览器和恢复分别记于关联台账。尚无真实模型请求。

## 风险与限制

模型是否开通源视频 Data URI、时长、方向与尺寸等需在用户配置的真实环境分别验证。服务端冻结文件占用私有存储配额。受理不明时保留原操作身份。

## 回滚方式

停用新增目录能力，保留已受理任务的正文、原键、权益与原查询身份。跨格式回退须用配套制品和升级前停写联备。

## 文档同步

[网关协议](../wiki/media-gateway.md) · [开发任务](../planning/tasks.json) · [推进记录](../planning/progress.md)

039 原视频任务还在同一次额度冻结中核验报价申请人，在最终事务提交前按数据库时钟复核原会话。涉及 `internal/studio/ledger.go`、`ledger_transactions.go` 与 `persistence.go`。真实锁等待跨越会话到期时，任务和收据均回滚，原报价保留。[238 实测](2026-11-23-238-source-video-integration-restore.md)

039 决策文档补充与仓库示例中现有规格匹配的原视频能力配置片段，以及服务端应用后从视频工作室核对的方法。
