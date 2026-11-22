# 112 · 人工选定首帧后显式执行视频节点

## 目的

V19-B 在项目工作流中增加四节点模板。用户取得已交付的脚本、生成候选图并人工选中一张，再决定是否使用独立报价生成一段视频。原三节点模板继续在确认选图后完成。媒体渠道与冻结 multipart 的实现单列于 111，工作台与联合验收由集成人登记。

## 范围

V19-B 工作流图、选图授权、独立视频节点和项目产物关联。

## 实际文件

- `db/migrations/024_workflow_video.sql` 为视频子任务建立项目外键、唯一关联索引和状态约束。
- `internal/studio/workflow.go`、`workflow_store.go`、`workflow_steps_store.go` 保存并校验版本、端口、冻结模型、独立子键和选图。
- `internal/studio/workflow_video.go`、`workflow_execution.go`、`workflow_outcomes.go` 实现报价和执行的首帧复核、终态推进及授权详情。
- `internal/studio/workflow_video_test.go` 验证图、冻结输入与准备后到受理前的资产变化。媒体、权益和真实浏览器的整合测试分别登记。

## 接口与数据影响

`text-image-approval-video/2` 新增 `approval.selected_image → video.first_frame` 和 `text.text → video.prompt` 两条边。保存图须提供完整四个节点与四条边，视频数量固定为 1。启动时核对并冻结支持首帧输入的视频模型及版本、规格和渠道模式，为三个生成步骤各保留私有子键。023 的定义、旧运行及既有收据保留原值。

四节点流程确认选图后停在 `VIDEO_READY`。用户读取节点输入时得到原运行身份、选定的项目资产、源任务与产物身份、用户确认的修订和内容版本，提示词使用脚本步骤实际公开的文本。首帧只读查询在同一数据库快照验证视频请求与冻结模型、参数、选图及提示词一致。改名保留原字节身份，回收、隔离或内容变化会拒绝新受理。

显式执行通过普通付费子任务受理，在同一事务内复核项目、流程版本、意图、完整请求摘要和已批准的源文件登记，关联视频任务、报价占用及幂等收据。视频状态从 `VIDEO_ACTIVE` 变成 `COMPLETED` 需要子任务成功、可公开，而且已在项目内登记唯一有私有字节身份的 MP4。详情提供 `video_task_id`、`video_task`、`video_artifacts`。受理不明或待审核保留为待核实，原键补读沿用原任务。

## 验证结果

- `go test ./internal/studio -run '^TestWorkflow(VideoGraphRequiresSelectionAndPublicText|VideoInputBindsChosenOriginalAndScript|GraphValidatesPortsBoundsAndDAG)$' -count=1` 在最终报价校验接线后通过，用时 0.200 秒。覆盖原三节点图、四节点缺边或错误端口、时长和数量限制、已选图片改名、回收或更换内容。
- 集成人串行验证 `TestPostgresWorkflowVideoRechecksSelectedAssetAtAdmission` 通过，用时 1.06 秒。意图准备后替换输入摘要、回收或换字节均被拒绝，库内无新子任务或子收据，父运行仍保留原选择。独立 024 构建还通过两种不同输入原件的实际提交与联备恢复（21.42 秒）、有价视频与原键收据（8.97 秒）、真实 Chromium 确认成片（16.90 秒）及原 023 升级（1.72 秒），见 `.runtime/v19b-first-pg.log`。

## 风险与限制

首帧模型能力由配置声明，所选渠道仍需支持冻结的 multipart 契约。真实网关鉴权、供应商支持范围、结算金额、审核准确率及生产吞吐需按部署环境独立验收。每个四节点运行只支持一次选图及一个视频结果。

## 回滚方式

停机恢复升级前同一时点的 023 数据库、私有资产和参考目录、目录 owner、交付及适用密钥和对应对象版本，并使用匹配的程序。024 期间产生的视频、消费和新工作流需另行核对。

## 文档同步

集成人更新根 README、[Wiki](../wiki/README.md)、[台账索引](README.md)、[任务源](../planning/tasks.json)、TODO 与推进记录。
