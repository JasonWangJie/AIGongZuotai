# 024 · 私有参考图与图片编辑工作台

日期：2026-11-13。关联批次：V07 参考素材上传与图片编辑。模块：H03、H07、H08、H09。前端版本：0.6.0。

## 目的

将图片创作从纯提示词扩展到先上传私有参考图、选择有序素材、再以具备编辑能力的模型提交任务。上传与任务各自保持明确的受理状态和原始幂等身份，防止把停止传输或重试错误误解为文件未保存。

## 范围

增加文字生成／参考图编辑切换、上传选择／拖放、真实传输进度、服务端缩略图、项目素材分页、有序选择、编辑任务及活动标签。模型按显式编辑能力开放；既有登录、项目隔离、草稿恢复、容量与目录行为继续保留。

仅本地前端与自有 API 协议实现。没有 mask、原图裁剪、视频编辑、生成结果直接转参考图、文件删除或 DAG 执行。真实网关图片编辑验收尚未执行，V07 对应外部验收不能据此勾选。

## 实际文件

- `apps/workbench/src/ReferencePicker.tsx`、`references.css`：编辑参考图界面、真实进度、缩略图、重排、复用及窄屏布局。
- `apps/workbench/src/useReferences.ts`、`referenceState.ts`、`referenceUpload.ts`：有界并发、File/key 内存隔离、传输错误、重试和列表竞态合并。
- `apps/workbench/src/referenceState.test.ts`、`referenceUpload.test.ts`：传输、顺序、失效响应、原键及响应边界测试。
- `apps/workbench/src/types.ts`、`api.ts`、`api.test.ts`：参考资产／限制／编辑输入类型、项目分页接口、私有 URL 白名单。
- `apps/workbench/src/modelCatalog.ts`、`modelCatalog.test.ts`、`ModelControls.tsx`：逐模型编辑能力、选择过滤、有序引用请求与未知任务不可变测试。
- `apps/workbench/src/App.tsx`、`useStudio.ts`：编辑模式、提交校验、历史编辑快照、项目离开提示及请求身份保持。
- `apps/workbench/src/activity.ts`、`ActivityPage.tsx`：脱敏的参考图上传活动。
- `apps/workbench/src/main.tsx`、`package.json`、`package-lock.json`：样式入口及 0.6.0 版本，无新增依赖。
- `docs/wiki/frontend.md`、本台账：实现、限制和验证；顶层 README、索引、任务源由集成人维护。

## 接口与数据影响

GET references 使用项目 header 与 limit/offset，POST references 使用同项目 header、原 Idempotency-Key 和单 file 的 FormData；由浏览器设置 multipart boundary，不手写 Content-Type。上传超时 120 秒，最多两路并发。前端根据 bootstrap.reference_limits 预检允许 MIME 和单文件体积，真实字节格式、像素／尺寸由后端验证。本页待处理原文件最多 64 MiB。

POST 返回的私有 thumbnail_url 用于缩略图，原件不在浏览器提前解码。URLs 带素材所属 project_id，服务器继续校验账户／项目。文件名普通文本显示，外部 URL 不进入预览。历史列表与上传回包撞到同一资产 ID 时合并，选中顺序保持不变。

TaskInput 增加 operation=generate/edit；仅编辑时在 inputs 中附有序 reference_asset_ids，模型版本和预设实际字段沿用现有构造。切换模型不静默丢弃参考图；超过当前模型参考数量时阻止新任务并提示调整。原未知生成操作的 body/key 不随参考选择、模型目录或新操作重建。

原上传 File/name/project/key 按身份与项目保存在当前页面内存。网络／超时／abort／无效响应为待确认；手动原文件重试沿用原键，历史未知不因后续 429/401 变成可随意新建。明确放弃本页重试才释放本页文件；从选择移出和停止传输均不等于服务端删除。刷新／关闭提示只提供当前页面保护，不承诺跨关闭恢复。

## 验证结果

- `npm --prefix apps/workbench run typecheck`：通过。
- `npm --prefix apps/workbench test`：100 项／15 文件通过，包含原 85 项回归及新参考上传／编辑契约边界。
- `npm --prefix apps/workbench run build`：通过，62 个模块；JS 345.86 kB（gzip 109.87 kB）、CSS 66.27 kB（gzip 13.97 kB）。
- 验证覆盖：8 MiB 边界及格式预检、不同完成顺序、有序引用、跨项目列表过滤、旧 attempt 拒收、列表／上传竞态去重、原 File/key 与用户项目隔离、100% 传输尚未受理、abort 未知、结构化 429／请求编号、畸形或 null 响应、编辑能力与数量／重复引用、目录变更不改原未知请求。
- `node scripts/check-docs.mjs`：模块文件与 Wiki 链接完整；全仓检查当前仅报告 023／024 尚未登记根台账索引，由集成人同步后复验。
- async_scheduler 独立只读交叉审阅通过，未发现此次素材数据、身份隔离与原键重试阻断问题。
- 本前端代理未启动服务或操作前台浏览器；真实 Go API／PostgreSQL／桌面与手机浏览器联合验收由集成人另行记录。fixture 和演示 Provider 不替代真实网关验收。

## 风险与限制

File/key 仅当前页面内存，刷新或关闭会失去未确认上传的原键重试信息；已存服务器素材可重新从列表读取。最多 100 张／128 MiB 工作区文件配额由服务器执行，当前没有删除回收界面。偏好的参考顺序不跨刷新保存。图片格式／像素验证在服务器，客户端预检不能替代它。

本地构建与状态测试不是大文件吞吐、低端手机解码、万级素材性能或真实编辑质量证明。列表有分页、缩略图和 lazy loading，尚无虚拟化。默认 demo 输出仍为示意图；真实 multipart 编辑链路及效果需在受控环境具备凭据与授权后另行验收。

## 回滚方式

恢复本台账列出的工作台文件到本批前版本并重建前端；不删除用户已上传素材、服务端幂等记录或任务快照。旧前端仍只发起 generate，不能表示本批新增编辑能力；数据库格式与服务器版本回滚需遵守后端迁移限制。

## 文档同步

已同步 frontend Wiki 的 0.6.0 参考图行为、API、内存恢复边界、文件职责与未完成真实验收。根 README、Wiki／台账索引与 V07 任务状态由集成人根据各模块及联合验证证据更新，未执行的真实网关验收保持待完成。
