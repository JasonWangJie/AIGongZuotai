# 图片与视频审核工作台

V14-B2 / H03、H04、H05、H06、H08、H09。v0.14的前端实现沿用深炭色工作台，新增图片、视频和参考上传的检查状态、整组交付与内容权限保护。范围对应[V14-B2契约](../planning/v14b2-contract.md)；服务端检查协议见[媒体审核引擎](media-moderation.md)。本页记录已实现的客户端行为及本地测试，不代表真实审核商或内容分类效果验收。

## 当前能力与状态

Bootstrap保留文本审核能力，增加`media_mode=disabled|fixtures|http`及可选媒体策略身份。只有`moderation.media_supported=true`才能开放当前新媒体提交；不满足时保留原未知请求核对和历史记录。`fixtures`标为“DEMO · 本地样本校验”：它按已登记样本字节匹配，未知样本需核实，不是通过关键词判断图像或视频语义。`http`仅说明私有适配方式，不能据此声明真实服务已验收。

fixtures说明同时明确“固定PNG/1秒视频样本，不按所选规格生成”，避免把用于审核链验证的样本误当成模型时长/分辨率效果；HTTP模式不显示该固定样本说明。

Task、ReferenceAsset及AssetEntry使用可选`moderation`摘要，包含冻结策略身份、`input|output`阶段、独立版本和`PENDING|ALLOWED|BLOCKED|REVIEW`状态。旧资源缺少摘要时兼容原行为，但没有“已通过”标签。关闭新准入配置不会清除已知摘要。

| 对象/状态 | 前端行为 |
| --- | --- |
| Task `MODERATING` / `PENDING` | 显示“内容检查中”和未完成的进度，不挂载产物、原图链接或视频；只读取原任务状态。支持取消的非终态demo仍可明确取消。 |
| Task `ALLOWED`且`SUCCEEDED|PARTIAL` | 整组结果开放；本地demo结果标为“本地审核样本，非 AI 生成”。 |
| Task `BLOCKED|REVIEW` | 整组隐藏，仅状态刷新；不展示供应商原始错误、不提供重新生成、重新审核或人工放行。 |
| Task `RECONCILE_REQUIRED`、摘要PENDING且服务端明确`can_reconcile=true` | 提供“查询原任务”：服务端确认原handle存在且没有待审候选，只恢复原查询，不创建生成、不重审隔离候选。MODERATING和终审核决定没有这个入口。 |
| Reference / Asset `ALLOWED` | 按项目权限及回收状态开放预览、下载和参考使用。 |
| Reference / Asset受限或摘要非法 | 不挂载缩略图、原文件和视频URL；不能用于新的生成。已有待确认操作不被改写。 |

状态有文字、符号和颜色，不依赖颜色单独表达；琥珀色表示等待/核实，批准使用较弱绿色标记。沿用窄屏换行及reduced-motion设置。账务摘要继续独立显示，未交付不代表费用为零，财务核实也不是内容放行。

## 乱序与内容投影

`mediaModeration.ts`分别合并资源版本与审核版本。相同甚至较旧的任务或资产业务版本仍可携带更高审核版本，立即撤除预览；后到的低版本、缺失摘要或策略身份冲突不能重新开放本地已知限制。只有与最终有效决定相匹配的快照可以提供内容URL，任务还必须满足可交付终态。

素材名称/标签的dirty状态、CAS版本、冲突和未知保存单独保留。`projectAssetSafety`只更新安全摘要与可用URL，不用安全刷新覆盖用户草稿；采用旧冲突元数据、载入历史名称、关闭再打开待确认编辑、回收后恢复，都不能抹去已知内容限制。资产详情同步投影父组件传来的安全状态，不等effect之后才隐藏旧预览。视频播放意图绑定素材及审核版本；限制时移除播放器，后续批准也需重新点击。

任务中心与最近创作使用相同安全组件。列表只加载允许状态的缩略图，详情原文件按需打开，视频不预加载正文。刷新按钮只调用原任务GET，并沿用用户、项目、页面代次及请求身份隔离；不因状态等待自动POST。“查询原任务”是服务端明确允许时的用户操作，调用原reconcile接口恢复查询，不能与重新审核混为一谈。当前页面无法撤回已经被浏览器获取的数据，服务端下载鉴权、候选隔离和同事务发布仍是最终边界。

## 上传与参考顺序

参考上传不是异步保存的PENDING素材：100%仅表示传输结束，客户端继续等待格式及内容检查。只有带原件URL、缩略图及允许摘要的成功响应才登记为ready；旧nil成功响应兼容，但不标记批准。非预期的成功受限响应、缺URL或外项目响应按受理不明保留原File/key。

明确前置拒绝采用固定说明，不透传审核器原文：`MEDIA_MODERATION_INPUT_BLOCKED`422、`INPUT_REVIEW`409、`INPUT_INVALID`422、`BUSY`429、`UNAVAILABLE`503及`CONTEXT_CHANGED`409。只有精确的503 `MEDIA_MODERATION_UNAVAILABLE`可视为本次前置未受理；其他503和网络异常继续保守保留原请求。

首次明确阻断、待核实或格式拒绝不会自动上传/重审；原File留在页面内存，用户可以移出并更换。繁忙或渠道暂不可用只能手动操作，并遵守Retry-After。**此前已经未知的请求不因后续明确拒绝丢失原File、body、模型、报价、顺序或key**；仍能按原请求核对。刷新/关闭会丢失页面内存恢复能力，不能称为离线持久保护。

选中的参考若出现受限摘要，保留选择顺序并提示用户主动移出或替换，新提交不能静默删掉一张图继续生成。旧列表或上传确认不覆盖已知限制。已受理的图片编辑原请求仍按其冻结正文核对，不随着当前选择、目录或检查能力改变。

## 实现与验证

- `mediaModeration.ts`、`MediaModerationStatus.tsx`、`moderation.ts`、`moderation.css`：安全合并、状态和模式说明。
- `App.tsx`、`types.ts`、`state.ts`、`useStudio.ts`：MODERATING任务、整组产物门槛和只读刷新。
- `useCreationDraft.ts`、`creationRender.test.tsx`：未完成表单的真实App渲染。报价、提交按钮和submit使用同一完整输入门槛，空参考/受限参考/上传未确认时不构造TaskInput，避免render先于按钮检查抛错；原未知请求不重建。
- `libraryState.ts`、`LibraryPage.tsx`、`LibraryDetail.tsx`：元数据与内容权限分别合并，缩略图、视频及下载入口。
- `referenceState.ts`、`ReferencePicker.tsx`、`useReferences.ts`、`referenceUpload.ts`：有序选择、成功确认、原File/key及前置拒绝。
- `api.ts`、`requestFeedback.ts`：固定安全错误与未知优先语义。

2026-11-14：新增27项媒体专项及6项实际App渲染回归；全前端270项/26文件通过，TypeScript与生产构建通过。覆盖实际组件SSR不泄URL、取消后迟到ALLOW、MODERATING、整组PARTIAL、dirty/CAS/未知编辑、回收恢复、参考顺序及真实multipart客户端的本地XMLHttpRequest fixture。此前264项未覆盖未完成编辑表单导致的render白屏，该缺陷由根浏览器发现后新增真实App测试复现并修复，不将先前通过结果冒充覆盖。测试与构建数据详见[057台账](../changes/2026-11-14-057-media-moderation-workbench.md)。Go/PG、实际浏览器图片与视频的联合结果由[058集成台账](../changes/2026-11-14-058-media-review-integration.md)记录；本代理未运行数据库或真实供应商调用。

当前未提供审核控制台、申诉、人工放行、自动重审、存量资源追溯审核或真实审核商语义效果报告。Vite主JS包505.19kB触发500kB提示，gzip159.37kB；未调高警告阈值，后续性能批次应按路由拆包并实际测量，不能把本地构建当生产容量证明。
