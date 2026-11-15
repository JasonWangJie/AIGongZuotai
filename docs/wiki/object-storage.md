# 私有对象存储与离线迁移

V08 / v0.7 增加 AWS SDK for Go v2 驱动的 S3 兼容私有资产存储。当前仍为单写者应用：数据库保存资产库归属、元数据和回收站状态；`ASSET_DIR` 小型 manifest 保存读取位置；S3 保存生成结果与图片缩略图。对象存储不是绕过平台权限的公开下载站点。

## 已实现与范围

- 默认 `local` 保留原本本地模式。`s3` 模式复用同一套结果 URL 白名单、连接时 DNS/IP 检查、不携带网关密钥的下载、字节/图片解码边界。先流式写本地不可变文件，再上传已知长度的文件句柄；不会把视频整体读入内存或数据库。
- 生成图片最长边 512px 的 PNG 缩略图。动画只取首帧；视频没有伪造缩略图或转码。图片完整解码仍需要有界内存：原来的 32M 像素上限继续适用，不能把流式传输理解为解码零内存。旧图片无缩略图时显示占位；迁移 apply 可补生成。
- 上传使用服务端凭据与 `If-None-Match: *`、预计算 SHA-256。成功后再次下载整对象，核对长度和 SHA-256，再原子切换 manifest；ETag 仅作为对象代次条件，不当作内容哈希。上传响应丢失可用原 key 核对，不覆盖不同内容。
- 原件和缩略图都由平台鉴权代理，支持 GET、HEAD、HTTP 条件请求和 Range。S3 代理单请求最多 8 段 Range、Range 头最多 256 字节；不生成 presigned URL。回收站中的条目先被核心权限层阻断，存储层不是授权层。
- manifest 一旦标记 S3，对象失踪、过期、权限拒绝或内容身份不匹配，明确不可用。不会回退残留本地备份，不会重新生成，也不会调用新的模型任务。
- 原件导入和迁移都保留本地副本。参考图原件、参考缩略图与编辑 multipart 正文仍在 `REFERENCE_DIR`；本批没有把它们迁移到对象存储。

## 服务端配置

| 环境变量 | 规则 |
| --- | --- |
| `ASSET_STORAGE_BACKEND` | `local`（默认）或 `s3` |
| `ASSET_DIR` | 私有 manifest 与保留的本地副本；默认 `.runtime/assets`，不得置于静态目录 |
| `S3_ENDPOINT` | S3 服务根 URL；生产只接受 HTTPS，无用户名、路径、查询或 fragment；仅 loopback 允许 HTTP 验收 |
| `S3_REGION` / `S3_BUCKET` | 非空区域与已存在的私有 bucket；应用不自动创建 bucket |
| `S3_PREFIX` | 可选隔离前缀，仅字母、数字、下划线、连字符及分隔 `/`，最多 128 字符 |
| `S3_ACCESS_KEY_ID` / `S3_SECRET_ACCESS_KEY` | 独立服务端存储凭据；不复用 AI Key，不读取隐式 AWS 账户/实例凭据链 |
| `S3_SESSION_TOKEN` | 可选临时凭据 token；本版不自动续期，需部署侧轮换 |
| `S3_FORCE_PATH_STYLE` | `true` 适合本地 S3 兼容服务；`false` 使用 SDK 标准 bucket 寻址 |

bucket 必须禁止匿名读取和公开策略。应用不开放对象 URL、bucket 名、对象 key、服务地址或凭据；存储信息仅在核心私有 `storage_info` 中保存。代理无法补救运维侧把 bucket 配成公开的错误，部署验收必须检查匿名访问。官方 SDK 使用共享 Transport、固定连接上限 8、最多 2 次 SDK 尝试，禁止 HTTP 重定向和环境代理，避免把签名发给重定向目标。S3 地址是管理员配置的基础设施，不使用面向不可信结果 URL 的“必须公网 IP”规则。

应用和维护命令通过[私有目录租约](../../internal/studio/storage_lease.go)防止不同进程同时写同一目录。OS 锁文件保留，进程退出释放锁；这不是分布式租约。存储层不再次获取同一应用已经持有的目录锁。

## 元数据、容量与错误

`AssetInfo` 保留 ID、内容类型、字节数、SHA-256、创建时间，新增可选的 `storage_backend`、配置身份哈希、对象 key/version/ETag 与 thumbnail 子对象。旧 manifest 不含这些字段时按 local 读取。配置身份绑定 endpoint、region、bucket、prefix；更改位置后，旧收据拒绝误读新位置，凭据轮换不改变配置身份。

S3 对象 key 由受信的 `asset_<64位hex>` ID 生成，客户端不能指定路径。`Artifact.StorageInfo` 不进入浏览器 JSON。DB 中保存的私有快照用于审计与恢复依据；当前下载仍依赖本地 manifest，没有实现仅凭数据库自动重建 manifest 的工具。迁移只切换 manifest，既有 DB `storage_info` 快照可能仍记录原 local 状态，实际读取位置以 manifest 为准。

每个 S3 存储实例最多 8 个重传/下载槽位；固定 64 个锁分片避免同 ID 并发写，同时不持全局网络锁。上传/校验和代理读取有 2 分钟 context 上限，Head 有 20 秒上限；更短的 worker/request context 优先。迁移按资产逐项运行，不把所有正文或对象列表加载到内存。大文件原件上限沿用 image 32MiB / video 256MiB，thumbnail 2MiB。实际客户端、网络、解码和对象服务容量仍需独立压测。

临时连接/服务错误返回 `ASSET_STORAGE_TEMPORARY`；已丢失、拒绝或改变的对象返回不可用/待核实。用户下载失败只给安全错误与请求编号，不输出 SDK 原始错误、bucket 或签名。部分传输发生网络错误时连接中断，不能把已发送的字节改写成 JSON。

## 本地到 S3 迁移

先备份数据库、`ASSET_DIR`、`REFERENCE_DIR`，停止持有这些私有目录的应用。命令不自动加载 `.env`，由当前进程注入服务端变量，避免把密钥放入命令参数或历史记录。

```sh
go run ./cmd/storage
go run ./cmd/storage --apply
go run ./cmd/storage --apply --limit 100 --after asset_上一页最后的64位ID
```

第一条只生成计划，不修改原资产文件；PG绑定目录需DATABASE_URL、控制写锁、目录锁、稳定UUID及精确020格式。可达性检查会读取并hash已登记交付的完整本地原件/缩略图，不能再称所有dry-run均不读取正文；计划阶段不上传S3。默认每页100项、最多1000，按ID游标重入。apply对普通旧资产验证同一文件句柄的长度与SHA256，补缩略图、create-only上传及完整读回后提交收据，已有对象不再次PUT。

018交付计划及收据绑定不可变存储定位，所有已登记delivery项（含NULL收据、取消及终态）目前禁止迁移，完整Delivery标记也不能绕过。更换其定位需后续显式追加证据方案；不能自行改manifest或PG收据。旧无DB的MigrateAssets发现owner即拒绝，只用于无owner的本地fixture；生产入口为MigrateOwnedAssets，具体门禁见[074](../changes/2026-11-14-074-delivery-storage-maintenance.md)。

**迁移不删除任何本地原件、缩略图或远端对象。** 回收站不是物理清空。独立离线 GC 仅处理全量持久引用证明不可达且超过宽限期的本地孤立文件，见维护台账；本批没有 S3 孤立对象扫描、版本历史清理或生命周期策略自动化。不能手工清掉仍被任务或 manifest 引用的数据。

备份必须同时涵盖DB、两个私有目录和S3对象/版本、已启用邮件队列密钥及018独立交付密钥；S3不是自动跨区域容灾。运行时完整远端校验成功不代表本地副本可删，维护仍要求本地原件/缩略图与冻结收据一致。回滚需成套恢复，禁止移除存储身份、Delivery标记、owner或迁移登记；当前没有S3→local反向迁移工具。

## 验证依据

2026-11-13 使用官方 SeaweedFS 4.53 Windows 发行包，在本机独立临时目录与随机测试凭据运行，所有监听均绑定 `127.0.0.1`。下载包 SHA-256 与 GitHub 官方 release `digest` 一致：`d458d60e6a4120a64244b0ad7bc02cbc0e2fca71a7f775a88edc01da3b435cd7`。不是 HTTP fixture 冒充对象存储，也未调用真实收费模型。

`TestS3RealPrivateImportMigrationAndRanges` 覆盖实际模型协议 fixture → 安全导入 → S3 原件与 thumbnail → 私有代理，匿名对象 403、完整下载、单/多 Range、HEAD、512px PNG、迁移重入、本地副本保留、配置变更拒绝及对象删除后 404。`TestS3RealLibraryLifecycle` 经真实 App HTTP 任务受理和资产库注册，验证不同项目拒绝、回收后新旧下载 URL 都 404 但对象仍存在、恢复后可读、原生成幂等不重提、存储信息不泄漏；无网关配置的新应用通过测试注入的内存源记录仍可读取 S3，此项没有冒充 PostgreSQL 重启恢复。其余测试覆盖 503 与丢失 PUT 响应、原收据提交顺序、本地字节篡改、游标分页、配置/重定向边界与原 SSRF 规则。新增 7 个存储测试；前6项启用真实服务时共0.499秒，新增真实HTTP生命周期单项0.47秒通过。默认环境两项真实服务测试明确 skip，不能将默认 `go test` 描述为 S3 验收。

可核对的[本机对象存储验收记录](assets/object-storage-v0.7-2026-11-13.json)不含凭据。迁移CLI也实际执行计划、apply、再次apply核验，最后确认原件与缩略图仍保留。

官方依据：[AWS SDK endpoint](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-endpoints.html)、[校验和](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/s3-checksums.html)、[文件句柄与传输内存](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/sdk-utilities-s3.html)、[S3 GetObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html)、[S3 PutObject 条件写入](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html)、[SeaweedFS S3 服务](https://github.com/seaweedfs/seaweedfs/wiki/Amazon-S3-API)。真实 AWS、其他兼容厂商、生产 TLS/临时凭据轮换、跨区域容灾和大容量稳定性尚未验收。

实现与验证记录：[027 对象存储](../changes/2026-11-13-027-object-storage.md)。

## V14-B2媒体审核与016候选

[媒体审核](media-moderation.md)新增服务端私有读取：local使用同一文件句柄重新校验完整长度、SHA256与编码；S3必须真实GET原收据绑定的版本/ETag，将完整字节流式写入私有暂存并校验。Head或对象元数据不能代替实际审核，S3缺失/变更时也不回落残留本地副本。暂存reader的Close/取消清理自身文件，未生成预签名URL或公开对象入口。

016中的私有候选尚未注册为公开资产，也可能已经写入私有S3。上传、迁移、缩略图生成和财务结案都不表示审核通过；只有主链路确认ALLOWED且SUCCEEDED/PARTIAL的交付事务后，新旧下载和缩略图入口才允许读取。未知、BLOCKED/REVIEW、取消及终态候选仍属于[离线维护](storage-maintenance.md)的可达保护集合，不能按“没有library条目”判断可删。

V14-B2交付时工具要求精确001—016，后经017治理、018交付、019首次审核分派和020公平调度升级；当前精确版本及PG-aware资产迁移规则以本页020说明与[维护Wiki](storage-maintenance.md)为准。升级/回退须成套保留PG、私有目录、owner、S3版本、本地manifest与必要密钥；迁移不得重置决定，关闭媒体审核不得放行旧候选。020新增冷却、游标和调度投影也必须随PG恢复，不得通过重启清除供应商退避。

本批S3审核读取使用真实本机HTTP协议fixture验证GET字节、版本条件、篡改/缺失及取消清理，结果见[055](../changes/2026-11-14-055-media-moderation-engine.md)与[058](../changes/2026-11-14-058-media-review-integration.md)。上节V08的SeaweedFS实测保持原有范围；本批fixture不冒充生产云S3或真实审核商验收。
