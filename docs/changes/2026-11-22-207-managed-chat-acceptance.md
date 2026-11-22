# 207 受管聊天子进程的真实会话、故障和回放验收

## 目的

V17-J1 / H03、H07。在同一 PostgreSQL 控制写者下，通过真实会话、模型 TCP 连接、子进程归属、执行租约和授权 SSE，核对受管子进程实际执行了多少请求，以及执行者丢失后能恢复哪些证据。

## 范围

正式档使用一个标准 `cmd/server` 控制进程和它建立的两个子进程，每个子进程有 40 个执行位；原本地执行配置为一个执行位。五个已登录的合成工作区，以四个冻结上游模型各创建正常会话、取本次报价并提交首轮。普通 80 个运行同时连接本机模型，随后每个运行读取 12 个非空增量、一次终态，以及终态后的完整 SSE 回放。另一个已授权读者在首个增量后暂停直至正常读者读完，当次暂停时间单独登记。

先行档使用两进程各两个执行位和四个正常会话，保留四模型、真实钱包和故障路径。两档准备相同的五个合成账户。准备服务通过 HTTP 注册后先停机，每个工作区单独通过既有离线权益工具预演和精确摘要执行，获得本地测试 CREDIT。运行、会话、执行租约、报价、结算和审核决定全部由真实服务写入。

## 实际文件

- `internal/studio/chat_child_acceptance_test.go` 建立账户、离线测试权益、报价及正常会话，核对并发、读者、工作区、冻结模型、PG 租约、日志、终态、取消与恢复，并生成聚合报告。
- `internal/studio/chat_child_process_acceptance_test.go` 启停本轮标准服务，使用操作系统查询只核对本轮环回网关端口和已连接的对端，以及本轮控制进程的直接子进程、可执行文件。
- `internal/studio/chat_child_gateway_test.go` 提供受控的模型 HTTP 流，校验冻结模型、会话消息、真实 usage 请求及每个合成动作的唯一 POST。
- `internal/studio/chat_child_moderation_acceptance_test.go` 单独核对待审正文、阻断决定和已执行冻结。其控制服务使用同包测试宿主，受管子进程执行 205 的正式固定子命令。私有 TLS 根仅传给该测试的审核 HTTP Transport。
- `internal/studio/chat_child_observation_test.go` 使用正式上游解析器检查模型帧，并用一个自有的子进程与已建立的单个 TCP 连接独立校准 Windows 归属采集。

## 接口与数据影响

先 POST `/api/v1/conversations`，再由 `/api/v1/billing/quotes` 对固定会话正文报价，并 POST `/api/v1/conversations/{id}/runs`。SSE、终态及取消走原 Run 接口。同一原键、正文和报价重放须取回原 Run、用户/助手节点以及原冻结；外工作区读取原 SSE 应返回 404。父进程、执行归属、原文、账号、凭据和套接字地址只在本轮内存中比较；报告只含计数、时间分布和固定状态。

独立 SQL 检查活跃租约的 owner、epoch、generation、截止时间、固定会话上下文与所属工作区/项目。Windows 从实际网关已建立的对端取得客户端端口，并检查对应套接字的进程属于本轮控制进程的直接子进程且可执行文件相同。每个物理子进程应对应一个独立租约 owner。报告按进程、模型和工作区分别列出实际占用数，不公布 PID 或业务标识。

每个正常流以 2 个输入、3 个输出 token 的可信合成 usage 完成，原预留结算 5 微 CREDIT。另有四次独立故障动作。队列项在首次执行前取消，应释放原冻结并保持模型零 POST；已确认首段的显式取消、物理子进程终止以及控制进程强制退出后恢复，须保留原已执行权益核对状态。运行中的子进程中断应保留原公开前缀，另一子继续按原代次工作。重启后须更换控制执行代次，原键不可重新派发。另用环回 TLS 审核实际读取候选，核对待审期间 PG 公共日志和 SSE 快照均无正文，阻断后原冻结保留 REVIEW。

## 验证结果

在 205/206 联合源码上通过 Go overlay 编译整个 studio 测试包，未设置 `TEST_DATABASE_URL`。运行 `go test -p 1 -count=1 ./internal/studio -run '^Test(ChatChild(GatewayContract|FilteredKernelOwnership)|ActiveStreamCapacity(Observation|ModelFrames))$' -v`，4 项通过，补充双子失联与固定句柄复核后，终版包用时 1.910 秒，Windows 实际自有套接字与 PID 归属核对用时 1.76 秒。此处只启动了一个短时测试 TCP 对端，未运行容量测试或 PostgreSQL。实际两档测试与数值待集成人串行运行后补记。

须使用当前 205、206、207 同一源码构建的标准服务。在仓库根目录设置 `TEST_DATABASE_URL` 为独立环回测试数据库，在已有 `.runtime` 目录指定两个尚不存在的报告文件。

```powershell
go build -o .runtime/server-207.exe ./cmd/server
$env:STUDIO_CHAT_CHILD_SERVER = (Resolve-Path .runtime/server-207.exe).Path
$env:STUDIO_CHAT_CHILD_ACCEPTANCE = 'pilot'
$env:STUDIO_CHAT_CHILD_REPORT = Join-Path (Resolve-Path .runtime) 'chat-child-207-pilot.json'
go test -p 1 -count=1 -timeout 12m ./internal/studio -run '^TestPostgresChatChildAcceptance$' -v
$env:STUDIO_CHAT_CHILD_ACCEPTANCE = 'full'
$env:STUDIO_CHAT_CHILD_REPORT = Join-Path (Resolve-Path .runtime) 'chat-child-207-full.json'
go test -p 1 -count=1 -timeout 12m ./internal/studio -run '^TestPostgresChatChildAcceptance$' -v
```

## 两个子进程均退出后的首次执行

正式并发波次之后另做全子退出场景。控制进程恢复后使用两子各一个槽位，两个新会话已取得租约、各有一个已提交增量，另两个已报价会话在同一工作区队列内尚未取得执行身份。对每个已知模型 TCP 对端先打开 Windows 进程对象句柄，持有句柄时第二次核对套接字归属、标准可执行文件与直接父进程，再仅终止该句柄。剩余子占满期间两项继续排队；最后一个子退出后，在首个本地模型 POST 到达时用预先打开的独立数据库连接检查两个旧运行均已关闭、清租约并保留 REVIEW。两项原队列请求此后各只发一次模型 POST，并以操作系统套接字拥有者证明出自仍存活的原父进程。第一项运行时第二项仍无执行证据，按原本地一个执行位顺序完成、各结算 5 微 CREDIT。原执行项重放须保留同一个已提交增量和中断终态。

本轮对已执行取消、失子和父崩溃的授权 SSE 逐事件检查原增量及终态。超出持久窗口时的 RESET 使用 204 已独立实测的旧格式与窗口过期专项。[204 回放连接与窗口验收](2026-11-22-204-run-replay-read-pool.md)

## 风险与限制

实测归属当前使用 Windows 的 CIM 进程关系与 TCP 所有者查询；其他操作系统尚无同一试具的等价实测。80 个普通读者的增量计时从本机模型写 SSE 前开始，到对应已登录读者解码完整平台 SSE 为止，含本机两个 HTTP 段、进程间通信、数据库提交和应用调度。慢读者可能由既有网络缓冲容纳。短时 80 个合成活动对话不构成一万真实活跃流或跨主机部署证明；本批未联系真实外部模型，也未执行生产资金交易。

## 回滚方式

停止独立测试。其应用使用本轮新建的 PostgreSQL schema、临时私有目录和进程。207 仅增加验收代码及记录，不变更生产接口和数据库格式；保留原始报告供复核。

## 文档同步

集成人依据正式 PG、标准服务、两档实测及审核专项证据维护 README、Wiki、变更索引及 V17 任务源。

## 排队快照的首次试跑与修正

037 冻结副本首次运行 PG 小档验收，在排队受理后的 GET 收到 404，专项耗时 6.11 秒。验收函数 `childConversation.wait` 遗漏了实际快照路由的 `/snapshot` 后缀。在相同冻结源码的内存模式中，占用一个真实会话执行位、提交另一个会话排队，用原读取函数同样复现 404。修正仅补齐请求路径，保留原用户和项目作用域、状态、序号及 HTTP 200 要求。

新增 `TestChatChildAcceptanceQueuedSnapshot` 通过正式 HTTP handler 复核返回的排队 Run 身份与项目。使用冻结源码、隔离文件覆盖，`go test -p 1 -count=3 ./internal/studio -run '^TestChatChildAcceptanceQueuedSnapshot$' -v` 连续三次通过，包耗时 0.186 秒。PG 小档和正式档结果由集成人另行登记。

## 集成人串行 PostgreSQL 与标准制品结果

标准服务在本机启动两个受管子进程，Windows TCP 归属与 PostgreSQL 租约分别核实每个子进程同时持有 40 条模型连接。5 个工作区各 16 条，4 个冻结模型各 20 条。80 个正常授权读者共读到 960 个原序增量，80 笔运行完成结算；模型写出到普通 SSE 读完的 P50/P95/P99 为 33.91/64.86/976.40 毫秒。另一个读者暂停约 2.07 秒。 排队取消未请求模型，已开始取消和单子进程或控制器故障的已执行运行进入 REVIEW。两个子进程全失时，只将两个尚未领取的排队请求交给控制器的一个执行位。含故障场景的标准制品共发出 87 次模型请求；隔离的审核测试宿主另发 1 次，屏蔽决定留下的候选公开增量为 0。两部分合计零额外或重复模型 POST。4 路试跑包 26.646 秒，正式档包 29.582 秒。 [正式报告](../wiki/assets/performance-managed-chat-037.json)。
