# 2026-11-14-057：媒体审核状态与前端交付保护

## 目的

V14-B2 / H03、H04、H05、H06、H08、H09：让图片、视频与参考上传的等待、批准和隔离有明确说明，并防止旧任务、资产元数据和上传回包重新显示已限制的内容。延续v0.13文本审核与原未知请求保护。

## 范围

Task整组交付、MODERATING状态、Reference同步上传检查、资产预览/视频/下载/参考选择，以及元数据编辑中的独立安全投影。沿用现有暗色设计，不新增审核控制台、人工放行或重审入口，不修改后端、数据库、依赖或真实渠道配置。根维护v0.14包版本、README、索引、任务源及058联合验收；App角标同步STUDIO / 0.14。

## 实际文件

- `apps/workbench/src/mediaModeration.ts`、`MediaModerationStatus.tsx`、`mediaModeration.test.tsx`：新增媒体安全合并、状态说明与27项专项。
- `apps/workbench/src/moderation.ts`、`moderation.css`：摘要增加input阶段、媒体Bootstrap字段、共享身份合并与紧凑媒体状态样式；文本依然仅output批准有效。
- `apps/workbench/src/App.tsx`、`types.ts`、`state.ts`、`useStudio.ts`：任务状态与整组产物门槛，安全TaskCard、原任务GET刷新和版本角标。
- `apps/workbench/src/useCreationDraft.ts`、`creationRender.test.tsx`：可独立控制的页面表单状态与6项真实App渲染回归；不新增依赖或持久化草稿渠道。
- `apps/workbench/src/libraryState.ts`、`LibraryPage.tsx`、`LibraryDetail.tsx`：独立安全投影、dirty/CAS/未知恢复、列表缩略图、详情视频与原件访问。
- `apps/workbench/src/referenceState.ts`、`ReferencePicker.tsx`、`useReferences.ts`、`referenceUpload.ts`：参考摘要、顺序保护、同步上传批准、原File/key与明确拒绝反馈。
- `apps/workbench/src/api.ts`、`requestFeedback.ts`：媒体错误安全文案、精确503前置例外及原未知优先。
- `docs/wiki/media-moderation-workbench.md`及本台账。
- `docs/wiki/frontend.md`、`development.md`、`references.md`、`roadmap.md`：v0.14当前范围与最终前端数据，保留历史证据；联合验收由058补充。

## 接口与数据影响

使用已冻结的Task/ReferenceAsset/AssetEntry可选moderation摘要及Bootstrap media_mode/media_policy_id/media_policy_version，不新增API路由。MODERATING为非终态，不用99%冒充交付完成。Task仅output ALLOWED且SUCCEEDED/PARTIAL开放整组产物，其他摘要状态不生成内容DOM或下载链接。nil旧记录兼容但不标审核通过。

安全版本与任务version/资产revision分开合并。旧/缺失摘要不能解除已知限制；dirty元数据及未知保存保留原CAS/body/key，但立即应用新安全决定。取消后迟到批准不得在已取消任务中公开；视频播放意图绑定当前版本，受限后移除src。回收恢复不代表批准内容。

参考上传仅真实成功允许响应登记ready，100%传输继续等待。首次明确BLOCKED/REVIEW/INVALID保留File供移出更换，不提供自动重新审核；未知网络保留原File/key。503只对精确MEDIA_MODERATION_UNAVAILABLE视作前置未受理，已有未知请求始终优先，不重拼原任务输入/报价或参考顺序。所有操作沿用账户、项目和代次隔离。

## 验证结果

| 命令/检查 | 结果 | 实际覆盖与限制 |
| --- | --- | --- |
| `npm --prefix apps/workbench run test -- mediaModeration.test.tsx libraryState.test.ts` | 39项通过，0.487秒 | 新27项媒体专项及原资产元数据回归 |
| `npm --prefix apps/workbench run test` | 270项/26文件通过，2.39秒 | 全前端回归，无真实供应商请求 |
| `npm --prefix apps/workbench run typecheck` | 通过 | 新摘要、MODERATING、现有文本和媒体接线 |
| `npm --prefix apps/workbench run test -- creationRender.test.tsx` | 6项通过，0.758秒 | 实际App渲染；空/受限/上传中参考、完整顺序、非法prompt及能力变化均不发送网络请求 |
| `npm --prefix apps/workbench run build` | 通过，Vite 1.62秒 | JS505.19kB/gzip159.37kB，CSS116.27kB/gzip23.25kB；有500kB分包提示，未关闭警告 |
| 状态、SSR与multipart fixture | 通过 | 较旧资源版本的新限制、缺摘要不复活、策略冲突、取消后迟到ALLOW、PARTIAL整组、实际预览/下载DOM隐藏、dirty/CAS/回收恢复、参考顺序、100%未确认、原未知优先 |
| PG/实际浏览器/真实审核渠道 | 本代理未运行 | 根058集成记录Go/PG和桌面手机证据；真实媒体审核商待后续选型与验收 |

初次回归中原资产测试发现旧observed的对象引用被无必要替换，已保留旧元数据的早期返回，同时允许更高安全决定先更新；未放宽原断言。新增参考SSR测试初稿误让允许素材和受限素材使用同一fixture URL，已分离两份URL并保持“不出现受限URL”的断言。

交付前契约复核补齐原查询恢复：仅RECONCILE_REQUIRED、有效output PENDING摘要且服务端can_reconcile明确为true时提供“查询原任务”，服务端保证有原handle且尚无候选；MODERATING、SUBMIT_UNKNOWN和终审核决定不开放。新增实际TaskCard SSR正反例，保留nil历史原核对行为；当时264项全量、TypeScript和构建重新通过；后续真实浏览器仍发现下述未完成表单的渲染缺陷，不能把264项视作已覆盖该路径。该入口不重新生成、不重新审核已隔离结果，刷新状态仍是GET。

根浏览器在PG/demo+credits+两审核开启环境点击“参考图编辑”、尚未选择图片时发现白屏：App在render阶段无条件构造报价TaskInput，虽然按钮会检查editInvalid，但严格构造函数先因空参考抛错；受限/上传未确认的引用会产生同样问题，关闭计费也不能规避该render路径。已让报价、提交按钮及submit共享creationInputReady完整表单门槛，不吞构造错误或放宽modelCatalog要求。草稿输入状态提取为useCreationDraft，生命周期与内存保护不变；新增实际App SSR测试先得到5个失败（4个同类白屏、1个空prompt仍构造报价），修后6项通过，包含允许参考的原顺序报价正例。原未知任务仓不受新表单门槛影响。

根浏览器还指出视频样本固定1秒而所选规格可能为5秒，因此fixtures说明明确补充“固定PNG/1秒视频样本，不按所选规格生成”，仅本地样本模式展示；HTTP模式不冒用该说明。更新后全量270项/26文件与生产构建通过，实际浏览器复验事实仍由058记录。

## 风险与限制

fixtures仅已登记字节样本，不代表图像或视频语义审核。HTTP字节协议和本地样本不等于真实供应商验收。本轮无存量追溯、人工放行、重新审核或申诉；旧nil记录不伪称批准，财务状态不替代内容决策。

前端隐藏不能撤回已经发送到浏览器的数据，必须依赖服务端私有候选、同事务发布和所有下载入口的鉴权。输入、待确认文件与元数据仅页面内存恢复；刷新/关闭不能保证保留原文件和key。Vite主包首次略超500kB提示，后续按路由拆包及浏览器性能实测另行验收，当前不宣称极限并发或线上加载指标。

## 回滚方式

前端回退须继续保留已受理任务的摘要和内容权限检查，不能通过删moderation字段、关闭新准入或恢复回收状态开放旧隔离内容。服务端016升级和冻结候选按联合备份/格式栅栏恢复；禁止删除决策、迁移登记或owner文件强退。无本批新增前端持久敏感数据需要清理。

## 文档同步

- [媒体审核工作台Wiki](../wiki/media-moderation-workbench.md)：实际状态、上传确认、内容权限、元数据恢复和验证边界。
- [V14-B2契约](../planning/v14b2-contract.md)及[媒体审核引擎](../wiki/media-moderation.md)由根及引擎代理统一维护，本页与其正式字段对应。
- README、Wiki/台账索引、任务源/TODO及联合验收归根[058台账](2026-11-14-058-media-review-integration.md)；本地B2、真实渠道和V14-C分开维护，不提前标记完整V14完成。
