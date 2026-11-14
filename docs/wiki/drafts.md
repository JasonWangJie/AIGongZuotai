# 项目云端草稿与版本

本页对应 **V03**，说明已经实现的 Go 保存契约、权限和 PostgreSQL 数据行为。前端交互见[工作台说明](frontend.md)，接口定义见 [OpenAPI](../api/openapi.yaml)。本批仍是文本节点草稿，不具备连线执行、DAG 调度或多人实时协作。

## 已实现范围

启用 PostgreSQL 后，每个项目拥有独立草稿。保存成功会生成新版本，旧版本不能通过应用修改或删除。读取旧版本后，以最新版本号重新保存其节点，即形成一个新版本；恢复不会倒退当前版本号，也不会清除中间历史。

未配置数据库时，四个云端草稿接口返回 `501 DRAFT_STORAGE_REQUIRED`。浏览器本地草稿仍是独立的开发模式，不伪装成云端持久化。账户模式必须先登录；工作区和用户来自服务端会话，不能由请求体指定。若显式发送 `X-Project-ID` 或 `project_id` 查询参数，其项目必须与 URL 路径一致，否则返回 404。

## 请求与响应

| 方法与路径 | 行为 |
| --- | --- |
| `GET /api/v1/projects/{project}/draft` | 当前版本；尚未保存时返回版本 0、空节点、`updated_at: null` |
| `PUT /api/v1/projects/{project}/draft` | 带 `Idempotency-Key` 保存；请求必须包含 `expected_revision` 和 `nodes` |
| `GET /api/v1/projects/{project}/draft/revisions` | 历史摘要，版本倒序；不返回所有版本正文 |
| `GET /api/v1/projects/{project}/draft/revisions/{revision}` | 指定不可变历史正文；版本号必须为正整数 |

当前版本和历史正文使用同一个响应结构，保存成功也返回此结构：

```json
{
  "project_id": "prj_example",
  "revision": 3,
  "nodes": [{"id": "node-1", "title": "开场", "note": "镜头与对白草稿"}],
  "updated_at": "2026-11-13T08:00:00Z"
}
```

保存示例：

```http
PUT /api/v1/projects/prj_example/draft
Content-Type: application/json
Idempotency-Key: draft-example-0001

{"expected_revision":2,"nodes":[{"id":"node-1","title":"开场","note":"镜头与对白草稿"}]}
```

`expected_revision` 为 0—1000 的整数；首次保存使用 0。节点最多 40 个，`id` 为 1—80 位 ASCII 字母、数字或 `._:-`，首位必须为字母或数字，同一草稿内不能重复。标题最多 80 个 Unicode 字符，正文最多 5000 个 Unicode 字符，两者均可为空；禁止 NUL 字符。节点顺序和文本空白会被保留。请求体最大 **1 MiB**，不得包含未知字段（包括 `user_id`）；节点也不接受任意附加字段。

历史列表默认 `limit=20`，允许 1—50；`before_revision=N` 为排他游标，只返回小于 N 的版本。返回：

```json
{
  "items": [{"revision":3,"updated_at":"2026-11-13T08:00:00Z","node_count":1}],
  "has_more": true,
  "next_before_revision": 3
}
```

继续读取时把 `next_before_revision` 作为下一次 `before_revision`。最后一页的 `has_more` 为 false，`next_before_revision` 为 null。版本 0 是未保存占位状态，不进入历史表或历史列表。

## 冲突、重试和恢复

同一项目内，幂等键绑定一次保存请求（包含原 `expected_revision` 和完整节点）。平台按解析后的请求结构计算哈希，JSON 排版和字段顺序不影响幂等判断，节点顺序和实际文本内容会影响判断。

- 同键同请求返回第一次保存的**原版本**，即使当前版本已前进；响应带 `Idempotency-Replayed: true`，不会再生成版本或审计事件。
- 同键不同请求返回 `409 IDEMPOTENCY_CONFLICT`。
- 新键但 `expected_revision` 已过期返回 `409 DRAFT_CONFLICT`。响应不夹带最新正文；调用者读取最新版本，保留本地修改后明确处理冲突。
- 网络超时不能证明未保存。保留原请求和原键重试，不能生成新键直接覆盖。
- 恢复旧版：读取旧版节点，再用当前版本号和新幂等键保存。如果期间有人保存，恢复同样受 CAS 冲突保护。

每个项目最多 **1000 个已保存版本**。达到上限的新保存返回 `429 DRAFT_REVISION_LIMIT`，历史仍可读，已成功请求的同键重放仍可返回原版本。不会为了继续保存而静默清理历史。正文按完整快照存储，尚无版本差分、归档或组织级存储配额；需要后续生命周期方案才能调整此限制。

其他主要错误：请求超限为 `413 DRAFT_BODY_TOO_LARGE`，节点或版本输入超限为 `422 INVALID_DRAFT`，非法 JSON/未知字段为 `400 INVALID_JSON`，不存在的历史版本为 `404 DRAFT_REVISION_NOT_FOUND`。访问其他工作区或显式选中其他项目时返回 404，避免正文和版本信息越权泄露。

## 事务与数据结构

迁移 [003_drafts.sql](../../internal/studio/migrations/003_drafts.sql) 增加三张表：

| 表 | 数据与索引 |
| --- | --- |
| `studio_project_drafts` | 每项目当前版本与更新时间；主键 `project_id` |
| `studio_draft_revisions` | 不可变节点 JSON、节点数和数据库时间；主键 `(project_id, revision)`，用于版本倒序范围扫描 |
| `studio_draft_idempotency` | 项目内幂等键、请求哈希和原版本；主键 `(project_id, idempotency_key)` |

所有表通过外键绑定项目与工作区。写入先锁项目行，覆盖“第一次保存尚无草稿头”的竞争情况；查验旧幂等键后，再锁草稿头、比较版本并执行条件更新。新正文、新版本指针、幂等映射和 `draft.saved` 审计事件在同一事务内提交。任何一步失败都回滚，客户端不会看到只生成一半的保存记录。

审计只记录项目、操作者、版本号和节点数，不记录草稿标题或正文。历史表通过数据库触发器拒绝 UPDATE 和 DELETE；这不是抵抗数据库管理员篡改的密码学证明。应用仍采用[持久单写者部署](persistence-auth.md)，没有因此获得多实例高可用或跨区域恢复能力。

## 已执行验证

2026-11-13，本地 Go 1.26.11 与真实 PostgreSQL 测试通过：

- 两组普通测试：Unicode 字符数、节点 ID/重复/数量、未知字段、非法 JSON、UTF-8、NUL、1 MiB 请求上限和内存模式 501。
- 四组 PostgreSQL 测试：完整保存/历史/恢复/重启；不同账户和显式项目范围隔离；两个独立数据库连接的 CAS 竞争和同键并发；1000 版本上限与历史防改删；审计写入失败整次回滚。
- 40 个节点、每节点 5000 个四字节 Unicode 字符的有效请求通过，验证草稿专用请求上限与普通 API 的 64 KiB 上限分离。
- 同键重放在版本前进和进程重启后仍返回原版本；不会生成额外审计；审计 API 未出现正文。

上述验证是功能和隔离证据，不是大规模并发容量测试。前端与浏览器的联合验收在[批次集成记录](../changes/2026-11-13-013-batch-integration.md)独立记录；未完成的生产配额、备份、版本归档和协作能力继续按 [TODO](../../TODO.md)推进。
