# 140 · 本机发行制品交接与原数据读回

## 目的

V18-B 在独立 Windows 工作台上演练正常停旧、确认写锁交还、启候选及切回原制品。操作者可以用原密码登录，并从同一项目读回原图与原业务操作的收据。

## 范围

V18 的 H01、H04、H07、H09。本机 PostgreSQL 16、同一迁移集合、同一对私有参考与资产目录。入口的 TLS 封流与候选放行见 139，发行制品构建与指纹见 141，联合演练见 142。

## 实际文件

- `scripts/release/release-process.mjs` 提供发行进程启停、锁探测、账号及业务数据准备和读回。
- `scripts/release/release-child.py` 持有 Windows 原始进程句柄，在独占隐藏控制台中向已核验的原进程发送 Ctrl+C，读取真实退出码。
- `internal/studio/release_process_test.go` 仅在 `releasedrill` 构建下提供专属数据库、目录 owner、格式与写锁实测。
- `scripts/release/release-process.test.mjs` 验证连接作用域、迁移指纹、配置白名单、独立活进程的正常退出及相邻监听的隔离。

## 接口与数据影响

在 131 的 `withSource` 回调内，以 `pg.privateRoot`、原 `databaseURL`、`drillId` 与两份制品绝对路径创建 `createReleaseProcess`。第一次启动只允许 baseline。进程管理器为本次运行创建独立私有子目录；应用环境只传本机必需项、回环地址、演示供应方、账户鉴权、数据库、私有目录和独立随机交付密钥。两次构建的二进制身份须不同，连续迁移版本及 SQL 全集摘要须相同。每次启动前重新核对原制品的清单摘要。

每个探针先核对专属 PGDATA 的 postmaster 记录，再验证 PostgreSQL 自报目录、数据库及 `pitr_drill_identity`。第一次运行前必须是未建立工作台表的专属库。运行中，数据库 advisory 写锁和两把 OS 文件锁均须确实被占用。进程正常退出后在同一探针中取得并同时持有三把锁，核对持久 storage UUID、两个 owner 的角色和连续格式，随后释放。每次复起重验同一组锁与原 UUID。

`seed()` 通过正常 HTTP 注册随机账号、上传 PNG、以原键创建工作流定义。演练使用原件下载和原键 GET 收据检查 SHA-256、项目、资源 ID、版次与图定义。恢复后 `verify()` 再以原密码登录并读取同一数据。测试数据不触发生成、邮件、扣费或外部存储。

## 验证结果

- `node --test scripts/release/release-process.test.mjs` 的三个专项通过。Windows 实际子进程收到 Ctrl+C，写入本次随机退出标识后以 0 结束；另一个无关监听仍返回原响应。
- 两份实际本地制品经过 `verifyReleaseArtifact` 与 `assertReleasePair` 核验，二进制 SHA-256 不同，迁移版本均为 029，迁移全集摘要相同。
- `go test -tags=releasedrill ./internal/studio -run '^TestReleaseProcessOwnership$' -count=1 -v` 编译并在未传演练参数时按预期跳过。独立空 PG、真实目录锁交接及回退读证据由集成人串行运行 `node scripts/release/workbench-release-drill.mjs --previous <旧制品目录> --candidate <候选制品目录>`。真实执行结果记入 142。

集成人用两份已核验 029 制品串行运行真实 PostgreSQL 16 与 TLS 工作台演练。候选拒绝放行 2.061 秒、切回原制品 3.478 秒、候选通过准入 3.949 秒。三次均实际验证数据库控制写锁和两把私有目录 OS 锁释放，并分别读回原账号私图与定义原键收据。切回原制品时用原密码重新登录验证。时长为本次本机交接观测。

## 风险与限制

脚本针对本机 Windows 和原生 PostgreSQL 16，需要 Go 构建缓存、Python 3.11 以上和已登记的两个制品。本次演练以独立 build ID 和实际字节区分同格式制品。生产流量、异地或跨实例切换另行验收。身份、写锁、正常退出或候选检查无法确认时，应保持入口维护状态并保留原目录和日志。

## 回滚方式

保持维护状态，停止已确认归属的候选进程并验证三把锁，使用固定的旧制品和同一数据库、私有目录及交付密钥启动。确认原账户、私有原件及原键收据后再开放入口。迁移指纹有变化时须使用匹配时间点的数据库和私有文件联备独立恢复。

## 文档同步

由集成人同步 README、Wiki、台账索引、任务源及生成 TODO。139—142 共同记录 V18-B 封流、制品与真实进程交接验收。
