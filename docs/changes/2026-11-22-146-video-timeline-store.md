# 2026-11-22-146 项目私有视频分镜及不可变修订

## 目的

让用户保留同一项目的多份剪辑稿，明确每个版本实际选用的原件、镜头顺序、入点和出点。保存响应不明时，可以凭本人原键查明已提交的唯一结果。回收或隔离视频后，旧版本仍能查看分镜文字和时间范围。

## 范围

V20-A 的 H02 项目修订、H04 异步媒体产物和 H10 分镜创作。此处交付 PostgreSQL 数据格式和项目存储接口；本批的 HTTP、工作台、播放器、实际 FFmpeg 导出及端到端恢复由各自的台账记录。

## 实际文件

- `db/migrations/030_video_timelines.sql`
- `internal/studio/video_timeline_store.go`
- `internal/studio/video_timeline_store_assets.go`
- `internal/studio/video_timeline_store_list.go`
- `internal/studio/video_timeline_store_test.go`
- `internal/studio/video_timeline_store_transactions_test.go`
- `internal/studio/video_timeline_restore_test.go`
- `docs/changes/2026-11-22-146-video-timeline-store.md`

## 接口与数据影响

- 每次保存、列举、读历史版本、原键收据、单镜原件或全稿来源均在事务内核验当前账户、会话和项目。创建提交 `expected_version=0`，更新提交稿件 ID 和所见版本。服务器将请求摘要与项目、用户、原键绑定。已提交原键优先于当前修订、容量和素材状态；同键改请求返回冲突。收据只读原版本。
- 030 增加 `studio_video_timelines`、`studio_video_timeline_versions`、`studio_video_timeline_clips`、`studio_video_timeline_keys`。当前版本逐次加一。修订、镜头、收据拒绝更新和删除，提交时再校验完整镜头数、连续顺序、累计帧、时间和字节。晚插一条镜头不能改成以前签收的版本。
- 每稿 1–8 镜，镜头 ID 为 1–48 位 ASCII 字母、数字、下划线或短横线，稿名和镜头名各 1–120 个字符。毫秒入出点满足 `0 ≤ in < out ≤ 300000`，总时长最多 30000 毫秒。按拼接累计边界取最接近的 24 fps 帧，每镜至少贡献一帧。实际源时长由播放／导出路径按真实文件另行核对。
- 仅接受本项目活跃、已经成功或部分成功并公开交付的生成 MP4。存在当前审核决策时须为 ALLOWED。保存校验用户所选资产元数据修订，逐镜冻结库资产 ID、修订、完整 SHA-256 及私有 `AssetInfo`。单镜原件最多 32 MiB，按每次出现累加，每稿最多 128 MiB。读原件再次检查资产、任务产物及当前审核状态，完整存储身份必须仍等于冻结值。单镜读取只核自己的原件；全稿导出必须全部通过。元数据修订可以继续递增。客户端看到的身份限于资产 ID、已选修订、内容摘要和当前是否可用。
- 每项目可建 64 份，全库 4096 份，每稿最多 128 个不可变版本，项目列表与版本历史每页最多 50 条。达到上限后可继续查询历史版本或原键收据。030 升级不回填旧剪辑稿，不产生新授权或权益记录。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./internal/studio -run '^TestVideoTimelineStoreBounds$' -count=1` | 通过，加入联备专项后复跑 0.255 秒 | 8 镜和 30 秒闭边界、重复 ID、URL、保留字段、毫秒范围、多字节标题、累计帧量化；所有数据库专项参与编译。 |
| `TestPostgresVideoTimelineFrozenOrderedSourcesAndScope` | 通过，12.21 秒 | 两个真实 fixture 生成的独立已批准视频、重排版本、另一账户项目、回收恢复、过期修订、冻结存储身份改变、当前隔离、单镜可见性和迟到插镜。 |
| `TestPostgresVideoTimelineConcurrentEditsAndCapacity`、`TestPostgresVideoTimelineSourceSizeAndDraftCapacity` | 通过，5.65 秒、6.00 秒 | 两条独立连接在同一版本竞争，达 128 版本／64 稿仍可据原键查读；在已登记素材的大小边界夹具上验证单镜、重复引用累计字节和列表分页。 |
| `TestPostgresVideoTimelineLostSaveAcknowledgement` | 通过，8.13 秒 | 代理选择停在收据写入前的真实后台 PID，丢弃该事务 COMMIT 应答；重开工作台与数据库连接后按原键只读核对唯一版本。 |
| `TestPostgresVideoTimelineUpgrade029PreservesVersionsAndQuote` | 通过，2.23 秒 | 从原 029 建库，核对旧演示修订、原键、报价、授权、素材与任务原数据，030 新表初始为空，旧写者拒绝继续。 |
| `TestPostgresVideoTimelineRestoreOldVersionAndOriginals` | 通过，19.39 秒，已使用 PostgreSQL 16 工具 | 停写后真实 `pg_dump` 到新库 `pg_restore`，复制原件／参考两个私有目录并保留交付 key；重新登录按原键和两条固定版本 URL 核对完整修订。三条镜头 URL 比对两个独立原件的完整字节，删除于新版的镜头只对旧版可读，原有两次生成次数保持不变。 |

## 风险与限制

存储层校验已登记的产物身份与数据库授权。实际字节、SHA-256 和真实视频可解码性还依赖同批私有读取及 FFmpeg 导出。存储位置、对象版本或私有回执改变会使对应旧镜头暂不可读，保留原件或创建绑定当前原件的新版本可以恢复相应工作。外部供应商的编辑、延长和创作质量仍需额外验收。

## 回滚方式

先停写并保留当前 030 数据库、私有目录、存储身份和已启用的密钥／对象版本。需要回到 029 时，恢复升级前的整套一致备份与匹配的 029 程序，核对停写时点后的分镜及其他项目写入，再决定何时对外开放。保留 030 原件与收据供对账。删除迁移登记会破坏旧写者栅栏。

## 文档同步

本批集成人统一维护 README、Wiki、任务源、生成的 TODO、进度记录及变更索引。模块文档需标明原件可见性、版本／容量上限、可实际运行的检查和成片验证范围。
