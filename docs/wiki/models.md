# V06 · 有限模型目录与能力版本

本模块把公开模型、精确参数组合和上游路由分开，关联 [V06任务](../planning/tasks.json)及 [018台账](../changes/2026-11-13-018-model-catalog.md)。它提供本地配置目录与真实服务端校验，不声称自动发现所有供应商模型或验证供应商实际支持范围。真实模型仍须按 V15 完成独立网关联调。

## 配置与发布方式

`MODEL_CATALOG_FILE` 指向 UTF-8 JSON 文件，最多64 KiB、1—32个模型，每个媒体模型最多32个预设。未设置时按现有环境变量生成 studio-chat、studio-image、studio-video 三条兼容目录。修改文件不会热更新；重启成功后才使用新版本。

可参考 [config/models.example.json](../../config/models.example.json)。示例 upstream_model 是占位值，不是供应商可用模型承诺；demo 模式不提交真实生成。文件不能包含网关地址、密钥或cookie，相关凭据继续只从服务端环境读取。

```json
{
  "schema_version": 1,
  "models": [
    {
      "id": "image-concept",
      "name": "概念图像",
      "kind": "image",
      "description": "已经核对参数范围的服务端模型",
      "enabled": true,
      "upstream_model": "your-private-upstream-model",
      "capabilities": {
        "max_count": 4,
        "max_prompt_bytes": 8000,
        "parameter_presets": [
          { "id": "square", "name": "方形", "aspect_ratio": "1:1", "size": "1024x1024" }
        ]
      }
    }
  ]
}
```

`enabled` 必须显式填写bool；未知字段、重复JSON键、重复ID、重复有效预设、超界数值、非法类型均拒绝启动。模型名称1—80字符、说明至多500字符；公开ID最多64位，studio-chat/image/video三个保留ID不能改变类型。私有upstream_model最多128字节，不能包含换行或NUL，不会返回浏览器。

`cancel_supported` 不可由配置宣称，按实际行为派生：chat可本地停止、demo媒体支持本地取消、live媒体不提供确认取消。模型已启用但网关未配置时，公开条目仍存在但 enabled=false、reason=provider_not_configured；配置停用为disabled_by_operator。live至少需要一个配置的网关，不允许空配置假装真实服务。

未配置目录文件的旧live视频配置仍要求明确 `ASYNC_VIDEO_SECONDS=1..600`，不会自动选择5秒并提交真实视频。demo默认视频5秒；自定义目录视频时长来自其预设，独立于旧环境默认值。目录停用或删除媒体条目后，只要保留原网关配置，仍能初始化查询适配器处理历史handle，不要求再填写当前默认模型。

## 公开接口与版本

- `GET /api/v1/models` 返回 `{version,models}`。每个Model包括id/name/kind/description/enabled/reason?/version/capabilities。
- bootstrap 返回相同models并增加 `catalog_version`。原按kind聚合的capabilities字段保留兼容，但新前端只依据逐模型精确能力提交。
- 单模型版本及目录版本都是稳定的opaque SHA-256标识。能力、名称、启停及私有上游模型映射变更会改变版本；密钥、URL不会进入版本输入或公开JSON。
- 模型接口响应在启动时序列化缓存，返回强ETag、`Cache-Control: private, no-cache`。匹配If-None-Match返回304。认证与项目归属检查先执行，匿名请求不能借已知ETag绕过401。
- 同一文件只改变空白/对象键书写顺序不改变规范化版本；模型顺序、预设顺序影响展示和默认匹配，属于目录内容。

## 精确组合与默认值

capabilities包括 `cancel_supported`、`max_count`、`max_prompt_bytes`、`parameter_presets`。V07新增 `image_edit_supported`、`max_reference_images`：只有image能开启，开启须1—4，关闭须0。默认demo图片支持4张，默认live不推断编辑能力，需目录显式声明。上传/引用/恢复规则见[参考素材](references.md)。chat预设始终是空数组；图片数量1—4、视频与chat为1；prompt上限按UTF-8字节计算，可在1—16000内收紧。

每个媒体预设是完整组合：id/name/aspect_ratio，以及可选size/resolution/seconds。image可配置size与resolution，但不接受seconds；video可配置size/resolution，时长须1—600秒。目录只声明运营者配置过的有限组合，不把独立参数枚举做笛卡尔积。size和resolution的具体值还须由供应商文档及fixture/真实验收确认。

客户端发送预设实际参数与count，不发送预设id/name。完全相同的参数组合优先匹配；省略参数时，从第一个兼容的完整预设补齐，count省略/0默认为1。如果参数不能共同匹配任何预设，返回422 INVALID_MODEL_PARAMETERS，不提交上游。前端显式发送选中的完整组合，避免依赖隐含默认值。

新请求可携带 `model_version`，新工作台总是填写。当前版本不匹配返回409 MODEL_VERSION_CHANGED，模型停用返回422 MODEL_DISABLED；模型不存在/类型不符为422 MODEL_NOT_FOUND。这些目录拒绝发生在受理前，不创建资源或幂等记录。旧客户端可以省略version，但仍按当前目录验证。

## 幂等与历史快照

受理顺序：会话/项目归属与JSON结构检查 → 幂等查询 → 原请求重放或同键异参409 → 当前模型版本/启停/参数验证 → 资源准入 → 原子持久受理。

新的幂等hash属于客户端原始typed请求；默认值填充发生在独立的执行副本上。因此目录默认组合变化、停用、删除、提示词上限收紧，都不会让原body/key重放失效。修改body或model_version同时使用原键仍为409 IDEMPOTENCY_CONFLICT，不能把旧未知请求偷偷换成新执行。

Run/Task保存model_name、model_version、model_capabilities能力快照；Task另存实际规范化parameters。它们和业务、幂等、审计同事务保存，返回副本深拷贝能力预设和参数，避免修改返回值影响后续执行。历史旧记录可能缺少这些字段，前端按旧模型ID/ProviderMode展示，不能补写当前能力冒充当时能力。

V06之前的task幂等记录采用固定count=1、aspect_ratio=16:9的旧归一化规则。重放先读旧资源，用是否存在model_version选择相应hash规则。固定历史JSON字面量的SHA-256测试验证字段顺序和omitempty兼容；仅用新结构重新生成测试hash不能证明真实旧字节兼容。

## 执行和恢复边界

旧单轮LLM受理时冻结私有upstreamModel到runRecord，通过StreamModel显式传入；V10对话同时冻结结构化messages、输出预算和私有协议字段，通过StreamMessages执行。两者共享网关Transport，绝不修改provider默认模型。运行生命周期内目录不可变；重启全部未完成LLM转INTERRUPTED，不重新请求供应商。

真实媒体在受理时执行无网络的PrepareModel，将已解析参数、上游模型、原始请求字节及上游幂等键保存为MediaSubmission，并与资源/审计/平台幂等记录同事务提交。V07图片编辑改用无网络PrepareEditModel，释放业务锁后生成不可变multipart文件，只把文件标识/摘要/长度与任务同事务保存；随后重新检查幂等和准入。worker只发送这份原始快照，不能在重启后按新目录重建。一个任务有handle后只Query，原目录停用不影响查询；已保存的凭据身份保护仍禁止换key/服务根后盲查。

旧任务有handle或原始submission时继续使用原记录。旧live任务若同时缺handle与submission，则无法证明原受理时使用的模型与默认参数，恢复为RECONCILE_REQUIRED、错误EXECUTION_SNAPSHOT_MISSING，禁止猜当前配置重提。demo旧队列可恢复。提交期间崩溃的SUBMITTING无handle仍为SUBMIT_UNKNOWN，不能因存在精确请求字节就自动再提交。

目录不会改变取消、读取、下载、SSE和安全核对的权限与历史状态。资源准入按公开模型ID累计，历史停用/删除模型的非终态仍占名额。runtime列出当前目录与仍占用的历史模型；全局metrics将非固定公开ID归入固定other桶，不把私有模型名变成标签。

## 数据格式升级与验证

[005_model_snapshots.sql](../../db/migrations/005_model_snapshots.sql)记录数据格式版本栅栏，不重写表。旧v0.4二进制可能在保存JSON时丢掉新快照字段，因此必须通过未知迁移拒绝启动来阻止它读取写入升级库。升级前备份；回退需在独立数据库恢复匹配旧版本的备份，不能删除005版本登记。

本地测试覆盖配置/版本/鉴权ETag、精确组合、深拷贝、受理无网络准备、固定旧hash，以及真实PG停用后重放/恢复、缺快照保守处理和004→005升级。实际命令见018；provider多模型路由与协议fixture见019。没有使用真实收费密钥，fixture不能替代V15真实供应商验收。

## V10 文本上下文能力

chat新增公开`context_window_tokens`/`max_output_tokens`，默认32768/4096，范围2048—1048576和128—32768；输出预算加272最低封装余量必须小于窗口。max_prompt_bytes仍是单条输入上限，不保证任意允许长度都能放入该模型窗口。非chat字段为0，拒绝错误媒体配置。

私有`chat_output_limit_field`明确选择max_tokens或max_completion_tokens，默认前者兼容New API，参与当前模型版本但不暴露给客户端。不能按模型名推断、不能同时发送或失败后换字段重新生成。每次受理以服务端保守估算验证完整上下文；这不是供应商实际token/计费。详见[多消息Provider](chat-provider.md)与[V10契约](../planning/v10-contract.md)，真实供应商支持仍由V15确认。
