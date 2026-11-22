# 037 工作台制品与升级证据

本页记录 V22-C/D 对一次固定构建的复核。服务为 037 集成源码内的 `.runtime/server-207.exe`，含受管聊天子进程的取消收据处理。前端为同一源码目录已经构建的 169 个模块。两者的完整 SHA-256 分别为 `6fb7b00b8899f84e9acca769bc9201a250c47c308de1524e714a0378ecb20caf`、`9d500caf860d05137c200bd695f80d1266c5c3a3e5e4864fc67d22f85a98c63c`。[制品清单](assets/workbench-037.spdx.json)

## 已复验的输入

在独立目录将 217 个本项目 Go 编译输入（含 SQL embed、go.mod 和 go.sum）与 191 个工作台源文件及配置按名称与内容摘要登记。使用 Windows amd64 的 Go 1.26.11、CGO 关闭及 `-trimpath` 重建服务，输出与上述原件逐字节相同。内嵌 Git 信息记录 `e10d13fa337a474551e56671bcfdc4c424e498c1` 和 dirty 标志，源文件摘要及重建一致性可用于识别这一具体构建。[完整输入与制品摘要](assets/workbench-037-verification.json)

生产前端离线重建得到 55 个相同文件，包括 20 个 JS、20 个 gzip、14 个 CSS 和一个 HTML。独立检查将每个 gzip 解压并与对应的 JS 原文逐字节比对。前端树摘要的输入为排序后的相对路径、NUL、文件内容 SHA-256 的小写十六进制、NUL。SPDX 2.3 清单通过本地已固定摘要的官方 Schema。45 个包唯一可达，两个 `DESCRIBES` 和 45 个 `DEPENDS_ON` 关系逐项对上原依赖；20 个 Go 模块对上编译记录和 go.sum。本机缓存中的 22 个 npm 生产包逐个验证锁文件归档完整性，再比对安装目录中的 434 个文件。原有许可声明、`NOASSERTION` 以及自有项目的分发许可状态保留。[复跑与故障检查](../changes/2026-11-22-215-release-evidence-037.md)

## 哪些运行使用了这两个制品

Go 集成试具归档的 `v17j-037-long-quick-a.json`（SHA-256 `5211c80e975a5df9b8b835484d51cf76b990f10ec00336aee84478bafb68bd50`）同时记录了上述服务和前端 SHA。该次为 12 秒档，25 个模型增量、一次模型 POST，真实 PostgreSQL 与浏览器读取，服务重开后读回原快照。对应 Go 用例 18.66 秒。证据对应一次短程重开。正式半小时、全部 PG 用例及整批放行仍以同一制品各自的结果为准。[持续回复试具](../../internal/studio/long_reply_browser_test.go)

## 已有 037 格式的升级、安全和联备历史

另一次 037 累计执行的日志 `v17-final-037-cumulative-pg.log`（SHA-256 `757a40618f9d3ad83506c0efbfaa22c3c09fcbe2753ebfc6bda0c69113707574`）逐项记录了下列结果。该目录留存的服务制品 SHA-256 为 `0b4fcb370bc64a8fa38729f447867ae12d6bc83d57ce99448309dd4485de32e6`。下表所涉测试源与本次冻结源码逐文件相同。该累计日志还记录了 `TestPostgresLongReplyBrowserAndReopen` 在 63.33 秒失败，整轮为 FAIL。

| 当时执行的用例 | 已通过部分 |
| --- | --- |
| `TestPostgresConversationAncestryUpgradeAndBoundedContext` | 035 旧数据升级到长会话格式，1.93 秒。[源码](../../internal/studio/conversation_ancestry_test.go) |
| `TestPostgresCanvasUpgrade036PreservesExistingData`、`TestPostgresCanvasOwnerHistoryFrozenSources` | 从 036 升级，校验原数据和画布所有者、来源范围，分别 1.93、3.66 秒。[源码](../../internal/studio/canvas_test.go) |
| `TestPostgresCanvasPairedRestorePreservesFrozenRevision`、`TestPostgresLongHistoryPairedRestoreWithOriginalLimits` | 原画布修订与私有来源联备恢复 15.86 秒，长链回到默认限制后的配套恢复 14.76 秒。[画布](../../internal/studio/canvas_restore_test.go) · [长会话](../../internal/studio/conversation_history_restore_test.go) |
| `TestPostgresCatalogReplaySnapshotsRecoveryAndLegacyHash`、`TestAsyncMediaCatalogRemovalKeepsSavedBytesAndHandles`、`TestChatUsageProviderFailuresNeverFallbackOrExposeBody` | 冻结请求、原 handle、供应错误与回退边界分别 2.42、0.01、0.00 秒，供应侧为本机夹具。[目录](../../internal/studio/catalog_test.go) · [媒体](../../internal/studio/provider_models_test.go) · [聊天](../../internal/studio/chat_usage_test.go) |

实际商户、网关、生产镜像、对象/WAL 备份环境、许可与隐私责任方核签、生产变更授权仍按发布闸门独立验收。[历史依赖与许可证](release-dependencies.md) · [已有安全证据](release-security-evidence.md)
