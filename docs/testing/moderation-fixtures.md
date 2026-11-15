# 本地媒体审核样本

这六个文件是本仓库在2026-11-14用Pillow程序绘制几何图案、FFmpeg编码的测试样本，不是AI生成、真实审核服务结果或互联网下载素材。

- `allow/block/review.png`：512×288 RGB PNG，颜色与文字区分测试分支。
- `allow/block/review.mp4`：对应PNG循环1秒，H.264、yuv420p、256×144、12fps，无音轨。
- `manifest.json`：实际SHA256、长度、kind及fixture预期分支；生产审核不能按这些样本推断普通内容安全。

构造视频使用FFmpeg：`-loop 1 -i <source.png> -t 1 -vf scale=256:144 -r 12 -c:v libx264 -preset veryfast -pix_fmt yuv420p -movflags +faststart <output.mp4>`。FFmpeg来自本地一次性测试工具imageio-ffmpeg，未增加应用运行依赖。解码验证及浏览器播放证据记录于058台账。

启用本地媒体审核后，专用demo Provider将这些已知字节导入私有存储。默认选择allow样本；请求中`[MEDIA_FIXTURE_BLOCK]`或`[MEDIA_FIXTURE_REVIEW]`仅用于明确选择另一份样本。审核引擎随后独立核验实际原件的完整SHA256与长度，不根据提示词作图像/视频内容判断；未在策略登记的字节保持REVIEW。旧静态SVG示意结果保持历史行为，不进入新审核链。

样本原件继续位于 [`internal/studio/testdata/moderation/`](../../internal/studio/testdata/moderation/)，测试读取路径未变。
