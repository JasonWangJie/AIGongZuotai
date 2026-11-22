# 215 固定 037 工作台的依赖、压缩资源与安全验收记录

## 目的

V22-C/D 补充 037 制品的离线依赖与运行证据。使用集成试具实际执行的服务文件，核实冻结源码能否产生相同服务与前端，并识别各次升级、恢复和权限验证对应的版本。

## 范围

H07、H09。输入为 `.runtime/v17j-037-integrated-source`、其中的 `.runtime/server-207.exe` 和 `apps/workbench/dist`，在独立工作目录用本机 Go 模块缓存及已安装 npm 包复验。全程关闭模块下载，使用本地 SPDX Schema 和已归档的试具结果。

## 实际文件

- `scripts/release/verify-workbench-sbom.py` 独立核对标准 Schema、SPDX 包和关系、Go 输入和实际制品、npm 归档与安装字节、gzip 与原 JS。可用 Python 3 和已安装的 jsonschema 运行。
- `scripts/release/test_verify_workbench_sbom.py` 验证合法/变动的 gzip、安装内容及归档的处置。
- `docs/wiki/assets/workbench-037.spdx.json`、`docs/wiki/assets/workbench-037-verification.json` 保存固定清单及独立校验摘要。
- `docs/wiki/release-evidence-037.md` 区分同一服务和前端的实际试验、先前 037 执行的具名结果与整轮状态。

## 接口与数据影响

原 `release-sbom.mjs` 负责离线重建和生成 SPDX 2.3 清单。复核工具从清单两个被描述的制品出发，校验成员唯一性、完整 SHA-256、每个依赖关系的两端及其来源。工具另外计算冻结源码中 Go 实际选择的本项目编译输入、Web 源码和配置的摘要。制品和许可证核对不访问账户、数据库或供应商。

## 验证结果

Go 1.26.11 按原 `-trimpath` 和 `CGO_ENABLED=0` 从冻结源码重建，服务逐字节相同。原清单生成器用 Vite 重现全部 55 个文件。独立校验通过 SPDX 官方 2.3 Draft 7 Schema、45 个包和 47 条关系，逐一核对 20 个 JS 与各自 gzip 原文、20 个编译记录内 Go 模块、22 个 npm 生产包带完整性校验的归档及 434 个安装文件。原件、重建产物、Go 与前端输入完整摘要见[独立结果](../wiki/assets/workbench-037-verification.json)。

纯 Python 测试两项通过。另对当前真实 dist 的私有副本仅替换一个 gzip，生成器明确拒绝且未写出清单；对清单副本修改一个关系终点，独立复核拒绝。首次负例驱动读取子进程异常时因 Windows 默认 GBK 解码 UTF-8 路径失败；使用原始字节匹配后，两项真实负例均通过。已出具的 SPDX 内容字节摘要为 `36ff3bce95892d7d6dc1a403a2e5dc127b70c61d28702f3b9b0edcb48840e5aa`。重新生成时创建时间与文档 UUID 独立产生。

以下命令在仓库根执行。`$source` 指定冻结源码的独立副本，需包含该版本的服务源码、迁移文件、Web 源码与已经安装的本地依赖。`$fresh` 指向新建空目录。Go 使用本机已缓存的版本，官方 Schema 和 npm 归档来自原机离线缓存。生成及核对均无需运行服务。

```powershell
$source = (Resolve-Path '.runtime/v17j-037-integrated-source').Path
$fresh = (New-Item -ItemType Directory -Path (Join-Path '.runtime' ('verify-215-' + [guid]::NewGuid().ToString('N')))).FullName
$server = Join-Path $source '.runtime/server-207.exe'
$dist = Join-Path $source 'apps/workbench/dist'
$schema = (Resolve-Path '.runtime/release-spdx-2.3-schema.json').Path
$verify = (Resolve-Path 'scripts/release/verify-workbench-sbom.py').Path
$env:GOTOOLCHAIN = 'local'
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:npm_config_offline = 'true'
$env:CGO_ENABLED = '0'
$tool = Join-Path (go env GOMODCACHE) 'golang.org/toolchain@v0.0.1-go1.26.11.windows-amd64/bin'
$env:PATH = $tool + ';' + $env:PATH
$cache = Join-Path $env:LOCALAPPDATA 'npm-cache/_cacache/content-v2'
Push-Location $source
try {
    go build -trimpath -o "$fresh/rebuilt-server.exe" ./cmd/server
    if ($LASTEXITCODE) { throw 'server rebuild failed' }
    node apps/workbench/scripts/release-sbom.mjs --server $server --dist $dist --output "$fresh/workbench.spdx.json"
    if ($LASTEXITCODE) { throw 'inventory failed' }
    python -B $verify --root $source --server $server --rebuild "$fresh/rebuilt-server.exe" --dist $dist --sbom "$fresh/workbench.spdx.json" --schema $schema --npm-cache $cache --go "$tool/go.exe"
    if ($LASTEXITCODE) { throw 'verification failed' }
} finally { Pop-Location }
python -B scripts/release/test_verify_workbench_sbom.py
```

## 风险与限制

本次逐字节重建和短程 PG 重开的结果绑定同一对 SHA；本项没有启动 PostgreSQL。以往 037 累计日志的升级与联备具名结果对应其原轮次，日志有一个持续回复失败。此版本的完整 PG 回归、真实供应渠道升级、外部组件制品及分发许可须分别核实。[运行身份与原证据](../wiki/release-evidence-037.md)

## 回滚方式

归档历史清单与核对结果，保留先前 034 和 035 记录。此专项可单独撤回，不影响线上数据库、运行时、制品构建方式或准入判断。

## 文档同步

V22-C/D 增补本机证据；整批继续等待原先定义的渠道、容量、灾备、规则及受控发布条件。总 README、Wiki 导航、任务源、进度和台账索引由集成人更新。[037 核对页](../wiki/release-evidence-037.md)
