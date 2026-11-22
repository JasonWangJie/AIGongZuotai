# 216 跨数据库格式退回与升级窗口对账

## 目的

V18，H01/H04/H07/H09。验证值班手册中“跨格式回退”一节的本机执行路径。旧制品只接入从升级前物理联备恢复的独立数据库和两份匹配的私有目录。保留升级后的源库、WAL、私有目录与窗口内的原回执，供业务负责人决定如何处理新增写入。

## 范围

仅使用已冻结的 Windows amd64 036 与 037 工作台。两份标准程序各走正常 HTTP 接口，独立环回 TLS 入口可封流，PG、私有目录、账户、交付密钥和窗口操作键均为该轮专用。此例在本地文件存储模式保存一张参考图，不启用外部模型、邮件、对象商或资金结算。工作流定义的保存不执行模型。各库的运行、异步任务、冻结和账务 journal 数量必须实际为零。

## 实际文件

- `scripts/release/cross-format-drill.mjs` 绑定真实制品、运行升级及物理回退、验证应用读写与 TLS 限制、产出去敏结果。
- `scripts/release/cross-format-evidence.mjs` 固定制品、迁移及源代码摘要，约束私有树复制，核对三阶段业务数量。
- `scripts/release/cross-format-evidence.test.mjs` 覆盖制品错配、迁移前缀、窗口差额、私有字节复制与链接拒绝。
- `internal/studio/release_process_test.go` 复用原 140 测试专用所有权探针，允许明确指定本轮 source 或 restored，核对 postmaster 数据目录、端口、数据库标记、实际迁移序列和 037 特有表、数据库 UUID、两个原目录 owner 与三把锁。原调用默认使用 source 和编译时格式。
- `docs/changes/2026-11-22-216-cross-format-recovery.md` 记录制品来源、命令和验证边界。

## 接口与数据影响

先在 036 上通过 TLS 注册合成用户、上传 PNG 并保存一个工作流定义。旧服务退出、数据库和两把目录锁均释放之后，逐文件复制并核对私有根、owner、锁文件与交付密钥，制作物理基础备份和命名 WAL 恢复点。

037 从相同源库、原目录和原 UUID 正常启动，执行真实迁移。只读候选入口核验原会话、页面、私图和原回执后开放。窗口只接受两个已知业务动作，各自使用独立原键：修改既有定义至 v2，再创建另一个定义的 v1。封流后实际提交第三个测试操作，必须被入口拒绝。仅在认证只读模式核验之前两份收据存在、被拒操作没有收据，再停 037 并核验三锁。

将原数据库和原归档停机移离。由基础备份与独立归档 WAL 恢复至原命名点，核对恢复位置、独立系统标记和恢复点后的排除证据，将升级前私有联备另复制到新目录。036 仅接入该恢复库，仅按获准账户开放固定 GET：须读回原页面、私图 SHA、原 v1 原键回执。窗口内两原键及早前被封流的原键应返回工作流收据缺失；在受限入口另一次新写须被拒绝。最终保持维护，仅在专用演练退出时关闭入口。业务修订总数必须实际为 1 → 3 → 1。两项窗口业务仍待处置，不能自动补写，或把恢复点后已确认的记录视作未发生。

最后检查停机的升级后数据库控制文件和原私有树摘要未改变。临时 PG 工作目录由宿主为当前用户配置私有 ACL。其 `offline-source`、`offline-live-wal` 和 `current-platform` 保留升级后的一侧；`point-platform` 和 `restored-platform` 是升级前私有联备和其还原副本。`cross-format-review` 中只在该受限目录保存本轮合成登录、PG 访问和原回执核对资料。共享报告只输出制品摘要、LSN、业务数量、是否封流及耗时。

## 验证结果

准备工作在本机离线执行。使用原 Go 1.26.11 编译器按标准 `go build -trimpath -o <独立目标> ./cmd/server` 各重建一次，两个结果分别与原冻结 `.runtime/server.exe` 逐字节相同。实际启动命令指向这些原冻结二进制。两个格式共同的 36 个迁移文件名和内容一致，第 37 项为 `037_project_canvases.sql`。

| 来源 | 标准服务 SHA-256 | 已编译前端文件 | 迁移清单 SHA-256 |
| --- | --- | ---: | --- |
| `.runtime/v17f2-036-source` | `e0e161e4e08fa42a74ca4380dd2ba78a72cfe08d520a3f951a34d484a9336103` | 33 | `5128b1a48ce374699d85700542b573e44eac2ff726107b14eced499d99c0df19` |
| `.runtime/v17j-037-integrated-source` | `6fb7b00b8899f84e9acca769bc9201a250c47c308de1524e714a0378ecb20caf` | 55 | `8ae862445afbf7cc8f02dad3c7fc8dea9073082d6ce6661e1276a6852b79e1e9` |

`--verify-only` 对上述两份实物通过，同时核对生产 Go 和探针可能加载的 Go 测试源码、go.mod/go.sum、前端全树以及迁移表。离线将冻结候选和本批 probe overlay 以 `go test -c -tags=releasedrill` 成功编译。未提供检查阶段时探针按预期跳过，无数据库连接。四个 Node 定向测试通过。

合入后在仓库根运行下述命令。Node 22、Python 3、缓存完备的 Go 1.26.11、Windows PostgreSQL 16 的客户端/服务端工具及配套 basic_archive 应事先就绪。专用临时父目录须已存在并为本机 ASCII 路径，目录下每次新建独立的 `studio-pitr-*`。在隔离 stage 中可把下列脚本路径前缀改为 `.runtime/v18-cross-format-drill/scripts/release/`，其余两份源路径仍指仓库根的冻结副本。

```powershell
$toolchain216 = Join-Path (go env GOMODCACHE) 'golang.org/toolchain@v0.0.1-go1.26.11.windows-amd64/bin'
$env:PATH = "$toolchain216;$env:PATH"
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOTOOLCHAIN = 'local'
node --test scripts/release/cross-format-evidence.test.mjs
node scripts/release/cross-format-drill.mjs --verify-only --previous-source .runtime/v17f2-036-source --candidate-source .runtime/v17j-037-integrated-source
```

仅集成人在确认其他 PG 已退出后执行实际演练，选用本机专用的暂存父目录。示例以已有的 ASCII 临时目录为父。输出位置需是本轮新文件。

```powershell
$parent216 = (Resolve-Path $env:TEMP).Path
node scripts/release/cross-format-drill.mjs --run --previous-source .runtime/v17f2-036-source --candidate-source .runtime/v17j-037-integrated-source --stage-parent $parent216 --bin-dir 'C:/Program Files/PostgreSQL/16/bin'
```

准备者尚未运行 PostgreSQL、升级、恢复或完整进程交接。真实运行证据由集成人在本节追加。

交付前复核发现，最初验完整 dist 后，启动仍直接读取原构建目录。现将 036、037 的完整前端树分别复制到本轮 ACL 私有目录，逐文件核对副本并按原树摘要验收，再进入数据库启动阶段。三个工作台启动点均使用对应的私有副本，036 回退复用与首次启动相同的文件。单测保持首页字节相同，仅改写源 JS，确认已冻结目录仍读到原字节，再使用原摘要重新复制则被拒绝。

本机运行 `node --test scripts/release/cross-format-evidence.test.mjs`，五项通过；`node --check scripts/release/cross-format-drill.mjs` 通过。本次专项只涉及文件与制品校验，完整 PostgreSQL 演练由集成人串行执行。

## 风险与限制

此例计量两个已知写入的真实原收据与三阶段原库数量，面向单机、单写者和固定的短窗口。其他企业数据或外部订单、云对象版本、真实审核和跨可用区复制的清点需要相应环境的独立证据。探针会拒绝不同制品、被改动的绑定源、迁移前缀不符、目录/数据库身份不符、锁状态不符及未核清的备份字节。037 降级不在原数据库上执行，141/142 原有同格式交接门禁继续生效。

## 回滚方式

结束当前专用演练，保持维护，保全受限目录和新旧两侧原始证据。仅在已核对的演练实例中使用私有恢复材料。停止演练不会撤销新版库中原已受理的两个操作，不删除迁移记录、owner 或私图。

## 文档同步

集成人依据实际运行结果登记 README、值班 Wiki、任务进度与台账索引。本记录不改变 V18 真实多可用区的外部验收状态。

