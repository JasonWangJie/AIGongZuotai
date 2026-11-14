# 038 · V10多轮对话联合验收

日期：2026-11-13。版本：v0.9.0。关联V10／H01、H02、H03、H06、H07、H08、H09。035/036/037分别记录领域、Provider与前端，本记录汇总根集成与最终验收。

## 目的

交付持久多轮对话、分支与上下文管理完整纵向链路。将“历史可看”推进为服务端根据选中祖先链构造真实messages；同时验证并发、受理不明、部分输出和刷新恢复，保留原单轮与媒体能力。

## 范围

会话树/运行关联与010增量迁移，真实多消息Provider、预算和整轮裁剪，前端会话/分支/继续，独立HTTP fixture、实际子进程Kill、PG全仓、浏览器及接口/运维/文档治理。单控制写者和本地开发边界不变，真实网关留给V07/V15。

## 实际文件

- `package.json`、`.github/workflows/ci.yml`：根版本0.9.0、新增`smoke:conversations`与CI步骤。
- `scripts/conversation-smoke.mjs`：独立构建Go服务+loopback聊天网关，检查真实role/content及输出参数、分支/游标、原键、停止/继续、预算和脱敏。
- `internal/studio/durable_process_test.go`：保留V09媒体Kill用例，新增真实PG聊天子进程Kill、持久部分输出、消息树、原键及显式继续。
- `internal/studio/observability.go`：五个固定会话路由标签，合计43类；`storage_maintenance.go`：维护格式栅栏001—010。
- `docs/api/openapi.yaml`：0.9.0、五个新路径/六项操作、会话/消息/分页/上下文schema及Run/模型/审计增量。
- `README.md`、`AGENTS.md`、`docs/wiki/README.md`、`architecture.md`、`backend.md`、`persistence-auth.md`、`models.md`、`observability.md`、`storage-maintenance.md`、`performance.md`、`roadmap.md`与`decisions/0007-conversations-and-context.md`：现状、规则、配置、实际验证和下一批建议。
- `docs/planning/v10-contract.md`、`tasks.json`、`TODO.md`、`docs/changes/README.md`和本记录：契约/任务与台账索引。
- `docs/wiki/assets/*-v0.9-2026-11-13.json`、`conversations-phase9.jpg`、`conversations-dirty-phase9.jpg`、`mobile-conversations-phase9.jpg`、`mobile-conversations-composer-phase9.jpg`：最终报告与浏览器证据。035/036/037列出对应模块的代码与测试文件。

## 接口与数据影响

新增项目私有会话创建/列表/详情、选中叶子的祖先分页、叶子分支分页及带expected_version的reply/continue受理。会话/消息/Run/冻结上下文/幂等/审计/Outbox同事务，只有确认提交后执行。原键重放先于新版本/CAS/忙状态；编辑创建兄弟节点，继续明确创建新运行。上下文通过服务端私有快照执行，API仅返回预算摘要，估算与实际收费分开。

010升级前停止本轮拥有的开发进程，联合备份数据库与reference/assets并保留owner身份。忽略目录`.runtime/backups/pre-v0.9-20261113-212345/`保存81212字节custom-format数据库dump、SHA256/时间清单和文件；`pg_restore --list`可读。应用最终代码启动后只读核对迁移001—010、7条资产目录、旧草稿最高v5、指定旧资产修订v10；浏览器仍可读原项目、媒体任务及单轮输出。本轮未做生产备份恢复/灾备演练。

## 验证结果

- 根`npm run test:db`全量独立真实PostgreSQL通过：studio **82.959秒**。包含新会话事务/作用域/不可变触发器/CAS、COMMIT确认丢失和实际Kill，以及既有Outbox、租约、存储、鉴权等回归。PG测试窗口串行。
- 根新增实际Kill专项先独立通过2.500秒：等待输出真正进入数据库后杀掉Go子进程，重启保留部分回答并标记INTERRUPTED；消息树两节点与原键仍在，不新增供应商POST；明确继续创建新Run，网关恰好收到第二次POST且包含原用户/持久部分回答/固定继续指令。后续包含在上述全量测试中。
- 根`npm run check`通过：8项任务治理、Go vet、普通Go测试（studio6.850秒）、**154项前端测试/19文件**、严格TS和生产构建；72模块，JS408.34kB/gzip128.56kB、CSS88.93kB/gzip18.05kB。未新增前端依赖。
- 六套独立冒烟均通过：`smoke`、`smoke:runtime`五组、`smoke:catalog`三组、`smoke:references`三组、`smoke:library`三组、`smoke:conversations`五组。临时服务/目录独立，不读取开发网关或发送真实付费请求。新聊天fixture最终接收7个预期POST；第二轮三条结构化消息、明确输出字段、兄弟分支隔离/分页、CAS/原键优先/跨项目隐藏、停止/继续、整轮裁剪及溢出前置拒绝均核对。
- 严格YAML重复键、所有引用、JSON Schema及路径参数检查通过：**41路径、52操作、267引用、45 schemas**。会话指标覆盖五个固定标签且无资源ID/正文/凭据。
- 浏览器真实PG+demo验收通过：创建、两轮发送/上下文摘要、历史编辑成新分支且原路径可读、停止保留输出、确认后新增继续、第二窗口通知更新而本地输入/父节点不变、显式确认版本、切分支仍保留输入所属路径、刷新后鉴权恢复会话及叶子选择、旧单轮历史输出。最终会话12节点，原停止输出未被改写。
- 1440×960桌面及390×844手机尺寸通过，手机document宽390/main宽380且无横向溢出，会话列表和输入区可操作。最终浏览器console warning/error为0，关闭临时对照窗口并恢复viewport。截图和[浏览器报告](../wiki/assets/conversations-browser-v0.9-2026-11-13.json)保留；不是移动实机测试。
- `npm run perf:local`通过：1000次bootstrap/32并发P95 **22.04ms**、2138.75请求/秒；任务创建+取消P95 12.36ms；demo首delta P95 56.79ms。[报告](../wiki/assets/performance-local-v0.9-2026-11-13.json)只代表同机memory/demo，不覆盖PG多轮/真实网关/生产容量。
- 五份专用冒烟JSON与性能报告均核对最终构建SHA256：`b7a95a1149528950aad362b4b8fd7088d5f6c9c5cffa791f3bf4ed832d277abb`。基础smoke以命令输出留证；其脚本本身不输出JSON。

交付前问题处理：独立审阅发现空/NUL消息ID可能污染PG操作、慢ResponseWriter持有全局锁、逐节点读取祖先Run；现分别通过严格输入保护、锁外响应及批量查询修复并有回归。Provider非法文本保留有效前缀后失败。前端审阅修复切分支后的迟到成功残留已提交草稿、旧组件迟到401删除新组件重试恢复槽，两处均加入确定性交错测试；最终只读复核无新增阻断。

新冒烟首轮将跨会话父节点预期写为422，实际按资源隐私返回404；已对齐约定为严格404后重新通过。升级只读SQL首次因Windows命令行中文编码失败，改用UTF-8十六进制常量后通过，未产生数据写入。任务源首次证据格式被TODO治理拒绝，改为带path/note的条目后重新生成并检查。这些是验收脚本/文档集成问题，不将失败隐去或宣称首轮即全部通过。

## 风险与限制

- 当前仍单控制写者；普通delta内存回放和约一秒检查点不等于逐token强耐久，崩溃只保留最近持久部分。继续可能消耗额外额度，但不会自动创建。
- 窗口和输出预算由配置提供，utf8_bytes_v1是保守估算，不是供应商精确tokenizer或计费。demo仅演示流式交互；真实messages由独立HTTP fixture验证，真实模型质量/usage和新API兼容留给V15。
- 输入、编辑目标与未知body/key仅当前页面内存；localStorage只存作用域隔离的选中ID，不承诺关闭后的草稿恢复。无自动摘要、工具调用、附件、Markdown/代码高亮或协作编辑。
- Go race本机缺CGO/C编译器，Linux CI已配置未远程运行；符号链接专项受Windows权限限制，真实S3兼容服务延用V08证据，本轮未重复未变更专项。性能报告不证明生产万级容量。
- 邮件恢复、账本、支付、审核、生产运维及真实供应商放行仍未完成，10/23批不代表总体工期百分比。

## 回滚方式

保留升级前联备及升级后会话数据。前端可退回上一dist并继续旧单轮入口；Go旧版本遇010必须拒绝启动。需要整体回退时使用与旧版本匹配的独立备份数据库和私有目录，保留新库和文件以供恢复；禁止删010登记/owner或强行在新库上运行旧程序。新会话没有自动降级成旧单轮的迁移。

## 文档同步

根README/Wiki/架构/后端/模型/持久化/指标/维护/性能/路线和ADR0007已更新，035—038已登记。V10完成条件、命令、证据与台账写回唯一任务源后生成TODO，累计10/23批。V07/V15保持外部条件待验收，下一批V11账户安全/自助恢复，V12精确账本可并行。最终`node scripts/tasks.mjs --check`通过（10模块/23批/10批已验收）；`node scripts/check-docs.mjs`通过（77文档/38台账）；`--changed`通过（330路径）。本轮不提交、不推送、不公开部署。
