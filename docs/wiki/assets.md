# V08 · 资产目录、元数据版本与回收站

资产库独立登记上传参考和任务产物，提供项目内分页搜索、名称/标签、不可变元数据历史及软删除/恢复。关联[冻结接口](../planning/v08-contract.md)、[026台账](../changes/2026-11-13-026-asset-library.md)和[任务源](../planning/tasks.json)。它保存的是**元数据版本**；原图片/视频内容、来源和所属项目不能在这里替换。

## 来源与一致性

每个参考上传对应一个目录条目；每个任务内的产物对应一个条目。生成条目的身份来自task ID加artifact ID，同一个演示artifact ID出现在不同任务时仍是不同条目。public source为reference/generated；generated携带原provider_mode，不能把演示内容显示成真实AI结果。

上传的reference元数据、上传幂等、目录revision1和上传审计同事务。生成结果的Task状态/产物与目录revision1同事务，目录写入失败则整个结果状态事务回滚并触发数据库熔断；已经写入的文件可能成为待核实孤立文件，不能声称数据库与存储跨介质原子。

任务普通状态保存会幂等补齐目录，不重命名用户已经编辑的条目，不把trash恢复为active，不重复创建修订。历史PG通过007迁移只回填来源、名称与revision1，不编造旧用户活动；已有数据缺少MIME、字节或缩略图信息时保持缺省，列表不会为了补信息读取整张原图。

## 查找与展示

`GET /api/v1/library`始终属于当前项目，使用现有cookie和X-Project-ID或project_id。limit默认30、1—100；按created_at和id倒序keyset分页。响应包括items、has_more、next_cursor和limit，cursor最多2048字节，绑定工作区/项目及全部过滤条件。

过滤项为state=active/trashed（默认active）、source=reference/generated、kind=image/video/poster、q和tag。q最多128字符，在名称及标签做文本包含搜索；百分号和下划线按普通文本处理，不是SQL通配符。tag最多32字符、规范化后精确匹配。没有全工作区或跨项目搜索入口。

AssetEntry返回当前名称、tags数组、state、revision、来源IDs、创建/修改/删除时间及同源内容URL。列表只使用thumbnail_url；没有缩略图就显示占位，视频只在用户明确播放后加载原件。reference有512px缩略图，新导入生成图按存储层能力提供缩略图，旧条目可为null。原件不能被当成列表缩略图预加载。

公开响应不包括bucket、object_key、磁盘路径、内容SHA256或网关凭据。生成结果导入的AssetInfo通过仅内部可见的Artifact.StorageInfo传入，同业务事务存于目录私有storage_info；该字段不会进入Task或AssetEntry JSON。下载不依赖当前模型或供应商是否启用。

## 元数据修订与并发保存

PATCH `/api/v1/library/{id}`接受完整`{expected_revision,name,tags}`和必需Idempotency-Key，成功200返回新条目。名称trim后1—120字符；标签最多10个，每个trim后1—32字符，转小写、去重；禁止NUL和控制字符。初次登记revision1，以后每次已受理操作递增并保存不可变历史。

写入先核对身份/JSON结构与幂等，再检查当前版本和新值。同键原body重放返回原响应及Idempotency-Replayed:true，即使条目后来变化，也不再写修订或审计；客户端按revision合并，不可用旧重放覆盖新状态。同键异body为409 IDEMPOTENCY_CONFLICT；版本变化为409 ASSET_REVISION_CONFLICT，须读取新状态后由用户处理。

PG事务锁定工作区行和目标条目，使同一工作区跨资产幂等键也一致。名称/标签/state、修订、幂等响应和asset.updated/asset.trashed/asset.restored审计同事务。审计只存动作、kind、版本、前后state，不存用户名称、标签或内容。数据库触发器拒绝UPDATE/DELETE历史，并拒绝改来源身份；仅服务端复用不同存储收据不会变成内容替换。

历史入口GET `/{id}/revisions`默认20、最多50条，before_revision是exclusive游标。历史返回revision/name/tags/state/updated_at，不返回可绕过回收站的旧内容URL。将旧名称/标签恢复为新版，要用当前expected_revision、新键及选中的旧元数据PATCH；它不会偷偷改变trash状态。

## 回收站与内容权限

POST `/{id}/trash`或`/{id}/restore`接受`{expected_revision}`和Idempotency-Key。trash条目保留元数据和历史，返回的content_url/thumbnail_url均为null。GET详情与历史仍可读取，原件/缩略图拒绝访问；回收站内PATCH返回409 ASSET_STATE_CONFLICT，须先恢复再修改。

软删除上传参考会从正常参考选择列表隐藏，新编辑请求不能引用；已经受理的任务仍保留原引用关联和不可变multipart文件，不因软删除改写执行。原上传键和原任务键仍能查回原受理结果，其旧URL实际读取时继续执行当前active检查。

新内容入口`/api/v1/library/{id}/content|thumbnail`检查条目、项目和active状态。旧`/api/v1/references/{id}/...`与`/api/v1/assets/{artifactID}`也查当前目录，不能绕过trash。多个同作用域任务持有相同artifact ID时，旧单ID接口按任意active授权处理；新目录URL严格绑定独立条目，其他条目active不会重开它。公开的demo SVG是固定公共示意素材，不能当成私有真实作品。

每资产最多1000个修订。trash在当前revision≥999时拒绝，为之后恢复保留一个修订；998→trash999→restore1000可完成。active元数据可写到1000，之后拒绝新改动；不是无限历史。memory模式全目录最多4096项、总修订最多16384，预留所有trash的恢复槽；容量满明确503，不悄悄清掉旧版本。

## 存储、迁移与维护边界

应用启动同时持有REFERENCE_DIR和ASSET_DIR的跨进程OS租用锁，禁止另一应用或离线维护并发写这些目录，即使它们指向不同数据库。PG007给数据库建立storage UUID，目录归属标记绑定同一UUID；首次绑定只接受空目录或数据库可达集合覆盖的已发布文件。memory应用拒绝写已归PG的目录。实际实现与离线维护证据由029记录。

软删除不会释放原图、缩略图、编辑正文或对象存储空间；trash仍可恢复，维护必须保留。当前没有永久删除、用户可选过期时间、修改二进制内容或跨项目移动。[对象存储接线和迁移](object-storage.md)由027负责，离线回收由029负责；对象地址始终由后端鉴权代理，不向浏览器泄漏签名链接。

007是数据格式和目录身份升级。升级前备份数据库、REFERENCE_DIR、ASSET_DIR及对象存储收据/对象；旧程序不认识007时拒绝启动。回退需恢复匹配版本的独立数据库和私有目录，不得删除迁移登记或目录归属标记绕过。只保留数据库不能重建文件内容。

## 已验证范围

普通HTTP测试覆盖目录来源去重、同时间keyset分页、文本搜索、标签规范化、CAS、旧响应幂等、元数据历史、回收恢复、原引用拒绝和旧下载绕过。真实PG验证两个独立连接的CAS/同键并发、数据库历史/source不可变、跨账户隔离、重启幂等、目录/审计故障注入事务回滚、006→007回填和旧程序拒绝。

固定内存上限和单PG writer仍是本地阶段限制。PG目录分页不受任务缓存淘汰影响，但名称包含搜索在当前项目候选上扫描，没有全文检索引擎或生产大库基准；不要把本批称为已完成分布式或极致吞吐验收。真实S3兼容服务器、供应商和公开运营放行分别按任务源记录，fixture与本机验证不替代生产验收。
