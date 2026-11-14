# 028 · 独立资产库与元数据工作台

日期：2026-11-13。关联批次：V08 资产库与对象存储。模块：H02、H04、H08。前端版本：0.7.0。

## 目的

让用户在独立项目目录中检索、命名、标注及回收恢复素材，而不再依赖浏览器已加载的任务记录。明确区分文件内容、可修改的元数据和元数据版本，保留并发冲突、未知写入与身份切换后的原操作。

## 范围

独立 library 列表、搜索／标签／来源／类型／回收状态过滤、keyset 分页；原生详情对话框、按需预览下载；名称／标签编辑、CAS 冲突、原键重试、不可变元数据历史、另存旧元数据、软回收及恢复。参考图上传与编辑链保留，并感知完整列表中已回收的旧素材。

没有文件内容替换、永久删除、浏览器 S3 控制台、文件夹、批量操作、图库虚拟化或自动付费请求。对象存储、迁移与孤立文件清理由对应后端模块交付，本台账只报告前端完成范围。

## 实际文件

- `apps/workbench/src/LibraryPage.tsx`：项目目录、过滤条件、keyset 游标、列表请求代次、缩略图与详情焦点恢复。
- `apps/workbench/src/LibraryDetail.tsx`、`LibraryDetail.test.tsx`：名称／标签、并发冲突、未知重试、元数据历史与回收恢复。
- `apps/workbench/src/libraryState.ts`、`libraryState.test.ts`：元数据规则、状态机、原操作复制、用户项目资产内存恢复和版本合并。
- `apps/workbench/src/library.css`、`main.tsx`：深色资产卡片、原生对话框、移动布局和动效降级。
- `apps/workbench/src/api.ts`、`api.test.ts`：library GET/PATCH/trash/restore/revisions、项目 header、CAS/幂等正文、平台素材 URL。
- `apps/workbench/src/App.tsx`：资产页替换、恢复仓生命周期及刷新关闭保护；不再用任务分页数冒充资产库范围。
- `apps/workbench/src/referenceState.ts`、`referenceState.test.ts`、`useReferences.ts`：完整参考图列表移出已不可见的旧 ready 项，保留新上传和未知原文件。
- `apps/workbench/src/activity.ts`、`ActivityPage.tsx`：资产元数据、回收和恢复活动的脱敏中文名称。
- `apps/workbench/package.json`、`package-lock.json`：0.7.0，无新增依赖。
- `docs/wiki/frontend.md`、本台账：行为、协议和边界；根 README／索引／任务源由集成人维护。

## 接口与数据影响

按 [V08 冻结契约](../planning/v08-contract.md) 使用同源 Cookie 和显式 X-Project-ID。目录每页 24，opaque cursor 只用于同一组项目和过滤条件。过滤切换使旧请求失效，分页按 ID/revision 去重，低版本重放不覆盖已知新版本。

PATCH 全量 name/tags 与 expected_revision；trash/restore 发送 expected_revision。三种写入均持原 Idempotency-Key。未知结果的操作 kind、body、key 在当前页面内存中按用户／项目／资产隔离，不能因重登、关闭详情或错误 429/401 变成新操作。CAS 保留编辑并要求显式取舍。历史只返回元数据，载入旧名称和标签后须新键、当前版本另存，不改变当前回收状态。

软回收关闭预览／下载，不永久删除内容。列表仅加载 thumbnail_url，缺少缩略图时不加载大原图；视频须用户点击后才挂载带 src 的播放器。所有 URL 限制为平台鉴权接口，生成记录逐条保留 DEMO 身份。参考选择器只有完整第一页明确缺失才移出请求开始时已知的 ready 素材，显式说明原因，原未知上传及任务请求保持不变。

## 验证结果

- `npm --prefix apps/workbench run typecheck`：通过。
- `npm --prefix apps/workbench test`：118 项／17 文件通过，包括此前 100 项回归、元数据状态边界与 3 项真实组件静态渲染测试。
- `npm --prefix apps/workbench run build`：通过，66 个模块；JS 369.11 kB（gzip 116.82 kB）、CSS 76.96 kB（gzip 15.90 kB）。
- 新验证覆盖：Unicode 元数据边界／标签规范、CAS 保留与显式取舍、原未知操作遇到 429/401、请求体复制、迟到当前快照不得覆盖编辑、身份／项目隔离恢复、历史载入不恢复回收状态、keyset 参数编码、分页版本去重、同源内容路径、回收恢复请求、已不可见参考素材与新上传竞态。
- `node scripts/check-docs.mjs`：前端模块链接与文件齐备；最新全仓检查仍有 10 项根联合浏览器截图／证据和存储维护 Wiki 链接尚未落盘，由对应负责人完成后复验。
- 本前端代理不启动服务、不操作用户前台浏览器。实际 Go／PG／对象存储 HTTP 和桌面／390px 浏览器验收由集成人在 030 记录；此处不以状态测试替代真实 UI／接口联调。

## 风险与限制

元数据未保存内容和未知操作只存在当前页面内存，刷新／关闭会丢失，已有 beforeunload 提示；不称为跨关闭的离线持久化。元数据历史不是媒体文件版本，没有旧文件内容下载入口。软删除不释放存储配额，也不改变已受理任务的不可变输入。

分页有界但用户继续加载可积累卡片，本批无列表虚拟化。视频原件需主动播放，参考及生成缩略图是否可用取决于服务端数据。构建体积和单元测试不代表低端设备性能、对象存储容量或真实 AI 效果验收。V07 的真实供应商图片编辑缺口仍独立保留。

## 回滚方式

恢复本台账列出的前端文件到 0.6 版本并重建，保留后端资产、元数据历史和幂等记录，不删除文件以恢复旧界面。旧前端没有资产目录管理入口；服务端格式／对象存储回滚需遵循对应后端台账，不能绕过格式栅栏。

## 文档同步

已同步 frontend Wiki 的 0.7 独立资产库、API／恢复行为、文件组织和能力限制；根 README、索引和 V08 checks/evidence 由集成人在 030 根据实际联合验收更新。029 属于离线维护与目录锁，不作为本前端的浏览器验收记录。

## 本批浏览器验收修正

集成人实测发现已回收详情仍可编辑元数据、恢复条目追加到尾部。已在同批修正：trashed 名称／标签／保存及旧元数据载入均禁用，历史查看和恢复保留，并提示先恢复再编辑；提交入口与 reducer 同样阻止 trash 上的新 metadata/edit/restore_metadata。后端 ASSET_STATE_CONFLICT 会进入显式冲突核对，保留本地编辑，不允许把 trashed 快照作为继续保存基准。

列表更新按完整 created_at 时间及 id 倒序合并，保留纳秒差异；仅重新插入当前过滤结果实际已加载过的 ID，恢复仓中位于已加载分页范围之外的资产不会被强行追加，不改变服务端尾游标。后续正常翻页仍按 ID／revision 去重。组件测试直接渲染详情及历史预览，验证 disabled、历史可读、恢复入口可用以及恢复后重启编辑；状态测试覆盖排序、尾游标外不插入、重叠页去重与并发回收冲突。
