# V08 · 资产目录接口契约

本文件为v0.7联合实现的冻结接口；实际交付证据见026—030台账，不能仅据契约宣称功能已完成。API继续使用cookie身份和X-Project-ID（或project_id查询参数）选择项目。

## 资源

AssetEntry字段：id、project_id、source（reference/generated）、reference_id?、task_id?、artifact_id?、provider_mode?（generated必有demo/live）、kind（image/video/poster）、name、tags数组、state（active/trashed）、revision、created_at、updated_at、deleted_at（日期/null）、content_url和thumbnail_url（同源字符串/null）、content_type?、byte_size?。

目录身份是reference ID或task+artifact的组合，绝不把跨任务重复的demo artifact ID单独作为主键。source内容与所属关系不可由元数据编辑更改。trashed条目两个URL均null；没有生成缩略图时thumbnail_url为null，列表不得取大原图代替缩略图。历史已导入条目可能缺少尺寸或MIME等元信息，不能伪造数值。

## 路由

| 接口 | 契约 |
| --- | --- |
| GET /api/v1/library | 当前项目目录；state=active/trashed默认active，source=reference/generated，kind=image/video/poster，q最多128字符，tag最多32字符，limit1—100默认30，cursor最多2048字节 |
| GET /api/v1/library/{id} | 可读取active或trash的元数据，须所属项目；不存在/越权404 |
| PATCH /api/v1/library/{id} | `{expected_revision,name,tags}`；必需Idempotency-Key；全量改名称/标签，200 AssetEntry |
| POST /api/v1/library/{id}/trash | `{expected_revision}`，必需Idempotency-Key，200 AssetEntry |
| POST /api/v1/library/{id}/restore | 同trash；恢复可访问性，不替换源内容 |
| GET /api/v1/library/{id}/revisions | limit1—50默认20、before_revision正整数exclusive；元数据不可变历史 |
| GET/HEAD /api/v1/library/{id}/content | active且归属校验后读取原内容，支持Range |
| GET/HEAD /api/v1/library/{id}/thumbnail | active且有缩略图才返回；否则404 |

列表响应为`{items,next_cursor:string|null,has_more:boolean,limit:number}`；游标按created_at+id倒序keyset，绑定项目和所有过滤条件，不能混用过滤条件翻页。q在名称/标签做文本包含搜索，tag为规范化后的精确匹配；都不解释SQL通配符。

历史响应为`{items:[{revision,name,tags,state,updated_at}],has_more,next_before_revision:number|null}`；历史项不返回原图URL、source或provider字段。选择历史元数据并保存新版本使用新键、当前expected_revision及旧name/tags PATCH；不偷偷修改trash状态。

## 校验、幂等与恢复

名称trim后1—120字符；最多10个标签，每个trim后1—32字符；标签转小写并去重。拒绝NUL及控制字符。每资产最多1000修订。初次注册为revision1，后续每次已接受动作追加一个不可变修订；幂等重放不会重复追加。

回收站内不允许PATCH，须先restore再编辑，返回409 ASSET_STATE_CONFLICT。trash在当前revision≥999时返回429 ASSET_REVISION_LIMIT，为之后恢复保留一个修订；998→trash999→restore1000有效。active元数据编辑可以到1000，但不能再接受新的变更。

写请求先做身份/JSON结构和幂等查回，再CAS与状态/新值验证。200重放带Idempotency-Replayed:true并返回原受理响应，即便资源后来继续变化。客户端必须按revision合并，不能将较旧的重放覆盖新状态。409 IDEMPOTENCY_CONFLICT表示原键换body；409 ASSET_REVISION_CONFLICT表示版本已变化；422 INVALID_ASSET_METADATA表示新元数据非法；429 ASSET_REVISION_LIMIT表示历史满。不存在/越权404 ASSET_NOT_FOUND。memory模式有界容量满503 LIBRARY_MEMORY_FULL；PG故障继承failclosed。

上传和生成产物注册目录与业务状态同PG事务；元数据变化、revision、幂等及asset.updated/asset.trashed/asset.restored审计同事务。审计不记录名称、标签或内容。007回填旧参考和task产物的revision1，不伪造历史用户动作。

软删除拒绝新的编辑引用及原图/缩略图访问；旧资产内容接口也检查active目录，不能绕过trash。多个同作用域任务持有同artifactID时，旧单ID下载按任意active授权处理；新目录URL按独立entry判断。已受理任务的不可变multipart文件、引用绑定和查询handle保持原状。恢复重新开放访问。

## 存储边界

源内容不可变，V08历史是元数据历史，不是图片/视频二进制版本。对象存储配置、权限与原件校验由027负责。公共接口不返回bucket、object_key、磁盘路径、内容SHA256、私有网关凭据或签名URL；同源内容URL由后端鉴权后代理。回收与迁移须持单写锁、以数据库可达集合为准，不删除trash中仍可恢复或任务仍引用的内容。
