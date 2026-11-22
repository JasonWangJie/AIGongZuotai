# 184　V17 单次聊天执行期限

## 目的

原方案要求对持续 30 分钟的活跃回复进行浏览器验证。标准服务此前始终沿用 10 分钟的聊天执行期限。本次提供启动时选择较长期限的入口，使受控演练可以使用标准服务二进制。

## 范围

V17 的 H03 模型与执行、H07 可靠性与性能。配置作用于本进程已开始执行的聊天 Run，覆盖实际聊天 provider 以及同一执行上下文内的输出检查。排队用时不占该期限。

## 实际文件

- `cmd/server/main.go` 解析可选环境变量并赋给已有的 `studio.Config.RunTimeout`。
- `.env.example` 说明单位、范围、默认值及 35 分钟本地演练示例。
- `scripts/lib/process.mjs` 在现有隔离执行配置中固定默认期限，供六套冒烟与常规负载脚本使用。
- `cmd/server/execution_config_test.go` 覆盖配置上下界、默认值、演练值和错误回显。
- `internal/studio/chat_run_timeout_test.go` 通过真实本机 HTTP 受理、内存 worker 和上下文感知的测试 provider 验证执行期限与已确认前缀。

## 接口与数据影响

`CHAT_RUN_TIMEOUT_SECONDS` 接受 1 至 3600 秒的 ASCII 十进制整数；未提供时由原有 Config 默认逻辑设为 600 秒。空值、带符号、空白、小数、指数、非 ASCII 数字等显式错误都会拒绝启动，错误只给出配置名与允许范围。配置随服务启动读取。31 分钟为 1860 秒，35 分钟为 2100 秒。

聊天 worker 在 Run 已领取并开始执行后，从应用上下文建立期限。主动取消、服务退出、租约失权仍能提前结束执行。浏览器 API、HTTP 读取、SSE 订阅和写出、媒体执行、数据库日常事务、租约持续期限与续租机制各自沿用原设置。无数据库增量或新增公开字段。源码核对路径为 `internal/studio/app.go` 的 `Config.defaults`、`executeRun`，以及 `internal/studio/provider.go` 的既有连接及响应头期限。

普通冒烟和负载脚本会继承开发机环境，因此其共同的隔离配置显式写 600 秒。前端基准和本机发布演练只向子服务传递允许的环境项，自然沿用默认值。需要 30 分钟验证的专用服务独立指定 2100 秒。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./cmd/server -count=1` | 通过，0.825 秒 | 含服务配置解析、空值/越界拒绝和原有设置回归。 |
| `go test ./cmd/server ./internal/studio -run '^TestChat(RunTimeoutEnvironment\|SchedulingEnvironmentStrictBoundsAndMissingDefaults\|ExecutionReceivesConfiguredDeadline\|ExecutionExpiresWithConfirmedPrefix)$' -count=1 -v` | 通过，两个包分别为 1.066 秒、1.230 秒 | 测试 provider 从真实 worker 收到默认 10 分钟、显式 35 分钟的截止时间。显式 1 秒的 Run 到时后以原失败状态结束，耗时 1.01 秒，保留已确认文本。首次编写测试误期望 202，按原接口 201 修正断言后复跑。 |
| Node 导入 `isolatedExecutionEnvironment`、`serviceEnv` 和 `releaseEnvironment` | 两组断言通过 | 分别给父进程注入 1 秒和一个非法值，冒烟子环境固定 600 秒，前端基准和发布子环境都未继承该值。未启动数据库或真实渠道。 |
| 持续 30 分钟的完整工作台活跃回复 | 由集成验收单独记录 | 读取 provider 上下文的 35 分钟截止时间与实际持续输出属于两项不同证据。 |

## 风险与限制

较长期限会允许单次运行更久占用现有执行预算。原调度上限、输出字节上限和背压仍生效；渠道侧超时或模型主动完成也会让 Run 提前结束。真实供应商、外部网络和 24 小时稳态验收独立跟踪。

## 回滚方式

移除 `CHAT_RUN_TIMEOUT_SECONDS` 后在受控停写窗口重启，即恢复默认 10 分钟。回退程序时保留已有聊天历史和数据库格式。运行中的 Run 依原有停机与恢复流程处理。

## 文档同步

集成人维护顶部 README、Wiki 性能说明、台账索引及 V17 任务源，分别登记本项配置验证与实耗 30 分钟验证。`.env.example` 已同步标准启动参数。

## 035 正式墙钟实测

显式 2,100 秒的标准服务配置在整段真实墙钟中有效。原受理从开始到终态使用同一次执行；默认 600 秒的独立配置测试保留。 [完整聚合报告](../wiki/assets/performance-long-reply-035.json)。
