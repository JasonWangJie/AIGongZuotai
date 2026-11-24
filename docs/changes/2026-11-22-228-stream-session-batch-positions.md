# 228　订阅批量复核按原位置返回有效会话

## 目的

V17-J4（H03、H07）。219 的 192 个真实读者专项在建立 Run 订阅时收到 503，慢套接字专项随后无法观察到仅普通读者继续占位。此修正恢复大于 128 个全局订阅时的共享会话复核。

## 范围

修改专用 PostgreSQL 只读连接上的批量身份查询，并增加实际会话顺序回归。配置 256 个全局订阅的 220 联合验收也使用这条查询。220 的两种规模均须用修正后重新构建的服务验证。

## 实际文件

- `internal/studio/stream_sessions.go` 显式给 PostgreSQL 生成的第五列命名。
- `internal/studio/stream_session_acceptance_test.go` 使用两条真实登录会话，分别构造凭据版本和用户身份不匹配的输入，核验批次四个位置的实际结果。

## 接口与数据影响

`unnest` 展开散列、会话、用户和凭据版本四个数组。`WITH ORDINALITY` 再生成从 1 开始的位置列。原查询引用 `k.pos`，但仅为前四列指定了别名，第五列的默认名为 `ordinality`。该引用在 PostgreSQL 中无效；当只读事务遇到这一非瞬时错误时，控制写者进入原有的故障保护。修正为第五列指定与 SELECT 对应的别名，使有权会话准确写回其原输入位置。接口、默认准入上限、认证撤销周期和数据库格式沿用原定义。

## 验证结果

修正前的串行 PostgreSQL 验收中，`TestPostgresStreamSessionShared192AndFailure` 在申请 Run 订阅时观测到 503，于 3.04 秒失败；`TestPostgresStreamSessionSlowSocketAndOriginalReplay` 在等待慢写超时后的占位条件时于 19.78 秒失败。相同轮次中，214 的默认 128 订阅和慢套接字专项分别通过。

隔离源码通过 Go overlay 编译。清空进程的 `TEST_DATABASE_URL` 后执行 `go test -p 1 -count=1 -run "^TestStreamSession(Monitor.*|ProjectObserverRequiresScopedEvents)$" ./internal/studio`，包耗时 0.222 秒并通过。

新增 `TestPostgresStreamSessionValidationPositions` 检查两条不匹配身份与两条有效身份交错输入时，结果依次为 false、true、false、true，并检查控制写者健康。修正后的隔离数据库验收命令为 `go test -p 1 -count=1 -timeout=4m -run '^TestPostgresStreamSession' -v ./internal/studio`。192 个读者、实际写超时、退出登录、断开独立连接及原游标回放沿用 219 的原断言。真实运行结果由集成人串行复验后追加。

## 风险与限制

共享复核只处理当前会话有效性。各订阅仍须独立核验当前资源和回放范围。服务端自有认证连接中断、撤权及快照身份异常均继续按原边界处理。

## 回滚方式

代码回退会恢复原有的无效列引用。需要暂时退出共享复核时，可在受控环境恢复默认全局上限 128 并按既有流程重启；原数据库、会话与回放游标无需变更。

## 文档同步

集成人关联根 README、Wiki、台账索引、任务源、生成 TODO 与进度。[219 订阅专项](2026-11-22-219-authenticated-sse-sessions.md) · [220 联合验收](2026-11-22-220-integrated-chat-sse-acceptance.md)

## 集成人验收

228 修正位置列后 192 个已鉴权订阅实测通过（96 个运行、96 个项目）。注销关闭 64 个，复核连接故障关闭其余 128 个，随后重新接入 2 个。慢读者实测 5000.32 ms 触发写截止；原游标补回 12 个增量，缓冲丢弃 0。 新增双账号四位置用例 3.19 秒通过。
