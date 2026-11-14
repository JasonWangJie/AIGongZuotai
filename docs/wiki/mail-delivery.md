# 安全邮件：TLS SMTP 与加密持久队列

V11 / H01、H06、H07、H09。本页记录已实现的邮件适配和本地验证。账户令牌、会话撤销和防枚举见[账户安全](account-security.md)，联合验收见[042 台账](../changes/2026-11-13-042-account-security-integration.md)。真实 SMTP 服务商、发件域名和收件邮箱尚未配置与验收；本地 TLS SMTP fixture 不等于实际渠道送达。

## 配置与启停

默认关闭。必须同时配置 PostgreSQL、`AUTH_MODE=required` 和下列服务端设置才能启用；只有用户明确请求验证或找回时创建邮件。启动、注册和登录不自动外发，不将令牌或找回链接打印到控制台。

| 环境变量 | 约束 |
| --- | --- |
| `AUTH_MAIL_ENABLED` | 默认 false；显式值只能为 true/false |
| `AUTH_MAIL_HOST` / `AUTH_MAIL_PORT` | 单一 SMTP 主机与十进制 1—65535 端口；不接受 URL、用户指定主机或跳转 |
| `AUTH_MAIL_TLS_MODE` | 必填 `implicit_tls` 或 `starttls`；无明文降级模式 |
| `AUTH_MAIL_USERNAME` / `AUTH_MAIL_PASSWORD` | 服务端 SMTP 凭据；当前只支持 TLS 后的 AUTH PLAIN，不自动尝试其他机制 |
| `AUTH_MAIL_FROM` | ASCII 裸邮箱地址，最长254字节；不接受显示名、控制字符、SMTPUTF8 地址 |
| `AUTH_MAIL_PUBLIC_ORIGIN` | 受信的固定 HTTPS origin，可带端口；禁止账号信息、子路径、query、fragment。仅 localhost/回环 IP 可使用 HTTP 进行本地验证 |
| `AUTH_MAIL_OUTBOX_KEY` | 标准 Base64 编码的32字节独立随机 AES 密钥；不得复用 SMTP、AI 网关或 metrics 凭据 |
| `AUTH_MAIL_TIMEOUT_SECONDS` | 默认20秒，范围5—60秒；覆盖连接、TLS、认证、DATA 及最终确认 |
| `AUTH_MAIL_WORKERS` | 默认1，范围1—4；固定并发，不按邮件数量创建 goroutine |

TLS 最低1.2，验证服务器证书与主机名，信任系统 CA；没有生产跳过证书校验的开关。私有 CA 应由部署环境正确安装。fixture 只通过 Go 包内的私有依赖注入使用临时 CA，不增加环境开关。

重启后加载新配置。已经入队的收件地址、令牌、From 和 PublicOrigin 加密冻结，旧邮件不会被新站点地址重写；SMTP 连接配置使用当前服务端配置。密钥变化若导致未过期 PENDING/RETRY 不能解密，启动返回 `MAIL_KEY_MISMATCH`，保留原队列且不把业务数据库标记为故障。当前没有多密钥轮换器；应保留原密钥完成待发项/等待其过期，再执行受控轮换。

## 用户操作与链接边界

固定纯文本模板只包含操作说明、有效期和 `/#auth=verify|reset&token=...` 链接。地址来自配置，不使用请求 Host、Referer 或客户端 redirect 参数。令牌放 URL fragment，浏览器不会把它附到普通 HTTP 请求路径；前端读取后移除地址栏片段，只在明确确认时向自有 API 提交。

邮箱、令牌、邮件正文、SMTP 凭据不进入访问日志、指标或公开 API。错误只使用固定代码，不透传 SMTP 回复正文。所有头字段校验，主题使用 MIME 编码，正文使用 quoted-printable，单封编码后上限64KiB。单次连接最多读取256KiB服务器回复，防止异常 SMTP 无限回复；断开和超时会结束网络 I/O。

## 加密队列与事务

核心011迁移创建 `studio_mail_outbox`。同一业务事务写一次性令牌哈希、安全审计和加密邮件；失败全部回滚。邮件表不存收件地址/令牌明文，仅存固定 ID、purpose、key_id、ciphertext、expiry、状态、次数、下次时间和安全错误代码。

`mailDelivery.seal(EmailEnvelope)` 是纯内存操作。AES-256-GCM 每封使用随机 nonce，认证附加数据绑定邮件 ID、用途和微秒精度到期时间；交换记录或修改这些字段无法解密。密文内部版本为1，长度上限8KiB。业务令牌表只存 SHA256；邮件中需要交付的原令牌仅存在加密载荷和发送中的进程内存。

当前仍使用单控制写者和 PostgreSQL advisory lock。worker 在短数据库操作中持久领取为 SENDING，然后释放数据库/应用锁执行 SMTP。发送结果再次持久保存；不在全局锁内等待外部网络。多个 worker 的领取使用 `FOR UPDATE SKIP LOCKED`，按到期调度、创建时间和 ID 排序。

| 状态 | 进入条件与下一步 |
| --- | --- |
| PENDING | 业务事务已经提交；等待领取 |
| SENDING | 本次 attempt 已持久领取；开始外部发送 |
| RETRY | 明确 SMTP 4xx 或 DATA 前可确认没有接收的网络失败；保留原令牌和 Message-ID |
| SENT | 读到 DATA 结束后的250确认；立即清加密载荷。这表示 SMTP 接收责任，不代表用户收件箱已收到 |
| FAILED | 明确永久拒绝、证书/能力不匹配、不可读载荷或重试耗尽；清加密载荷 |
| SUBMIT_UNKNOWN | DATA 后未读到确定结果，或重启发现旧 SENDING；永不自动重新投递 |
| EXPIRED | 已过期，或下一次安全重试晚于过期时间；清加密载荷 |

收到250后连接关闭不会撤销 SENT。DATA 写入/确认结果不明时，不能以 Message-ID 相同为理由重发：SMTP 不保证幂等去重。若用户仍未收到，可明确申请新邮件，由账户领域签发新令牌；本实现没有把不明投递伪装成确定失败或已送达。

## 容量、重试与保留

未过期 PENDING/SENDING/RETRY/SUBMIT_UNKNOWN 合计最多10000项；满额返回 `503 MAIL_BACKLOG_FULL`，业务事务不提交且不会熔断数据库。公开找回在查找账户前统一检查渠道和容量，避免用队列错误泄漏账户存在性。

每封最多5次网络尝试。明确未接收的失败按15、60、300、900秒退避，每次加0—10秒抖动；到期不再领取，未知不参加重试。网络超时受当前请求上下文约束，进程正常停止取消正在等待的 SMTP；已经观察到接收结果仍尝试保存，数据库确认丢失则按现有 fail-closed 机制停止，重启将未确认 SENDING 停在未知状态。

启用邮件时，启动及各 worker 每分钟分批清理：每批最多1000条过期 PENDING/RETRY/SUBMIT_UNKNOWN，清正文并置 EXPIRED；SENT/FAILED/EXPIRED 的状态元数据保留7天后分批删除。极端积压可能需要多个周期，非瞬时全表清理。关闭邮件也会停止此 worker 和清理；账户令牌自身仍按到期时间拒绝使用。离线邮件清理工具、全局硬限历史行数和多密钥滚动轮换尚未实现。

## 已验证与真实渠道恢复步骤

8个普通测试函数覆盖隐式 TLS/STARTTLS、拒绝明文降级/不可信证书、4xx/5xx、确认丢失、取消、服务器回复有界、头注入、可信地址、AES-GCM 绑定、凭据脱敏和稳定 Message-ID。3个真实 PostgreSQL 函数覆盖重启未知、密钥错配、真实本机 TLS SMTP 接受/确认丢失、多 worker、保留原令牌重试、5次封顶、10000项原子背压和到期清载荷。见[040 台账](../changes/2026-11-13-040-security-mail.md)。根集成另验实际 App HTTP 权限链与账户状态，不以 provider 单测代替领域验收。

实际渠道仍需：确定 SMTP 供应商的 TLS/PLAIN 支持、配置服务端凭据与独立加密密钥、正确设置 PublicOrigin 和发件地址；由域名管理配置 SPF/DKIM/DMARC、供应商配额和退信管理；取得明确授权后向指定测试收件箱验实际到达、垃圾箱与链接回站。当前没有真实外发、DKIM 签名器、退信 webhook、送达回执、多渠道自动切换或送达 SLA。

协议依据：[Go net/smtp](https://pkg.go.dev/net/smtp)提供低层 SMTP/TLS API，本实现不采用可机会降级的便捷发送函数；[RFC 5321](https://www.rfc-editor.org/rfc/rfc5321.html#section-6.1)区分 SMTP 接收责任与最终投递，DATA 最终响应丢失可能造成重复；[OWASP 找回密码指南](https://cheatsheetseries.owasp.org/cheatsheets/Forgot_Password_Cheat_Sheet.html)要求随机、单次、过期令牌、统一响应和可信 HTTPS 地址。队列加密、未知停止自动重发、固定容量与本页具体数值是本项目设计。
