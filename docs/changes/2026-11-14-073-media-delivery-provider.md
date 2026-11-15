# 2026-11-14-073：状态查询与私有交付收据

## 目的

实现 V16-A3.1 的适配器与存储部分，让 Query 不再被原件下载占用，并用一次性 delivery ID 固定一组结果，防止重新查询后的同下标文件混批。

## 范围

关联 V16-A3.1、H03 模型与执行、H07 可靠性与性能、H09 工程与交付。新增私有分阶段接口、稳定资产身份、完整收据和下载预算；应用 PG/memory 移交、加密、领取、审核、账务和 GC 由同批其他模块负责。完整 A3 的渠道公平、独立审核预算及 V16-B/C 另验。

## 实际文件

- [internal/studio/delivery_provider.go](../../internal/studio/delivery_provider.go)：私有 DTO/interface、稳定 ID、Import/Load、不可变完成标记与完整校验。
- [internal/studio/media_http.go](../../internal/studio/media_http.go)：QueryStatus 不下载，旧 Query 兼容包装，结果清单边界，DeliveryConcurrency。
- [internal/studio/assets_local.go](../../internal/studio/assets_local.go)：AssetInfo 私有 marker、固定导入锁分片、绝对来源 URL 和完成原件保护。
- [internal/studio/asset_storage.go](../../internal/studio/asset_storage.go)：marker/缩略图有效性、完成 manifest 全等幂等与拒绝改写。
- [internal/studio/media_delivery_provider_test.go](../../internal/studio/media_delivery_provider_test.go)：9 个测试函数，含 6 个本地损坏与 4 个 S3 损坏子例，以及交付层 PUT 确认丢失验证。
- [docs/wiki/media-delivery-provider.md](../wiki/media-delivery-provider.md)及本台账。

没有修改其他代理的 App/worker、PG 迁移、S3 迁移入口或现有测试断言；根负责人同步 README、导航、任务源和联合记录。

## 接口与数据影响

MediaDeliveryProvider 增加 QueryStatus、ImportDeliveryItem、LoadDeliveryReceipt。状态返回 MediaQueryResult{Result, Items}，Result 无公开产物；Items 的来源 URL 仅交给应用加密持久化，单 URL 8 KiB、最多 4 项。地址在查询时按原 base 绝对化并验证，下载时继续原 SSRF 与跳转防护。

mediaDeliveryAssetID 使用随机 delivery ID + slot 固定资产身份，AssetInfo.Delivery 私有标记绑定版本、交付、位置和完整 item 描述摘要。完成收据同时保留实际 hash/size/type、缩略图及 S3 定位，写入后仅允许完整 JSON 全等幂等。完整收据丢失/损坏不得重下载；PG 已有收据但磁盘完全消失的判断由 core 依据 PG 记录拒绝，不能仅凭适配器返回 found=false 就重新导入。

DeliveryConcurrency 默认 2、范围 1—16，配置下载槽与连接池，应用入口绑定交付 worker。旧 Query 保留同步导入和旧资产身份；生产原生 Async 的阶段选择由同批 App 接线，不能在失败时降级。新增文件元数据依赖同批 018 格式栅栏与联备；本模块没有自行创建或修改 SQL。

## 验证结果

| 检查 | 实际结果 |
| --- | --- |
| 并行接线期间定向编译 | 最初因 App delivery 字段、观测 Delivery 指标及 executeMemoryDeliveryRecord 尚未写齐而失败；未改其他代理文件，接齐后复验。 |
| 第一轮 7 个新增 provider 函数与旧媒体回归 | 通过，1.211s。 |
| 加入慢下载/实际槽位/取消隔离后 | 相关回归通过，1.808s，静态检查通过。 |
| 首次收口下方命令 | 通过，1.718s；当时测试列表实际含 31 函数：本模块 8、旧媒体/transport 20、存储回归 2、并行新增 memory 阶段集成 1。之后追加 PUT 确认丢失专项，保留本次历史结果。 |
| PUT 确认丢失专项（下方第二条命令） | 新增第 9 个 provider 测试通过，0.149s；无 PG，无产品代码改动。 |
| go vet ./internal/studio | 最终通过。 |
| npm run check:docs | 交接时仅索引待集成：073 和并行模块 074 尚未登记；无断链。根负责人补索引后复验。 |
| git diff --check（本模块文件） | 通过；仅 Git 的 Windows LF/CRLF 提示，无空白错误。 |

    go test ./internal/studio -run '^TestMediaDelivery|^TestMediaTransport|^TestAsyncMedia|^TestAsyncImageEdit|^TestMediaRetryAfter|^TestLocalAssetStorage|^TestS3FailureRecovery' -count=1
    go test ./internal/studio -run '^TestMediaDeliveryS3LostPutAcknowledgementPublishesOnlyAfterVerification$' -count=1 -v

具体证据：QueryStatus 不请求结果 URL；源域名/协议/长度不合法不下载；相同完整收据在关闭再打开后复用，来源变动拒绝，新 delivery 不借旧 slot 文件；本地同长度原件/thumbnail 篡改、缺失和坏 marker 都不 redownload；中间原件验证后仅续收尾；并发同 slot 只下载一次。S3 fixture 的 HEAD 保持原长度/hash/ETag，但实际 GET 字节变动仍被发现，丢远端不回退本地。长下载占据唯一 slot 时 QueryStatus 仍完成，第二下载取消不触达来源，第一取消后槽位可复用。

075 联合验收补充要求已覆盖到真正 ImportDeliveryItem：对象服务写入原件/thumbnail 后返回 500，分别拦住两者首次完整 GET，确认尚未写 S3 切换收据或 Delivery marker，LoadDeliveryReceipt 仍为未完成。释放完整读回后才返回收据；核对本地两文件、远端两对象与 hash/size/type/ETag 一致。再 Load 与 Import 只验证原收据，PUT 计数保持不变、原 URL 总共只下载一次。本测试没有用底层 promote 成功替代交付完成标记的验证。

补测后 go vet 与本模块 diff --check 再次通过；文档检查仅 072/073/074 三份台账待根集成索引，无断链，未在子任务中覆盖共享索引。

本模块没有运行 PG，也没有真实 AI/SMTP/审核外发；S3 为 SDK + 本地 HTTP 对象协议 fixture，不等于真实对象服务或生产云验收。旧断言未删除或放宽，整批数据库验证由根负责人串行执行。

## 风险与限制

PG 清单是来源不可变的权威；适配器不接受浏览器选择 URL/交付 ID，也不单独授权内容访问。磁盘完成收据只含来源描述的摘要，不能代替加密清单、PG 归属或内容审核许可。部分导入留下的未公开文件仍需按 planned ID 纳入可达保护。

完整 S3 校验会读回原件和 thumbnail，存在额外带宽成本；这里只保证有界单项处理，未做生产容量或供应商性能承诺。完成 marker 禁止一般迁移改写，必须与同批维护门禁配合。独立审核调度、跨渠道公平和持久 token 流尚未由本模块完成。

## 回滚方式

停止服务并按同批 018 升级方案恢复匹配数据库、私有目录和密钥备份。不能仅删 Delivery marker、018 登记或 owner 来运行旧写者；不能用旧 Query 重新取得一组来源覆盖已移交清单。保留已保存的原请求、handle、planned ID、收据与候选，故障只进入待核实，不重新 POST 生成。

## 文档同步

新增 [媒体交付 Wiki](../wiki/media-delivery-provider.md)，关联 [媒体连接池](../wiki/media-transport-lanes.md)、[V16 契约](../planning/v16-contract.md)和[私有存储维护](../wiki/storage-maintenance.md)。根负责人同步 README、旧媒体页当前 Query 描述、Wiki/台账索引、任务源与 TODO；本模块专项不单独勾选完整 V16 或 A3。

联合收口补记：根完成索引并通过163文档/75台账检查，完整check/PG/六smoke均通过，另用实际本机SeaweedFS验证图/视频和保留对象的联备恢复。其范围与本模块HTTP fixture区分，详见075。
