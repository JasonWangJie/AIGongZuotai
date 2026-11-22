# 156 · 两账户站内邀请、项目草稿共编与恢复

## 目的

让项目所有者邀请指定账户，以只读或编辑角色在同一份持久草稿协作，同时保护两人的私人项目及账户。

## 范围

V21-A1 落在 H01、H02、H07、H08、H10。共享数据限于授权项目的节点草稿及其修订，复用 V03 的 CAS 和 V09 的事务 Outbox。站内接受与用户自有会话不依赖实际发邮件。

## 实际文件

角色与关系的实现见 [153](2026-11-22-153-project-collaboration-members.md)，项目草稿及成员原键见 [154](2026-11-22-154-shared-project-draft.md)，工作台与浏览器见 [155](2026-11-22-155-project-collaboration-workbench.md)。

- `internal/studio/http.go` 接入项目协作接口。`internal/studio/collaboration_integration_test.go` 建立不同工作区的真实账户、宿主兄弟项目以及原始私有草稿，检验浏览器工作流和成套备份恢复。
- `internal/studio/storage_maintenance.go`、`operations_admin.go`、`ledger_admin.go`、`storage_maintenance_delivery_test.go` 将维护栅栏升至 032。
- `docs/api/openapi.yaml`、`docs/wiki/database-migrations.md`、`docs/wiki/decisions/0032-project-collaboration.md` 说明 13 条路径、14 个操作与项目数据边界。

## 接口与数据影响

032 新增三张表。个人登录与默认工作区保持本人的身份。共享端点由服务端在事务内从目标项目取得宿主并重核当前账户、会话和成员角色。保存与恢复使用同一 003 草稿。邀请/授权收据与成员草稿原键分别持久化，当前权限检查覆盖历史和原键读取。

## 验证结果

修正两项夹具的等待边界后，032 完整 PostgreSQL 累计回归通过，工作台 1639.820 秒，编解码包 2.652 秒，无失败或跳过。

41 个前端测试文件共 471 项通过，含本批 13 项。TypeScript、Vite 的 146 模块构建通过。集成人在冻结 032 串行运行 14 项真实 PG / 协议专项，58.729 秒通过。其中 Chromium 两个真实账户及三个既有会话 14.95 秒，旧原键与角色的联备恢复 7.20 秒。覆盖 031→032、显式过期、读写竞争、不明确认、满额释放和新轮邀请。另跑 `npm run check` 及六套冒烟通过。[真实冲突界面](../wiki/assets/collaboration-conflict-2026-11-22.png)

## 风险与限制

未保存的协作草稿停留在当前页面。站内邀请需要对方有现有账号，实际邮件投递继续在 V11 验收。实时光标和同时逐字编辑独立于本批的版本冲突处理。完整系统仍为单控制写者。

## 回滚方式

先停止写入并保留 032 数据库、私有文件、对象版本及启用的密钥。需要恢复 031 时，使用升级前一致备份及原程序，核对备份点以后的草稿和角色变化。不能仅删除成员表或迁移登记。

## 文档同步

根 README、Wiki 导航、迁移与架构说明、OpenAPI、变更台账、任务源、生成 TODO 与进度记录同步实际操作和验收证据。
