# 199 启动 JavaScript 的实际压缩传输

## 目的

V17-H 的初始工作台 JavaScript 压缩传输预算为 300 KiB。035 的初始 JS 离线 gzip 后为 160,040 字节，原 Go 服务收到 gzip 和 identity 请求时都发送了 502,972 字节。本次让构建产物和 Go 的正常静态入口支持浏览器协商的 gzip 表示。

## 范围

仅处理 Vite 生成的公开 `/assets/<name>-<hash>.js`。构建时一次生成对应 gzip 文件，仍保留原文。HTML、CSS、认证接口、SSE、私有缩略图及媒体下载继续走原入口。构建及服务均使用现有依赖。

## 实际文件

- `apps/workbench/vite.config.ts` 在生产构建的后置钩子中生成指纹 JS 的 gzip 表示。
- `apps/workbench/scripts/verify-static-encoding.mjs` 将每个 gzip 实际解压并与 JS 比较，按现有 SBOM 流程在内存中重建一次，并核对两种表示的完整字节。
- `internal/studio/http.go`、`internal/studio/static_encoding.go` 在原有公开目录和文件校验之后处理协商。`internal/studio/static_encoding_test.go` 使用真实 HTTP 连接验证。
- `scripts/release/release-artifact.mjs` 将生成的 JS gzip 文件纳入制品清单、身份和验证，并在源码包含 030 以来的 `internal/videoexport` 时纳入 Go 制品快照。

## 接口与数据影响

GET 和 HEAD 按 `Accept-Encoding` 返回 gzip 或 identity，沿用 `public, max-age=3600`，增加 `Vary: Accept-Encoding`、分别对应两种表示的弱 ETag，以及实际编码字节数。支持多行、多个编码、质量权重、通配符和显式禁用。Range 使用 identity 原件；客户端排除可用表示时返回 406。旧 dist 无 gzip 文件时仍可提供 identity。304 按所选表示复核，直接请求 gzip 文件返回 404。无数据库迁移及业务操作。

## 验证结果

- 在独立目录 `.runtime/v17-static-encoding/apps/workbench` 对复制时为 **036 级、161 模块** 的工作台执行 `npm run build`、`node scripts/verify-static-encoding.mjs`。19 个 JS 的 gzip 全部解压与最终原文一致，第二次内存构建所得的每种表示逐字节一致。该源的唯一启动 JS 原文 515,866 字节、gzip 164,830 字节。Vite 保留既有原始 chunk 超 500 kB 的提示。
- Go 定向 `go test -count=1 -run 'TestFrontend(JavaScriptNegotiation|FallbackDoesNotServeSecretsOrUnknownAPI)$' ./internal/studio -v` 以 overlay 使用现有集成源，两项用例通过，末次 0.884 秒；`go vet -overlay <overlay.json> ./internal/studio` 通过。实际 HTTP 检查多编码与权重、缺少 gzip、GET/HEAD、304/412、Range、不可接受返回 406、缓存和 API/非 JS 资源。
- 单独快照包含画布代码与 037 迁移，运行 `node scripts/release/release-artifact.mjs build --source <受控源码> --out <新目录> --role candidate --version static199`，离线构建及清单核验通过。制品含 20 个 JS gzip 文件。自有环回 memory/demo 进程按原制品身份启动；实际入口原文 517,225 字节、gzip 165,208 字节。解压后与制品原件逐字节相同，原 ETag 回读返回 304。私有演练目录首次未准备时启动计划在拉起服务前拒绝；建立该专属空目录后完成测量并退出。

## 风险与限制

上列数据分别对应两个明确的源码快照。最终集成制品、受限网络的真实传输与冷暖缓存结果由 200 另验。发布代理或 CDN 可以改变编码或缓存；对应放行需在实际部署入口重新测量。构建目录应整套部署，保留原文与同一制品清单中的压缩文件。未调用 PostgreSQL、实际渠道或生产环境。

## 回滚方式

切回先前兼容的服务与完整 dist；旧版 dist 无 gzip 文件时会发送原文。数据库和用户资产无需更改。对不同数据库格式的服务继续执行原维护与恢复门禁。

## 文档同步

集成人在 README、性能 Wiki、台账索引和 V17 任务里分别登记本机压缩传输、最终受限网络测量与待验外部条件。已有 035 直连原文及离线 gzip 数据保留，便于在固定版本间比较。
