# 141 · V18-B 双制品准备、身份和校验

## 目的

为本机回退演练构建可分别启动的当前版和候选版。通过实际 Go 二进制、React 静态文件的字节、数据库迁移指纹及独立保存的清单 SHA 确认访问到哪套制品。

## 范围

2026-11-22，关联 V18-B、H07、H09。制品角色固定为 `baseline`、`candidate`。可分别提供固定源码根和显式版本。本次二者来自同一份 029 业务源码，构建身份、可执行文件和前端入口各自独立。本机 TLS 放量、真实账户与 PostgreSQL 写者交接由同批其他逻辑变更验收。

## 实际文件

- [release-artifact.mjs](../../scripts/release/release-artifact.mjs) 提供 `build`、`verify`、`plan` 以及 `verifyReleaseArtifact`、`buildReleaseArtifact`、`prepareReleasePlan` 供演练调用。
- [release-artifact.test.mjs](../../scripts/release/release-artifact.test.mjs) 从真实源码分别离线编译当前版、候选版、相同身份复现版；在独立临时目录和不同环回端口从两套制品启动真实 Go 服务，以内存演示模式读取公开身份、主页、构建资源、就绪与 bootstrap，再退出进程。另覆盖文件变动、额外文件与目录链接等拒绝路径。

## 接口与数据影响

1. `build --source DIR --role baseline --version VERSION --out NEW_DIR` 在独立临时目录复制构建所需代码、内建演示审核样本和 SQL。使用锁文件离线安装开发依赖并编译 TypeScript 与 Vite，使用 `go.mod` 的本机缓存工具链、只读模块和 `-trimpath` 编译 `./cmd/server`。源码树和既有输出目录均获保留。在源码树内指定输出时，应使用现有 `.runtime` 下的全新目录。候选制品使用另一目录和 `--role candidate`。
2. 包含 `server.exe`（其他平台为 `server`）、`dist/`、`manifest.json`。清单记录文件相对路径、字节数、SHA-256，另含平台、架构、源码和连续迁移身份。用独占新目录准备成品，清单最后写入。构建过程采用环境白名单、空 npm 配置与离线缓存，不将运行环境文件、凭据、工作树、私有目录或数据库配置打包。
3. 角色、版本、源码和实际迁移指纹进入 Go build ID。已编译 HTML 写入角色/版本/源码元信息；静态 `/release-identity.json` 给出角色、版本、二进制和前端摘要、迁移数与摘要，以及制品 ID。迁移摘要按 001 至 N 的顺序累积文件名、NUL、原字节、NUL；清单格式 1 和数据库格式各自独立。
4. `verify --artifact DIR --role ROLE --version VERSION --sha256 TRUSTED_SHA` 对照外部保存的清单摘要，重新检查包与每个文件、普通文件身份、实际 Go 主程序路径和 build ID。禁止链接、硬链接、跨根路径、额外文件和未知清单字段。未提供外部 SHA 时只进行包内一致性校验。
5. `plan --artifact DIR --state STATE_PARENT --port PORT` 对已验制品生成 JSON，给出环回地址、只读静态根及对应角色的独立运行/素材/参考目录。参数准备不创建 Windows 服务、修改路由或启动数据库。演练负责人另行提供限定的临时身份和存储配置。

## 验证结果

- `node --test scripts/release/release-artifact.test.mjs` 从零实际构建三次并启动两套独立内存 Go 服务，8 项通过（含 7 个子例），75.594 秒。提供已构建的 `TEST_RELEASE_BASELINE_DIR` 与 `TEST_RELEASE_CANDIDATE_DIR` 后重复测试，8 项通过，35.174 秒。验证私有状态分离、实际前端资源、角色/版本错配、SHA 固定、出界与链接、未知字段及二进制/静态资源核对。
- 本机 Windows/amd64、Node 22.22.2、Go 1.26.11。两套制品均为 029，迁移指纹 `319905e6e6902a1a2a655667be7fee20da5d01f810a28105799aff283e807bc8`，源码指纹 `af9d30877c120287bd0e3b4e5a50b9bddf468e8752d9933e4347e369eba6f837`。基线 `v0.20.029` 清单 SHA 为 `6b7bb7600c25939074945e0845bdb3da0410308520d55630b541fdd4c437b252`，候选 `v0.20.029-candidate` 为 `dabaad12955597599f0600bfaa76ff65251b1e9722c07b3e6addd0858134230c`。独立再次构建基线后清单、服务器及全部前端文件 SHA 与原基线一致。输出目录均在忽略的 `.runtime/release-artifact-141/`。

## 风险与限制

复现需要同一源码、平台架构、Node/npm、Go 工具链及已填充的锁定依赖缓存。SHA 可用于与独立可信来源核对；生产组织的签名密钥和发布审批须另行部署。当前两个版本的业务源码一致，本次验证区分了真实构建和运行实例。公开生产环境、真实渠道和跨可用区容灾均未执行。

## 回滚方式

保留两个原制品及各自可信摘要，核对实际数据库格式与对应私有存储后，按同批本机交接流程验证回退。重新准备制品使用新输出目录，避免覆盖已有可回退文件。文件校验不会更改数据或恢复旧业务版本。

## 文档同步

集成人维护[根 README](../../README.md)、[Wiki](../wiki/README.md)、[变更索引](README.md)、[任务源](../planning/tasks.json)、生成 TODO 及[进度](../planning/progress.md)。真实 TLS、账户灰度、受理与切回结果由同批集成台账分别记录。
