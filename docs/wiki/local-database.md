# 本地数据库与隔离验证

本地开发可以在无数据库演示模式与 PostgreSQL 账户模式之间选择。数据库模式的状态持久化和故障恢复边界见[持久化与账户](persistence-auth.md)。本地数据库工具不会安装 Windows 服务，也不会修改系统 PostgreSQL；数据位于 `%LOCALAPPDATA%/AIWorkbenchPostgres/<工作区路径哈希>/pgdata`。独立英文路径避免 Windows PostgreSQL 工具在中文仓库路径下初始化时出现 UTF-8 编码错误。

## Windows 便携开发环境

```powershell
npm run db:setup
npm run dev:db
```

`db:setup` 从 [PostgreSQL 官方 Windows 下载入口](https://www.postgresql.org/download/windows/)所链接的 EDB 分发站下载 PostgreSQL 18.7 x64 便携包，仅解压服务器必需的 bin/lib/share，初始化并启动监听 `127.0.0.1:55432` 的数据库。初次下载约 390MB；下载包缓存于仓库 `.runtime/`。初始化使用随机密码与 SCRAM 认证，生成 `aigongzuotai_dev` 和 `aigongzuotai_test` 两个数据库。凭据只保存在 Git 忽略的 `.runtime/postgres.env`，不打印密码。

已有实例目录下的 `pgdata/PG_VERSION` 时复用数据，不重新初始化。目录存在但初始化未完成会明确停止，由开发者先检查；脚本没有删除数据库的操作。数据库进程的启动与工作台独立，关闭工作台不会关闭数据库。可设置 `POSTGRES_DEV_ROOT` 自定义英文根目录，已有实例启停时需保持该值一致。

```powershell
npm run db:status
npm run db:stop
npm run db:start
```

`dev:db` 先读取根 `.env`，再补充 `.runtime/postgres.env` 中尚未设置的变量，并强制 `AUTH_MODE=required`。因此显式环境变量和根 `.env` 的数据库地址优先；不要用生产数据库地址进行本地试验。普通 `npm run dev` 只读取根 `.env`。

## 已有 PostgreSQL / Linux / macOS

自行创建应用数据库及独立测试数据库，在根 `.env` 中配置 `DATABASE_URL` 与 `AUTH_MODE=required`，随后运行 `npm run dev:db`。应用需要运行迁移和业务写入的权限。测试使用另一个 `TEST_DATABASE_URL`，不要指向生产或开发业务库。

本地示例使用 `sslmode=disable` 仅因为监听回环地址；远程数据库应按环境配置证书验证。`.env`、`.runtime`、数据库目录、日志、生成素材和凭据均不得提交。

## 验证分层

| 命令 | 验证对象 |
| --- | --- |
| `npm run check` | Go vet、默认单元测试、前端测试、构建与文档检查；未设置测试库时数据库专项可能跳过 |
| `npm run test:db` | 加载独立测试库地址并重跑 Go 测试；缺少 `TEST_DATABASE_URL` 会失败 |
| `npm run smoke` | 启动随机端口、强制 demo + 内存 + 关闭认证的隔离进程；验证构建产物和 API 主链路 |

CI 声明 PostgreSQL 18.7 服务容器，设置仅用于 CI 的固定测试凭据，并在 Go 测试和 race 检查中运行数据库用例。CI 的配置存在不代表远程 CI 已执行；实际验证证据写入[本轮集成台账](../changes/2026-11-13-007-integration-database.md)。

## 排障与数据保留

- 端口 55432 被占用时先核对是否已有本仓库 PostgreSQL 进程；不要结束未知进程。
- `.runtime/postgres.env` 丢失时先恢复凭据，脚本不会重建数据库或尝试覆盖已有数据。
- Windows 防病毒扫描可能使解压较慢；观察正在运行的初始化命令，避免重复下载或并发初始化同一目录。
- `Ctrl+C` 关闭 `dev:db` 启动的 Go/Vite 子进程；数据库另用 `db:stop` 正常关闭。
- 本工具面向本地研发，不包含生产备份、PITR、主从切换、连接池容量规划或托管数据库运维。
