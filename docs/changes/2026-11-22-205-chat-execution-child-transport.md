# 2026-11-22-205：受管聊天子进程的私有流传输

## 目的

让单个工作台控制写者把已取得执行权的聊天模型请求交给同机子进程。数据库控制权、聊天上下文裁剪、审核、计费和对浏览器发布事件仍由父进程处理。

## 范围

V17-J1，关联 H03 模型与执行、H07 可靠性与性能。205 提供进程和流协议，206 接入受理、调度和既有 022 租约，207 独立验证真实工作台负载。协议只传父进程选中的冻结聊天消息和上游模型，媒体任务与旧式单提示词执行继续使用原路径。

## 实际文件

- `cmd/server/main.go` 在读取配置前分流固定子命令。
- `internal/studio/chat_child_protocol.go` 定义带类别、长度和执行身份的双向帧及大小检查。
- `internal/studio/chat_child_server.go` 负责握手、冻结请求校验、一次模型 I/O、逐段确认及取消。
- `internal/studio/chat_child_pool.go`、`internal/studio/chat_child_process.go`、`internal/studio/chat_child_stream.go` 负责进程准备、一次性占位、管道读写、故障收敛和退出。
- `internal/studio/chat_child_guard_windows.go`、`internal/studio/chat_child_guard_unix.go` 负责父退出时的进程守护。
- `internal/studio/chat_child_protocol_test.go`、`internal/studio/chat_child_process_test.go` 验证协议、真实子进程和本机 HTTP 模型。
- `internal/studio/chat_child_cancel_receipt_test.go` 验证父端保存尚未返回时同进程两个槽位的实际运行、取消收据和超时退出。
- `docs/changes/2026-11-22-205-chat-execution-child-transport.md` 记录此次实现和本地证据。

## 接口与数据影响

生产父进程只启动自身可执行文件和固定参数 `chat-execution-child`，为每个子进程创建两条匿名管道。子命令直接调用 `RunChatExecutionChild(os.Stdin, os.Stdout)`。父显式提供空的应用环境，子进程从初始帧取得专用模型配置，Windows 运行库可提供 SystemRoot。子进程没有数据库连接、目录写锁、会话、审核凭据、HTTP 监听或业务日志。

每实例握手确认后才提供 1 至 64 个执行位，父最多持有 4 个实例。每个实例有随机 owner，每次派工带槽号、递增票号、Run ID、execution ID、owner、generation 和 epoch。模型 I/O 只在接收完整派工并写出确认后开始。START 的写入或回执不明时退出该执行，不重发。流内的 DELTA 按连续序号传输，父调用原 emit 成功才回 ACK。每个位至多有一个待确认的正文段，父审核或保存慢时其他位仍可消费各自响应。

每帧头 5 字节，先按类别检查长度再分配。配置帧不超过 32 KiB，执行身份、确认和终态帧不超过 4 KiB。正文上限继续使用既有每请求 256 KiB、每次输出合计 128 KiB，单帧为 JSON 最坏转义和元数据预留空间。每次请求最多 129 条历史消息。执行只返回固定的失败类别或既有结构化 usage。回环明文和远端 HTTPS 的校验、禁代理和重定向、单次 POST 的规则复用原聊天适配器。

取消可在父端 emit 阻塞期间传到子进程。收到实际 I/O 终态或确认子进程退出后，stream 才会结束；父端仍需等审核、保存退出并显式 release 才可再用该位。握手、管道写入或取消收敛超过 10 秒会终止本进程并等待 OS 退出。一个失效进程上的其余在途流也会中断。Windows 采用不可继承的 kill-on-close Job Object，Unix 以父独占的写端 EOF 取消上下文。父端主动关闭会终止并等待全部自有子进程。公开 API、数据库格式及资产引用不变。

## 验证结果

与 210 联审时发现，父端保存单个增量超过 10 秒且 Run 同时取消，原看门狗会因保存回调尚未返回而终止已关闭该路模型 I/O 的整个子进程。私有管道协议第 2 版在每次取消中加入一次性随机标识。子进程等原模型调用退出并成功写出 END 后，回传同一标识、票、执行身份和末序号。父端读管道逐帧核对，匹配的收据解除该次取消计时。正文 ACK 仍须等保存成功，槽位仍须等回调实际退出及父端释放。缺少收据使用原 10 秒期限，错误票、标识或顺序立即终止并等待该进程退出。

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./internal/studio -run '^TestChatChild' -count=20 -timeout=120s` | Windows 通过，4.892 秒 | 在 206 准备目录以 Go overlay 引入本次源码。七项测试重复 20 轮。真实子进程访问环回 HTTP，核对冻结模型、完整历史、私有凭据、usage、逐条 ACK 和独立两槽。 |
| 进程故障与单次派工专项 | 通过 | 杀掉两个真实进程中的一个，仅该执行中断，另一个保留至显式停机。杀死持有运行中子进程的父进程后，上游观察到已提交的一次 POST 断开。重复票、跨槽重复尝试和父管道 EOF 都保持原一次 POST。 |
| 协议与限额专项 | 通过 | 错误执行 epoch、序号跳跃、异常帧长度、非对象、附加字段、截断、矛盾 usage 被拒。最多 256 KiB 的转义输入可往返；第二段跨过 128 KiB 总额时仅保留第一个合法段。 |
| `go build ./cmd/server` 与已编译 Windows 标准程序的早分流检查 | 通过 | 正常服务不可用的地址和数据库环境哨兵下，专用子命令仍完成管道握手、本机 demo 的 30 个已确认增量、完整 usage 及安静退出。 |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -p=2 ./cmd/server` | 交叉编译通过 | 与同一份协议及非 Windows 守护代码一起生成 Linux amd64 程序。运行时观测来自 Windows。 |
| 真实 PostgreSQL、80 路业务验收和外部网关 | 分别由 207、集成人及另行的实际渠道验收负责 | 205 本地传输专项未使用 PostgreSQL、浏览器或外部服务。 |
| `go test ./internal/studio -run '^TestChatChild' -count=1 -timeout=60s -v` | Windows 独立 overlay 通过，12.348 秒 | `TestChatChildCancelReceiptWhileParentSavePending` 10.19 秒。一个真实子进程中 A 的环回 HTTP 已断开，父端受控保存回调仍持槽，A 的增量 ACK 为零。B 在同一原请求上持续收到增量并完成，网关共计两次 POST。打开保存门闩后 A 才能换票。 |
| `TestChatChildCancelFencesMissingOrInvalidReceipt` | 四种真实子进程对端通过 | 只发 END 的对端在 10.04 秒退出。错误取消标识、错误票和先于 END 的收据均在 0.04 秒被拒。父进程未接受错误收据或回送正文 ACK。本专项用受控回调复现父端等待，数据库故障及账务另验。 |

## 风险与限制

受管进程共享同一主机和控制写者。子进程死亡后的上游计费或最终状态仍可能未知，父必须按已有租约和权益规则收尾。一个子进程失联影响其全部运行中槽位，退出前的未确认响应不能作为恢复提交的凭据。受管池本次不自动重启失效实例。独立 OS 子进程和本机网关专项只验证模型 I/O 边界，云端多实例、实际供应商及生产容量需另验。

## 回滚方式

保持受管聊天配置关闭可继续使用父进程内的标准执行路径。已有受管任务先停止新受理并按当前进程实际退出与已提交的租约状态交接。回退程序保留业务记录、账务、私有目录及现有格式 037。

## 文档同步

集成人依据 205、206 和 207 的独立结果维护 README、Wiki 及进度、台账索引和任务源。传输专项与联合数据库、HTTP、性能证据分别登记。
