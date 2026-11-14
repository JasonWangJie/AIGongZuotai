# 020 · 逐模型能力与规格工作台

日期：2026-11-13。关联批次：V06 模型目录与参数能力。模块：H03、H08、H09。前端版本：0.5.0。

## 目的

将工作台从每种类型固定能力升级到有限的服务端模型目录，准确处理模型启停、规格差异、版本更新与目录缓存。模型切换只影响新的创作选择，既有未知请求仍保持原始模型版本、参数和幂等键，历史结果保留原模型身份。

## 范围

新增逐模型选择与完整规格预设；支持图像比例／尺寸／分辨率／数量及视频时长／分辨率／尺寸，按服务端明确组合提交。增加目录版本、ETag 条件读取、60 秒可见性校验、手动刷新、模型变更错误提示、历史模型名称／版本。保留既有草稿、容量、会话隔离及未知受理保护。

本批不增加用户可写模型后台、不暴露供应商路由／密钥、不任意拼接参数组合，不调用真实付费 Provider。

## 实际文件

- `apps/workbench/src/modelCatalog.ts`、`modelCatalog.test.ts`：有限公开目录校验、中文原因、选择归一化、请求构造、缓存与历史身份规则。
- `apps/workbench/src/ModelControls.tsx`、`models.css`：模型、完整规格、数量、版本与刷新界面。
- `apps/workbench/src/types.ts`：Model/ModelCatalog/ParameterPreset/MediaParameters、model_version 输入与历史字段。
- `apps/workbench/src/api.ts`、`api.test.ts`：GET models、ETag/304、缺失缓存错误、LLM model_version 原样传递。
- `apps/workbench/src/useStudio.ts`：独立目录读取、身份缓存、可见校验与中止、逐模型提交校验、稳定 SSE 依赖。
- `apps/workbench/src/App.tsx`：模型选择接入、UTF-8 字节计数、历史名称／版本、停用提示及原请求身份。
- `apps/workbench/src/pendingRequests.ts`、`requestFeedback.ts`、`main.tsx`：原请求版本、目录变更反馈、样式入口。
- `apps/workbench/package.json`、`package-lock.json`：版本 0.5.0，无新增依赖。
- `docs/wiki/frontend.md`、本台账：当前能力、协议、边界与证据；根 README／索引／任务源由集成人维护。

## 接口与数据影响

新版 bootstrap 增加 catalog_version，models 使用版本化逐模型能力。GET `/api/v1/models` 返回 `{version,models}` 与 ETag，带项目请求头；同身份／工作区缓存可用时发送 If-None-Match，304 复用已有目录对象。缓存仅一个当前作用域，退出清理，bootstrap 版本变化清旧 ETag。非法公开结构不覆盖有效目录；无缓存的异常 304 直接报错。

按后端契约允许 1—32 模型；聊天无媒体预设，图像／视频每个 1—32 预设；图像数量 1—4 范围内逐模型配置，视频固定 1。图片预设不可带 seconds，视频 seconds 必须 1—600；图像 resolution 与视频 size 均有效。每次只发送选中预设实际 aspect_ratio/size/resolution/seconds 和 count，不发送 preset id/name。新 run/task 带 model_version，提示词按模型 max_prompt_bytes 和前端 5,000 UTF-16 单元边界共同校验。

主动换模型采用首个规格，数量按新上限调整；目录更新尽量保留仍有效的规格和数量，调整时提示确认，不裁剪提示词。提交瞬间检查界面模型版本与当前目录一致。MODEL_VERSION_CHANGED、MODEL_DISABLED、INVALID_MODEL_PARAMETERS 提示手动刷新／确认，不自动 POST；未知操作重试直接使用已登记 operation，完全不经过新目录参数重建。

目录读取在可见页面每 60 秒进行，重新可见和手动操作可校验；隐藏／切作用域中止 GET、使代次失效，遵守 Retry-After。目录更新仅修改 models/catalog_version。SSE effects 改为稳定项目／工作区 ID 依赖，目录更新不触发重订阅。历史 task/run 使用 model_name/model_version 快照，旧记录回退 model_id；取消按历史 provider_mode 判断 demo 媒体可取消、live 媒体不可撤回、LLM 始终有本地取消路径，不因当前目录停用而隐藏原记录。

## 验证结果

- `npm --prefix apps/workbench run typecheck`：通过。
- `npm --prefix apps/workbench test`：85 项测试、13 文件通过，原 70 项保持。新增覆盖逐模型字节上限／首项规格、版本变化后失效预设与数量、停用切换／全部停用、准确参数字段与不发送 preset 标签、未知操作不变、历史名称与取消、UTF-8 文本、有限目录和 600 秒边界、图像 resolution／视频 size、中文 reason、身份缓存与 ETag 变化、304 空体复用／缺失缓存拒绝、原 LLM model_version。
- `npm --prefix apps/workbench run build`：通过，57 模块；JS 327.45kB（gzip 103.69kB）、CSS 58.45kB（gzip 12.72kB）。仅为产物体积，不代表性能容量验收。
- 独立只读复核通过：完整预设组装、原 pending body/key 不随目录变化、用户／工作区缓存隔离、304 条件、旧回包代次与中止、SSE 稳定依赖与后端界限一致；复核代理未改文件或运行服务。
- 集成人复核提出初版 64 上限、视频秒数约束与英文 reason，均已对齐 32／600 及中文反馈，并重新执行类型、测试、构建。初次参数界面替换发现两处旧 capability 引用，修正后严格类型通过。
- 首轮浏览器复核后的小修：隐藏核心流程中的 opaque 版本哈希，改为“规格已确认／模型目录／受理时规格”，完整版本保留在 title／辅助标签；显式切换模型后参数变化给出一次核对提示，重复点击当前模型不重置规格。按要求未增加镜像测试，重新执行严格 TypeScript 与生产构建通过，产物体积已更新为最终结果。
- 根集成人负责统一后端启动、HTTP／PostgreSQL／浏览器联合验收与截图；本模块记录不将尚未完成的浏览器或真实供应商验证写成通过。

## 风险与限制

有限目录由服务端配置，不提供前端新增／修改模型或任意供应商参数输入。规格是明确定义的组合，不能随意混搭；默认首个规格可能不同于旧选择，界面会提示核对。目录读取失败时保留已有有效目录，最终服务端版本／启用／参数校验仍是权威，缓存不授予权限。

旧服务没有新版目录字段时保留 kind 能力兼容路径；刷新 endpoint 缺失会明确显示读取失败。过期目录不会修改历史数据，但既有未知操作只能在本页内存继续原键恢复，刷新／关闭仍存在既有恢复边界。历史 Task.parameters 是服务端快照，本批没有新建完整详情编辑页。没有目录热写管理、动态探测供应商、浏览器 Key 或跨标签页共享缓存；真实模型效果与供应商联调仍归后续 V15。

## 回滚方式

回退本批前端模型控件、目录读取／类型与版本号，保留后端已保存的 model_name/model_version/parameters 历史。回退前核对本页受理未知操作，避免丢失原 body/key；不能因旧界面不认识新规格而重新生成同一请求。后端新目录的旧客户端兼容由服务端处理，前端回退不应删除模型配置或历史任务。

## 文档同步

已同步 frontend Wiki 的 v0.5、逐模型规格、字节限制、目录缓存、原请求不变、历史身份、测试与边界。根 README、Wiki／台账索引、任务源和 V06 最终放行由集成人按实际证据维护；本台账不自行勾选批次。后端目录／Provider 记录及根联合验收分别见本批相关台账。
