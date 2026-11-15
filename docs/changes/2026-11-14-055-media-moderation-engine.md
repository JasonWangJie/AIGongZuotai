# 055 · V14-B2真实媒体字节审核与私有证据

## 目的

为参考图及生成图片/视频提供基于真实完整字节的审核接口，保留冻结政策、私有候选和不可变决定，避免把提示词、文件名或存储元数据误认为输出已经审核。

## 范围

V14-B2本地子批次，关联H01/H03/H06/H07/H09。独立media-v1协议与精确文件fixture、016证据表、local/S3已验证私有读取；真实审核商尚未选型，不调用外部真实服务。应用接线、参考素材/全部下载门禁、任务租约/账务、前端和联合验收由其他负责人登记。

## 实际文件

- `internal/studio/media_moderation.go`：独立配置/冻结政策/实际字节检查、严格fixture身份、候选私有编码、task/reference SQL事务helper。
- `internal/studio/media_moderation_http.go`：完整multipart媒体上传、固定身份绑定、有界并发/期限/响应、无redirect/proxy/自动重试。
- `internal/studio/media_moderation_test.go`：真实PNG/MP4双阶段判定、冻结策略、严格政策、私有codec及PG证据事务用例。
- `internal/studio/media_moderation_http_test.go`：完整字节、绑定负例、恶意响应、提前响应、限额/并发/超时/不重试测试。
- `internal/studio/media_moderation_store.go`、`media_moderation_store_test.go`：同FD校验、真实私有S3 GET/临时文件、取消清理及存储负例；委派只修改这两个新文件。
- `internal/studio/migrations/016_media_moderation.sql`：冻结task/reference政策、单次不可变候选和决定历史。
- `config/media-moderation.example.json`：按根集成人生成的原创PNG/MP4 manifest登记12条input/output精确规则。
- `docs/wiki/media-moderation.md`、本台账：配置、协议、存储证据、恢复和未完成边界。
- `docs/wiki/backend.md`、`storage-maintenance.md`、`object-storage.md`、`billing-operations.md`、`operations-access.md`、`operations-cases.md`：交付前同步016当前格式、候选全状态GC保护与联备、原handle核对及财务不放行内容，保留015历史事实。

## 接口与数据影响

新增`MediaModerationConfig`、`MediaModerationPolicy`、`MediaModerationContent`及私有`MediaModerationRecord`。Mode为disabled/fixtures/http；fixtures只准demo、未知完整hash保持REVIEW。`Check`验证实际长度/hash/类型后处理完整字节；HTTP响应绑定request/policy/phase/kind/type/size/hash，只有固定allow/allowed等闭集组合有效。

政策最大64KiB/64条fixture，图片32MiB、视频256MiB；HTTP完整multipart含封装最大256MiB、总期限60秒、每引擎2并发、响应8KiB。HTTP实际读取字节再次hash和计数，不只相信调用方元数据。服务端endpoint/key只参与私有身份散列，不落库/日志/浏览器。

016候选与公开Task.Artifacts分开，候选显式保存`Artifact.StorageInfo`，避免公开json:-丢失恢复身份。task冻结实际文本及媒体政策；PENDING/version1仅一次最终/version2，精确重放只读，nil-private取消/运营调用加载原候选和政策。reference仅批准后注册ALLOWED/input/version2。所有SQLhelper由业务事务包围，调用方先任务lease/version检查，再决定/目录/账务/Outbox，确认提交后才更新缓存。

`OpenMediaModerationAsset`返回必须Close的受控reader；local同FD重新hash，S3真实绑定版本GET后私有暂存验证，缺失不回落本地。全进程4活跃reader、每文件256MiB，2分钟覆盖打开至Close，取消清理自身临时文件；没有创建公开对象URL或任何额外用户下载权限。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| 读取helper子代理定向普通测试 | 通过 | `go test ./internal/studio -run '^TestMediaModeration(Local\|S3\|Returned)' -count=1`，0.350s；本地真实文件与本机HTTP S3协议fixture，非真实云服务 |
| `go test ./internal/studio -run '^TestMediaModeration' -count=1 -v` | 通过 | 初次16函数0.623s；补齐multipart总长和公开交付一致性后普通专项18函数0.646s；六个真实PNG/MP4上传、22项响应负例、完整字节/模式/冻结策略、2并发/早响应/无重试redirect与读取取消清理 |
| PostgreSQL helper专项及根全量 | 通过 | 根执行`TestPostgresMediaModerationFrozenCandidatesAndReference`专项1.90s、后续专项1.54s；最终真实PG全量192.482s包含本helper。本子代理没有另占PG窗口，联合细节见058 |
| 文档治理及根`npm run check` | 通过 | 首次本地检查仅缺055与058台账索引，保留该失败事实；根补齐导航后报告最终check通过，122份文档、58份台账。本次文档同步未重跑代码或测试 |
| 真实审核服务、语义质量、生产S3与PG容量 | 未运行 | 用户尚未选型；本机fixture仅验证协议、字节与隔离机制 |

PG用例覆盖冻结政策/候选事务回滚、private state省略时保留原记录、精确重放、终态不可覆盖、不可变候选/text policy，以及reference批准回读。最终保存还强制ALLOWED须SUCCEEDED/PARTIAL且公开产物顺序/数量/ID/Kind/URL与私有候选一致；其他状态须没有公开产物，精确summary重放同样重核。真实图像/影片文件由集成人维护来源与完整解码证据，本模块只读取其manifest和实际文件。根后补PARTIAL专项单函数1.675s通过；完整联合验证、浏览器修正及最终批次状态以[058](2026-11-14-058-media-review-integration.md)为准，不由本记录提前宣告B2全部完成。

## 风险与限制

精确文件白名单只适用于demo，不是语义审核；未匹配文件保持REVIEW。MP4本地预检不是全片解码或音轨审核，真实服务仍须检查收到的完整媒体。S3用本机HTTP协议fixture验证了GET字节和条件，不声称生产云渠道通过。无人工重审/放行、申诉或封禁；候选不公开不等于供应商未产生费用。完整V14继续in_progress，真实渠道继续独立pending。

## 回滚方式

016升级前停写联备数据库、私有参考/产物目录、S3对象/收据、owner及必要密钥。不能删除私有候选、决定历史或迁移登记绕过格式栅栏；关闭审核不会批准旧候选。退版使用匹配升级前联备并先核对后续新增任务/权益，不直接用旧程序写新格式数据库。

## 文档同步

本模块同步本台账、媒体审核Wiki及上述六份已有相关Wiki；保留B1/015历史验收，明确当前B2/016边界。根集成人负责README、Wiki导航、台账索引、AGENTS、任务源/TODO及联合记录。应用/下载门禁/CLI格式和浏览器效果以对应负责人实际验证为准，不把本模块完成等同B2全链路或完整V14放行。
