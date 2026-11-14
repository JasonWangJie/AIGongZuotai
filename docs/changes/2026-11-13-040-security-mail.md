# 2026-11-13-040：TLS 安全邮件与加密持久投递

## 目的

V11 / H01、H06、H07、H09：为邮箱验证/密码找回提供可配置的真实 SMTP 适配与持久队列，避免把链接打印到控制台、明文持久令牌、网络不明时重复投递。外发只由已授权的用户请求触发；本轮全部测试使用本机 fixture，不向真实邮箱发送。

## 范围

独立 MailConfig、AES-GCM 密文、固定模板、TLS/STARTTLS SMTP、有限 worker、可确认失败重试、未知受理停车、过期/保留清理与测试。011 schema/账户令牌/权限/事务接线由039负责，环境配置和 App 级纵向验收由042汇总。没有修改这些共享文件或增加依赖。

## 实际文件

- `internal/studio/mail_config.go`：私有配置默认、范围、TLS与可信origin、地址/控制字符/独立key校验。
- `internal/studio/mail_crypto.go`：EmailEnvelope、密文与key指纹、AES-GCM认证绑定、冻结地址/From/origin。
- `internal/studio/mail_smtp.go`：有界TLS SMTP、固定模板、稳定Message-ID、固定安全失败类别。
- `internal/studio/mail_delivery.go`：同事务enqueue、容量、领取、重启未知、有限重试/清理worker。
- `internal/studio/mail_crypto_test.go`、`mail_smtp_test.go`、`mail_delivery_test.go`：8个普通函数和3个真实PG函数；SMTP fixture用真实本机socket/TLS，不外发。
- `internal/studio/durable_process_test.go`：补充1个真实PG/进程强杀邮件恢复函数；保留V09媒体和V10会话测试，子进程配置仅增加可选邮件配置/测试CA，生产入口不新增TLS绕过。
- `docs/wiki/mail-delivery.md`、本台账：配置、投递语义、持久边界、验收与真实渠道差距。

## 接口与数据影响

`MailConfig` 使用服务端 `AUTH_MAIL_*` 环境接线；默认关闭，启用需要PG/required身份模式。只有TLS implicit/STARTTLS与TLS后AUTH PLAIN；无跳过证书验证或明文开关。timeout默认20秒/5—60，worker默认1/1—4。

`newMailDelivery(config,store)`、`seal(EmailEnvelope)`、`enqueueMailSQL(ctx,tx,item)`、`checkMailCapacitySQL`、`prepare()`、`run(ctx)` 供核心调用；外部网络不占用业务锁。`studio_mail_outbox`由011创建，收件人/令牌只存加密payload；AAD绑定ID/purpose/微秒expiry，SMTP/公共地址变更不会重写既有加密模板输入。

10000个未过期待投递/未知项上限；最多5次，15/60/300/900秒加0—10秒抖动。确认250才SENT，DATA后不明或重启SENDING进入SUBMIT_UNKNOWN不自动重试；确定4xx可重试，5xx/证书错误等明确失败。SENT不代表收件箱到达。终态清ciphertext，未知到期分批清；每批最多1000，终态元数据7天后分批删。停用邮件暂停邮件worker清理，令牌验证仍拒绝过期。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./internal/studio -run '^TestMail' -count=1` | 通过 | 8个普通函数0.296秒；全部SMTP只监听127.0.0.1，使用临时CA验证真实TLS，无真实邮箱外发 |
| `go vet ./internal/studio` | 通过 | 静态检查通过 |
| `go test ./internal/studio -run '^TestPostgresMail' -count=1`，载入忽略的本地测试DB环境 | 通过 | 独占PG窗口，3函数2.536秒；实际重启连接/领取/容量/worker、SMTP接受与最终确认丢失、过期清理 |
| `go test ./internal/studio -run '^TestPostgresMailProcessCrashAfterDATA$' -count=1 -v` | 通过 | 后补真实进程测试4.529秒；真实TLS DATA到达且DB为SENDING后Process.Kill，重启同配置后SUBMIT_UNKNOWN，观察2.2秒多个poll周期，累计仍仅1连接/1份DATA |
| `go test ./internal/studio -run '^(TestPostgresCrashAfterSupplierAcceptanceDoesNotResubmitAndReplaysEvents\|TestPostgresConversationActualCrashPreservesPartialAndExplicitContinue)$' -count=1` 与 `go vet ./internal/studio` | 通过 | 既有V09/V10两个真实进程恢复函数4.342秒通过，vet通过；确认可选邮件fixture配置未改变旧媒体/会话子进程行为 |
| App HTTP + PG + TLS SMTP 全链路 | 根集成负责 | 042记录根独立执行结果，不把provider单测代替账户流程 |
| 实际 SMTP 服务商/域名/邮箱送达 | 未运行 | 缺真实渠道配置与指定测试收件箱，本轮未外发；V11真实渠道仍待验收 |

开发中巨量SMTP回复测试曾失败：`smtp.Client.Extension`隐藏EHLO网络错误，导致错误分类为AUTH能力缺失。已增加显式Hello，恢复可确认未受理网络错误的安全重试语义；同一负例随后通过。TLS网络超时与证书错误也分别按临时/永久失败处理，没有把错误吞成成功。

## 风险与限制

当前仍是单控制写者，未实现跨实例邮件租约、多密钥滚动轮换、SMTPUTF8、AUTH LOGIN/OAuth2、DKIM签名、退信/投递回执、运营重查界面或渠道容灾。关闭邮件后需恢复启用才能继续分批清理；数据保留不是到期瞬间全部删除。消息ID不能提供SMTP幂等，未知不得自动重发。最多5次是平台策略，不是供应商送达保证。

## 回滚方式

可先设置 `AUTH_MAIL_ENABLED=false` 停止投递，保留数据库与独立outbox密钥用于后续恢复；已发送邮件无法撤回，令牌仍由领域过期/消费策略控制。011格式升级后不得删除迁移记录启动旧程序；完整回滚按备份同时恢复数据库和当前私有存储，不能清队列来伪造成功。不要删除密钥后期待恢复未发送加密邮件。

## 文档同步

- README：根042更新V11能力、配置、限制和最近变更。
- Wiki：[邮件交付](../wiki/mail-delivery.md)说明协议与操作；039账户安全链接；根维护导航。
- 台账索引：根增加040并与039/041/042关联。
- 批次状态：根按实际证据更新任务源，保留真实渠道未验收项。
