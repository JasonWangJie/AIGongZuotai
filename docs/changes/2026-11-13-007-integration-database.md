# 007 · PostgreSQL 开发环境与第二阶段集成

## 目的

将账户、项目、持久任务和真实媒体适配器整合为可复现的本地开发闭环，提供独立数据库验证环境，并更新已实现范围。

## 范围

Windows 便携 PostgreSQL 初始化/启停、数据库开发与测试脚本、隔离冒烟、CI 数据库服务，以及 README、Wiki、规则与台账导航同步。模块实现分别记入 004、005、006。

## 实际文件

- `scripts/postgres-dev.ps1`、`scripts/lib/process.mjs`、`scripts/dev.mjs`、`scripts/run.mjs`、`scripts/smoke.mjs`
- `package.json`、`.env.example`、`.github/workflows/ci.yml`、`go.mod`（最低 Go 补丁版本）
- `README.md`、`AGENTS.md`、`docs/changes/README.md`
- `docs/wiki/README.md`、`docs/wiki/development.md`、`docs/wiki/local-database.md`、`docs/wiki/architecture.md`、`docs/wiki/roadmap.md`、`docs/wiki/decisions/0002-durable-single-writer.md`
- `docs/wiki/assets/login-phase2.jpg`、`mobile-phase2.jpg`、`workbench-phase2.jpg`：本地浏览器验收截图，不含真实账户凭据。
- `docs/api/openapi.yaml`：集成核对后修正持久幂等、真实进度及公开演示素材的安全声明。

## 接口与数据影响

新增 `db:setup/start/stop/status`、`dev:db`、`test:db` 命令。本地初始化仅创建仓库专属数据库目录，凭据随机生成并存放于 Git 忽略文件，不覆盖或删除已有数据。冒烟强制关闭认证和 PostgreSQL，避免继承当前开发数据库配置。

## 验证结果

PostgreSQL 18.7 Windows 包和 CI 镜像版本已对照 PostgreSQL 官方下载入口及 Docker 官方镜像清单确认。第一次在中文仓库路径初始化遇到 UTF-8 字节错误；修正为 LocalAppData 下按工作区哈希划分的英文目录，并只提取服务器文件后，`db:setup` 成功初始化、启动并创建开发/测试数据库。`db:status` 和重复 `db:start` 通过，后者复用已有数据与进程。

Go 最低版本从 1.26.5 更新为 1.26.11，对照 [Go 官方发布说明](https://go.dev/doc/devel/release)中的 2026-11-10 补丁。默认工具链下载曾因本机访问 proxy.golang.org 超时失败；在单次命令使用镜像下载并经 Go 工具链校验后，`go version` 确认 1.26.11。没有改全局代理或关闭校验。独立的官方 ZIP 下载在工具链已可用后停止，没有作为测试环境使用。

第一轮 `npm run check`（Go vet、Go单测、前端34项、严格TS、构建、文档检查）、`npm run test:db`（真实PG，5.581秒）、`npm run smoke`、前端生产依赖 `npm audit --omit=dev` 均通过。前端构建 JS282.44kB/gzip89.46kB、CSS44.39kB/gzip10.14kB；这些是文件体积而不是浏览器运行性能。

真实 Go + PostgreSQL + required 账户服务的前端 HTTP 集成通过。浏览器已验证桌面登录、创建项目、图片任务、LLM流、跨项目不同历史、手机390×844任务卡片/导航/登录页、退出与重新登录。发现刷新默认项目问题后补齐按账户保存项目偏好，并确认刷新恢复正确项目。

最终恢复序号修正后，根 `npm run check` 再次通过，`npm run test:db` 5.717秒通过。Go共31个Test函数及其子用例，包含上限事件序号、静默失锁、已知handle核对、无handle禁止重提与私有资产跨账户拒绝。实际结束本轮创建的Go/Vite进程并以最新代码重新启动后，浏览器仍保持登录，恢复“月光品牌计划”的原任务ID、版本和对话输出。最终桌面截图已保存，控制台warning/error列表为空，手机viewport已恢复。

最后独立契约复核发现3处OpenAPI旧描述并修正：PG幂等不再标为仅当前进程；live进度来自上游；公开demo素材不要求会话。使用现有Python+PyYAML实际解析，18路径、21操作、60个本地引用全部有效、operationId无重复；不是全面OpenAPI规范认证。发现旧历史结果按当前Provider模式标记的问题后，前端改为逐记录provider_mode优先，缺失回退；修正后36项测试与构建通过，最终JS282.67kB/gzip89.54kB、CSS44.53kB/gzip10.16kB。

## 风险与限制

本地 PostgreSQL 启停与工作台独立；关闭工作台不停止数据库。便携包首次下载和解压较大。当前不是生产集群，真实付费 AI、支付商户、对象存储及目标容量验证不由这些脚本自动完成。

## 回滚方式

停止本命令创建的工作台进程，执行 `npm run db:stop` 正常停止数据库。保留 LocalAppData 下的实例数据目录和 `.runtime/postgres.env` 凭据；切回无数据库 demo 配置即可继续 UI 开发。不要通过删除数据目录回滚应用迁移。

## 文档同步

README 更新启动命令、阶段状态和最近变更；Wiki 新增本地数据库说明，并同步架构/开发/路线/导航；台账索引关联 004—007，历史 001—003 保持原始验收记录。
