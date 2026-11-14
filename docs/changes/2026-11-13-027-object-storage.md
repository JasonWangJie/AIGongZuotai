# 2026-11-13-027：私有 S3 对象存储、缩略图与可重入迁移

## 目的

V08 / H04、H07：将生成结果从仅本地文件扩展为可校验的私有 S3 兼容存储，保留平台鉴权与回收站控制；资产列表使用512px缩略图，避免加载32MiB原件。故障不能变成重新生成或绕过权限的公开链接。

## 范围

服务端结果导入、私有原件/缩略图代理、存储身份、离线本地到 S3 copy+verify、官方 SDK 与真实本地兼容服务验收。参考原件和编辑正文仍本地；核心资产库/授权/回收站与GC由同批其他台账负责。

## 实际文件

- `internal/studio/assets_local.go`：兼容存储元信息，保留原URL/IP/解码限制，local不读取S3收据。
- `internal/studio/asset_storage.go`：接口/配置、原子manifest、私有512px PNG、导入桥接、本地读取与受控删除原语。
- `internal/studio/asset_storage_s3.go`：官方SDK、create-only写入、完整SHA256读回、固定锁分片/槽位、私有Range代理及错误分类。
- `internal/studio/asset_storage_migrate.go`、`cmd/storage/main.go`：分页计划、目录锁、可重入apply、不自动删本地。
- `internal/studio/asset_storage_test.go`、`internal/studio/asset_storage_s3_test.go`、`internal/studio/asset_storage_library_test.go`：普通故障fixture、真实SeaweedFS及App HTTP资产库生命周期测试。
- `internal/studio/media_http.go`：存储配置与私有 `Artifact.StorageInfo`，旧provider下载入口兼容。
- `go.mod`、`go.sum`：AWS SDK / smithy与root目录锁使用的gofrs/flock依赖。
- `docs/wiki/object-storage.md`、`docs/wiki/assets/object-storage-v0.7-2026-11-13.json`：实际配置、范围、恢复/备份、协议来源与脱敏验收证据。

## 接口与数据影响

`AssetStorage` 提供 Stat、ServeAsset、ServeThumbnail、Delete、Close；应用在调用前检查资产库/项目/回收站权限。新配置 `ASSET_STORAGE_BACKEND` 和 `S3_*`，不读取隐式AWS凭据链。存储定位仅private字段，浏览器永远读取平台URL。已有manifest无新增字段仍按local；S3收据绑定配置身份，远端缺失不fallback。

`cmd/storage` 默认只计划，`--apply` 上传并完整校验后原子切换manifest；`--limit`1..1000与`--after`支持分页重入。保留所有本地副本，DB快照不被CLI改写。目录租约由root实现，CLI持有它防止与运行中的应用竞争。迁移不是S3对象GC或反向迁移。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./internal/studio -run 'TestS3\|TestAssetStorage\|TestLocalAsset' -count=1 -v`（注入本机TEST_S3配置） | 通过 | 6函数，0.499秒；真实SeaweedFS4.53验收包含实际SDK上传/Range/匿名403/缺失拒绝及provider查询导入；不调用付费AI |
| `go test ./internal/studio -run '^TestS3RealLibraryLifecycle$' -count=1 -v` | 通过 | 新增第7函数0.47秒；实际App HTTP/模型fixture/真实S3，项目隔离、缩略图/Range、trash阻断新旧URL而保留对象、restore、幂等、private字段无泄漏、独立无网关配置下载；memory源记录由测试注入，不声称PG持久恢复 |
| 同上存储专项最终复跑（启用TEST_S3） | 通过 | 7函数全过，studio1.444秒；随后Go vet全仓通过 |
| `go test ./... -count=1`（无TEST_S3/DB配置） | 通过 | studio6.285秒；真实S3及PG测试明确skip，此结果不替代单独真实服务验证 |
| 原异步Provider+本地存储专项 | 通过 | 0.382秒；保留URL白名单、SSRF、原body/handle行为 |
| `go vet ./...` | 通过 | 存储实现与核心接线落盘时执行；最终全仓由集成人复核 |
| `go mod tidy` | 通过 | SDK direct/indirect已整理，版本锁定；flock供同批root目录锁使用 |
| 官方服务二进制校验 | 通过 | SeaweedFS4.53官方发布SHA256与本地一致，独立随机凭据，所有服务监听为loopback |
| `go run ./cmd/storage`，两次 `go run ./cmd/storage --apply`（隔离临时目录） | 通过 | 依次返回copy_verify计划、copy_verify完成、verify_existing重入；原件和缩略图均保留，未接触用户资产 |
| 真实AWS/其他S3厂商/公网TLS/生产负载 | 未运行 | 不把本机兼容服务与小样本验证称为生产完成 |

开发中初次真实服务因测试卷数不足返回500，调整隔离服务可写卷后通过；不是忽略服务错误。故障测试曾发现先补缩略图会掩盖旧原件哈希错误，已调整为先校验原件，重跑通过。目录锁、HTTP权限及PostgreSQL联合回归以集成台账为准。

## 风险与限制

必须配置私有bucket；应用不创建公开URL，也不能修复运维误设的匿名bucket策略。完整校验增加读流量/磁盘读取，但内存不随整视频大小增长；图片解码仍有32M像素预算内的内存成本。代理最多8段Range，存在2分钟传输上限，尚无大文件长稳态容量结论。

本地manifest仍是当前读取位置的必要收据，DB仅保留私有快照；备份应包含DB、两个私有目录、S3对象/版本。S3丢失不从残留local自动恢复，避免绕过过期/回收语义；需管理员明确恢复。临时云凭据续期、S3孤立对象清理、历史版本清理和多区域一致性仍未实现。视频仅沿用ftyp容器头检验，不是完整播放/安全转码保证。

## 回滚方式

新S3导入/迁移前备份一致数据。仅本地配置可继续使用local。已经切换S3收据后，不得通过删除身份字段或简单改backend强行恢复旧程序；使用迁移前一致备份与保留的本地副本恢复，或继续运行支持S3的当前版本。迁移默认不删除源文件，失败可保留原环境重跑，不能重新提交模型任务。

## 文档同步

- README：root同步V08版本、存储变量、迁移命令、当前边界与027链接。
- Wiki：[对象存储](../wiki/object-storage.md)记录配置、数据身份、范围与验收；root补导航。
- 台账索引：root登记027；任务源/TODO由root依据联合验证更新。
