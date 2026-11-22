# 226 工作台四语与 IP 地区识别

## 目的

首次访问按地区展示工作台，并让访客在登录前后选择自己习惯的语言。

## 范围

V23，H08、H09。依赖 V00。涵盖四语界面、地区判断、手动选择与浏览器端验证。

## 实际文件

- 服务端配置与协议：`.env.example`、`cmd/server/main.go`、`internal/studio/{app.go,http.go,catalog.go,locale_http_test.go}`、`docs/api/openapi.yaml`。
- 地区识别与数据：`internal/localeip/{locale.go,locale_test.go,ipv4.bin,ipv6.bin,refresh.py,SOURCES.txt}`，包括快照来源、许可证和发布文件 SHA-256。
- 前端入口与词典：`apps/workbench/index.html`、`apps/workbench/src/{main.tsx,App.tsx,locale.ts,language.css,LanguagePicker.tsx,serverDisplay.ts,locales/}`。
- 界面接入覆盖 `apps/workbench/src/` 下的账户/找回、聊天和媒体创作、参考图、素材、任务、项目草稿/画布、协作、分镜、视频剪辑、演示、工作流、订单、账务、工单、申诉与运营组件，以及这些组件的标签、日期、验证与请求状态。原 API 和审阅读取入口分别沿用 `api.ts`、`useStudio.ts`、`types.ts`、`RequestDetails.tsx` 等，服务返回的用户正文不进入词典。画布冲突处理修改 `useFreeCanvas.ts`、`FreeCanvasWorkbench.tsx`，保留首存基准。
- 验证文件：`apps/workbench/src/{locale.test.ts,authLocale.test.tsx,serverDisplay.test.ts,freeCanvasWorkbench.test.tsx}`、`apps/workbench/scripts/{locale-browser-smoke.mjs,local-browser.mjs}`。旧中文浏览器用例显式设置简体偏好，新地区用例从全新浏览器上下文开始。

## 接口与数据影响

首次访问公开只读接口 `GET /api/locale`。响应只有语言代码。手动选择保存在当前浏览器本站的 `xingtu.language.v1`，自动模式重新按连接 IP 检测。内置模型描述带有可选的 `builtin_description` 显示标识，便于同时识别 bootstrap 与目录刷新响应；原模型版本和任务快照沿用原身份。地区查询和偏好存储无需数据库迁移。

## 验证结果

| 命令或检查 | 本机结果 |
| --- | --- |
| `go test -p 1 -count=1 ./...` | 通过。子进程清除真实渠道配置和数据库测试连接，PG 专项按其条件跳过。另执行 `go test ./internal/localeip ./internal/studio ./cmd/server -run '^(TestLocale\|TestCatalog)' -count=1` 通过。内置库保留 19,286 段 IPv4、14,951 段 IPv6。 |
| `npm --prefix apps/workbench test -- --maxWorkers=3` | 56 个文件、606 项全部通过。三个语言包逐键验证相同的插值参数；受限存储、检测失败、迟到响应和手动偏好有回归。既有错误在登录表单切换后按当前语言显示，表单元素保持原身份。 |
| `tsc --noEmit`、`vite build --outDir <临时制品目录>`、`go build -o <临时可执行路径> ./cmd/server` | 通过。在独立目录构建，应用入口 527.38 kB（gzip 168.27 kB），Vite 输出 500 kB 分块大小提示。繁体、英语、日语各为一个独立词典块。 |
| `node apps/workbench/scripts/locale-browser-smoke.mjs` | 本机 Chromium 与实际 Go 服务通过。使用独立 memory/demo 服务、临时私有目录、编译后的静态文件，`LOCALE_TRUSTED_PROXIES=127.0.0.1/32`。`LOCALE_BROWSER_URL` 指向本机服务，测试向该代理入口提供大陆、香港、澳门、台湾、日本、美国出口地址样例。新上下文逐个核对显示语言及面包屑。390px 手机视口在四语间切换保留同一输入，刷新使用已选语言且零地区查询，恢复自动后重新显示日语。未登录场景仅替换会话响应，核对登录文字和输入、找回页切换。另实际查看英文桌面及日文手机截图。 |
| `npm run todo:sync`、`npm run check:todo`、`npm run check:docs`、`npm run check:changes` | 已执行，核对任务源、生成的入口、台账与 Wiki 同步。 |

首次前端全量回归暴露静态渲染缺少语言快照，已修正并通过最终全量回归。首次浏览器联验使用繁体术语修订前的旧制品，重新构建后完成六个地区的实际页面检查。

## 风险与限制

IP 库给出网络出口的地区估计，快照日期为 2026-11-21。完整反代配置和数据更新命令见 [前端 Wiki](../wiki/frontend.md#工作台语言与地区识别)。地区接口不可用时使用英语；如果语言包文件本身加载失败，启动过程仍可使用内置简体。当前验证使用本机回环服务、请求头样例和受控浏览器，未发布，也未从实际公网的地区出口验收。

## 回滚方式

恢复本次语言入口、组件文案及查表服务的上一版，并使用与该版本匹配的静态资产。浏览器可以单独删除语言偏好；无需处理用户草稿或数据库。

## 文档同步

根 README、Wiki、台账索引、任务源、生成的 TODO 和本轮推进记录同步。
