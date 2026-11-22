# 161 · 本机回归负载的进程资源取样

## 目的

在已有 bootstrap、任务创建与取消、聊天流耗时的同一次运行中，记录服务进程实际占用的 CPU、驻留内存及系统资源数量。每份延迟分位数可回到对应阶段的观测时间和样本数。

## 范围

V17-A 涉及 H03、H07。沿用单机随机回环端口、无认证 memory/demo 服务和固定并发。每次运行仅观察本脚本启动的服务子进程。现有压测数量为 1000 次 bootstrap（32 并发）、100 组任务创建与取消（8 并发）、12 条聊天流（4 并发）。聊天首增量与整段聊天共享资源观察窗口。

## 实际文件

- `scripts/load.mjs` 在三段操作外取得开始与结束样本，保留各段吞吐和 P50/P95/P99，写入制品摘要与机器配置摘要。请求禁止重定向，阶段失败或收到中断后取消余下请求，等待自有服务退出后清理临时目录。
- `scripts/perf/owned-resource-sampler.mjs` 接受直接启动的子进程对象，在各阶段共用同一个读取器。Windows 分别记录工作集与句柄数，Linux 分别记录 VmRSS 与打开的文件描述符数。
- `scripts/perf/owned-resource-sampler.ps1` 从本机系统核验被测服务和隐藏读取器的父进程相同，持有目标进程句柄，响应固定的计数器查询。
- `scripts/perf/owned-resource-sampler.test.mjs` 用一个轻量自有子进程验证实际计数、异常退出、阶段失败和释放。

## 接口与数据影响

`npm run perf:local` 沿用 `.runtime/performance-local.json` 输出。三段 `results[].resources` 给出阶段开始/结束 UTC、首末样本单调时间跨度、总样本数、各指标有效样本数和读取器忙时跳过的轮次。采样目标间隔为 100 毫秒，每阶段另取首末样本。CPU 时间包含用户态和内核态，平均值和区间峰值以一个逻辑核心为 100%。`peak_rss_bytes_observed` 是采到的最大驻留字节数。`peak_windows_handles_observed` 与 `peak_linux_fds_observed` 分别表示原生句柄和文件描述符数量，非适用和缺失值为 null。

资源阶段通过 `resource_phase` 与耗时关联。进程退出、取样不可用或样本不足会给出状态，已有观测计数保留。报告不写目标 PID、命令行、进程文件路径、账户、主机名或请求 URL。机器配置摘要基于同时公布的 OS、CPU、内存和 Node 字段，制品摘要按本次启动的二进制字节计算，结束时再次核对。

## 验证结果

Windows 本机执行 `node --test scripts/perf/owned-resource-sampler.test.mjs`，2 项通过，2.439 秒。实际自有子进程取得正数 CPU 时间差、工作集与 Windows 句柄数，Linux FD 字段为 null。覆盖拒绝 PID 参数、采样目标退出、阶段失败、重复关闭和关闭后拒绝新阶段。另通过 `node --check scripts/load.mjs` 与 `node --check scripts/perf/owned-resource-sampler.mjs`。

完整复跑入口为 `npm run build` 后执行 `npm run perf:local`。本项开发期间仅运行轻量子进程专项；整段负载、样本量与报告归档由集成人另行实测。Linux 源在当前 Windows 机器未实测。

集成人在 031 累计 PG 进程实际退出后单独执行完整 `npm run perf:local`，exit 0。本机 Windows 031 实际运行 1000 次 bootstrap、100 次建任务并取消、12 次完整 demo 聊天。三段 P95 分别 19.77、10.67、3412.07 毫秒。观测到的最大驻留工作集 23,560,192 字节、原生句柄峰值 287；各窗口有 6、3、97 个样本。[分段原始报告](../wiki/assets/performance-owned-031-2026-11-22.json)

## 风险与限制

两个观测点之间的内存和句柄瞬时峰值可能被漏过，短 CPU 区间受系统计时精度影响。客户端与服务在同一机器，客户端和取样器会参与系统调度。此测量不覆盖 PG 等待、GC、慢订阅者、万级活跃流、真实媒体在途或 24 小时稳态；这些仍按 V17 原验收逐项测量。

## 回滚方式

可还原 `scripts/load.mjs` 的取样接线，并移除本条列出的新取样器与测试。保留已归档的原始报告及其口径。此次修改不调整服务端、数据库或线上路由。

## 文档同步

集成人依据完整本机负载的实际输出同步 README、性能 Wiki、台账索引、任务源及生成的 TODO。
