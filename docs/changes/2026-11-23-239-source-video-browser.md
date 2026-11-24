# 239 · V20-C Chromium 视频工作室选原片、报价与丢回执

## 目的

从真实登录页面核对两种原视频操作、用户确认与丢回应后的读取行为。

## 范围

V20-C 的普通视频工作室浏览器验收，依赖 234 的原片适配、039 的授权和结算、236 的 React 操作，以及 238 的 MP4 与本机 HTTP 接收试具。本项新增浏览器和 Go 验收程序。

## 实际文件

- `apps/workbench/scripts/source-video-browser.mjs` 在受限本机 Chromium 会话登录，从“视频工作室”分别选择“编辑原视频”和“延长原视频”，两次都显式选择已审核的原片修订，延长选择“向后延长”。页面核对各自的报价、原片修订和方向后点击“确认额度并提交”。
- `internal/studio/source_video_browser_test.go` 创建红色带音轨的原片，监听真实应用和本机供应商的 HTTP 流量。浏览器刚看到报价时，任务提交数须分别为 0 和 1。普通任务接口已完成延长受理后由 HTTP 服务端关闭该 TCP 连接，页面须出现“受理状态待确认”，禁用提交。点击“查询原任务收据”之前收据请求数为 0，点击后须为同项目、同原键的一次 GET。两个业务请求各只允许一次 POST。
- `docs/changes/2026-11-23-239-source-video-browser.md` 记录执行条件与结果边界。

本机供应商逐字节核对收到的红色原片与操作字段。编辑输出为蓝色、延长输出为绿色，三个 MP4 均经 FFmpeg 实际解码。应用侧核对审核、授权下载、冻结的原片修订与内部 CREDIT 的 200000、350000 微单位结算。可选 JSON 只保留界面标签与总数，没有任务、操作键、项目、账号、正文、URL 或视频内容。浏览器数据使用本机临时配置。测试代理的计数路由仅在试具存在，且只返回计数。

## 接口与数据影响

沿用普通任务、报价及原项目收据接口，试具只在本机环回监听器模拟回应中断。

## 验证结果

- `node --check apps/workbench/scripts/source-video-browser.mjs` 通过。Go 1.26.11 清空测试数据库配置和浏览器开关，`go test ./internal/studio -run '^TestPostgresSourceVideoBrowser039$' -count=1 -v` 编译通过，明确跳过真实浏览器用例。
- 集成人先构建包含 236 的 workbench/dist，配置隔离 `TEST_DATABASE_URL`、`TEST_VIDEO_FFMPEG`、`SOURCE_VIDEO_BROWSER_TEST=1` 和可用的本机 Chromium（可选 `WORKFLOW_CHROME`），运行 `go test ./internal/studio -run '^TestPostgresSourceVideoBrowser039$' -count=1 -v -timeout 8m`。如需保留脱敏结果，`SOURCE_VIDEO_BROWSER_REPORT` 指向其父目录已存在的绝对文件路径。真实 PG 和 Chromium 结果由集成人串行执行后登记。

## 风险与限制

初次串行 Chromium 日志停在登录段，实际导航按钮带有“03”编号，原先精确匹配“视频工作室”没有命中。放宽导航匹配后，一次停在编辑报价的大段等待；拆为固定检查点后，又在延长的报价显示阶段记录到两份报价、一次任务提交。第一条创作交付带来的作品库事件会清空下一次创作仍有效的原片和报价；236 已修复为鉴权复核当前修订。普通浏览器脚本每项只选择一次，等待时只读页面。

此前的完整断线试验在延长受理后、不写任何响应字节即关闭复用连接，真实边缘统计为 2 次报价、3 次任务 POST、0 次读取收据，重复请求触发旧断言。普通档现保留真实 202 响应头并截断正文，检验 UI 的受理不明与原键查询。另行记录完整丢响应头时的浏览器传输行为及幂等结果。

独立 Windows Chromium 以本机 HTTP 样本跑完整视频工作室，延长报价期间发送库事件并推迟报价响应，两项操作各只点击一次确认。共 2 次报价、2 次提交、1 次收据 GET、2 次原片详情 GET，两个交付卡均通过核对。PG 实际投递与账务另按串行测试的结果登记。

此处核对本机同源浏览器、真实 HTTP 适配器、私有交付和内部权益。正式供应商与真实资金渠道仍需对应环境。

## 回滚方式

移除上述新增验收文件即可回退；保留 238 的普通 API 和联备测试。

## 文档同步

README、Wiki、任务源、进度和台账索引由集成人同步。

## 无响应头断线与浏览器传输重放

另外两个试具文件为 `internal/studio/source_video_transport_test.go`、`apps/workbench/scripts/source-video-transport-browser.mjs`。普通工作台中只显式确认一次延长报价。CDP 在应用加载前安装页面级 fetch 计数器，向原生 fetch 透传调用方、全部参数和原 Promise，分别统计提交与读收据。服务端在正常受理成功后关闭第一次 POST 的实际连接，不向浏览器写入响应头或正文。

若浏览器自行补发，第二次 HTTP POST 也走正常鉴权及受理入口，核对原始字节、操作键、任务、冻结记录和 `Idempotency-Replayed: true`。若浏览器交付网络错误，则在工作台点击一次原收据 GET。两种分支均独立使用原账户、项目和原键读取已交付结果，核对下载字节、真实上游延长请求、SQL 来源及唯一 SETTLED 冻结。公开报告分别列出页面 POST 调用数、实际 HTTP POST 数、重放响应数、界面与独立核对的收据 GET、延长上游 POST 及结算金额。

`node --check apps/workbench/scripts/source-video-transport-browser.mjs` 通过。Go 1.26.11 隔离编译后用例默认跳过。正式串行入口要求同源构建的 `apps/workbench/dist`、隔离的环回 `TEST_DATABASE_URL`、`TEST_VIDEO_FFMPEG` 绝对路径及可用的 Chromium；设置 `SOURCE_VIDEO_TRANSPORT_BROWSER_TEST=1` 后执行 `go test -p 1 -parallel 1 -count=1 -timeout 8m -v -run '^TestPostgresSourceVideoTransportBrowser039$' ./internal/studio`。可将 `SOURCE_VIDEO_TRANSPORT_REPORT` 指向新的绝对报告路径。实际落在哪条分支须依据运行结果登记。

## 集成人串行验收

在 PostgreSQL 16.14、当前生产前端构建和真实 Chromium 上，运行 `go test -p 1 -parallel 1 -count=1 -timeout=11m -run '^TestPostgresSourceVideo(Browser039|TransportBrowser039)$' -v ./internal/studio`，两项均通过。此前一次测试发现素材库事件会清空无关的延长报价，另一次只允许一条 HTTP 请求的试具拦下了浏览器重送；这两轮均未作为通过证据。

- 常规操作用例 17.82 秒。两次报价、两次任务 POST，两个产物已交付。延长时截断已开始的响应正文，用户按原键查询一次，两个操作合计结算 0.55 CREDIT。[工作室计数](../wiki/assets/source-video-workbench-039.json)
- 无响应头用例 12.96 秒。页面调用一次原生 fetch，浏览器实际发送两条相同的任务 POST。第二次返回既有任务与冻结记录。本机上游实际收到一条延长请求，数据库有一条任务和一条已结算记录，金额 0.35 CREDIT。独立回读同一收据成功。[传输计数](../wiki/assets/source-video-transport-039.json)
