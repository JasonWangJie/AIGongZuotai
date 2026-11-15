# 异步媒体协议 fixtures

这些 JSON 为依据[公开接口文档](https://api.aiimg.lol/async-image-api-docs)手工构造的合成样例，没有用户数据、真实任务、模型账号或密钥，也不是调用真实模型得到的录制结果。文档显示更新日期为 2026-11-12，取证日期为 2026-11-13。

公开 SPA 路由加载 `/static/js/async/6168.9dd1da9042.js` 中的 Markdown 文档。实现契约与未确认项见[媒体网关 Wiki](../wiki/media-gateway.md)。`{{RESULT_URL}}` 仅由测试替换为本机 httptest 地址。测试用 `fixture-key-not-real` 为固定非真实占位字符串。

覆盖受理、生成中、完成、执行未知、保存失败和结果过期。视频回包、HTTP 拒绝/不确定、超时、下载重定向及非法素材由 Go 测试内的本地 handler 动态构造。

样例文件继续位于 [`internal/studio/testdata/media/`](../../internal/studio/testdata/media/)，这里只集中维护说明。
