# 023 · V07 私有参考图与异步图片编辑适配

日期：2026-11-13。关联 **V07 参考素材上传与图片编辑**；本记录覆盖 provider 模块，批次完成以[任务源](../planning/tasks.json)和整体验收为准。

## 目的

将已授权的私有参考图用于真实图片编辑协议，保持原始multipart正文、boundary和幂等身份不变。避免把本地鉴权URL交给供应商，以及大量图片Base64在内存和JSONB间反复复制。

## 范围

图片编辑Prepare/Submit适配、私有请求文件接口、流式正文校验、旧JSON Body兼容、本地协议和失败测试。引用上传、实际图片解码、数据库ACL/关联、并发准入和前端由V07其他模块实现；本模块不提供URL下载型参考图或mask。

## 实际文件

- [internal/studio/media_http.go](../../internal/studio/media_http.go)：MediaSubmission可选文件字段、BodyStore注入、编辑路径与按正文类型发送。
- [internal/studio/media_edit.go](../../internal/studio/media_edit.go)：MediaBodyStore/Info、MediaEditSource接口、PrepareEditModel、固定multipart文件、同句柄hash/size验证与流式提交。
- [internal/studio/media_edit_test.go](../../internal/studio/media_edit_test.go)：5个编辑测试函数，临时私有文件store、合成PNG和本地HTTP fixtures。
- [docs/wiki/media-gateway.md](../wiki/media-gateway.md)与本记录；README、索引和任务源由集成人维护。

## 接口与数据影响

上游编辑端点为POST `/v1/images/edits_async`，使用multipart重复image[]文件部分及model/prompt/n/可选尺寸字段。202后继续原统一查询端点；不增加取消或按客户端请求号查回接口。平台参考资产ID和操作字段由core验证，适配器仅接受已验证私有源，不接收浏览器URL或用户给定本地路径。

MediaSubmission新增omitempty的BodyFileID、BodySize、ContentType，并复用BodySHA256；文件上限33 MiB。旧非空JSON Body序列化保持兼容，生成正文上限仍128 KiB。数据库保存文件标识而非图片正文/原始网关Key。实际存储由App持有的MediaBodyStore实现并负责生命周期，Provider.Close不关闭共用引用存储。大型编辑使用预建2分钟client共享同一Transport，原JSON/Query的30秒client不变，worker总context仍可提前截止；测试核对共享连接池与两类固定超时。

图片multipart重放依赖完全相同的原始字节，含boundary。准备本地文件不能在全局业务锁或持锁数据库连接内执行；core在准备后重新核对授权、幂等和准入再保存受理事务。未被受理事务采用的孤立文件暂保留并计入存储2 GiB硬限，V08实施安全回收。provider保留存储忙/容量耗尽的安全错误码，core分别映射429/507；其他本地校验失败不泄漏磁盘路径。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| 只读访问官方文档及公开SPA正文 | 通过 | 当前正文31988字节、SHA256与既有source.json一致；核对JSON/Data URI、multipart、mask边界与图片原字节幂等，没有真实生成请求 |
| `go test ./internal/studio -run 'TestAsyncImageEdit\|TestAsyncMedia\|TestOpenAI\|TestLive\|TestAsset\|TestMediaRetry' -count=1` | 通过 | 新5项编辑加旧20项provider专项；核心接线与独立client修正后0.441秒，本地httptest和临时文件，不是供应商验收 |
| 原请求与重启兼容 | 通过 | 解析multipart逐字段/参考图校验，持久元数据后用无默认模型Provider保持header、长度、原字节及Key重放，并续查同一handle |
| 文件和引用负例 | 通过 | 缺文件、长度/摘要错误、同大小篡改、boundary改动、错误endpoint、双正文在联网前拒绝；格式/数量/大小限制与磁盘错误脱敏 |
| 准备取消 | 通过 | 32 KiB读取后取消不会发布文件，也不会访问网关 |
| 存储背压与脱敏 | 通过 | 包裹ErrReferenceBusy/ErrReferenceStorageFull仍保留分类；错误中的私有路径不进入公开message |
| `go vet ./...` | 通过 | 当前provider和既有仓库静态检查通过；整批接线后仍由集成人重跑 |
| Provider专项联合真实适配HTTP链 | 通过 | 26函数合计3.629秒；新增上传→multipart编辑→查询→PNG导入→项目ACL下载→原键不重提测试归根025台账，不替代跨账户PG验证 |
| 全部上传/ACL/真实PG/浏览器验收 | 由V07集成人记录 | 适配器专项未操作测试数据库，不替代整批验证 |

## 风险与限制

单张8 MiB、最多4张、1600万像素及PNG/JPEG/WebP是平台政策，不能称为供应商通用要求。适配器依赖上传层已完成真实格式和像素校验。mask、GIF/SVG参考图、视频编辑/延长不在本批范围。只有本地文件存储，尚无对象存储/跨区复制；发布后文件须保持不可变。上游真实模型开通、编辑效果、计费与实际素材限制仍未验证。临时网络失败仍可能已经受理，不能用新Key重生；已经受理但正文丢失的记录须核实，不能按新目录拼回。

## 回滚方式

停止新编辑受理并保留原参考图、正文文件和数据库记录；已知handle仍可查询。回退须同步core的Operation/参考绑定与MediaSubmission文件分支，旧版程序不能识别新的编辑正文，不能只移除provider代码或删除版本栅栏强行运行。采用匹配版本的独立备份验证，保留升级后的原库及私有文件，不清理用户资产。

## 文档同步

- README、台账索引、任务源及TODO：由集成人结合V07实际验收同步。
- Wiki：[异步网关](../wiki/media-gateway.md)记录确切编辑协议、私有文件正文、字段限制、幂等和验证范围。
- 官方来源记录沿用[原始来源](../../internal/studio/testdata/media/source.json)，本次只读核验哈希一致；未添加真实响应、用户素材或凭据。
