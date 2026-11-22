# 206 受管聊天子进程的统一派单

## 目的

V17-J1 将有冻结上下文的会话模型请求交给本机受管子进程。控制服务继续承担业务受理、执行租约、审核、记账、撤销和用户 SSE。201 的真实模型流及 202 的日志实验为本项提供原始对照。

## 范围

本项覆盖 H02、H05、H07。启用条件是 PostgreSQL、必需的账户鉴权、live 模式和已配置的标准聊天网关。两个环境变量须一起指定，`CHAT_CHILD_PROCESSES` 为 1–4，`CHAT_CHILD_SLOTS` 为每个子进程 1–64。默认省略二者，使用原控制进程执行。受管进程的私管道、握手、模型适配与退出由 205 实现。

## 实际文件

- `cmd/server/chat_children_config.go`、`cmd/server/chat_children_config_test.go`、`cmd/server/main.go` 负责显式配置和入口接线。
- `internal/studio/chat_child_scheduling.go`、`internal/studio/chat_child_scheduling_test.go` 实现控制进程选槽和故障后的派单判据。
- `internal/studio/execution_scheduler.go`、`internal/studio/conversation.go` 复用同一工作区轮转、冻结上游模型预算和等待队列，只为成功持久受理的新会话标记可委派。
- `internal/studio/app.go` 接入子进程生命周期、原 Run 租约、审核与终态。`internal/studio/chat_execution_observability.go`、`internal/studio/observability.go` 在运维指标中记录存活槽、占用槽、累计派单和从未领取的会话首次本地执行次数。

## 接口与数据影响

运行服务等待受管子进程握手后才接受请求。单一调度器在共享工作区和冻结模型配额内，给普通 Run 分配原本地执行位，给已保存完整请求快照的会话分配真实空闲子进程位。一个工作区队首因该类执行位已满而等待时，其他可执行工作仍可取得相应槽。子进程退出会唤醒调度器；存活子进程仍使用自己的原槽数。

控制进程保留 PostgreSQL 控制写锁及私有目录锁。领取原 022 租约并提交 RUNNING 后，才会使用对应子进程的唯一票据发送启动命令。普通增量先提交再发布；开启输出审核时，候选在父进程隔离，按原策略、真实使用量和原保存路径收尾。子进程失联时父进程通过原控制事务从已提交的公开前缀保存 `INTERRUPTED`，返回固定错误码 `CHAT_CHILD_INTERRUPTED`。取消后占位一直保留到模型 I/O、审核及终态处理确实退出。原键读取、会话所属项目校验、用户 SSE、账户撤权及窗口回放继续走原平台接口。

当子进程全部退出、其在途运行也已收尾，仍在父队列中且从未领取的会话可在原本地槽内首次执行。之后的新会话同样受原配额约束。曾经领取或交付过的运行不回到等待队列。平台重启保留既有中断语义。数据库继续使用 037，所有者代次与业务格式均使用现有表。

## 验证结果

在准备目录执行 `go test -overlay overlay205.json -count=1 -run '^(TestManagedChat.*|TestChatChildrenFromEnv|TestChatScheduler.*|TestChatScheduling.*)$' -v ./cmd/server ./internal/studio`，其中 overlay 只映射同包 205 的实际传输文件。命令包两项通过，用时 1.216 秒；studio 包九项通过，用时 0.196 秒。`go vet -overlay overlay205.json ./cmd/server ./internal/studio` 通过。合并到同一工作树后可省略 overlay 参数。测试覆盖默认关闭、显式参数及运行前提、两类执行共用工作区与模型配额、满本地槽绕过阻塞队首、只剩部分子进程时保持原有路由、所有子进程退出后的首次本地执行、已领取运行不再进入队列。207 的真实 PG/OS 子进程、同一时段模型 HTTP 与授权 SSE、终态与原持久回放由集成人串行验证。

## 风险与限制

当前仍是一套独占控制服务、同机进程和单一私有聊天网关凭据。配置值表示允许占用的资源，实际活跃数和延迟以 207 实测为准。共享上游还受实际网关配额约束。SSE 保留 128 个全服务连接的原限制。进程失联不证明外部供应商已停止或免费。

## 回滚方式

停止新派单并等待在途处理退出，按现有停机规则保留未完成 Run 的已提交前缀。省略两个受管变量后重新启动同一版本，使用原本地调度。既有 Run 和原键保留。

## 文档同步

由集成人按 205、206、207 的独立证据更新 V17 任务源、README、Wiki、进度、台账索引及普通验证脚本环境隔离。
