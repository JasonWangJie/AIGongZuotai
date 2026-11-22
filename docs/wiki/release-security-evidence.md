# 发布验收中的本地安全与升级证据

本文归集 V22 已有的访问控制、数据隐私和兼容性验收。032、033、034 表示工作台数据库格式。下文秒数来自对应冻结副本的实际测试输出。日志仅列文件名和测试名称，便于集成人在本机复核。

| 证据范围 | 本机日志 | 已观察到的结果 |
| --- | --- | --- |
| 032 累计回归 | `v21a1-final-full-pg.log` | `internal/studio` 1639.820 秒、`internal/videoexport` 2.652 秒，通过。完整运行及修正过的两个夹具已登记。[032 累计验收](../changes/2026-11-22-156-project-collaboration-integration.md) |
| 033 订单及个人资料专项 | `v13a-v22a-first-pg.log` | `internal/studio` 62.099 秒，通过。双账户订单、个人资料下载和新库恢复是本轮的指定范围。[订单验收](../changes/2026-11-22-160-order-intent-integration.md) · [资料下载验收](../changes/2026-11-22-164-personal-data-export-integration.md) |
| 034 客服工单及升级专项 | `v22b-first-pg.log` | `internal/studio` 48.800 秒，通过。含普通用户、独立客服及外人的 Chromium 流程，10.69 秒。[034 工单验收](../changes/2026-11-22-170-support-ticket-integration.md) |
| 034 离线资料核对专项 | `v13b-final-cli-pg.log` | 成品命令及工作区只读数据库用例通过，数据库专项 3.15 秒。报告按引用订单计数，区分吻合线索、缺证和矛盾。[对账边界](../changes/2026-11-22-173-offline-order-reconciliation.md) |

## 会话、授权与撤权

| 控制 | 实际通过的用例 | 已核对的接口边界 |
| --- | --- | --- |
| 登录会话及改密 | 032 `TestPostgresAccountSecuritySessionsChangeAndIsolation` 3.96 秒，`TestPostgresAccountLoginPasswordRaceSerialized` 3.56 秒 | 本人可查询和撤销自己的会话。未登录访问安全接口为 401，跨账户撤销为 404。改密与登录并发时，事务重核凭据版本，旧密码校验结果无法签发有效的新会话。[账户安全测试](../../internal/studio/account_security_test.go) |
| 工作区和项目 | 032 `TestPostgresCollaborationDraftSharedRevisionAndPersonalIsolation` 3.15 秒 | 接受邀请后，成员可按角色在指定项目读写共享草稿。两人同修订写入产生冲突；原键收据按操作者隔离。成员的个人项目和创作访问范围保持原状，无法借草稿授权读取宿主钱包。[项目隔离测试](../../internal/studio/collaboration_draft_test.go) |
| 降权、撤权及正在等待的写入 | 032 `TestPostgresCollaborationDraftDowngradeAndRevocationFence` 3.10 秒，`TestPostgresOperationsHTTPMetadataPagingAndScopeRevocation` 2.38 秒 | 协作成员降为只读后写入为 403，撤权后详情、历史、原收据受保护。数据库等待中的草稿写入会重新核验角色。工作区所有者访问未获授权的运营接口为 404，撤销原运营授权后原有入口也受保护。[协作撤权测试](../../internal/studio/collaboration_draft_test.go) · [运营 HTTP 测试](../../internal/studio/operations_http_test.go) |
| 客服及运营权限 | 034 `TestPostgresSupportTicketHTTPScopedReplyPasswordAndOwnerReceipts` 3.42 秒；032 `TestPostgresOperationsAccessScopeAndLiveRevocation` 2.25 秒，`TestPostgresOperationsPasswordProofFencesAndBounds` 3.12 秒 | 用户可读自己的工单。独立客服只能处理获授权工作区的工单，队列不带对话正文，回复需当前密码。错误密码为 403，已提交回复可只读原键查询。运营授权按角色和工作区核验，密码校验结果受会话、到期和撤权约束。[客服 HTTP 测试](../../internal/studio/support_ticket_http_test.go) · [运营授权测试](../../internal/studio/operations_access_test.go) |

## 私有文件、个人资料与权益

| 控制 | 实际通过的用例 | 已核对的接口边界 |
| --- | --- | --- |
| 私有原件与既有导出授权 | 032 `TestPostgresVideoTimelinePrivateRangeAndRevokedExport` 10.04 秒，`TestPostgresComicPrivateExportConfirmationAndRevokedImage` 3.97 秒 | 对固定版本的私有原片范围读取会核对实际原件。撤回素材后，受影响的旧版导出和原件读取受限，其他镜头仍可访问。漫剧固定版图像还须核对作者确认和素材的当前可用性。[私有视频访问测试](../../internal/studio/video_timeline_private_http_test.go) · [固定版私图测试](../../internal/studio/comic_http_test.go) |
| 个人资料下载 | 033 `TestPostgresAccountExportPersonalSectionsAndRevocation` 4.54 秒，`TestPostgresAccountExportAppealsPrivacyAndBytePagination` 4.37 秒，`TestPostgresPersonalDataBrowserDownloads` 10.11 秒 | 每页要求本人有效会话和当前密码，错误密码为 403，失效会话为 401，跨账户游标被拒绝。双账户浏览器取回本人资料、100 加 12 条安全事件和受限用户的本人申诉；数据库逐条核对了页界、顺序和申诉字段。[下载边界测试](../../internal/studio/account_export_test.go) · [浏览器下载测试](../../internal/studio/personal_data_browser_test.go) |
| 订单意向与权益隔离 | 033 `TestPostgresOrderIntentHTTPFrozenCatalogAndDisabledHistory` 5.80 秒，`TestPostgresOrderIntentTwoAccountBrowser` 7.58 秒；032 `TestPostgresBillingHTTPWalletPaginationAndIsolation` 2.32 秒 | 本人的意向与原收据按原商品快照显示。关闭或改变目录后可核对旧记录，其他账户无法获取此意向。033 的登记和取消均未触发权益分录或媒体任务；钱包与账目分页依独立的工作区授权读取。[订单 HTTP 测试](../../internal/studio/order_intent_http_test.go) · [订单与数据库核对](../changes/2026-11-22-160-order-intent-integration.md) · [钱包 HTTP 测试](../../internal/studio/billing_http_test.go) |
| 商户材料只读核对 | 034 `TestPostgresOrderReconciliationReadOnlyWorkspace` 3.15 秒 | 在在线控制连接和订单行锁仍存在时比较两次，忽略材料未引用的订单；外部工作区的订单只列为范围内缺证。前后比较订单、原收据及七张权益表的内容摘要，没有业务写入。该程序处理未经认证的人工资料，不据此确认到账。[只读事务与成品测试](../../internal/studio/order_reconciliation_postgres_test.go) |

## 恢复与版本变化

| 真实路径 | 已通过的用例及边界 |
| --- | --- |
| 032 联备恢复 | `TestPostgresCollaborationPairedRestoreKeepsRolesAndDraftReceipts` 16.88 秒。恢复到新库后核对共享角色、草稿修订与原键回执。[恢复专项](../../internal/studio/collaboration_integration_test.go) |
| 032 → 033 | `TestPostgresOrderIntentUpgrade032PreservesMemberAndDraftEvidence` 1.80 秒。保留成员关系及原草稿证据，不生成历史订单。033 的 `TestPostgresOrderIntentRestoreWithCatalogDisabled` 7.53 秒，在新库和已关闭目录的情况下核对历史意向与收据。[升级专项](../../internal/studio/order_intent_store_transactions_test.go) · [目录关闭后的恢复](../../internal/studio/order_intent_integration_test.go) |
| 033 → 034 | `TestPostgresSupportTicketUpgrade033PreservesOrderAndOperatorRoles` 1.59 秒。原意向及原运营角色保留，升级不自动授予客服角色或生成工单。`TestPostgresSupportTicketRestoreHonorsRevocation` 10.57 秒，恢复后，原客服仍无权访问，申请人可核对对话、旧收据并关闭工单。[升级专项](../../internal/studio/support_ticket_store_transactions_test.go) · [撤权及恢复专项](../../internal/studio/support_ticket_integration_test.go) |
| 工作台升级时对原供应请求的兼容 | 032 `TestPostgresCatalogReplaySnapshotsRecoveryAndLegacyHash` 5.08 秒，`TestAsyncMediaCatalogRemovalKeepsSavedBytesAndHandles` 0.01 秒。目录映射、启停状态或旧模型名称发生变化后，已受理请求仍按冻结快照和原 handle 核对。032 `TestChatUsageProviderFailuresNeverFallbackOrExposeBody` 0.01 秒，验证本机网关错误不会触发隐式回退或暴露原响应正文。以上供应端响应来自环回夹具。[目录兼容测试](../../internal/studio/catalog_test.go) · [媒体适配测试](../../internal/studio/provider_models_test.go) · [文本用量错误处理](../../internal/studio/chat_usage_test.go) |

已归档的 [034 工作台 SPDX 清单](assets/workbench-034.spdx.json) 可独立阅览。[制品核对台账](../changes/2026-11-22-178-release-sbom.md) 记录了前端输出不一致时生成器拒绝出具清单的负例。它们证明所核对的工作台制品可与本地锁定依赖和构建输出比对。网关、商户、邮件、外置存储和媒体处理程序有各自的发布周期；真实供应商的升级前后版本、线上兼容结果及实际签收责任方仍待外部验收。上表不能据以确认生产部署、真实渠道付款或生产灾备能力。

## 035 累计验收补录

033 的零权益写入与 034 的只读对账沿用各自的原始验收范围。035 完整回归与十八表资金复核的结果见下文。

## 034 累计运行与剪辑选择问题

固定 034 累计运行 studio 1702.178 秒，视频导出包通过（2.465 秒）。611 个顶层用例通过，1 个失败，0 个跳过。唯一失败是视频剪辑 Chromium 用例（14.72 秒）：核对首次保存的原回执后，页面已显示固定版本，项目选择框在目录响应前读到空值。订单、资料、客服和只读对账均实际通过。该轮完整结果为失败，后续制品保留原浏览器断言复验。

## 剪辑稿回执与选择

首次保存或恢复原回执后，即使当前目录页暂未列出该稿件，选择框也能显示已经核对的项目与固定版本。验收保留原浏览器选中身份断言。

## 035 本机专项复验

集成人在固定 035 工作台、真实本机 PostgreSQL、Chromium 和 FFmpeg 上串行执行 17 项，全部通过（82.279 秒）。20 个独立数据库连接竞争同一经济交易的用例 3.55 秒，取消与收款两种顺序 2.64 秒，丢失提交应答并重启 2.73 秒，034 升级保留客服、意向和账本 2.23 秒。收款/退款、已花/冻结及其他购买的保护，权益容量不足、等待席位、身份冲突和精确维护栅栏均通过。

## 035 固定制品

[035 SPDX 清单](assets/workbench-035.spdx.json)对应本轮验证制品，服务和 33 个工作台文件已独立复核。外部供应商、生产镜像及签收另行取得。

## 固定 035 累计验收

固定 035 完整回归通过，629 个顶层用例，零失败、零跳过。studio 包 1670.146s，视频导出包 2.669s。实际启用 PostgreSQL、版本化本机对象存储、真实 Chromium、FFmpeg、十万调度积压、二十万旧任务迁移和编译出的只读订单核对命令。该制品的 553 项工作台测试、157 模块生产构建、Go vet 和六套冒烟均通过。 034 原先失败的剪辑稿选择断言、本人订单三类状态、实际导出与客服授权均通过。真实网关、SMTP 和正式商户仍需相应环境。

## 035 十八表资金复核

累计运行结束后，集成人在同一 035 服务和工作台上串行复验扩大后的两项断言。先写入真实隔离 PostgreSQL 的合成收款、同额退款及唯一购入分录，再用编译好的只读核对命令检查业务。核对前后 18 张订单、权益及资金表的所有行摘要一致（3.12 秒）。真实 Chromium 与新库恢复后重放原部分退款，18 表和本人状态也保持一致（12.91 秒）。两项合计 16.178 秒，未使用真实商户。

## 037 离线构建和证据身份

037 标准服务与工作台 dist 的核验记录已归档，列出已绑定的专项和先前构建单独取得的升级、联备结果。[制品证据](release-evidence-037.md)。
