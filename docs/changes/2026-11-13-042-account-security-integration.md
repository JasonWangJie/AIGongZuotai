# 042 · V11账户安全本地联合集成

## 目的

交付账户安全与自助恢复的本地可验收链路，实际邮件渠道按用户安排后续配置。

## 范围

V11／H01、H06、H07、H08、H09；根配置、接口、指标、升级联备、隔离验收及文档治理。

## 实际文件

- `cmd/server/mail_config.go`、`mail_config_test.go`、`main.go`：严格服务端环境映射、字段级错误与邮件配置注入。
- `internal/studio/account_security_integration_test.go`：独立App HTTP + PostgreSQL + 生产TLS SMTP完整链，使用测试私有CA。
- `internal/studio/observability.go`：9个固定账户路由，运维令牌与邮件凭据分离；`storage_maintenance.go`：011格式栅栏。
- `scripts/lib/process.mjs`及`smoke/runtime-smoke/catalog-smoke/reference-smoke/library-smoke/conversation-smoke/load.mjs`：测试子进程显式隔离全部AUTH_MAIL环境配置。
- `.env.example`、根`package.json`、`docs/api/openapi.yaml`：默认关闭的邮件配置、0.10.0版本与50路径/61操作契约。
- `AGENTS.md`、README、TODO、tasks.json，Wiki导航/架构/开发/可观测/维护/路线/性能与ADR0008，039—042索引及脱敏浏览器/升级/性能证据。
- 核心039、邮件040及前端041分别记录其实现文件；真实Kill测试由040追加到既有`durable_process_test.go`，本记录汇总根最终回归。

## 接口与数据影响

011增量迁移与账户/会话版本隔离；新增9个安全路径，Identity/Bootstrap返回邮件能力，User返回邮箱验证状态。OpenAPI 0.10.0共50路径、61操作、58 schemas、293内部引用。通用Retry-After契约允许认证900秒和资源繁忙3秒。指标总共52个固定路由桶（含other），没有邮箱/token/session ID标签。

邮件变量`AUTH_MAIL_*`只在服务端读取，enabled显式值只能true/false；端口、超时、worker必须在范围内。SMTP默认关闭；队列密钥独立于AI/SMTP/metrics凭据，不能以未配置为由输出明文找回链接。六套冒烟和性能脚本即便父进程带邮件配置，也强制关闭并清理连接/密钥设置。

## 验证结果

| 实际执行 | 结果 |
| --- | --- |
| `npm run check`（最终逻辑） | 通过；Go vet、Go全仓、173项前端/21文件、8项治理、严格TS与生产构建；85份文档/42份台账链接和章节有效 |
| `npm run test:db` | 根独立全量通过，studio115.317秒；包含旧业务、账户竞争/回滚/隔离/升级、SMTP与实际进程Kill |
| `TestPostgresAccountSecurityTLSMailHTTPJourney`独立运行 | 通过4.894秒；TLS邮件实际DATA、邮箱验证、用途/重复拒绝、找回一致202、密码重置/改密、旧会话401、跨账户拒绝、安全游标和指标脱敏 |
| 六套`smoke*` | 全通过；父进程注入启用邮件及无效host/key哨兵，子进程仍隔离成功。覆盖既有流/异步媒体/目录/参考图/资产/多轮；未接触真实AI或SMTP |
| OpenAPI严格解析/内部引用/JSON Schema/path参数 | 通过50路径/61操作/58 schemas/293引用；无重复键、坏引用或替换字符 |
| 实际主开发库011升级 | 停写联备后启动；旧浏览器Cookie有效。只读查询与升级前dump数据对照：全库8资产（主项目6）、草稿最高v5、资产最高修订10、会话12节点不变；2条旧单轮仍在，邮件/token表0项 |
| CUA桌面和390×844 | 通过；安全页能力禁用、3→2个会话真实撤销与安全事件、被撤销测试Cookie再访问401；全部撤销确认取消保留当前登录；恢复页及时清fragment/显式确认/错误后不重试，匿名找回未配邮件禁用，无横向溢出 |
| 最终文案修正 | 全部撤销提示改为“当前有效的账户凭据”，避免误称密码已变化；TS/构建通过，未为文案重复全量PG |
| `npm run perf:local` | 1000次bootstrap/32并发P95 22.08ms、2262.21次/秒；100次任务创建+取消/8并发P95 12.42ms；12条demo流/4并发首delta P95 56.93ms |

真实TLS HTTP链只向独立127.0.0.1模拟器发送2份邮件（验证、已知账户找回）；未知邮箱不创建邮件。测试使用临时用户，不修改开发QA密码。实际Kill在DATA到达且SENDING已提交后杀进程，重启为SUBMIT_UNKNOWN，观察多个周期仍1连接/1份DATA；这不等于外部送达验收。

阶段复核修复：session/security ID误用聊天正则；SMTP EHLO网络错误被Extension吞成不支持AUTH；前端错误当前密码401误登出；900秒认证冷却被通用300秒截短；旧生成请求迟到401与同键finally污染新scope。对应负例与最终根回归通过。最终跨模块只读复核未发现剩余阻断项。

联备位置为忽略的`.runtime/backups/pre-v0.10-20261113T142119491Z/`：PG custom dump108642字节，SHA256 `59d34b4f32f4eac8a191cdb1abdb7a120ccbeb399b3bcf0584dd38b40fe1c00b`，pg_restore目录可读；参考目录5文件、资产目录2文件含owner联备。备份表格中的数量为联备时统计，不代表资产逻辑条目数。

当前构建Go SHA256 `1945fb5f3875691826a7368454316ef71eb6247b4ca998a8dbece510001e5ae4`；最终前端JS433.49kB（gzip136.22kB）、CSS98.68kB（gzip19.80kB）。本地性能报告、升级比对和浏览器记录见下方链接；console近期warning/error为0。文档收口后的check:todo、check:docs、check:changes再次通过，最终OpenAPI/任务状态只读复核无实质不一致。

## 风险与限制

V11本地验收通过，整批保持blocked：用户明确选择真实渠道后续配置。尚需实际SMTP、发件域名/可信回站地址、独立密钥和指定测试邮箱，受控完成真实到达/验证/找回，才能标done；未外发真实邮件、未提交付费生成。

当前单控制写者、进程内限流，无MFA/完整失败登录风控/多实例邮件租约/多密钥轮换。邮件SENT不等于收件箱送达，未知不重投，用户可另申请新链接。前端内存保护不是跨关闭离线恢复；浏览器未输入或提交新密码，改密/重置实际写操作由独立Go HTTP+PG测试覆盖。手机是viewport模拟，非真机。

性能是本机memory/demo回归，不覆盖PG账户、bcrypt、TLS、真实网关或集群，不能宣称生产容量。本机Go race所需CGO/C编译器不可用，Linux CI已配置但未远程执行。S3后端未变，本批未重复V08真实兼容服务验收。

## 回滚方式

保留升级前数据库及私有文件联备。先停新服务，在独立环境恢复匹配旧程序的数据库和目录；保留011后的原库/安全事件，不删除迁移登记或owner强行降级。已更改密码、已撤销会话或实际发送的邮件不能靠回退前端撤销；启用邮件后须安全联备独立密钥，停用邮件也会暂停其worker清理。

## 文档同步

README/Wiki及AGENTS同步v0.10边界，新增ADR0008；tasks.json三项本地checks标passed，实际邮件blocked，TODO仍10/23整批已验收。下一步优先V12精确权益账本、报价冻结、释放/结算/冲正，usage缺失进入待核实；V13支付依赖V11/V12及真实商户。

- [账户安全](../wiki/account-security.md)、[邮件投递](../wiki/mail-delivery.md)、[前端](../wiki/frontend.md)、[决策](../wiki/decisions/0008-account-security-and-mail.md)
- [升级比对](../wiki/assets/account-security-upgrade-v0.10-2026-11-13.json)、[浏览器记录](../wiki/assets/account-security-browser-v0.10-2026-11-13.json)、[本地性能](../wiki/assets/performance-local-v0.10-2026-11-13.json)
- [桌面会话撤销](../wiki/assets/account-security-phase10.jpg)、[手机安全页](../wiki/assets/mobile-account-security-phase10.jpg)、[手机会话](../wiki/assets/mobile-account-sessions-phase10.jpg)、[未配邮件找回](../wiki/assets/account-recovery-disabled-phase10.jpg)
