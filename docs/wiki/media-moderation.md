# 图片、视频与参考素材审核

本页对应V14-B2本地媒体审核子批次。真实审核服务尚未选型；精确文件fixture与本机HTTP协议测试只验证字节绑定和隔离链路，不能证明图片、视频或音轨的语义审核质量。完整V14仍在进行中。

A3.2已验收的调度增量见[首次审核分派契约](../planning/v16a32-contract.md)及[共享许可](media-review-permits.md)：新候选与019 READY同事务，确认STARTED后才审核；只有从未启动的新READY允许首次执行。旧候选不回填，STARTED确认不明不重审，取消/失租或关闭审核不放行。此增量不改变本页完整字节审核、策略冻结和整组公开/权益边界，联合验收另记079。

## 配置与启用边界

媒体审核独立使用`MediaModerationConfig`，不改写015已经冻结的text-v1政策。启用要求PostgreSQL、账户认证和已启用的[文本审核](moderation.md)，由应用启动入口统一检查。任务提示词经过文本引擎检查，参考图和生成产物另行检查真实完整媒体字节。

| 变量 | 约定 |
| --- | --- |
| `MEDIA_MODERATION_MODE` | `disabled`（默认）、`fixtures`或`http` |
| `MEDIA_MODERATION_POLICY_FILE` | 启动读取的完整服务端JSON政策，最多64KiB；修改后重启 |
| `MEDIA_MODERATION_ENDPOINT` | http模式私有完整HTTPS端点；demo fixture仅允许loopback IP字面量的HTTP |
| `MEDIA_MODERATION_API_KEY` | 独立16—2048字节可见ASCII Bearer密钥，不含空白；不落库、日志或浏览器 |
| `MEDIA_MODERATION_CREDENTIAL_ID` | 最多64个安全ASCII字符的稳定凭据标签 |

只开启文本审核而未启用媒体审核时，新媒体生成继续拒绝。开启媒体审核不追溯批准旧资产；关闭模式也不能放行既有隔离候选。旧幂等请求先回读原结果，不因政策改变创建第二次生成。

## 有限fixture政策

[示例配置](../../config/media-moderation.example.json)精确列出仓库原创PNG和真实MP4文件在input/output两个阶段的判定。每条规则必须包含`kind/phase/sha256/byte_size/decision`，其中kind为image/video，phase为input/output，decision为allow/block/review。最多64条；同kind、phase、hash不能重复。**没有匹配的完整字节身份一律REVIEW**，不从文件名、prompt、缩略图或影片封面猜测结果。

fixture仅允许`PROVIDER_MODE=demo`，真实渠道不能配置此模式。文件来源和长度/hash见[testdata/moderation](../testing/moderation-fixtures.md)及[manifest](../../internal/studio/testdata/moderation/manifest.json)。视频是有实际视频轨的短MP4；它们用于完整字节传输和状态测试，不能用演示静态SVG或视频海报代替视频审核验收。

政策顶层固定字段为`schema_version:1/policy_id/policy_version/max_image_bytes/max_video_bytes/fixtures`。JSON拒绝未知字段、重复键、大小写别名、非法UTF-8、非整数长度和尾随数据。图片最大32MiB、视频最大256MiB，政策可进一步收紧。HTTP政策要求fixtures为空数组。冻结快照保存完整规则/限额/契约/模式及适配器身份散列，不保存服务地址、密钥或媒体正文；政策自身另有SHA256摘要。

## 私有media-v1协议

这是本平台自定义的适配协议，不是假定任何厂商原生API已经兼容。接口为POST multipart/form-data，仅两部分，顺序固定：`metadata` JSON和`file`完整字节，文件名固定`media.bin`。不向审核服务提供可回调下载的任意URL，不发送用户/工作区标识、生成供应商凭据或本地路径。真实审核商需独立适配和受控验收。

metadata包含以下10个字段：`contract='media-v1'`、`request_id`、`policy_id`、`policy_version`、`policy_digest`、`phase`、`kind`、`content_type`、`byte_size`、`sha256`。响应必须HTTP 200、application/json，只能包含原样回传的这10个字段，加`decision/reason_code`。绑定项必须全部一致；允许的结果组合与text-v1相同：allow/allowed、block/policy_block、review/policy_review。

每引擎最多2个审核请求，总期限60秒。multipart完整请求体（含边界和metadata）最大256MiB，因此最大视频文件还需留出编码开销；响应最多8KiB。流式发送文件，不将视频整体加载进内存或Base64。请求前校验完整长度与hash，并对HTTP传输实际读取的文件字节再次计数/hash，只有完整multipart发送后才接受通过响应。

客户端禁用环境代理、重定向、Cookie和自动重试。未知/缺失/重复/别名字段、尾随响应、身份不匹配、超时、过大数据或提前返回均不能得到ALLOWED。固定安全原因码不包含服务原始错误、地址或内容。对旧任务使用原冻结政策；适配器身份或启用模式变化返回REVIEW，不以新政策猜测旧结果。

## 私有读取与候选持久化

`OpenMediaModerationAsset`用于服务端私有读取，不授予HTTP下载权限。本地文件在同一文件描述符上验证regular file、长度、完整SHA256及编码，然后rewind交给审核。S3收据必须真实读取绑定版本/ETag的私有对象，将完整字节流式写入受控临时文件并校验；不存在或篡改时不回落残留本地副本，不把Head/hash元数据当成实际审核内容。

读取helper每文件最大256MiB，全进程最多4个活跃reader；S3下载还受既有8路存储槽位限制。2分钟期限覆盖从打开、校验到调用者关闭reader的整个生命周期，调用者更短context优先。调用者必须Close；关闭或取消会删除自身S3暂存并释放槽位。临时文件使用现有维护可识别的命名空间，不替换原件或manifest。MP4本地格式预检只检查容器头，不声称在运行时完成全片解码、帧/音轨语义审核。

016新增四张表：

| 表 | 私有内容与不变式 |
| --- | --- |
| `studio_task_media_moderation` | task/workspace/project复合归属、完整媒体政策和文本政策、公开安全摘要；PENDING/version1只能一次变为最终状态/version2 |
| `studio_task_media_candidates` | 每task至多4个图片候选或1个视频候选；显式保留Artifact的私有StorageInfo；第一次插入后禁止修改/删除 |
| `studio_media_moderation_decisions` | task/version追加决定历史，禁止修改/删除 |
| `studio_reference_media_moderation` | 仅同步批准后注册的reference；固定ALLOWED/input/version2，冻结政策不可修改/删除 |

任务首次受理冻结媒体及实际文本政策，候选尚为空。供应商完成后，先在任务租约/版本检查通过的事务中登记私有候选；未审核字节不进入公开Task.Artifacts、资产库URL或事件。锁外检查后，最终决定、允许的公开产物、目录、权益与Outbox由主链路在同一事务提交，确认后才替换缓存。

候选登记和最终决定可分别重放，精确重放只读；不同候选或终态覆盖拒绝。运营/取消路径即使没有携带私有结构，helper也加载原政策与候选，只接受公开摘要的合法变化。未执行完、取消、重启或提交确认不明不自动重新生成；候选保留供核实，财务结案不放行内容。本轮没有人工重审/放行入口。

## 参考图、下载与维护边界

新上传先写私有文件，再在业务锁外按实际原件检查；只有批准后，reference、ALLOWED摘要、资产库、幂等和审计同事务注册。阻断或未知不创建reference，遗留未引用文件仍计入既有存储硬限和离线维护范围。新编辑使用的旧参考图也按当前冻结任务政策检查实际字节，不能把历史nil摘要当审核通过；历史资源的原有可见性不追溯变化。

生成任务采用整批交付：只要一项候选未通过，本批结果集保持隔离；不把未交付部分直接当作无成本。已执行但未交付继续进入账务REVIEW，原始冻结的处理仍见[权益操作](billing-operations.md)。本地/S3代理、目录原件/缩略图、legacy assets/references、参考复用和公开任务快照均需主链路审核门禁，不能靠隐藏前端按钮放行。

全部持久候选都属于维护可达集合，包括BLOCKED/REVIEW、取消及终态任务，不因为未注册公开目录而删除。私有S3迁移不改变内容判定；物理复制不是放行。016升级前继续停写联备数据库、两个私有目录、owner和必要密钥，离线工具须接受精确001—016；不删除迁移登记、审核决定或owner绕过格式保护。

## 验证与限制

引擎、私有HTTP和读取helper专项见[055台账](../changes/2026-11-14-055-media-moderation-engine.md)。真实PG、所有下载门禁、生成/账务/恢复和浏览器联合证据由本批集成记录汇总，不能把引擎测试冒充全链路完成。没有调用真实审核商或收费生成服务，也未做语义准确率、生产吞吐/灾备或全站多实例验收。
