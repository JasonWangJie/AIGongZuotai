# 163 · 本人账户资料分页下载

## 目的

账户用户在原「账户安全」页面选择资料范围，逐页下载可以独立核对的 JSON 文件。受限账户可从其专属导航进入账户安全，下载同样限定为本人的资料。

## 范围

本条交付 V22-A 的 H08 工作台操作、下载字节校验和真实浏览器验收程序。三种范围为本人账户资料、本人安全事件与本人账户申诉。保留 027 的受限会话入口和项目权限边界。完整 V22 公开收费发布及外部依赖继续独立验收。

## 实际文件

- `apps/workbench/src/AccountSecurityPage.tsx` 在已有账户安全页接入资料下载。普通和受限账户共用这个组件。
- `apps/workbench/src/AccountDataExportPanel.tsx` 实现范围选择、每页密码确认、读取时间、创建截止时间与文件摘要展示。
- `apps/workbench/src/accountDataExport.ts` 校验下载响应的附件身份、缓存指令、长度、原始字节摘要、当前账户与各分区字段。
- `apps/workbench/src/account-data-export.css`、`apps/workbench/src/accountDataExport.test.tsx` 提供桌面和窄屏样式、附件和交互专项测试。
- `apps/workbench/scripts/account-data-export-browser-smoke.mjs` 用独立普通及受限登录，保存真实浏览器下载的四份 JSON 和两张截图。

## 接口与数据影响

`POST /api/v1/auth/data-export` 每次只提交当前密码、所选分区以及续页时的原游标。同源会话负责身份；浏览器不会发送指定账户、项目、幂等键或自定页大小。选择范围或准备下一页只更新页面。每次下载均由用户重新输入密码并显式确认，提交时即清空输入框。离开账户安全页会中止传输，换身份、切换范围或重新开始后丢弃旧页位置；本地存储和恢复仓均不保存资料、密码、分页或请求副本。

浏览器限流读取最多 512 KiB，每页最多 100 条。成功响应须为指定分区的 JSON 附件、限定私有且禁止缓存，且原始字节 SHA-256 与响应头相同。检查文件格式、当前账户 ID、分区字段白名单、页内唯一记录和续页过往创建截止时间后才生成下载文件。错误响应、迟到响应和校验失败均不产生文件。事件和申诉项按各页读取时间提供，首页的创建截止时间用于限定后续新记录。页面仅显示下载范围、计数、时间与摘要。

## 验证结果

集成人对冻结的 033 运行真实账户及 PostgreSQL 专项。会话撤销与账户分区 4.54 秒，申诉隔离与强转义字节分页 4.37 秒，双账户 Chromium 下载 10.11 秒。四份文件分别包含一份本人资料、100 加 12 条安全事件及一份受限账户本人的申诉；数据库逐条核对顺序、范围和原始申诉正文通过。

| 命令或检查 | 结果 | 实际范围 |
| --- | --- | --- |
| `node node_modules/typescript/bin/tsc --noEmit -p tsconfig.json` | 通过 | 账户安全入口、受限会话和下载组件的类型检查。 |
| `node node_modules/vitest/vitest.mjs run --no-cache --configLoader runner src/accountDataExport.test.tsx` | 16 项通过，2.86 秒 | 附件身份及校验值、实际读取上限、字段白名单、跨身份/分区/截止、错误密码、清空输入和离开页面后的迟到响应。 |
| `node node_modules/vitest/vitest.mjs run --no-cache --configLoader runner` | 43 个文件、504 项通过，11.94 秒 | 工作台完整测试套件。 |
| `node node_modules/vite/bin/vite.js build --configLoader runner` | 152 模块，11.71 秒 | 账户安全按需加载。 |
| `node apps/workbench/scripts/account-data-export-browser-smoke.mjs` | 实际 Chromium 配本机独立 HTTP 示例通过，6.79 秒 | 下载普通账户资料、安全事件第 1、2 页及受限账户本人申诉的四份 JSON；另行重算每页 SHA-256。错误密码无附件，外人会话持原游标得到拒绝，准备续页无请求。已查看桌面 1280×900 和手机 390×844 截图。 |

浏览器验收输入为 `ACCOUNT_EXPORT_BROWSER_URL` 环回地址、`ACCOUNT_EXPORT_BROWSER_OUTPUT` 输出目录和两组 `ACCOUNT_EXPORT_{USER,RESTRICTED}_{EMAIL,PASSWORD}` 临时账户。服务端真实 PostgreSQL 两账户、分页和落盘解析由集成人串行复验。脚本仅输出通过状态，不打印下载内容、账户身份、密码或游标。

## 风险与限制

下载使用浏览器 Web Crypto SHA-256，需要本机环回或 HTTPS 安全上下文。JSON 文件包含用户主动选择的个人数据；交给浏览器后的文件由用户管理。关闭页面后须重新选区并从首页开始。账户资料分区不含项目内容、模型请求、支付、私有媒体及内部凭据。此项不代表已完成数据删除、全部隐私合规或公开收费发布。

## 回滚方式

撤回本条账户安全页入口与专用组件即可恢复此前界面。页面不迁移数据，也不会修改账户、申诉或安全事件。撤回服务端路由由配套服务端台账管理。

## 文档同步

集成人同步 V22-A 的 README、相关 Wiki、OpenAPI、任务源、生成 TODO、进展与台账索引；本条聚焦前端下载和浏览器证据。

