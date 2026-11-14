# V11 账户安全与自助恢复契约

本地实现与测试不等于真实邮件渠道验收。默认关闭发送；本批不自动外发真实邮件，不开放公网，不改变现有账户的工作区权限。

## HTTP 与公开字段

路径前缀均为 `/api/v1`。`/auth/session`（含匿名）及 bootstrap 增加 `account_security:{enabled,mail_configured,verification_enabled,password_reset_enabled,reason?}`。User 增加 `email_verified:boolean`、`email_verified_at:null|date-time`。

|接口|身份/输入|成功|
|---|---|---|
|POST /auth/email/verification/request|已登录；`{}`|202 `{ok:true,message:固定文字}`|
|POST /auth/email/verification/confirm|公开；`{token}`|200 `{ok:true}`；不建立会话|
|POST /auth/password/reset/request|公开；`{email}`|统一202；不说明邮箱存在或已经发送|
|POST /auth/password/reset/confirm|公开；`{token,new_password}`|200 `{ok:true,reauthenticate:true}`；所有会话失效，不自动登录|
|POST /auth/password/change|已登录；`{current_password,new_password}`|200 同上；清当前 cookie|
|GET /auth/sessions|已登录|`{items:[{id,current,created_at,expires_at}]}`；最多10个未过期会话|
|POST /auth/sessions/{id}/revoke|已登录；`{}`|200 `{ok:true,reauthenticate:boolean}`；其他账户ID返回404|
|POST /auth/sessions/revoke-all|已登录；`{}`|200 `{ok:true,reauthenticate:true}`；清cookie|
|GET /auth/security/events|已登录；`limit=1..50`默认20、可选cursor|`{items:[{id,action,created_at}],next_cursor:null|string}`|

密码沿用10至72字节，必须有效UTF-8且不含NUL。令牌为32随机字节的43字符base64url；格式错误、过期、已用、用途不符或凭据版本不符统一400 `ACCOUNT_TOKEN_INVALID`。邮件禁用为503 `MAIL_UNAVAILABLE`，能力字段明确禁用原因。限流429附 `Retry-After`。客户端不自动重试敏感操作；失败不将密码/token写入草稿或恢复仓。

邮件链接为配置可信origin加 `/#auth=verify|reset&token=...`。前端读取后立即清除fragment，用户显式确认才POST；验证链接不切换当前登录账户。邮件request返回202只是统一受理回复，不能宣称邮件已送达。

## 存储与并发语义

011为账户加凭据版本与验证时间；会话新增独立随机公开ID及凭据版本，旧会话兼容回填版本1。令牌表只存SHA256、用途、用户、凭据版本、到期及消费状态：验证24小时，找回30分钟。每用户用途60秒冷却、最多3个有效链接；新申请不立即撤销旧链接，消费或密码变更使旧凭据版本全部失效。

bcrypt在数据库锁外执行；签发登录会话时事务重新锁定用户并核对原密码散列/凭据版本。改密还重新核对当前会话；改密/重置递增凭据版本、撤销所有会话和令牌，并同事务写账户安全事件。数据库失败不对外报告成功，提交确认丢失按未知处理且不自动重试。

令牌签发、加密邮件outbox和安全事件在同一事务。邮件密钥与业务/供应商密钥分离；数据库不存明文收件人/令牌/正文。固定worker在事务外SMTP发送，未知接收状态不自动重发。账户安全事件独立于项目审计及Outbox，无邮箱、密码、cookie、令牌、IP、UA或任意details。

## 边界

邮件发送依赖PG和显式配置；默认disabled，无console明文令牌后门。邮箱验证只显示所有权状态，不扩大权限。会话撤销对新请求立即生效，已打开SSE在下一次现有心跳检查时结束。进程内有界限流重启会重置，不声称分布式防刷。真实邮件渠道仍待独立配置与授权验收。
