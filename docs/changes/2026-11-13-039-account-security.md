# 039 · V11 账户安全与自助恢复核心

日期：2026-11-13。关联V11；本地实现与真实邮件渠道验收分开，不提前将整批标done。

## 目的

提供邮箱验证、一次性找回、密码修改及会话撤销，关闭bcrypt验证结束后并发改密仍能签发旧会话的竞态。

## 范围

Go领域、HTTP与公开能力、011迁移、独立脱敏安全事件、事务/并发/旧session兼容测试。SMTP与密文投递由040负责，UI由041负责，root负责配置入口、OpenAPI、备份/任务源与联合验收。没有真实外发邮件、收费请求或主库直接数据操作。

## 实际文件

- `internal/studio/account_security.go`、`account_security_test.go`：token消费、改密、邮件排队受理、session管理、安全分页与专项。
- `internal/studio/auth.go`：登录事务再校验凭据、注册/登录/注销原子安全审计、session版本、邮箱/名称/密码输入兼容与验证。
- `internal/studio/app.go`、`http.go`、`model.go`：Mail生命周期、只测试可用的sender注入、私有auth路由、no-store、User与能力。
- `internal/studio/migrations/011_account_security.sql`：用户/session增量、token、安全记录及密文邮件队列/索引。
- `docs/planning/v11-contract.md`、`docs/wiki/account-security.md`、`persistence-auth.md`、`backend.md`及本记录。

## 接口与数据影响

新增verification request/confirm、reset request/confirm、change password、session列表/单/全部撤销、security events共9路由。匿名session和bootstrap公开邮件能力，User增加验证bool/时间。token43字符base64url、只存SHA256、绑定用途/凭据版本；验证24小时、找回30分钟，每用途60秒冷却且最多3有效链接。

改密/重置提升credential_version、删全部session、作废token和安全事件同事务。登录签session前事务锁user重查bcrypt快照。旧session回填随机ID/version1，不公开token hash。私有新auth路由显式查身份，撤销和游标绑定当前user。

找回存在/不存在同202，发起找回不改密码/session、不立即失效旧链接；禁用与队列容量按全局语义检查。密文邮件和token同事务，网络只在worker执行。账户审计无私密正文，不进入项目Outbox。

## 验证结果

- `go test ./internal/studio -run '^TestPostgresAccount' -count=1`：本模块6个PG函数最终通过8.537秒。session隔离/改密/撤销；token用途/过期/并发一次性/无自动登录；独立双PG连接登录/改密两种先后锁序；审计异常回滚密码/session/token；邮件插入失败不单独落token、已知未知同响应/冷却；010旧session升级011及旧writer拒绝。
- 首次专项捕获复用聊天ID正则导致ses/sec被拒；已改独立闭集`validSecurityID`并复验，不扩大聊天ID校验范围。
- 普通 `go test ./internal/studio -run '^TestAccountSecurity' -count=1` 最终2函数通过0.080秒：格式/密码/NUL/UTF8/ASCII投递规范、旧UTF8邮箱登录兼容、匿名能力、ID隔离、拒绝与Provider/metrics相同原文或decoded邮件密钥。
- 阶段联合 `go test ./internal/studio -run '^TestAccountSecurity|^TestMail' -count=1` 通过0.329秒；`go vet ./...`通过。这不是根最终全仓结果。
- 根独立TLS SMTP+HTTP+PG、最终全仓及浏览器结果由042记录；不提前声称外部邮箱已验收。011最后补active/cleanup部分索引纳入root最终数据库验收。

## 风险与限制

仍为单控制写者、本地部署，IP/身份限流重启清空。统一找回响应与异步投递不等于恒时或全站无枚举；注册保留重复邮箱409。无MFA、CAPTCHA、完整失败登录风控或安全事件保留策略。验证不扩大权限、不强制阻断旧账户。

SMTP仅ASCII邮箱；旧UTF8地址可登录/改密但不支持投递。消费不自动重试，提交确认丢失需重新登录/申请链接核对。改密后旧排队邮件可能到达但token无效。真实渠道待配置、授权和独立验收，V11不得只凭fixture整批完成。

## 回滚方式

011前停写并联合备份PG、私有目录/对象收据和邮件密钥。旧程序拒绝011，需独立环境恢复匹配旧版本备份，保留新库与安全事件；不得删版本登记/owner marker强退。无自动向下迁移，不能重置状态重发可能已被SMTP接受的邮件。

## 文档同步

新增[账户安全Wiki](../wiki/account-security.md)和[V11契约](../planning/v11-contract.md)，同步基础账户及后端互链。root汇总README/Wiki/台账索引、OpenAPI、tasks/TODO与实际验收，邮件/UI各自维护Wiki/台账。V11本地通过与真实渠道待验收分别标注。
