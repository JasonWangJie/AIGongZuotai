# 首次媒体审核分派：独立验收

V16-A3.2的078独立专项已通过，整批尚待根联合放行。本页记录维护门禁和独立App/HTTP/PG测试；[075](../changes/2026-11-14-075-media-delivery-integration.md)仍是A3.1历史交付成绩。执行协议见[冻结契约](../planning/v16a32-contract.md)，故障处理与核心证据由076负责，共享许可引擎由077负责。

## 验收边界

测试使用真实App受理、鉴权HTTP、原生异步媒体适配器和独立PG连接。媒体与审核服务都是本机HTTP fixture，审核请求必须传完整真实PNG/MP4字节并绑定metadata哈希；不是实际模型、真实审核商或语义准确率证据。所有PG用例按数据库串行，随机schema不绕控制写锁；原本机S3服务保持停止，本专项不将历史S3成绩算为新验收。

| 用例 | 实际链路 |
| --- | --- |
| 慢审核隔离 | 原生POST、QueryStatus、持久导入后停在READY；阻塞review HTTP时，第二任务仍完成提交、查询和导入；受保护指标有固定review槽及上限，私有任务/提示/URL/请求身份不泄漏 |
| 输入共享许可 | 两个真实参考上传占满审核HTTP的两个许可；输出保持READY、不写attempt、不外发审核、无等待占槽，输入完成后首次审核执行一次 |
| 两连接与取消 | 独立review PG连接争领同一候选；只出现一次STARTED/HTTP，取消后迟到ALLOW不得公开，原幂等保持原任务 |
| 整组与权益 | 图片PARTIAL、图片一件ALLOW一件BLOCK、视频ALLOW/BLOCK；READY不下载、不结算，整组决定、目录、权益和访问门禁一致 |
| 018真实升级 | 用原001—018嵌入SQL新建旧库；原任务/输入/handle/submission/幂等/审核JSONB及文件字节不变，018旧写者被拒，旧候选零回填，新任务才得到READY |
| 离线维护 | 双锁保持；019精确门禁、旧018/缺018/未来020拒绝；候选及018收据全阶段保护，单独任务修订及READY→FINISHED均使旧GC计划失效 |

普通memory模式继续拒绝配置媒体审核。首版测试错误地从memory启动启用审核，生产校验正确拒绝；已移除该入口并把完整逻辑放在PG用例，没有放宽产品门禁。

## 当前证据

- `go test ./internal/studio -run '^$'`通过，0.031s。
- 根首次普通测试9.914s因上述memory夹具失败；更正后仍待根统一复验。
- 独占PG首轮`node .runtime/v16a32-check.mjs target '^TestPostgresMediaReviewDispatch|^TestPostgresDelivery(Maintenance|Migration|Offline)'`失败，23.693s。既有维护五函数与019格式三子例通过；新六函数在STARTED后的最终决定或取消失败，未记通过。
- 核心审核时间身份以`reflect.DeepEqual`比较PG重读与内存时区位置，误判同一时刻的attempt变化。076修正为`Time.Equal`，仍校验真实时刻不变，078原断言全部保留。
- 修后`node .runtime/v16a32-check.mjs target '^TestPostgresMediaReviewDispatch'`通过，15.003s、exit0；六函数及整组四子例全通过。074五函数和019三格式子例已于首轮通过，不无理由重复执行。
- 第二次窗口授权与核心1191的释放通知发生交错，消息不能确认实际是否重叠，两组没有锁争用失败。根079记录协调事实，之后所有PG均由根亲自串行执行，最终全量覆盖两组；078不再单独启动PG。

## 维护和恢复规则

019分派不新增文件正文，也不增加可删除内容。所有旧候选、所有018计划身份和完整收据继续可达；审核完成、取消、BLOCK/REVIEW或权益处置不能释放文件。维护计划另覆盖分派摘要/结果状态/阶段/版本/attempt/时刻及任务版本，数据库字段无法解释时拒绝维护。

升级与恢复仍须联备数据库、私有文件和owner、交付密钥、已启用队列密钥及S3对象/版本。已STARTED不可重审，旧018候选不可补造READY；恢复与维护不得删表记录、迁移登记或owner绕过。根079安排独立联备和最终全量，不能用本页部分通过替代整批验收。

源码：[独立集成](../../internal/studio/media_review_dispatch_integration_test.go)、[018升级](../../internal/studio/media_review_dispatch_upgrade_test.go)、[维护阶段](../../internal/studio/storage_maintenance_review_test.go)、[既有维护与格式](../../internal/studio/storage_maintenance_delivery_test.go)。台账：[078](../changes/2026-11-14-078-media-review-dispatch-validation.md)；运行说明：[存储维护](storage-maintenance.md)。

根于2026-11-15完成全部联合验证：check与368.160s全PG、真实版本化S3/019联备和六smoke通过。完整收据问题已修并以五种篡改验收零外发，细节见079；旧中间专项记录保留。
