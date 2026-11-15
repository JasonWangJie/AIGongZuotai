# 媒体状态查询、私有交付与完整收据

V16-A3.1 把供应商状态查询与原件下载分开。原生异步适配器实现 MediaDeliveryProvider：QueryStatus 只取状态和私有结果清单；应用确认持久移交后，交付 worker 才调用 ImportDeliveryItem。旧 Query 保留给兼容调用和既有协议测试，仍可同步导入；新应用执行路径必须选用分阶段接口，不能因错误或未配置密钥而悄悄回退。

本页记录适配器、私有文件和对象存储边界；加密清单、PG 移交、领取及恢复以 [V16 契约](../planning/v16-contract.md)和同批核心台账为准。它不是完整 V16、跨实例控制写或真实供应商验收。

## 接口与身份

| 方法或类型 | 当前职责 |
| --- | --- |
| QueryStatus(ctx, handle) | 原凭据、原任务 ID 和独立 query Transport；返回 MediaQueryResult{Result, Items}，不下载或注册资产 |
| MediaDeliveryItem | 私有 slot/kind/source_url/content_type/byte_size；来源地址必须进入加密清单，不进入 Task、事件、审计、日志或磁盘资产收据 |
| ImportDeliveryItem(ctx, deliveryID, item) | 只导入已经确认移交的冻结 item，不查询供应商、不提交生成 |
| LoadDeliveryReceipt(ctx, deliveryID, item) | 检查完整收据及真实存储字节；确认后才能返回已有 Artifact |
| AssetInfo.Delivery | 私有完成标记，包含 version 1、delivery ID、slot、规范化 item JSON 的 SHA-256；不包含来源 URL |

状态查询仍核对 handle 的 provider identity、凭据标签、kind 和数量。完成状态必须满足供应商成功、未过期、1—4 个结果且不超过原数量；不足原数量保留 PARTIAL。查询将相对 URL 按原服务根转成绝对 URL，单地址最多 8 KiB，并立即核对来源政策。该步不解析 DNS、不下载文件；实际连接时继续执行精确域名、DNS/公网地址与每次跳转检查。

资产 ID 为 asset_ 加 SHA-256(delivery-v1 + 换行 + deliveryID + 换行 + slot十进制)。delivery ID 使用服务端随机 delivery_ ID，slot 为 0—3。同一交付同一位置稳定，不再以“供应商 handle + 下标”复用另一轮查询的文件。改变同一 item 的来源或元信息会使摘要不匹配；上游的 content_type/byte_size 只是提示，最终收据记录实际编码、字节数和内容哈希。

## 完成、恢复与不可变

导入按原下载限制校验图片或 MP4、生成必要缩略图，完成 S3 promotion（若启用），核对存储内容，最后原子写入完整 asset manifest 和 Delivery 完成标记。不同 slot 使用不同文件；同 slot 通过固定 64 个锁分片串行，避免无界锁表和重复发布。没有在全局业务锁或 PG 控制连接中执行这些 I/O。

本地完整校验在同一文件描述符上读取长度、SHA-256 和真实编码；原件与缩略图都校验。S3 收据核对存储身份、对象位置、版本/ETag、长度、类型，再完整 GET 校验原件和缩略图 SHA-256；HEAD 元数据相同不能掩盖字节被改。ETag 不是内容哈希。S3 对象缺失不会退回留存的本地副本。

存在完成标记后，不允许修改或移除标记、替换原件、改写定位或补下载来“修复”收据。相同完整 manifest 写入可幂等返回。缺失或变动进入安全错误/待核实，错误正文不带来源 URL、私有路径或存储凭据。

若崩溃发生在完成标记之前，已经存在中间 manifest 的原件必须先通过完整验证，才能继续生成缩略图或对象上传；不会重新下载覆盖它。只有 .bin 而无可核验 manifest 的孤立文件不能当作已完成资产，也不能直接覆盖。

**应用若已持久保存某个 item 的 PG 收据，而 LoadDeliveryReceipt 返回不存在，必须停止并待核实，不能转去 Import。** 适配器无法仅凭空目录知道历史文件曾存在；PG 记录是这一判断的依据。没有 PG 收据但已存在完成标记时，也先完整验证再复用，不下载、不重新查询。

完成后的资产定位不可直接迁移；同批存储迁移入口会拒绝所有已登记交付资产（包括 planned ID）。GC 按持久计划和收据保留所有可达文件，不因终态或回收站状态删除。维护规则见 [私有存储维护](storage-maintenance.md)和 [对象存储](object-storage.md)。

## 预算与兼容

AsyncMediaConfig.DeliveryConcurrency 的 Go 零值默认 2，范围 1—16；应用传入 MEDIA_DELIVERY_WORKERS 对应预算。原下载客户端的槽位和 HTTP 连接池据此设置，仍独立于 submit/query；下载总超时、SSRF、真实编码校验和取消分类保留。连接数不是 HTTP/2 stream 并发上限，完整执行并发仍由应用交付 worker 控制。

旧 JSON 与 multipart 提交路径、不可变正文、凭据身份、提交不明保护没有变化。兼容 Query 保留旧资产 ID，避免改写旧协议测试和历史行为；新分阶段导入只使用交付 ID。原 SVG 演示 provider 没有真实下载，不据此宣称走过完整交付链。真实 PNG/MP4、HTTP 供应商及 S3 模拟对象必须在测试说明中单独标明。

## 本模块证据

9 个新增测试函数覆盖：状态查询零下载、URL 绝对化/拒绝、重启复用、批次身份、同长篡改、原件/缩略图缺失、中间导入恢复、并发同 slot、S3 字节与身份检查、PUT 写入后确认丢失、预算与慢下载隔离。丢失 PUT 确认专项分别阻塞原件和缩略图的首次完整读回：核验未完成时没有 Delivery marker；两者完成后本地副本、S3 对象与完整收据一致，重入不再 PUT 或下载原 URL。已有协议、multipart、transport 和存储断言保留；命令和实际结果见 [073 台账](../changes/2026-11-14-073-media-delivery-provider.md)。

本模块 S3 用真实 AWS SDK 请求本地 HTTP 对象协议 fixture，未把它描述为 SeaweedFS 或生产云验收；没有发真实收费模型请求。整批 PG 事务、失联、恢复、审核和账务是否通过，以根负责人联合台账为准。
